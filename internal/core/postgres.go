package core

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lib/pq"
	"github.com/thomdehoog/origoa/internal/gitx"
)

// pgProjection is the PostgreSQL Projection: plain SQL, no ORM. The database
// is never authoritative — repo_state.processed_hash records the Git revision
// the projection represents. Apply advances it with a compare-and-swap that
// mirrors the Git branch CAS, so two server processes sharing one database
// can never silently skip a commit; on any mismatch the caller falls back to
// a full Sync from Git.
//
// Reads fail closed: database errors surface as ErrUnavailable instead of
// fabricated empty answers (which would, e.g., let duplicate HIDs into Git).
type pgProjection struct {
	git *gitx.Repo
	db  *sql.DB
}

const pgSchema = `
CREATE TABLE IF NOT EXISTS repo_state (
	id             int  PRIMARY KEY CHECK (id = 1),
	processed_hash text NOT NULL
);
CREATE TABLE IF NOT EXISTS artifacts (
	guid        text  PRIMARY KEY,
	kind        text  NOT NULL,
	type        text  NOT NULL,
	title       text  NOT NULL DEFAULT '',
	hid         text  NOT NULL DEFAULT '',
	base        text  NOT NULL DEFAULT '',
	source      text  NOT NULL DEFAULT '',
	target      text  NOT NULL DEFAULT '',
	subject     text  NOT NULL DEFAULT '',
	created     text  NOT NULL DEFAULT '',
	workflows   jsonb,
	file_path   text  NOT NULL,
	folder      text  NOT NULL,
	etag        text  NOT NULL,
	search_text text  NOT NULL DEFAULT '',
	search      tsvector GENERATED ALWAYS AS (to_tsvector('simple', search_text)) STORED
);
CREATE INDEX IF NOT EXISTS artifacts_file_path ON artifacts (file_path);
CREATE INDEX IF NOT EXISTS artifacts_folder    ON artifacts (folder text_pattern_ops);
CREATE INDEX IF NOT EXISTS artifacts_kind_type ON artifacts (kind, type);
CREATE INDEX IF NOT EXISTS artifacts_hid       ON artifacts (hid);
CREATE INDEX IF NOT EXISTS artifacts_source    ON artifacts (source);
CREATE INDEX IF NOT EXISTS artifacts_target    ON artifacts (target);
CREATE INDEX IF NOT EXISTS artifacts_subject   ON artifacts (subject);
CREATE INDEX IF NOT EXISTS artifacts_fts       ON artifacts USING GIN (search);
CREATE TABLE IF NOT EXISTS config_files (
	file_path  text  PRIMARY KEY,
	scope      text  NOT NULL,
	category   text  NOT NULL,
	definition jsonb NOT NULL
);
CREATE INDEX IF NOT EXISTS config_scope ON config_files (scope, category);
`

func newPGProjection(g *gitx.Repo, dsn string) (*pgProjection, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(pgSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("postgres schema: %w", err)
	}
	return &pgProjection{git: g, db: db}, nil
}

func dbErr(err error) error {
	return fmt.Errorf("%w: %v", ErrUnavailable, err)
}

func (p *pgProjection) Head() string {
	var head string
	_ = p.db.QueryRow(`SELECT processed_hash FROM repo_state WHERE id = 1`).Scan(&head)
	return head
}

