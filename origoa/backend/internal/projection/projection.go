// Package projection maintains the PostgreSQL projection of the Git
// repository: GUID resolution, hierarchy index, metadata projection,
// field index, full-text search, link and comment summaries, HID history
// and deleted-artifact tracking.
//
// The database is never authoritative; everything here can be rebuilt
// from Git. Plain SQL is used throughout — no ORM.
package projection

import (
	"context"
	_ "embed"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schemaSQL string

// DB wraps the connection pool.
type DB struct {
	Pool *pgxpool.Pool
}

// Connect opens the pool and applies the projection schema.
func Connect(ctx context.Context, dsn string) (*DB, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if _, err := pool.Exec(ctx, schemaSQL); err != nil {
		pool.Close()
		return nil, fmt.Errorf("projection: apply schema: %w", err)
	}
	return &DB{Pool: pool}, nil
}

// ConnectWithRetry is Connect with a bounded startup retry, so the backend
// can start alongside a database that is still coming up (for example under
// container orchestration) instead of crashing on the first refused
// connection. It gives up when ctx is cancelled or the deadline passes.
func ConnectWithRetry(ctx context.Context, dsn string, timeout time.Duration) (*DB, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for attempt := 0; ; attempt++ {
		db, err := Connect(ctx, dsn)
		if err == nil {
			return db, nil
		}
		lastErr = err
		if time.Now().After(deadline) || ctx.Err() != nil {
			return nil, fmt.Errorf("projection: database not reachable after %s: %w", timeout, lastErr)
		}
		wait := time.Duration(min(attempt+1, 5)) * time.Second
		log.Printf("projection: database not ready (%v); retrying in %s", err, wait)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
}

// Close releases the pool.
func (db *DB) Close() { db.Pool.Close() }

// ftsCap bounds full-text input. PostgreSQL rejects a tsvector larger than
// 1 MiB; capping the source text keeps a hostile multi-megabyte value from
// wedging the projection while still indexing a generous prefix.
const ftsCap = 900_000

// sanitize strips NUL bytes, which PostgreSQL cannot store in a text or
// tsvector column. Projected columns are derived display values; the
// authoritative bytes are always preserved verbatim in Git.
func sanitize(s string) string {
	if strings.IndexByte(s, 0) < 0 {
		return s
	}
	return strings.ReplaceAll(s, "\x00", "")
}

// clampFTS sanitizes and truncates text destined for a tsvector on a rune
// boundary, keeping the result valid UTF-8.
func clampFTS(s string) string {
	s = sanitize(s)
	if len(s) <= ftsCap {
		return s
	}
	cut := ftsCap
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// ---- ltree path encoding ----

var ltreeSafe = regexp.MustCompile(`^[A-Za-z0-9_]{1,60}$`)

// EncodePath converts a repository folder path ("a/b c/d") into an ltree
// path rooted at label "r". Segments that are not ltree-safe (or that
// collide with the escape prefix) are hex-encoded behind "x_", which
// keeps the mapping deterministic and collision-free.
func EncodePath(p string) string {
	p = strings.Trim(p, "/")
	if p == "" {
		return "r"
	}
	segs := strings.Split(p, "/")
	out := make([]string, 0, len(segs)+1)
	out = append(out, "r")
	for _, s := range segs {
		if ltreeSafe.MatchString(s) && !strings.HasPrefix(s, "x_") {
			out = append(out, s)
			continue
		}
		out = append(out, "x_"+fmt.Sprintf("%x", []byte(s)))
	}
	return strings.Join(out, ".")
}

// Artifact is the projected metadata of one native repository artifact.
type Artifact struct {
	GUID          string
	Kind          string // entry | document | link | comment
	Type          string
	Title         string
	HID           string
	RepoPath      string // GUID directory (entries/documents) or metadata file (links/comments)
	ParentPath    string // enclosing folder
	Content       []byte // raw JSON
	UpdatedCommit string
}

// UpsertArtifact writes the artifact projection row.
func (db *DB) UpsertArtifact(ctx context.Context, tx pgx.Tx, a Artifact) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO artifacts (guid, kind, type, title, hid, repo_path, parent_path, ltpath, content, updated_commit, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8::ltree,$9,$10, now())
		ON CONFLICT (guid) DO UPDATE SET
		  kind=$2, type=$3, title=$4, hid=$5, repo_path=$6, parent_path=$7,
		  ltpath=$8::ltree, content=$9, updated_commit=$10, updated_at=now()`,
		a.GUID, sanitize(a.Kind), sanitize(a.Type), sanitize(a.Title), sanitize(a.HID),
		a.RepoPath, a.ParentPath, EncodePath(a.ParentPath), sanitize(string(a.Content)), a.UpdatedCommit)
	if err != nil {
		return fmt.Errorf("upsert artifact %s: %w", a.GUID, err)
	}
	// Remove a possible tombstone if the artifact reappears.
	_, err = tx.Exec(ctx, `DELETE FROM deleted_artifacts WHERE guid=$1`, a.GUID)
	return err
}

// ArtifactRepoPath resolves GUID → storage path. Returns "" if unknown.
func (db *DB) ArtifactRepoPath(ctx context.Context, tx pgx.Tx, guid string) (string, error) {
	var p string
	err := queryRow(ctx, db, tx, `SELECT repo_path FROM artifacts WHERE guid=$1`, []any{guid}, &p)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	return p, err
}

func queryRow(ctx context.Context, db *DB, tx pgx.Tx, sql string, args []any, dest ...any) error {
	if tx != nil {
		return tx.QueryRow(ctx, sql, args...).Scan(dest...)
	}
	return db.Pool.QueryRow(ctx, sql, args...).Scan(dest...)
}

// DeleteArtifact removes the projection row and records a tombstone.
func (db *DB) DeleteArtifact(ctx context.Context, tx pgx.Tx, guid, deletedInCommit string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO deleted_artifacts (guid, kind, type, title, hid, last_path, deleted_in_commit)
		SELECT guid, kind, type, title, hid, repo_path, $2 FROM artifacts WHERE guid=$1
		ON CONFLICT (guid) DO UPDATE SET deleted_in_commit=$2, last_path=EXCLUDED.last_path,
		  kind=EXCLUDED.kind, type=EXCLUDED.type, title=EXCLUDED.title, hid=EXCLUDED.hid`,
		guid, deletedInCommit)
	if err != nil {
		return err
	}
	for _, stmt := range []string{
		`DELETE FROM artifacts WHERE guid=$1`,
		`DELETE FROM field_index WHERE guid=$1`,
		`DELETE FROM fts WHERE guid=$1`,
		`DELETE FROM link_index WHERE guid=$1`,
		`DELETE FROM comment_index WHERE guid=$1`,
	} {
		if _, err := tx.Exec(ctx, stmt, guid); err != nil {
			return err
		}
	}
	return nil
}

// ReplaceFieldIndex replaces the indexed key/value pairs of an artifact.
func (db *DB) ReplaceFieldIndex(ctx context.Context, tx pgx.Tx, guid string, fields map[string][]string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM field_index WHERE guid=$1`, guid); err != nil {
		return err
	}
	for field, values := range fields {
		for _, v := range values {
			if v == "" {
				continue
			}
			if _, err := tx.Exec(ctx,
				`INSERT INTO field_index (guid, field, value) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`,
				guid, sanitize(field), sanitize(v)); err != nil {
				return err
			}
		}
	}
	return nil
}

// UpsertFTS replaces the full-text search document of an artifact.
func (db *DB) UpsertFTS(ctx context.Context, tx pgx.Tx, guid, text string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO fts (guid, tsv) VALUES ($1, to_tsvector('simple', $2))
		ON CONFLICT (guid) DO UPDATE SET tsv=to_tsvector('simple', $2)`, guid, clampFTS(text))
	return err
}

