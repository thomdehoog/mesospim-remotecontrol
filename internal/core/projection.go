package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/thomdehoog/origoa/internal/gitx"
	"github.com/thomdehoog/origoa/internal/ojson"
)

// Projection is the derived query layer over the Git repository. It is never
// authoritative: every implementation must be fully reconstructable from the
// repository via Sync. Reads fail closed — a projection that cannot answer
// returns an error, never a fabricated empty result.
type Projection interface {
	// Head returns the Git revision the projection represents ("" if none).
	Head() string
	// Sync rebuilds the entire projection from the repository HEAD.
	Sync() error
	// Apply projects the changes of one published commit whose parent is
	// parentHead. If the projection is not at parentHead it applies nothing
	// and returns errStaleProjection — the caller falls back to Sync. This
	// mirrors the Git CAS and prevents processed revisions from silently
	// skipping commits (two writers sharing one projection database).
	Apply(parentHead, newHead string, changes []Change) error

	Get(guid string) (*Meta, error) // nil, nil when absent
	List(q ListQuery) ([]*Meta, error)
	LinksFor(guid string) (incoming, outgoing []*Meta, err error)
	CommentsFor(subject string) ([]*Meta, error)
	HIDOwner(hid string) (guid string, ok bool, err error)
	MaxHIDNumber(prefix string) (int, error)
	Folders() ([]string, error)
	// SchemaDefs returns definitions for typ in the given scopes, ordered
	// root -> leaf (composition order).
	SchemaDefs(typ string, scopes []string) ([]*Schema, error)
	SchemasByScope() (map[string][]*Schema, error)
	// Workflow resolves a workflow definition; the nearest scope wins.
	Workflow(id string, scopes []string) (*Workflow, error)

	Close() error
}

// errStaleProjection: Apply was called against a projection that does not
// represent the commit's parent revision.
var errStaleProjection = errors.New("projection is not at the commit's parent revision")

// Change is one file-level repository change to project.
type Change struct {
	Path    string
	SHA     string
	Content []byte // nil for deletions
	Delete  bool
}

// ListQuery filters artifact listings. Text is a case-insensitive search
// term; empty matches everything. Limit 0 means unlimited.
type ListQuery struct {
	Kind    string
	Type    string
	Folder  string
	Subtree bool
	Text    string
	Limit   int
}

func (q ListQuery) matches(m *Meta, searchText string) bool {
	if q.Kind != "" && m.Kind != q.Kind {
		return false
	}
	if q.Type != "" && m.Type != q.Type {
		return false
	}
	if q.Folder != "" || !q.Subtree {
		if q.Subtree {
			if m.Folder != q.Folder && !strings.HasPrefix(m.Folder, q.Folder+"/") {
				return false
			}
		} else if m.Folder != q.Folder {
			return false
		}
	}
	return q.Text == "" || strings.Contains(searchText, strings.ToLower(q.Text))
}

// record is the classified content of one repository file.
type record struct {
	filePath string
	meta     *Meta  // set for artifacts of any kind
	text     string // searchable text for artifacts
	scope    string // set for configuration files
	category string // "schema" | "workflow"
	schema   *Schema
	workflow *Workflow
	raw      json.RawMessage
}

// relevantPath cheaply decides whether a repository file can matter to the
// projection, so Sync never fetches blobs of unrelated content (large
// binaries, foreign files).
func relevantPath(filePath string) bool {
	if path.Base(filePath) == ArtifactFile {
		return true
	}
	p := "/" + filePath
	return strings.Contains(p, "/"+MetaDir+"/links/") ||
		strings.Contains(p, "/"+MetaDir+"/comments/") ||
		strings.Contains(p, "/"+MetaDir+"/schemas/") ||
		strings.Contains(p, "/"+MetaDir+"/workflows/")
}

// syncRecords loads and classifies every relevant file at HEAD. Shared by
// both projection implementations.
func syncRecords(g *gitx.Repo) (head string, recs []*record, err error) {
	head, err = g.Head()
	if err != nil {
		return "", nil, err
	}
	entries, err := g.ListTree(head, "")
	if err != nil {
		return "", nil, err
	}
	var relevant []gitx.TreeEntry
	shas := make([]string, 0, len(entries))
	for _, e := range entries {
		if relevantPath(e.Path) {
			relevant = append(relevant, e)
			shas = append(shas, e.SHA)
		}
	}
	blobs, err := g.ReadBlobs(shas)
	if err != nil {
		return "", nil, err
	}
	for _, e := range relevant { // ls-tree order is path-sorted: deterministic
		if rec := classify(e.Path, e.SHA, blobs[e.SHA]); rec != nil {
			recs = append(recs, rec)
		}
	}
	return head, recs, nil
}