// Sync is the full repository reindex: all derived rows are rebuilt from the
// Git HEAD tree in one transaction.
func (p *pgProjection) Sync() error {
	head, recs, err := syncRecords(p.git)
	if err != nil {
		return err
	}
	tx, err := p.db.Begin()
	if err != nil {
		return dbErr(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM artifacts; DELETE FROM config_files`); err != nil {
		return dbErr(err)
	}
	for _, rec := range recs {
		if err := upsertRecord(tx, rec); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO repo_state (id, processed_hash) VALUES (1, $1)
		ON CONFLICT (id) DO UPDATE SET processed_hash = EXCLUDED.processed_hash`, head); err != nil {
		return dbErr(err)
	}
	if err := tx.Commit(); err != nil {
		return dbErr(err)
	}
	return nil
}

// Apply projects one published commit. The processed_hash advance is a CAS
// against the commit's parent: if another process already moved the
// projection (or it lags), nothing is applied and errStaleProjection tells
// the caller to Sync instead. Git remains the source of truth throughout.
func (p *pgProjection) Apply(parentHead, newHead string, changes []Change) error {
	tx, err := p.db.Begin()
	if err != nil {
		return dbErr(err)
	}
	defer tx.Rollback()

	res, err := tx.Exec(`UPDATE repo_state SET processed_hash = $2 WHERE id = 1 AND processed_hash = $1`,
		parentHead, newHead)
	if err != nil {
		return dbErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if parentHead != "" {
			return errStaleProjection
		}
		// First commit ever: claim the row; a concurrent claimant loses.
		if _, err := tx.Exec(`INSERT INTO repo_state (id, processed_hash) VALUES (1, $1)`, newHead); err != nil {
			return errStaleProjection
		}
	}
	for _, c := range changes {
		if c.Delete {
			if _, err := tx.Exec(`DELETE FROM artifacts WHERE file_path = $1`, c.Path); err != nil {
				return dbErr(err)
			}
			if _, err := tx.Exec(`DELETE FROM config_files WHERE file_path = $1`, c.Path); err != nil {
				return dbErr(err)
			}
			continue
		}
		// An overwrite may change what the file classifies as; clear both.
		if _, err := tx.Exec(`DELETE FROM artifacts WHERE file_path = $1`, c.Path); err != nil {
			return dbErr(err)
		}
		if _, err := tx.Exec(`DELETE FROM config_files WHERE file_path = $1`, c.Path); err != nil {
			return dbErr(err)
		}
		if err := upsertRecord(tx, classify(c.Path, c.SHA, c.Content)); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return dbErr(err)
	}
	return nil
}

func upsertRecord(tx *sql.Tx, rec *record) error {
	switch {
	case rec == nil:
		return nil
	case rec.meta != nil:
		m := rec.meta
		var workflows any
		if m.Workflows != nil {
			b, _ := json.Marshal(m.Workflows)
			workflows = b
		}
		_, err := tx.Exec(`
			INSERT INTO artifacts (guid, kind, type, title, hid, base, source, target, subject,
			                       created, workflows, file_path, folder, etag, search_text)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
			ON CONFLICT (guid) DO UPDATE SET
				kind = EXCLUDED.kind, type = EXCLUDED.type, title = EXCLUDED.title,
				hid = EXCLUDED.hid, base = EXCLUDED.base, source = EXCLUDED.source,
				target = EXCLUDED.target, subject = EXCLUDED.subject,
				created = EXCLUDED.created, workflows = EXCLUDED.workflows,
				file_path = EXCLUDED.file_path, folder = EXCLUDED.folder,
				etag = EXCLUDED.etag, search_text = EXCLUDED.search_text`,
			m.GUID, m.Kind, m.Type, m.Title, m.HID, m.Base, m.Source, m.Target, m.Subject,
			m.Created, workflows, m.FilePath, m.Folder, m.ETag, rec.text)
		if err != nil {
			return dbErr(err)
		}
		return nil
	default:
		// jsonb rejects the \u0000 escape; the repository may contain it via direct Git
		// edits, and one hostile byte must never wedge Sync.
		def := bytes.ReplaceAll([]byte(rec.raw), []byte(`\u0000`), nil)
		_, err := tx.Exec(`
			INSERT INTO config_files (file_path, scope, category, definition)
			VALUES ($1,$2,$3,$4)
			ON CONFLICT (file_path) DO UPDATE SET
				scope = EXCLUDED.scope, category = EXCLUDED.category, definition = EXCLUDED.definition`,
			rec.filePath, rec.scope, rec.category, def)
		if err != nil {
			return dbErr(err)
		}
		return nil
	}
}

// ---- queries ----

const metaCols = `guid, kind, type, title, hid, base, source, target, subject, created, workflows, file_path, folder, etag`

func scanMetas(rows *sql.Rows) ([]*Meta, error) {
	defer rows.Close()
	var out []*Meta
	for rows.Next() {
		m := &Meta{}
		var workflows []byte
		if err := rows.Scan(&m.GUID, &m.Kind, &m.Type, &m.Title, &m.HID, &m.Base,
			&m.Source, &m.Target, &m.Subject, &m.Created, &workflows, &m.FilePath, &m.Folder, &m.ETag); err != nil {
			return nil, dbErr(err)
		}
		if len(workflows) > 0 {
			_ = json.Unmarshal(workflows, &m.Workflows)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, dbErr(err)
	}
	return out, nil
}

func (p *pgProjection) Get(guid string) (*Meta, error) {
	rows, err := p.db.Query(`SELECT `+metaCols+` FROM artifacts WHERE guid = $1`, guid)
	if err != nil {
		return nil, dbErr(err)
	}
	metas, err := scanMetas(rows)
	if err != nil {
		return nil, err
	}
	if len(metas) == 0 {
		return nil, nil
	}
	return metas[0], nil
}

func (p *pgProjection) List(q ListQuery) ([]*Meta, error) {
	text := strings.ToLower(strings.TrimSpace(q.Text))
	limit := q.Limit
	if limit <= 0 {
		limit = 1 << 30
	}
	rows, err := p.db.Query(`
		SELECT `+metaCols+` FROM artifacts
		WHERE ($1 = '' OR kind = $1)
		  AND ($2 = '' OR type = $2)
		  AND (CASE WHEN $3 = '' AND $4 THEN TRUE
		            WHEN $4 THEN folder = $3 OR folder LIKE $5
		            ELSE folder = $3 END)
		  AND ($6 = '' OR search @@ plainto_tsquery('simple', $6)
		               OR search_text LIKE $7)
		ORDER BY file_path, guid
		LIMIT $8`,
		q.Kind, q.Type, q.Folder, q.Subtree, escapeLike(q.Folder)+"/%",
		text, "%"+escapeLike(text)+"%", limit)
	if err != nil {
		return nil, dbErr(err)
	}
	return scanMetas(rows)
}

func (p *pgProjection) LinksFor(guid string) (incoming, outgoing []*Meta, err error) {
	rows, err := p.db.Query(`SELECT `+metaCols+` FROM artifacts
		WHERE kind = 'link' AND (source = $1 OR target = $1) ORDER BY file_path, guid`, guid)
	if err != nil {
		return nil, nil, dbErr(err)
	}
	metas, err := scanMetas(rows)
	if err != nil {
		return nil, nil, err
	}
	for _, m := range metas {
		if m.Target == guid {
			incoming = append(incoming, m)
		}
		if m.Source == guid {
			outgoing = append(outgoing, m)
		}
	}
	return incoming, outgoing, nil
}

func (p *pgProjection) CommentsFor(subject string) ([]*Meta, error) {
	rows, err := p.db.Query(`SELECT `+metaCols+` FROM artifacts
		WHERE kind = 'comment' AND subject = $1 ORDER BY file_path, guid`, subject)
	if err != nil {
		return nil, dbErr(err)
	}
	return scanMetas(rows)
}

func (p *pgProjection) HIDOwner(hid string) (string, bool, error) {
	var guid string
	err := p.db.QueryRow(`SELECT guid FROM artifacts WHERE hid = $1 LIMIT 1`, hid).Scan(&guid)
	switch {
	case err == sql.ErrNoRows:
		return "", false, nil
	case err != nil:
		return "", false, dbErr(err)
	}
	return guid, true, nil
}

func (p *pgProjection) MaxHIDNumber(prefix string) (int, error) {
	rows, err := p.db.Query(`SELECT hid FROM artifacts WHERE hid LIKE $1`, escapeLike(prefix)+"-%")
	if err != nil {
		return 0, dbErr(err)
	}
	defer rows.Close()
	max := 0
	for rows.Next() {
		var hid string
		if err := rows.Scan(&hid); err != nil {
			return 0, dbErr(err)
		}
		if n, ok := hidNumber(hid, prefix); ok && n > max {
			max = n
		}
	}
	if err := rows.Err(); err != nil {
		return 0, dbErr(err)
	}
	return max, nil
}

func (p *pgProjection) Folders() ([]string, error) {
	rows, err := p.db.Query(`SELECT DISTINCT folder FROM artifacts
		UNION SELECT DISTINCT scope FROM config_files`)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	folders := map[string]bool{}
	for rows.Next() {
		var f string
		if err := rows.Scan(&f); err != nil {
			return nil, dbErr(err)
		}
		folders[f] = true
	}
	if err := rows.Err(); err != nil {
		return nil, dbErr(err)
	}
	return withAncestors(folders), nil
}

func (p *pgProjection) SchemaDefs(typ string, scopes []string) ([]*Schema, error) {
	rows, err := p.db.Query(`SELECT scope, definition FROM config_files
		WHERE category = 'schema' AND scope = ANY($1)
		  AND definition->>'artifactType' = $2
		ORDER BY file_path`, pq.Array(scopes), typ)
	if err != nil {
		return nil, dbErr(err)
	}
	byScope, err := scanSchemas(rows)
	if err != nil {
		return nil, err
	}
	var defs []*Schema // composition order: root -> leaf, as given by scopes
	for _, scope := range scopes {
		defs = append(defs, byScope[scope]...)
	}
	return defs, nil
}

func (p *pgProjection) SchemasByScope() (map[string][]*Schema, error) {
	rows, err := p.db.Query(`SELECT scope, definition FROM config_files
		WHERE category = 'schema' ORDER BY file_path`)
	if err != nil {
		return nil, dbErr(err)
	}
	return scanSchemas(rows)
}

func scanSchemas(rows *sql.Rows) (map[string][]*Schema, error) {
	defer rows.Close()
	byScope := map[string][]*Schema{}
	for rows.Next() {
		var scope string
		var def []byte
		if err := rows.Scan(&scope, &def); err != nil {
			return nil, dbErr(err)
		}
		var s Schema
		if json.Unmarshal(def, &s) == nil {
			byScope[scope] = append(byScope[scope], &s)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, dbErr(err)
	}
	return byScope, nil
}

func (p *pgProjection) Workflow(id string, scopes []string) (*Workflow, error) {
	rows, err := p.db.Query(`SELECT scope, definition FROM config_files
		WHERE category = 'workflow' AND scope = ANY($1)
		  AND definition->>'id' = $2
		ORDER BY file_path`, pq.Array(scopes), id)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	byScope := map[string]*Workflow{}
	for rows.Next() {
		var scope string
		var def []byte
		if err := rows.Scan(&scope, &def); err != nil {
			return nil, dbErr(err)
		}
		var w Workflow
		if json.Unmarshal(def, &w) == nil {
			byScope[scope] = &w
		}
	}
	if err := rows.Err(); err != nil {
		return nil, dbErr(err)
	}
	for i := len(scopes) - 1; i >= 0; i-- { // nearest scope wins
		if w, ok := byScope[scopes[i]]; ok {
			return w, nil
		}
	}
	return nil, nil
}

func (p *pgProjection) Close() error { return p.db.Close() }