// UpsertLinkIndex writes the link summary row.
func (db *DB) UpsertLinkIndex(ctx context.Context, tx pgx.Tx, guid, typ, source, target string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO link_index (guid, type, source, target) VALUES ($1,$2,NULLIF($3,'')::uuid,NULLIF($4,'')::uuid)
		ON CONFLICT (guid) DO UPDATE SET type=$2, source=NULLIF($3,'')::uuid, target=NULLIF($4,'')::uuid`,
		guid, typ, source, target)
	return err
}

// UpsertCommentIndex writes the comment summary row.
func (db *DB) UpsertCommentIndex(ctx context.Context, tx pgx.Tx, guid, subject, parent string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO comment_index (guid, subject, parent) VALUES ($1,NULLIF($2,'')::uuid,NULLIF($3,'')::uuid)
		ON CONFLICT (guid) DO UPDATE SET subject=NULLIF($2,'')::uuid, parent=NULLIF($3,'')::uuid`,
		guid, subject, parent)
	return err
}

// RecordHID appends to the HID history.
func (db *DB) RecordHID(ctx context.Context, tx pgx.Tx, guid, hid, commit string) error {
	if hid == "" {
		return nil
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO hid_history (guid, hid, commit_hash) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`,
		guid, hid, commit)
	return err
}

// LatestHID returns the most recently recorded HID for an artifact, or ""
// when none has been recorded. Ordering by seq (not changed_at, which is
// constant within a transaction) makes this correct during a reindex that
// reconstructs the whole history in one transaction.
func (db *DB) LatestHID(ctx context.Context, tx pgx.Tx, guid string) (string, error) {
	var hid string
	err := queryRow(ctx, db, tx,
		`SELECT hid FROM hid_history WHERE guid=$1 ORDER BY seq DESC LIMIT 1`, []any{guid}, &hid)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	return hid, err
}

// DropFTSIndex / CreateFTSIndex bracket a bulk full-text rebuild: dropping
// the GIN index before repopulating the fts table and recreating it
// afterwards is dramatically faster than maintaining the index row-by-row,
// as the reindex algorithm prescribes. Both run inside the reindex
// transaction, so a rollback restores the original index.
func (db *DB) DropFTSIndex(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `DROP INDEX IF EXISTS fts_gin`)
	return err
}

// CreateFTSIndex recreates the full-text GIN index after a bulk rebuild.
func (db *DB) CreateFTSIndex(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `CREATE INDEX IF NOT EXISTS fts_gin ON fts USING gin (tsv)`)
	return err
}

// BoostMaintenanceMem raises maintenance_work_mem for the current
// transaction so the full-text index build (and any other maintenance work
// in the reindex) can use more memory, as the reindex algorithm prescribes.
// SET LOCAL scopes the change to the transaction; it reverts on commit.
func (db *DB) BoostMaintenanceMem(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SET LOCAL maintenance_work_mem = '256MB'`)
	return err
}

