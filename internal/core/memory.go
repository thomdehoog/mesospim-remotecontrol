package core

import (
	"maps"
	"sort"
	"sync"

	"github.com/thomdehoog/origoa/internal/gitx"
)

// memProjection is the in-memory Projection: zero dependencies, rebuilt from
// the Git HEAD on Sync and updated incrementally per commit on Apply.
//
// Caveat (shared with any projection): duplicate HIDs planted by direct Git
// edits collapse to one owner; a full Sync re-establishes deterministic
// (path-ordered, last-wins) state.
type memProjection struct {
	git *gitx.Repo

	mu        sync.RWMutex
	head      string
	artifacts map[string]*artEntry // file path -> artifact
	byGUID    map[string]*artEntry
	byHID     map[string]string  // HID -> GUID
	configs   map[string]*record // file path -> config record
	// derived views, rebuilt from configs whenever configs change (rare)
	schemas   map[string][]*Schema
	workflows map[string]map[string]*Workflow
}

type artEntry struct {
	meta *Meta
	text string
}

func newMemProjection(g *gitx.Repo) *memProjection {
	p := &memProjection{git: g}
	p.reset()
	return p
}

func (p *memProjection) reset() {
	p.artifacts = map[string]*artEntry{}
	p.byGUID = map[string]*artEntry{}
	p.byHID = map[string]string{}
	p.configs = map[string]*record{}
	p.schemas = map[string][]*Schema{}
	p.workflows = map[string]map[string]*Workflow{}
}

func (p *memProjection) Head() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.head
}

func (p *memProjection) Sync() error {
	head, recs, err := syncRecords(p.git)
	if err != nil {
		return err
	}
	fresh := &memProjection{}
	fresh.reset()
	fresh.head = head
	for _, rec := range recs {
		fresh.ingest(rec)
	}
	fresh.rebuildConfigViews()

	p.mu.Lock()
	p.head = fresh.head
	p.artifacts, p.byGUID, p.byHID = fresh.artifacts, fresh.byGUID, fresh.byHID
	p.configs, p.schemas, p.workflows = fresh.configs, fresh.schemas, fresh.workflows
	p.mu.Unlock()
	return nil
}

func (p *memProjection) Apply(parentHead, newHead string, changes []Change) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.head != parentHead {
		return errStaleProjection
	}
	configsChanged := false
	for _, c := range changes {
		if c.Delete {
			configsChanged = p.removeByPath(c.Path) || configsChanged
			continue
		}
		rec := classify(c.Path, c.SHA, c.Content)
		if rec == nil {
			// The file at this path may previously have been projectable.
			configsChanged = p.removeByPath(c.Path) || configsChanged
			continue
		}
		configsChanged = p.removeByPath(c.Path) || configsChanged
		if rec.meta != nil {
			// The same GUID may exist at another path (e.g. a move applies
			// delete+add); drop the stale location first.
			if other, ok := p.byGUID[rec.meta.GUID]; ok && other.meta.FilePath != c.Path {
				p.removeByPath(other.meta.FilePath)
			}
		}
		p.ingest(rec)
		configsChanged = configsChanged || rec.meta == nil
	}
	if configsChanged {
		p.rebuildConfigViews()
	}
	p.head = newHead
	return nil
}

// ingest inserts one record (callers hold p.mu; config views rebuilt by
// callers when needed).
func (p *memProjection) ingest(rec *record) {
	if rec.meta != nil {
		e := &artEntry{meta: rec.meta, text: rec.text}
		p.artifacts[rec.filePath] = e
		p.byGUID[rec.meta.GUID] = e
		if rec.meta.HID != "" {
			p.byHID[rec.meta.HID] = rec.meta.GUID
		}
		return
	}
	p.configs[rec.filePath] = rec
}

// removeByPath drops whatever record lives at path; reports whether a config
// file was removed (callers hold p.mu).
func (p *memProjection) removeByPath(path string) bool {
	if e, ok := p.artifacts[path]; ok {
		delete(p.artifacts, path)
		if cur, ok := p.byGUID[e.meta.GUID]; ok && cur == e {
			delete(p.byGUID, e.meta.GUID)
		}
		if e.meta.HID != "" && p.byHID[e.meta.HID] == e.meta.GUID {
			delete(p.byHID, e.meta.HID)
		}
		return false
	}
	if _, ok := p.configs[path]; ok {
		delete(p.configs, path)
		return true
	}
	return false
}

