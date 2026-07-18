package projection

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// ArtifactRow is a projected artifact returned by queries.
type ArtifactRow struct {
	GUID          string `json:"guid"`
	Kind          string `json:"kind"`
	Type          string `json:"type"`
	Title         string `json:"title"`
	HID           string `json:"hid,omitempty"`
	RepoPath      string `json:"repoPath"`
	ParentPath    string `json:"parentPath"`
	UpdatedCommit string `json:"updatedCommit,omitempty"`
	LinkCount     int    `json:"linkCount"`
	CommentCount  int    `json:"commentCount"`
	Content       []byte `json:"-"`
}

const artifactCols = `
	a.guid::text, a.kind, a.type, a.title, a.hid, a.repo_path, a.parent_path, a.updated_commit, a.content::text,
	(SELECT count(*) FROM link_index l WHERE l.source=a.guid OR l.target=a.guid) AS link_count,
	(SELECT count(*) FROM comment_index c WHERE c.subject=a.guid) AS comment_count`

func scanArtifactRows(rows pgx.Rows) ([]ArtifactRow, error) {
	defer rows.Close()
	var out []ArtifactRow
	for rows.Next() {
		var a ArtifactRow
		var content string
		if err := rows.Scan(&a.GUID, &a.Kind, &a.Type, &a.Title, &a.HID, &a.RepoPath,
			&a.ParentPath, &a.UpdatedCommit, &content, &a.LinkCount, &a.CommentCount); err != nil {
			return nil, err
		}
		a.Content = []byte(content)
		out = append(out, a)
	}
	return out, rows.Err()
}

// GetArtifact loads one projected artifact by GUID.
func (db *DB) GetArtifact(ctx context.Context, guid string) (*ArtifactRow, error) {
	rows, err := db.Pool.Query(ctx,
		`SELECT `+artifactCols+` FROM artifacts a WHERE a.guid=$1`, guid)
	if err != nil {
		return nil, err
	}
	list, err := scanArtifactRows(rows)
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return &list[0], nil
}

// GetArtifactByHID loads one projected artifact by its current HID.
func (db *DB) GetArtifactByHID(ctx context.Context, hid string) (*ArtifactRow, error) {
	rows, err := db.Pool.Query(ctx,
		`SELECT `+artifactCols+` FROM artifacts a WHERE a.hid=$1`, hid)
	if err != nil {
		return nil, err
	}
	list, err := scanArtifactRows(rows)
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return &list[0], nil
}

// SearchQuery describes a repository search / filter request.
type SearchQuery struct {
	Text    string            // full-text query, empty = no text filter
	Kind    string            // entry | document | link | comment
	Type    string            // artifact type
	Folder  string            // restrict to folder
	Subtree bool              // include nested folders below Folder
	Fields  map[string]string // schema-defined indexed field equals-filters
	Limit   int
}

// Search runs a combined metadata / field / full-text query.
func (db *DB) Search(ctx context.Context, q SearchQuery) ([]ArtifactRow, error) {
	var (
		where []string
		args  []any
	)
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	if q.Kind != "" {
		where = append(where, "a.kind="+arg(q.Kind))
	}
	if q.Type != "" {
		where = append(where, "a.type="+arg(q.Type))
	}
	if q.Folder != "" || q.Subtree {
		if q.Subtree {
			where = append(where, "a.ltpath <@ "+arg(EncodePath(q.Folder))+"::ltree")
		} else {
			where = append(where, "a.parent_path="+arg(strings.Trim(q.Folder, "/")))
		}
	}
	if q.Text != "" {
		where = append(where,
			"a.guid IN (SELECT guid FROM fts WHERE tsv @@ websearch_to_tsquery('simple', "+arg(q.Text)+"))")
	}
	for field, value := range q.Fields {
		where = append(where,
			"a.guid IN (SELECT guid FROM field_index WHERE field="+arg(field)+" AND value="+arg(value)+")")
	}
	sql := `SELECT ` + artifactCols + ` FROM artifacts a`
	if len(where) > 0 {
		sql += " WHERE " + strings.Join(where, " AND ")
	}
	limit := q.Limit
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	sql += " ORDER BY a.parent_path, a.title, a.guid LIMIT " + arg(limit)
	rows, err := db.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return scanArtifactRows(rows)
}

// Folder is a projected repository folder.
type Folder struct {
	Path string `json:"path"`
	Name string `json:"name"`
}

// ChildFolders lists the immediate sub-folders of a folder.
func (db *DB) ChildFolders(ctx context.Context, folder string) ([]Folder, error) {
	lt := EncodePath(folder)
	rows, err := db.Pool.Query(ctx, `
		SELECT path FROM folders
		WHERE ltpath <@ $1::ltree AND nlevel(ltpath) = nlevel($1::ltree) + 1
		ORDER BY path`, lt)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Folder
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		name := p
		if i := strings.LastIndex(p, "/"); i >= 0 {
			name = p[i+1:]
		}
		out = append(out, Folder{Path: p, Name: name})
	}
	return out, rows.Err()
}

// TypeCount summarizes artifacts per (kind, type) for type-based navigation.
type TypeCount struct {
	Kind  string `json:"kind"`
	Type  string `json:"type"`
	Count int    `json:"count"`
}