// EnsureFolders inserts the folder and all its ancestors.
func (db *DB) EnsureFolders(ctx context.Context, tx pgx.Tx, folder string) error {
	folder = strings.Trim(folder, "/")
	if folder == "" {
		return nil
	}
	segs := strings.Split(folder, "/")
	for i := 1; i <= len(segs); i++ {
		p := strings.Join(segs[:i], "/")
		if _, err := tx.Exec(ctx, `
			INSERT INTO folders (path, ltpath) VALUES ($1,$2::ltree) ON CONFLICT DO NOTHING`,
			p, EncodePath(p)); err != nil {
			return err
		}
	}
	return nil
}

// PruneFolders removes folder rows that no longer contain artifacts,
// configuration objects or child folders. Called after deletions.
func (db *DB) PruneFolders(ctx context.Context, tx pgx.Tx) error {
	// Repeated single-level pruning until stable (bounded by tree depth).
	for i := 0; i < 64; i++ {
		ct, err := tx.Exec(ctx, `
			DELETE FROM folders f WHERE
			  NOT EXISTS (SELECT 1 FROM folders c   WHERE c.ltpath <@ f.ltpath AND c.path <> f.path)
			  AND NOT EXISTS (SELECT 1 FROM artifacts a WHERE a.ltpath <@ f.ltpath)
			  AND NOT EXISTS (SELECT 1 FROM config_objects o WHERE o.scope_lt <@ f.ltpath)`)
		if err != nil {
			return err
		}
		if ct.RowsAffected() == 0 {
			return nil
		}
	}
	return nil
}

