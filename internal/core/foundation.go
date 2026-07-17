package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/thomdehoog/origoa/internal/gitx"
	"github.com/thomdehoog/origoa/internal/ojson"
)

// Foundation exposes all repository operations. Git is the single source of
// truth; every write produces exactly one commit describing the logical
// operation, which is then projected into the derived query layer.
//
// Writes follow the design guide's transactional procedure (§10.1): sync the
// projection to the Git head, validate and build the changeset against that
// state, publish with a compare-and-swap, and project the result. If the
// branch moved (another process, a direct push), the whole prepare step is
// re-run against the new head — validations such as If-Match and HID
// uniqueness are therefore never applied to a stale state.
type Foundation struct {
	git  *gitx.Repo
	proj Projection
	wmu  sync.Mutex // serializes writers and reindexing in this process
}

var typeRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*$`)

// Open initializes (if needed) the bare repository at gitDir with the
// in-memory projection.
func Open(gitDir string) (*Foundation, error) {
	g, err := gitx.Init(gitDir)
	if err != nil {
		return nil, err
	}
	return open(g, newMemProjection(g))
}

// OpenPostgres initializes the repository with the PostgreSQL projection.
// When the stored processed_hash already matches the Git HEAD the projection
// is reused as-is; otherwise it is rebuilt from Git.
func OpenPostgres(gitDir, dsn string) (*Foundation, error) {
	g, err := gitx.Init(gitDir)
	if err != nil {
		return nil, err
	}
	proj, err := newPGProjection(g, dsn)
	if err != nil {
		return nil, err
	}
	return open(g, proj)
}

func open(g *gitx.Repo, proj Projection) (*Foundation, error) {
	f := &Foundation{git: g, proj: proj}
	head, err := g.Head()
	if err != nil {
		proj.Close()
		return nil, err
	}
	if head != proj.Head() {
		if err := proj.Sync(); err != nil {
			proj.Close()
			return nil, err
		}
	}
	return f, nil
}

// Reindex rebuilds all derived state from the Git HEAD. It takes the write
// lock so a concurrent writer can never be overwritten by a stale snapshot.
func (f *Foundation) Reindex() error {
	f.wmu.Lock()
	defer f.wmu.Unlock()
	return f.proj.Sync()
}

// Head returns the Git revision the projection represents.
func (f *Foundation) Head() string { return f.proj.Head() }

func (f *Foundation) Close() error { return f.proj.Close() }

// errNoChange lets a prepare step report that the operation is a no-op; no
// commit is created.
var errNoChange = errors.New("no change")

// write runs one logical repository operation. prepare is called with the
// projection synchronized to the current Git head and must perform all
// validation and build the ops; it is re-invoked from scratch whenever the
// branch moves underneath us.
func (f *Foundation) write(prepare func() (msg string, ops []gitx.Op, err error)) error {
	f.wmu.Lock()
	defer f.wmu.Unlock()

	for attempt := 0; attempt < 20; attempt++ {
		if attempt > 0 {
			// Escalating jittered backoff de-synchronizes competing writer
			// processes ping-ponging on the branch CAS.
			backoff := time.Duration(attempt)*3*time.Millisecond + time.Duration(rand.IntN(10))*time.Millisecond
			time.Sleep(backoff)
		}
		head, err := f.git.Head()
		if err != nil {
			return err
		}
		if head != f.proj.Head() {
			if err := f.proj.Sync(); err != nil {
				return err
			}
			head = f.proj.Head()
		}
		msg, ops, err := prepare()
		if errors.Is(err, errNoChange) {
			return nil
		}
		if err != nil {
			return err
		}
		newHead, err := f.git.CommitOnce(head, msg, ops)
		if errors.Is(err, gitx.ErrStale) {
			continue // branch moved: resync and re-validate
		}
		if err != nil {
			return err
		}
		changes := make([]Change, len(ops))
		for i, op := range ops {
			changes[i] = Change{Path: op.Path, Delete: op.Delete}
			if !op.Delete {
				changes[i].SHA = gitx.BlobSHA(op.Content)
				changes[i].Content = op.Content
			}
		}
		if err := f.proj.Apply(head, newHead, changes); err != nil {
			// The commit is durable in Git either way. Repair the projection
			// by full rebuild; only if that also fails is the write reported
			// as (partially) failed — the next sync self-heals.
			if serr := f.proj.Sync(); serr != nil {
				return fmt.Errorf("write committed as %.12s but projection failed: %w", newHead, serr)
			}
		}
		return nil
	}
	return fmt.Errorf("%w: repository is being modified concurrently, retry", ErrConflict)
}

// commitMsg renders the structured commit message: a human-readable subject
// plus machine-parseable trailers.
func commitMsg(subject, op, guid string) string {
	return fmt.Sprintf("%s\n\nOrigoa-Op: %s\nOrigoa-Guid: %s", subject, op, guid)
}

// ---- reads ----

func (f *Foundation) Meta(guid string) (*Meta, error) {
	m, err := f.proj.Get(guid)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, fmt.Errorf("%w: artifact %s", ErrNotFound, guid)
	}
	return m, nil
}

// Artifact returns the projected meta and the full stored object.
func (f *Foundation) Artifact(guid string) (*Meta, *ojson.Obj, error) {
	m, err := f.Meta(guid)
	if err != nil {
		return nil, nil, err
	}
	blobs, err := f.git.ReadBlobs([]string{m.ETag})
	if err != nil {
		return nil, nil, err
	}
	obj, err := ojson.Parse(blobs[m.ETag])
	if err != nil {
		return nil, nil, err
	}
	return m, obj, nil
}

// Objects fetches the stored objects for many metas in one batch read.
func (f *Foundation) Objects(metas []*Meta) ([]*ojson.Obj, error) {
	shas := make([]string, len(metas))
	for i, m := range metas {
		shas[i] = m.ETag
	}
	blobs, err := f.git.ReadBlobs(shas)
	if err != nil {
		return nil, err
	}
	objs := make([]*ojson.Obj, 0, len(metas))
	for _, m := range metas {
		if obj, err := ojson.Parse(blobs[m.ETag]); err == nil {
			objs = append(objs, obj)
		}
	}
	return objs, nil
}

// List returns artifact metas filtered by kind, type and folder. With
// subtree, artifacts in nested folders are included. limit 0 = unlimited.
func (f *Foundation) List(kind, typ, folder string, subtree bool, limit int) ([]*Meta, error) {
	return f.proj.List(ListQuery{Kind: kind, Type: typ, Folder: folder, Subtree: subtree, Limit: limit})
}

func (f *Foundation) Folders() ([]string, error) { return f.proj.Folders() }

func (f *Foundation) Search(q, kind, typ string, limit int) ([]*Meta, error) {
	return f.proj.List(ListQuery{Kind: kind, Type: typ, Subtree: true, Text: q, Limit: limit})
}

func (f *Foundation) EffectiveSchema(typ, folder string) (*Schema, error) {
	folder, err := CleanFolder(folder)
	if err != nil {
		return nil, err
	}
	s, err := f.effSchema(typ, folder)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, fmt.Errorf("%w: no schema for type %q", ErrNotFound, typ)
	}
	return s, nil
}

func (f *Foundation) effSchema(typ, folder string) (*Schema, error) {
	defs, err := f.proj.SchemaDefs(typ, scopeChain(folder))
	if err != nil {
		return nil, err
	}
	if len(defs) == 0 {
		return nil, nil
	}
	return composeSchemas(defs), nil
}

// Schemas returns all schema definitions grouped by configuration scope.
func (f *Foundation) Schemas() (map[string][]*Schema, error) { return f.proj.SchemasByScope() }

func (f *Foundation) WorkflowDef(id, folder string) (*Workflow, error) {
	w, err := f.proj.Workflow(id, scopeChain(folder))
	if err != nil {
		return nil, err
	}
	if w == nil {
		return nil, fmt.Errorf("%w: workflow %q", ErrNotFound, id)
	}
	return w, nil
}

// ResolveOverlay merges the fields of an entry with its base chain.
// The overlay's own fields win; unresolved fields come from the nearest base.
func (f *Foundation) ResolveOverlay(guid string) (fields map[string]json.RawMessage, chain []string, err error) {
	fields = map[string]json.RawMessage{}
	visited := map[string]bool{}
	current := guid
	for current != "" {
		if visited[current] {
			return nil, nil, vErr("overlay cycle at %s", current)
		}
		visited[current] = true
		chain = append(chain, current)
		m, obj, err := f.Artifact(current)
		if err != nil {
			return nil, nil, err
		}
		if m.Kind != KindEntry {
			return nil, nil, vErr("overlay base %s is not an entry", current)
		}
		if raw, ok := obj.Get("fields"); ok {
			var fm map[string]json.RawMessage
			if json.Unmarshal(raw, &fm) == nil {
				for k, v := range fm {
					if _, done := fields[k]; !done {
						fields[k] = v
					}
				}
			}
		}
		current = m.Base
	}
	return fields, chain, nil
}

// Links returns incoming and outgoing links of an artifact.
func (f *Foundation) Links(guid string) (incoming, outgoing []*Meta, err error) {
	return f.proj.LinksFor(guid)
}

// Comments returns all comments whose subject is guid, oldest first.
func (f *Foundation) Comments(guid string) ([]*Meta, error) {
	metas, err := f.proj.CommentsFor(guid)
	if err != nil {
		return nil, err
	}
	// created is RFC3339, which sorts lexically; ties break on GUID.
	sort.Slice(metas, func(i, j int) bool {
		if metas[i].Created != metas[j].Created {
			return metas[i].Created < metas[j].Created
		}
		return metas[i].GUID < metas[j].GUID
	})
	return metas, nil
}

// History returns the commit log touching an artifact — across moves, since
// the GUID directory name is globally unique.
func (f *Foundation) History(guid string, limit int) ([]gitx.LogEntry, error) {
	m, err := f.Meta(guid)
	if err != nil {
		return nil, err
	}
	pathspec := ":(literal)" + m.FilePath
	if m.Kind == KindEntry || m.Kind == KindDocument {
		pathspec = ":(glob)**/" + m.GUID + "/**"
	}
	return f.git.Log(pathspec, limit)
}

// ---- writes ----

// allowedCreateKeys and updatableKeys define the artifact body contract; the
// same unknown-property rules apply to create and update.
var (
	updatableKeys     = map[string]bool{"title": true, "hid": true, "base": true, "fields": true, "content": true}
	allowedCreateKeys = map[string]bool{"path": true, "type": true, "title": true, "hid": true, "base": true, "fields": true, "content": true}
)

// CreateArtifact creates an entry or document. body may provide title, hid,
// base, fields and (for documents) content.
func (f *Foundation) CreateArtifact(kind, folder, typ string, body *ojson.Obj) (*Meta, error) {
	if kind != KindEntry && kind != KindDocument {
		return nil, vErr("invalid kind %q", kind)
	}
	folder, err := CleanFolder(folder)
	if err != nil {
		return nil, err
	}
	if !typeRe.MatchString(typ) {
		return nil, vErr("invalid artifact type %q", typ)
	}
	for _, k := range body.Keys() {
		if !allowedCreateKeys[k] {
			return nil, vErr("unknown property %q", k)
		}
		if k == "content" && kind != KindDocument {
			return nil, vErr("only documents have content")
		}
	}
	guid := NewGUID()

	err = f.write(func() (string, []gitx.Op, error) {
		obj := ojson.New()
		obj.SetString("guid", guid)
		obj.SetString("kind", kind)
		obj.SetString("type", typ)

		schema, err := f.effSchema(typ, folder)
		if err != nil {
			return "", nil, err
		}
		hid := stripNUL(strings.TrimSpace(body.GetString("hid")))
		if hid == "" && schema != nil && schema.HIDPrefix != "" {
			max, err := f.proj.MaxHIDNumber(schema.HIDPrefix)
			if err != nil {
				return "", nil, err
			}
			hid = fmt.Sprintf("%s-%d", schema.HIDPrefix, max+1)
		}
		if hid != "" {
			if err := f.checkHID(hid, guid); err != nil {
				return "", nil, err
			}
			obj.SetString("hid", hid)
		}
		obj.SetString("title", stripNUL(strings.TrimSpace(body.GetString("title"))))

		if base := body.GetString("base"); base != "" {
			if kind != KindEntry {
				return "", nil, vErr("only entries support overlays")
			}
			if err := f.checkBase(base, guid); err != nil {
				return "", nil, err
			}
			obj.SetString("base", base)
		}

		if schema != nil && len(schema.Workflows) > 0 {
			states := map[string]string{}
			for _, wfID := range schema.Workflows {
				wf, err := f.proj.Workflow(wfID, scopeChain(folder))
				if err != nil {
					return "", nil, err
				}
				if wf != nil {
					states[wfID] = wf.Initial
				}
			}
			if len(states) > 0 {
				obj.SetAny("workflows", states)
			}
		}

		if raw, ok := body.Get("fields"); ok {
			if err := requireObject(raw, "fields"); err != nil {
				return "", nil, err
			}
			obj.Set("fields", raw)
		}
		if raw, ok := body.Get("content"); ok && kind == KindDocument {
			obj.Set("content", raw)
		}

		content, err := obj.Encode()
		if err != nil {
			return "", nil, err
		}
		filePath := path.Join(folderOrRoot(folder), guid, ArtifactFile)
		msg := commitMsg(fmt.Sprintf("%s %s in /%s created", titleKind(kind), guid, folder),
			kind+".create", guid)
		return msg, []gitx.Op{{Path: filePath, Content: content}}, nil
	})
	if err != nil {
		return nil, err
	}
	return f.Meta(guid)
}

// UpdateArtifact patches the mutable properties of an entry or document.
// ifMatch, when non-empty, must equal the artifact's current ETag.
func (f *Foundation) UpdateArtifact(guid string, patch *ojson.Obj, ifMatch string) (*Meta, error) {
	err := f.write(func() (string, []gitx.Op, error) {
		m, obj, err := f.Artifact(guid)
		if err != nil {
			return "", nil, err
		}
		if m.Kind != KindEntry && m.Kind != KindDocument {
			return "", nil, vErr("%s artifacts are immutable; delete and recreate", m.Kind)
		}
		if ifMatch != "" && ifMatch != m.ETag {
			return "", nil, fmt.Errorf("%w: artifact was modified concurrently", ErrPrecondition)
		}
		for _, k := range patch.Keys() {
			if !updatableKeys[k] {
				return "", nil, vErr("property %q cannot be updated", k)
			}
			if k == "content" && m.Kind != KindDocument {
				return "", nil, vErr("only documents have content")
			}
		}
		if patch.Has("hid") {
			hid := stripNUL(strings.TrimSpace(patch.GetString("hid")))
			if hid == "" {
				obj.Delete("hid")
			} else if hid != m.HID {
				if err := f.checkHID(hid, guid); err != nil {
					return "", nil, err
				}
				obj.SetString("hid", hid)
			}
		}
		if patch.Has("base") {
			base := patch.GetString("base")
			if base == "" {
				obj.Delete("base")
			} else {
				if m.Kind != KindEntry {
					return "", nil, vErr("only entries support overlays")
				}
				if err := f.checkBase(base, guid); err != nil {
					return "", nil, err
				}
				obj.SetString("base", base)
			}
		}
		if patch.Has("title") {
			obj.SetString("title", stripNUL(strings.TrimSpace(patch.GetString("title"))))
		}
		if raw, ok := patch.Get("fields"); ok {
			if err := requireObject(raw, "fields"); err != nil {
				return "", nil, err
			}
			obj.Set("fields", raw)
		}
		if raw, ok := patch.Get("content"); ok {
			obj.Set("content", raw)
		}
		content, err := obj.Encode()
		if err != nil {
			return "", nil, err
		}
		if gitx.BlobSHA(content) == m.ETag {
			return "", nil, errNoChange
		}
		msg := commitMsg(fmt.Sprintf("%s %s updated", titleKind(m.Kind), guid), m.Kind+".update", guid)
		return msg, []gitx.Op{{Path: m.FilePath, Content: content}}, nil
	})
	if err != nil {
		return nil, err
	}
	return f.Meta(guid)
}

// DeleteArtifact removes an artifact of any kind. Links and comments
// referencing it remain valid history and become dangling by design.
func (f *Foundation) DeleteArtifact(guid string) error {
	return f.write(func() (string, []gitx.Op, error) {
		m, err := f.Meta(guid)
		if err != nil {
			return "", nil, err
		}
		ops, err := f.artifactOps(m, true, "")
		if err != nil {
			return "", nil, err
		}
		if len(ops) == 0 {
			return "", nil, fmt.Errorf("artifact %s has no files at %s", guid, m.FilePath)
		}
		msg := commitMsg(fmt.Sprintf("%s %s deleted", titleKind(m.Kind), guid), m.Kind+".delete", guid)
		return msg, ops, nil
	})
}

// MoveArtifact relocates an entry or document to another folder. The GUID and
// all references remain unchanged.
func (f *Foundation) MoveArtifact(guid, newFolder string) (*Meta, error) {
	newFolder, err := CleanFolder(newFolder)
	if err != nil {
		return nil, err
	}
	err = f.write(func() (string, []gitx.Op, error) {
		m, err := f.Meta(guid)
		if err != nil {
			return "", nil, err
		}
		if m.Kind != KindEntry && m.Kind != KindDocument {
			return "", nil, vErr("only entries and documents can be moved")
		}
		if newFolder == m.Folder {
			return "", nil, errNoChange
		}
		ops, err := f.artifactOps(m, true, newFolder)
		if err != nil {
			return "", nil, err
		}
		if len(ops) == 0 {
			return "", nil, fmt.Errorf("artifact %s has no files at %s", guid, m.FilePath)
		}
		msg := commitMsg(fmt.Sprintf("%s %s moved to /%s", titleKind(m.Kind), guid, newFolder),
			m.Kind+".move", guid)
		return msg, ops, nil
	})
	if err != nil {
		return nil, err
	}
	return f.Meta(guid)
}

// CreateLink creates a typed directed link between two artifacts, stored in
// the metadata scope nearest to the source artifact.
func (f *Foundation) CreateLink(typ, source, target string, fields json.RawMessage) (*Meta, error) {
	if !typeRe.MatchString(typ) {
		return nil, vErr("invalid link type %q", typ)
	}
	guid := NewGUID()
	err := f.write(func() (string, []gitx.Op, error) {
		src, err := f.Meta(source)
		if err != nil {
			return "", nil, vErr("source artifact %s not found", source)
		}
		tgt, err := f.Meta(target)
		if err != nil {
			return "", nil, vErr("target artifact %s not found", target)
		}
		// Schema relationship definitions constrain links when present.
		schema, err := f.effSchema(src.Type, src.Folder)
		if err != nil {
			return "", nil, err
		}
		if schema != nil && len(schema.Relationships) > 0 {
			var rel *Relationship
			for i := range schema.Relationships {
				if schema.Relationships[i].LinkType == typ {
					rel = &schema.Relationships[i]
					break
				}
			}
			if rel == nil {
				return "", nil, vErr("link type %q not allowed for source type %q", typ, src.Type)
			}
			if len(rel.TargetTypes) > 0 && !contains(rel.TargetTypes, tgt.Type) {
				return "", nil, vErr("target type %q not allowed for link type %q", tgt.Type, typ)
			}
			if len(rel.SourceTypes) > 0 && !contains(rel.SourceTypes, src.Type) {
				return "", nil, vErr("source type %q not allowed for link type %q", src.Type, typ)
			}
		}
		obj := ojson.New()
		obj.SetString("guid", guid)
		obj.SetString("kind", KindLink)
		obj.SetString("type", typ)
		obj.SetString("source", source)
		obj.SetString("target", target)
		if len(fields) > 0 {
			if err := requireObject(fields, "fields"); err != nil {
				return "", nil, err
			}
			obj.Set("fields", fields)
		}
		content, err := obj.Encode()
		if err != nil {
			return "", nil, err
		}
		filePath := path.Join(metaScope(src.Folder), "links", guid+".json")
		msg := commitMsg(fmt.Sprintf("Link %s from %s to %s created", typ, source, target),
			"link.create", guid)
		return msg, []gitx.Op{{Path: filePath, Content: content}}, nil
	})
	if err != nil {
		return nil, err
	}
	return f.Meta(guid)
}

// CreateComment attaches a comment to an artifact, optionally replying to a
// parent comment on the same subject.
func (f *Foundation) CreateComment(subject, text, parent, author string) (*Meta, error) {
	text = stripNUL(strings.TrimSpace(text))
	if text == "" {
		return nil, vErr("comment text is required")
	}
	guid := NewGUID()
	err := f.write(func() (string, []gitx.Op, error) {
		subj, err := f.Meta(subject)
		if err != nil {
			return "", nil, vErr("subject artifact %s not found", subject)
		}
		if parent != "" {
			p, err := f.Meta(parent)
			if err != nil || p.Kind != KindComment || p.Subject != subject {
				return "", nil, vErr("parent %s is not a comment on the same subject", parent)
			}
		}
		obj := ojson.New()
		obj.SetString("guid", guid)
		obj.SetString("kind", KindComment)
		obj.SetString("subject", subject)
		if parent != "" {
			obj.SetString("parent", parent)
		}
		if author != "" {
			obj.SetString("author", stripNUL(author))
		}
		obj.SetString("text", text)
		// Fixed-width nanosecond format: lexical order == chronological order.
		obj.SetString("created", time.Now().UTC().Format("2006-01-02T15:04:05.000000000Z"))
		content, err := obj.Encode()
		if err != nil {
			return "", nil, err
		}
		filePath := path.Join(metaScope(subj.Folder), "comments", guid+".json")
		msg := commitMsg(fmt.Sprintf("Comment %s added to %s", guid, subject), "comment.create", guid)
		return msg, []gitx.Op{{Path: filePath, Content: content}}, nil
	})
	if err != nil {
		return nil, err
	}
	return f.Meta(guid)
}

// Transition executes a workflow state change on an entry or document.
func (f *Foundation) Transition(guid, workflowID, to string) (*Meta, error) {
	err := f.write(func() (string, []gitx.Op, error) {
		m, obj, err := f.Artifact(guid)
		if err != nil {
			return "", nil, err
		}
		if m.Kind != KindEntry && m.Kind != KindDocument {
			return "", nil, vErr("artifact kind %q has no workflows", m.Kind)
		}
		schema, err := f.effSchema(m.Type, m.Folder)
		if err != nil {
			return "", nil, err
		}
		if schema == nil || !contains(schema.Workflows, workflowID) {
			return "", nil, vErr("workflow %q is not assigned to type %q", workflowID, m.Type)
		}
		wf, err := f.proj.Workflow(workflowID, scopeChain(m.Folder))
		if err != nil {
			return "", nil, err
		}
		if wf == nil {
			return "", nil, vErr("workflow definition %q not found", workflowID)
		}
		from := m.Workflows[workflowID]
		if from == "" {
			from = wf.Initial
		}
		if !wf.CanTransition(from, to) {
			return "", nil, vErr("no transition from %q to %q in workflow %q", from, to, workflowID)
		}
		states := m.Workflows
		if states == nil {
			states = map[string]string{}
		}
		states[workflowID] = to
		obj.SetAny("workflows", states)
		content, err := obj.Encode()
		if err != nil {
			return "", nil, err
		}
		msg := commitMsg(fmt.Sprintf("Workflow transition: Item %s transitioned from %s to %s", guid, from, to),
			"workflow.transition", guid)
		return msg, []gitx.Op{{Path: m.FilePath, Content: content}}, nil
	})
	if err != nil {
		return nil, err
	}
	return f.Meta(guid)
}

// PutSchema stores a schema definition file in a configuration scope. The
// file name must equal the artifact type it defines, so a scope can never
// hold two silently shadowing definitions for the same type.
func (f *Foundation) PutSchema(scope, name string, s *Schema) error {
	if s.ArtifactType != name {
		return vErr("schema file name %q must equal its artifactType %q", name, s.ArtifactType)
	}
	valid := s.ArtifactType != "" && typeRe.MatchString(s.ArtifactType)
	return f.putConfig(scope, "schemas", name, s, valid,
		fmt.Sprintf("Schema %s in /%s updated", name, scope))
}

// PutWorkflow stores a workflow definition file in a configuration scope.
// The file name must equal the workflow id.
func (f *Foundation) PutWorkflow(scope, name string, w *Workflow) error {
	if w.ID != name {
		return vErr("workflow file name %q must equal its id %q", name, w.ID)
	}
	valid := w.ID != "" && w.Initial != "" && contains(w.States, w.Initial)
	for _, s := range w.States {
		valid = valid && s != "" && s == stripNUL(strings.TrimSpace(s)) && len(s) <= 100
	}
	for _, t := range w.Transitions {
		valid = valid && contains(w.States, t.From) && contains(w.States, t.To)
	}
	return f.putConfig(scope, "workflows", name, w, valid,
		fmt.Sprintf("Workflow %s in /%s updated", name, scope))
}

func (f *Foundation) putConfig(scope, dir, name string, v any, valid bool, subject string) error {
	scope, err := CleanFolder(scope)
	if err != nil {
		return err
	}
	if !typeRe.MatchString(name) {
		return vErr("invalid config name %q", name)
	}
	if !valid {
		return vErr("invalid %s definition", strings.TrimSuffix(dir, "s"))
	}
	content, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	filePath := path.Join(metaScope(scope), dir, name+".json")
	return f.write(func() (string, []gitx.Op, error) {
		return commitMsg(subject, "config.update", name), []gitx.Op{{Path: filePath, Content: append(content, '\n')}}, nil
	})
}

// ---- helpers ----

// artifactOps returns delete (and, for moves, re-create) ops for all files of
// an artifact.
func (f *Foundation) artifactOps(m *Meta, del bool, moveTo string) ([]gitx.Op, error) {
	var ops []gitx.Op
	if m.Kind == KindLink || m.Kind == KindComment {
		return []gitx.Op{{Path: m.FilePath, Delete: true}}, nil
	}
	dir := path.Dir(m.FilePath)
	head, err := f.git.Head()
	if err != nil {
		return nil, err
	}
	entries, err := f.git.ListTree(head, dir+"/")
	if err != nil {
		return nil, err
	}
	shas := make([]string, len(entries))
	for i, e := range entries {
		shas[i] = e.SHA
	}
	blobs, err := f.git.ReadBlobs(shas)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if del {
			ops = append(ops, gitx.Op{Path: e.Path, Delete: true})
		}
		if moveTo != "" || !del {
			rel := strings.TrimPrefix(e.Path, dir+"/")
			ops = append(ops, gitx.Op{
				Path:    path.Join(folderOrRoot(moveTo), m.GUID, rel),
				Content: blobs[e.SHA],
			})
		}
	}
	return ops, nil
}

func (f *Foundation) checkHID(hid, guid string) error {
	if len(hid) > 100 || strings.ContainsAny(hid, " \t\n") {
		return vErr("invalid HID %q", hid)
	}
	for _, r := range hid {
		if r < 0x20 || r == 0x7f {
			return vErr("HID contains control characters")
		}
	}
	owner, ok, err := f.proj.HIDOwner(hid)
	if err != nil {
		return err
	}
	if ok && owner != guid {
		return fmt.Errorf("%w: HID %q is already assigned to %s", ErrConflict, hid, owner)
	}
	return nil
}

// checkBase validates an overlay base: it must exist, be an entry, and the
// chain from it must not lead back to guid.
func (f *Foundation) checkBase(base, guid string) error {
	seen := map[string]bool{guid: true}
	for base != "" {
		if seen[base] {
			return vErr("overlay cycle via base %s", base)
		}
		seen[base] = true
		m, err := f.proj.Get(base)
		if err != nil {
			return err
		}
		if m == nil {
			return vErr("base artifact %s not found", base)
		}
		if m.Kind != KindEntry {
			return vErr("base artifact %s is not an entry", base)
		}
		base = m.Base
	}
	return nil
}

func requireObject(raw json.RawMessage, name string) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return vErr("%s must be a JSON object", name)
	}
	return nil
}

func folderOrRoot(folder string) string {
	if folder == "" {
		return "."
	}
	return folder
}

func titleKind(kind string) string {
	return strings.ToUpper(kind[:1]) + kind[1:]
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