// classify inspects one repository file and returns its projected record, or
// nil if the file is irrelevant or malformed. The projection must tolerate
// arbitrary direct Git modifications, so malformed files are skipped, never
// fatal, and all projected text is sanitized (no NUL bytes, bounded size) so
// that no repository content can wedge a projection backend.
func classify(filePath, sha string, content []byte) *record {
	dir, base := path.Split(filePath)
	dir = strings.TrimSuffix(dir, "/")

	switch {
	case base == ArtifactFile && IsGUID(path.Base(dir)):
		obj, err := ojson.Parse(content)
		if err != nil {
			return nil
		}
		m := &Meta{
			GUID:     obj.GetString("guid"),
			Kind:     obj.GetString("kind"),
			Type:     stripNUL(obj.GetString("type")),
			Title:    stripNUL(obj.GetString("title")),
			HID:      stripNUL(obj.GetString("hid")),
			Base:     obj.GetString("base"),
			FilePath: filePath,
			Folder:   parentFolder(dir),
			ETag:     sha,
		}
		if m.GUID != path.Base(dir) || (m.Kind != KindEntry && m.Kind != KindDocument) {
			return nil
		}
		if raw, ok := obj.Get("workflows"); ok {
			var wf map[string]string
			if json.Unmarshal(raw, &wf) == nil {
				m.Workflows = map[string]string{}
				for k, v := range wf {
					m.Workflows[stripNUL(k)] = stripNUL(v)
				}
			}
		}
		return &record{filePath: filePath, meta: m, text: searchText(obj)}

	case strings.Contains("/"+filePath, "/"+MetaDir+"/links/"):
		return classifyMetaArtifact(filePath, sha, content, KindLink)

	case strings.Contains("/"+filePath, "/"+MetaDir+"/comments/"):
		return classifyMetaArtifact(filePath, sha, content, KindComment)

	case strings.Contains("/"+filePath, "/"+MetaDir+"/schemas/"):
		var s Schema
		if err := json.Unmarshal(content, &s); err != nil || s.ArtifactType == "" {
			return nil
		}
		return &record{filePath: filePath, scope: scopeOf(filePath), category: "schema", schema: &s, raw: content}

	case strings.Contains("/"+filePath, "/"+MetaDir+"/workflows/"):
		var w Workflow
		if err := json.Unmarshal(content, &w); err != nil || w.ID == "" {
			return nil
		}
		return &record{filePath: filePath, scope: scopeOf(filePath), category: "workflow", workflow: &w, raw: content}
	}
	return nil
}

func classifyMetaArtifact(filePath, sha string, content []byte, kind string) *record {
	obj, err := ojson.Parse(content)
	if err != nil || obj.GetString("kind") != kind {
		return nil
	}
	m := &Meta{
		GUID:     obj.GetString("guid"),
		Kind:     kind,
		Type:     stripNUL(obj.GetString("type")),
		Source:   obj.GetString("source"),
		Target:   obj.GetString("target"),
		Subject:  obj.GetString("subject"),
		Created:  stripNUL(obj.GetString("created")),
		FilePath: filePath,
		Folder:   scopeOf(filePath),
		ETag:     sha,
	}
	if !IsGUID(m.GUID) {
		return nil
	}
	return &record{filePath: filePath, meta: m, text: searchText(obj)}
}

// scopeOf maps ".../<scope>/.origoa/xxx/file.json" to "<scope>".
func scopeOf(filePath string) string {
	i := strings.LastIndex("/"+filePath, "/"+MetaDir+"/")
	if i <= 0 {
		return ""
	}
	return filePath[:i-1]
}

func parentFolder(dir string) string {
	p := path.Dir(dir)
	if p == "." {
		return ""
	}
	return p
}

// searchTextCap bounds the searchable text extracted per artifact — both to
// keep memory predictable and to stay far under PostgreSQL's 1 MiB tsvector
// limit. Content beyond the cap is simply not searchable.
const searchTextCap = 256 << 10

// searchText flattens an artifact object into lowercase text for search
// using a single linear token scan (never recursive re-parsing), bounded by
// searchTextCap and free of NUL bytes.
func searchText(obj *ojson.Obj) string {
	var b strings.Builder
	for _, k := range obj.Keys() {
		if k == "guid" || k == "base" || k == "kind" {
			continue
		}
		raw, _ := obj.Get(k)
		dec := json.NewDecoder(bytes.NewReader(raw))
		for b.Len() < searchTextCap {
			tok, err := dec.Token()
			if err != nil {
				break
			}
			if s, ok := tok.(string); ok && s != "" {
				b.WriteString(strings.ToLower(s))
				b.WriteByte(' ')
			}
		}
		if b.Len() >= searchTextCap {
			break
		}
	}
	return stripNUL(strings.TrimSuffix(b.String(), " "))
}

// stripNUL removes NUL bytes, which PostgreSQL text columns reject — one
// hostile byte must never wedge the projection.
func stripNUL(s string) string {
	if !strings.ContainsRune(s, 0) {
		return s
	}
	return strings.ReplaceAll(s, "\x00", "")
}

// withAncestors expands a set of folders with all their ancestors, sorted.
func withAncestors(folders map[string]bool) []string {
	all := map[string]bool{}
	for f := range folders {
		for f != "" {
			all[f] = true
			f = parentFolder(f)
		}
	}
	out := make([]string, 0, len(all))
	for f := range all {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// hidNumber extracts N from "<prefix>-<N>".
func hidNumber(hid, prefix string) (int, bool) {
	rest, ok := strings.CutPrefix(hid, prefix+"-")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(rest)
	return n, err == nil
}

// escapeLike escapes SQL LIKE wildcards (default backslash escape).
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// sortMetas orders metas deterministically (path, then GUID) and applies the
// query limit.
func sortMetas(out []*Meta, limit int) []*Meta {
	sort.Slice(out, func(i, j int) bool {
		if out[i].FilePath != out[j].FilePath {
			return out[i].FilePath < out[j].FilePath
		}
		return out[i].GUID < out[j].GUID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}