// UpsertConfigObject stores a configuration object (schema, workflow, ...).
func (db *DB) UpsertConfigObject(ctx context.Context, tx pgx.Tx, storagePath, scopePath, category, name string, content []byte) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO config_objects (storage_path, scope_path, scope_lt, category, name, content)
		VALUES ($1,$2,$3::ltree,$4,$5,$6)
		ON CONFLICT (storage_path) DO UPDATE SET scope_path=$2, scope_lt=$3::ltree, category=$4, name=$5, content=$6`,
		storagePath, scopePath, EncodePath(scopePath), sanitize(category), sanitize(name), sanitize(string(content)))
	return err
}

// DeleteConfigObject removes a configuration object row.
func (db *DB) DeleteConfigObject(ctx context.Context, tx pgx.Tx, storagePath string) error {
	_, err := tx.Exec(ctx, `DELETE FROM config_objects WHERE storage_path=$1`, storagePath)
	return err
}

// ConfigObject is a projected configuration object.
type ConfigObject struct {
	StoragePath string
	ScopePath   string
	Category    string
	Name        string
	Content     []byte
}

// ConfigObjectsAlongPath returns all configuration objects of a category
// whose scope contains the given folder, ordered from the repository root
// towards the folder (lexical resolution order).
func (db *DB) ConfigObjectsAlongPath(ctx context.Context, tx pgx.Tx, category, folder string) ([]ConfigObject, error) {
	sql := `
		SELECT storage_path, scope_path, category, name, content::text
		FROM config_objects
		WHERE category=$1 AND $2::ltree <@ scope_lt
		ORDER BY nlevel(scope_lt) ASC, name ASC`
	args := []any{category, EncodePath(folder)}
	var rows pgx.Rows
	var err error
	if tx != nil {
		rows, err = tx.Query(ctx, sql, args...)
	} else {
		rows, err = db.Pool.Query(ctx, sql, args...)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ConfigObject
	for rows.Next() {
		var o ConfigObject
		var content string
		if err := rows.Scan(&o.StoragePath, &o.ScopePath, &o.Category, &o.Name, &content); err != nil {
			return nil, err
		}
		o.Content = []byte(content)
		out = append(out, o)
	}
	return out, rows.Err()
}

// ProcessedHash returns the Git revision the projection represents.
func (db *DB) ProcessedHash(ctx context.Context, tx pgx.Tx) (string, error) {
	var h string
	err := queryRow(ctx, db, tx, `SELECT processed_hash FROM repo_state WHERE id=1`, nil, &h)
	return h, err
}

// SetProcessedHash records the Git revision the projection represents.
// This runs inside the repository update transaction, immediately after
// the Git branch reference was published.
func (db *DB) SetProcessedHash(ctx context.Context, tx pgx.Tx, hash string) error {
	_, err := tx.Exec(ctx, `UPDATE repo_state SET processed_hash=$1 WHERE id=1`, hash)
	return err
}

// Reset clears every projection table except repo_state (used by reindex).
func (db *DB) Reset(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `
		TRUNCATE artifacts, field_index, fts, link_index, comment_index,
		         hid_history, deleted_artifacts, folders, config_objects`)
	return err
}

// HIDExists checks HID uniqueness within the repository.
func (db *DB) HIDExists(ctx context.Context, tx pgx.Tx, hid, excludeGUID string) (bool, error) {
	var n int
	err := queryRow(ctx, db, tx,
		`SELECT count(*) FROM artifacts
		 WHERE hid=$1 AND ($2 = '' OR guid <> $2::uuid)`, []any{hid, excludeGUID}, &n)
	return n > 0, err
}

// NextHIDNumber returns 1 + the highest numeric suffix used with prefix
// ("REQ-" → scans hid values and hid history for "REQ-<n>").
func (db *DB) NextHIDNumber(ctx context.Context, tx pgx.Tx, prefix string) (int, error) {
	sql := `
		SELECT COALESCE(MAX(n), 0) FROM (
		  SELECT (substring(hid from '^' || $1 || '-([0-9]+)$'))::int AS n FROM artifacts WHERE hid LIKE $1 || '-%'
		  UNION ALL
		  SELECT (substring(hid from '^' || $1 || '-([0-9]+)$'))::int AS n FROM hid_history WHERE hid LIKE $1 || '-%'
		  UNION ALL
		  SELECT (substring(hid from '^' || $1 || '-([0-9]+)$'))::int AS n FROM deleted_artifacts WHERE hid LIKE $1 || '-%'
		) s WHERE n IS NOT NULL`
	var max int
	if err := queryRow(ctx, db, tx, sql, []any{prefix}, &max); err != nil {
		return 0, err
	}
	return max + 1, nil
}