// TypeCounts lists artifact types below a folder.
func (db *DB) TypeCounts(ctx context.Context, folder string) ([]TypeCount, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT kind, type, count(*) FROM artifacts
		WHERE ltpath <@ $1::ltree AND kind IN ('entry','document')
		GROUP BY kind, type ORDER BY kind, type`, EncodePath(folder))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TypeCount
	for rows.Next() {
		var t TypeCount
		if err := rows.Scan(&t.Kind, &t.Type, &t.Count); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// LinkRow is a projected link with resolved endpoint summaries.
type LinkRow struct {
	GUID        string `json:"guid"`
	Type        string `json:"type"`
	Source      string `json:"source"`
	Target      string `json:"target"`
	SourceTitle string `json:"sourceTitle,omitempty"`
	TargetTitle string `json:"targetTitle,omitempty"`
	Content     []byte `json:"-"`
}

// LinksFor lists all links where the artifact is source or target.
func (db *DB) LinksFor(ctx context.Context, guid string) ([]LinkRow, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT l.guid::text, l.type, COALESCE(l.source::text,''), COALESCE(l.target::text,''),
		       COALESCE(s.title,''), COALESCE(t.title,''), COALESCE(a.content::text, '{}')
		FROM link_index l
		LEFT JOIN artifacts s ON s.guid=l.source
		LEFT JOIN artifacts t ON t.guid=l.target
		LEFT JOIN artifacts a ON a.guid=l.guid
		WHERE l.source=$1::uuid OR l.target=$1::uuid
		ORDER BY l.type, l.guid`, guid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LinkRow
	for rows.Next() {
		var l LinkRow
		var content string
		if err := rows.Scan(&l.GUID, &l.Type, &l.Source, &l.Target, &l.SourceTitle, &l.TargetTitle, &content); err != nil {
			return nil, err
		}
		l.Content = []byte(content)
		out = append(out, l)
	}
	return out, rows.Err()
}

// CommentRow is a projected comment.
type CommentRow struct {
	GUID    string `json:"guid"`
	Subject string `json:"subject"`
	Parent  string `json:"parent,omitempty"`
	Content []byte `json:"-"`
}

// CommentsFor lists comments attached to an artifact.
func (db *DB) CommentsFor(ctx context.Context, subject string) ([]CommentRow, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT c.guid::text, COALESCE(c.subject::text,''), COALESCE(c.parent::text,''),
		       COALESCE(a.content::text,'{}')
		FROM comment_index c
		LEFT JOIN artifacts a ON a.guid=c.guid
		WHERE c.subject=$1::uuid
		ORDER BY a.updated_at, c.guid`, subject)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CommentRow
	for rows.Next() {
		var c CommentRow
		var content string
		if err := rows.Scan(&c.GUID, &c.Subject, &c.Parent, &content); err != nil {
			return nil, err
		}
		c.Content = []byte(content)
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeletedArtifact is a tombstone row.
type DeletedArtifact struct {
	GUID            string `json:"guid"`
	Kind            string `json:"kind"`
	Type            string `json:"type"`
	Title           string `json:"title"`
	HID             string `json:"hid,omitempty"`
	LastPath        string `json:"lastPath"`
	DeletedInCommit string `json:"deletedInCommit"`
}

// GetDeletedArtifact returns the tombstone for a GUID, or nil.
func (db *DB) GetDeletedArtifact(ctx context.Context, guid string) (*DeletedArtifact, error) {
	var d DeletedArtifact
	err := db.Pool.QueryRow(ctx, `
		SELECT guid::text, kind, type, title, hid, last_path, deleted_in_commit
		FROM deleted_artifacts WHERE guid=$1`, guid).
		Scan(&d.GUID, &d.Kind, &d.Type, &d.Title, &d.HID, &d.LastPath, &d.DeletedInCommit)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// HIDHistoryEntry is one historical HID assignment.
type HIDHistoryEntry struct {
	HID        string `json:"hid"`
	CommitHash string `json:"commit"`
}

// HIDHistory lists all HIDs ever assigned to an artifact.
func (db *DB) HIDHistory(ctx context.Context, guid string) ([]HIDHistoryEntry, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT hid, commit_hash FROM hid_history WHERE guid=$1 ORDER BY seq`, guid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HIDHistoryEntry
	for rows.Next() {
		var e HIDHistoryEntry
		if err := rows.Scan(&e.HID, &e.CommitHash); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Stats summarizes the repository.
type Stats struct {
	Entries   int    `json:"entries"`
	Documents int    `json:"documents"`
	Links     int    `json:"links"`
	Comments  int    `json:"comments"`
	Folders   int    `json:"folders"`
	Deleted   int    `json:"deleted"`
	Revision  string `json:"revision"`
}

// GetStats computes repository statistics.
func (db *DB) GetStats(ctx context.Context) (Stats, error) {
	var s Stats
	err := db.Pool.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM artifacts WHERE kind='entry'),
		  (SELECT count(*) FROM artifacts WHERE kind='document'),
		  (SELECT count(*) FROM artifacts WHERE kind='link'),
		  (SELECT count(*) FROM artifacts WHERE kind='comment'),
		  (SELECT count(*) FROM folders),
		  (SELECT count(*) FROM deleted_artifacts),
		  (SELECT processed_hash FROM repo_state WHERE id=1)`).
		Scan(&s.Entries, &s.Documents, &s.Links, &s.Comments, &s.Folders, &s.Deleted, &s.Revision)
	return s, err
}

// ArtifactsUnder lists projected artifacts whose repo_path lies below a
// folder (used for impact analysis of structural operations).
func (db *DB) ArtifactsUnder(ctx context.Context, tx pgx.Tx, folder string) ([]ArtifactRow, error) {
	sql := `SELECT ` + artifactCols + ` FROM artifacts a WHERE a.ltpath <@ $1::ltree`
	var rows pgx.Rows
	var err error
	if tx != nil {
		rows, err = tx.Query(ctx, sql, EncodePath(folder))
	} else {
		rows, err = db.Pool.Query(ctx, sql, EncodePath(folder))
	}
	if err != nil {
		return nil, err
	}
	return scanArtifactRows(rows)
}