// rebuildConfigViews derives schema/workflow lookup structures from the
// config file set in deterministic path order (callers hold p.mu).
func (p *memProjection) rebuildConfigViews() {
	p.schemas = map[string][]*Schema{}
	p.workflows = map[string]map[string]*Workflow{}
	paths := make([]string, 0, len(p.configs))
	for fp := range p.configs {
		paths = append(paths, fp)
	}
	sort.Strings(paths)
	for _, fp := range paths {
		rec := p.configs[fp]
		switch rec.category {
		case "schema":
			p.schemas[rec.scope] = append(p.schemas[rec.scope], rec.schema)
		case "workflow":
			if p.workflows[rec.scope] == nil {
				p.workflows[rec.scope] = map[string]*Workflow{}
			}
			p.workflows[rec.scope][rec.workflow.ID] = rec.workflow
		}
	}
}

func (p *memProjection) Get(guid string) (*Meta, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	e, ok := p.byGUID[guid]
	if !ok {
		return nil, nil
	}
	cp := *e.meta
	cp.Workflows = maps.Clone(e.meta.Workflows) // callers may mutate
	return &cp, nil
}

func (p *memProjection) List(q ListQuery) ([]*Meta, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.collect(q.Limit, func(e *artEntry) bool { return q.matches(e.meta, e.text) }), nil
}

func (p *memProjection) LinksFor(guid string) (incoming, outgoing []*Meta, err error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, m := range p.collect(0, func(e *artEntry) bool { return e.meta.Kind == KindLink }) {
		if m.Target == guid {
			incoming = append(incoming, m)
		}
		if m.Source == guid {
			outgoing = append(outgoing, m)
		}
	}
	return incoming, outgoing, nil
}

func (p *memProjection) CommentsFor(subject string) ([]*Meta, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.collect(0, func(e *artEntry) bool {
		return e.meta.Kind == KindComment && e.meta.Subject == subject
	}), nil
}

func (p *memProjection) HIDOwner(hid string) (string, bool, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	guid, ok := p.byHID[hid]
	return guid, ok, nil
}

func (p *memProjection) MaxHIDNumber(prefix string) (int, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	max := 0
	for hid := range p.byHID {
		if n, ok := hidNumber(hid, prefix); ok && n > max {
			max = n
		}
	}
	return max, nil
}

func (p *memProjection) Folders() ([]string, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	folders := map[string]bool{}
	for _, e := range p.artifacts {
		folders[e.meta.Folder] = true
	}
	for _, rec := range p.configs {
		folders[rec.scope] = true
	}
	return withAncestors(folders), nil
}

func (p *memProjection) SchemaDefs(typ string, scopes []string) ([]*Schema, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var defs []*Schema
	for _, scope := range scopes {
		for _, s := range p.schemas[scope] {
			if s.ArtifactType == typ {
				defs = append(defs, s)
			}
		}
	}
	return defs, nil
}

func (p *memProjection) SchemasByScope() (map[string][]*Schema, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := map[string][]*Schema{}
	for scope, defs := range p.schemas {
		out[scope] = append([]*Schema(nil), defs...)
	}
	return out, nil
}

func (p *memProjection) Workflow(id string, scopes []string) (*Workflow, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for i := len(scopes) - 1; i >= 0; i-- {
		if w, ok := p.workflows[scopes[i]][id]; ok {
			return w, nil
		}
	}
	return nil, nil
}

func (p *memProjection) Close() error { return nil }

// collect returns matching metas sorted and limited (callers hold p.mu).
func (p *memProjection) collect(limit int, filter func(*artEntry) bool) []*Meta {
	var out []*Meta
	for _, e := range p.artifacts {
		if filter(e) {
			out = append(out, e.meta)
		}
	}
	return sortMetas(out, limit)
}
