package repo

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/google/uuid"

	"origoa/internal/artifact"
	"origoa/internal/gitstore"
	"origoa/internal/ojson"
	"origoa/internal/projection"
	"origoa/internal/resolve"
	"origoa/internal/schemamodel"
)

func parseArtifact(b []byte) (*artifact.File, error) { return artifact.Parse(b) }

func (s *Service) guidFileName() string {
	return s.Scanner.Config().GUIDFiles[0]
}

func cleanFolder(p string) (string, error) {
	p = strings.Trim(path.Clean("/"+p), "/")
	if p == "." {
		p = ""
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" {
			continue
		}
		if strings.HasPrefix(seg, ".") {
			return "", validationErr("folder segments must not start with '.': %q", seg)
		}
		if artifact.IsGUID(seg) {
			return "", validationErr("folder segments must not be GUIDs: %q", seg)
		}
	}
	return p, nil
}

// artifactPath returns the storage file of an entry/document GUID dir.
func (s *Service) artifactFilePath(parentFolder, guid string) string {
	return path.Join(parentFolder, guid, s.guidFileName())
}

// loadArtifactAt reads and parses an artifact file from a revision.
func (s *Service) loadArtifactAt(head plumbing.Hash, repoPath, kind string) (*ojson.Doc, *artifact.File, error) {
	filePath := repoPath
	if kind == artifact.KindEntry || kind == artifact.KindDocument {
		filePath = path.Join(repoPath, s.guidFileName())
	}
	b, ok, err := s.Git.ReadBlob(head, filePath)
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		return nil, nil, ErrNotFound
	}
	doc, err := ojson.Parse(b)
	if err != nil {
		return nil, nil, err
	}
	af, err := artifact.Parse(b)
	if err != nil {
		return nil, nil, err
	}
	return doc, af, nil
}

// requireArtifact loads the projected row and checks optimistic
// concurrency: ifRevision, when non-empty, must match the revision the
// client loaded.
func (s *Service) requireArtifact(ctx context.Context, guid, ifRevision string) (*projection.ArtifactRow, error) {
	row, err := s.DB.GetArtifact(ctx, guid)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, ErrNotFound
	}
	if ifRevision != "" && row.UpdatedCommit != ifRevision {
		return nil, ErrConflict
	}
	return row, nil
}

// CreateArtifactParams describes a new entry or document.
type CreateArtifactParams struct {
	Kind   string         `json:"kind"`
	Folder string         `json:"folder"`
	Type   string         `json:"type"`
	Title  string         `json:"title"`
	HID    string         `json:"hid,omitempty"`
	Base   string         `json:"base,omitempty"`
	Fields map[string]any `json:"fields,omitempty"`
}

// CreateArtifact creates an entry or document.
func (s *Service) CreateArtifact(ctx context.Context, p CreateArtifactParams) (string, error) {
	folder, err := cleanFolder(p.Folder)
	if err != nil {
		return "", err
	}
	if p.Kind != artifact.KindEntry && p.Kind != artifact.KindDocument {
		return "", validationErr("kind must be entry or document")
	}
	if p.Type == "" {
		return "", validationErr("type is required")
	}
	if strings.TrimSpace(p.Title) == "" {
		return "", validationErr("title is required")
	}
	if p.Base != "" {
		if p.Kind != artifact.KindEntry {
			return "", validationErr("only entries support overlays")
		}
		base, err := s.DB.GetArtifact(ctx, p.Base)
		if err != nil {
			return "", err
		}
		if base == nil || base.Kind != artifact.KindEntry {
			return "", validationErr("overlay base %s is not an existing entry", p.Base)
		}
	}
	guid := uuid.NewString()

	_, err = s.Update(ctx, func(head plumbing.Hash, cs *gitstore.Changeset) (string, error) {
		eff, err := resolve.EffectiveSchema(ctx, s.DB, nil, folder, p.Type)
		if err != nil {
			return "", err
		}
		hid := p.HID
		generated := false
		if hid == "" && eff != nil && eff.HIDPrefix != "" {
			n, err := s.DB.NextHIDNumber(ctx, nil, eff.HIDPrefix)
			if err != nil {
				return "", err
			}
			hid = fmt.Sprintf("%s-%d", eff.HIDPrefix, n)
			generated = true
		}
		// User-supplied HIDs are validated here; generated HIDs rely on
		// the unique index, which turns a racing duplicate into a retry
		// with a freshly generated number.
		if hid != "" && !generated {
			exists, err := s.DB.HIDExists(ctx, nil, hid, guid)
			if err != nil {
				return "", err
			}
			if exists {
				return "", validationErr("HID %q is already in use", hid)
			}
		}

		doc := ojson.NewDoc()
		obj, _ := doc.RootObject()
		obj.Set("guid", guid)
		obj.Set("kind", p.Kind)
		obj.Set("type", p.Type)
		obj.Set("title", p.Title)
		if hid != "" {
			obj.Set("hid", hid)
		}
		if p.Base != "" {
			obj.Set("base", p.Base)
		}
		obj.SetAny("fields", normalizeFields(p.Fields))
		// Initialize workflow states from the effective schema.
		workflows := map[string]any{}
		if eff != nil {
			for _, wfName := range eff.Workflows {
				wd, err := resolve.Workflow(ctx, s.DB, nil, folder, wfName)
				if err != nil {
					continue // undefined workflow reference: ignore
				}
				workflows[wfName] = wd.Initial
			}
		}
		obj.SetAny("workflows", workflows)
		if p.Kind == artifact.KindDocument {
			obj.SetAny("sections", []any{})
		}
		cs.Write(s.artifactFilePath(folder, guid), doc.Bytes())

		kindName := "Entry"
		if p.Kind == artifact.KindDocument {
			kindName = "Document"
		}
		return fmt.Sprintf("%s %s in %s created", kindName, guid, folderLabel(folder)), nil
	})
	if err != nil {
		return "", err
	}
	s.emit(Event{Type: "artifact-created", GUID: guid, Path: folder})
	return guid, nil
}

func folderLabel(folder string) string {
	if folder == "" {
		return "/"
	}
	return folder
}

func normalizeFields(fields map[string]any) map[string]any {
	if fields == nil {
		return map[string]any{}
	}
	return fields
}

// UpdateArtifactParams patches an entry or document.
type UpdateArtifactParams struct {
	Title      *string         `json:"title,omitempty"`
	HID        *string         `json:"hid,omitempty"`
	Base       *string         `json:"base,omitempty"`
	Fields     map[string]any  `json:"fields,omitempty"`     // merged; null removes a field
	Sections   json.RawMessage `json:"sections,omitempty"`   // documents: replaces the section tree
	IfRevision string          `json:"ifRevision,omitempty"` // optimistic concurrency
}

// UpdateArtifact applies a patch to an entry or document.
func (s *Service) UpdateArtifact(ctx context.Context, guid string, p UpdateArtifactParams) error {
	row, err := s.requireArtifact(ctx, guid, p.IfRevision)
	if err != nil {
		return err
	}
	if row.Kind != artifact.KindEntry && row.Kind != artifact.KindDocument {
		return validationErr("artifact %s is a %s; use the %s API", guid, row.Kind, row.Kind)
	}
	if p.HID != nil && *p.HID != "" {
		exists, err := s.DB.HIDExists(ctx, nil, *p.HID, guid)
		if err != nil {
			return err
		}
		if exists {
			return validationErr("HID %q is already in use", *p.HID)
		}
	}
	if p.Base != nil && *p.Base != "" {
		if row.Kind != artifact.KindEntry {
			return validationErr("only entries support overlays")
		}
		if *p.Base == guid {
			return validationErr("an entry cannot overlay itself")
		}
		if err := s.checkOverlayCycle(ctx, guid, *p.Base); err != nil {
			return err
		}
	}

	_, err = s.Update(ctx, func(head plumbing.Hash, cs *gitstore.Changeset) (string, error) {
		cur, err := s.DB.GetArtifact(ctx, guid)
		if err != nil {
			return "", err
		}
		if cur == nil {
			return "", ErrNotFound
		}
		doc, _, err := s.loadArtifactAt(head, cur.RepoPath, cur.Kind)
		if err != nil {
			return "", err
		}
		obj, err := doc.RootObject()
		if err != nil {
			return "", err
		}
		if p.Title != nil {
			if strings.TrimSpace(*p.Title) == "" {
				return "", validationErr("title must not be empty")
			}
			obj.Set("title", *p.Title)
		}
		if p.HID != nil {
			if *p.HID == "" {
				obj.Delete("hid")
			} else {
				obj.Set("hid", *p.HID)
			}
		}
		if p.Base != nil {
			if *p.Base == "" {
				obj.Delete("base")
			} else {
				obj.Set("base", *p.Base)
			}
		}
		if p.Fields != nil {
			fieldsObj := obj.GetObject("fields")
			if fieldsObj == nil {
				obj.SetAny("fields", map[string]any{})
				fieldsObj = obj.GetObject("fields")
			}
			// Deterministic merge order for reproducible commits.
			keys := make([]string, 0, len(p.Fields))
			for k := range p.Fields {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				v := p.Fields[k]
				if v == nil {
					fieldsObj.Delete(k)
				} else {
					fieldsObj.SetAny(k, v)
				}
			}
		}
		if p.Sections != nil {
			if cur.Kind != artifact.KindDocument {
				return "", validationErr("sections can only be set on documents")
			}
			var secs []artifact.Section
			if err := json.Unmarshal(p.Sections, &secs); err != nil {
				return "", validationErr("invalid sections: %v", err)
			}
			var generic any
			b, _ := json.Marshal(secs)
			json.Unmarshal(b, &generic)
			obj.SetAny("sections", generic)
		}
		if !doc.Modified() {
			return "", nil // no logical change: no commit
		}
		filePath := path.Join(cur.RepoPath, s.guidFileName())
		cs.Write(filePath, doc.Bytes())
		kindName := "Entry"
		if cur.Kind == artifact.KindDocument {
			kindName = "Document"
		}
		return fmt.Sprintf("%s %s updated", kindName, guid), nil
	})
	if err == nil {
		s.emit(Event{Type: "artifact-updated", GUID: guid})
	}
	return err
}

// DeleteArtifact removes any native artifact (entry, document, link or
// comment) from the repository.
func (s *Service) DeleteArtifact(ctx context.Context, guid, ifRevision string) error {
	row, err := s.requireArtifact(ctx, guid, ifRevision)
	if err != nil {
		return err
	}
	_, err = s.Update(ctx, func(head plumbing.Hash, cs *gitstore.Changeset) (string, error) {
		cur, err := s.DB.GetArtifact(ctx, guid)
		if err != nil {
			return "", err
		}
		if cur == nil {
			return "", ErrNotFound
		}
		switch cur.Kind {
		case artifact.KindEntry, artifact.KindDocument:
			cs.DeleteDir(cur.RepoPath)
			kindName := "Entry"
			if cur.Kind == artifact.KindDocument {
				kindName = "Document"
			}
			return fmt.Sprintf("%s %s deleted", kindName, guid), nil
		case artifact.KindLink:
			cs.Delete(cur.RepoPath)
			return fmt.Sprintf("Link %s deleted", guid), nil
		case artifact.KindComment:
			cs.Delete(cur.RepoPath)
			return fmt.Sprintf("Comment %s deleted", guid), nil
		}
		return "", validationErr("unknown artifact kind %q", cur.Kind)
	})
	if err == nil {
		s.emit(Event{Type: "artifact-deleted", GUID: guid, Path: row.ParentPath})
	}
	return err
}

// MoveArtifact relocates an entry/document GUID directory to another
// folder. Related metadata stored for the artifact is relocated with it.
func (s *Service) MoveArtifact(ctx context.Context, guid, newFolder, ifRevision string) error {
	folder, err := cleanFolder(newFolder)
	if err != nil {
		return err
	}
	row, err := s.requireArtifact(ctx, guid, ifRevision)
	if err != nil {
		return err
	}
	if row.Kind != artifact.KindEntry && row.Kind != artifact.KindDocument {
		return validationErr("only entries and documents can be moved")
	}
	_, err = s.Update(ctx, func(head plumbing.Hash, cs *gitstore.Changeset) (string, error) {
		cur, err := s.DB.GetArtifact(ctx, guid)
		if err != nil {
			return "", err
		}
		if cur == nil {
			return "", ErrNotFound
		}
		if cur.ParentPath == folder {
			return "", nil
		}
		oldDir := cur.RepoPath
		newDir := path.Join(folder, guid)
		// Copy every file of the GUID directory to the new location.
		moved := 0
		err = s.Git.WalkTree(head, func(p string, read func() ([]byte, error)) error {
			if p != oldDir && !strings.HasPrefix(p, oldDir+"/") {
				return nil
			}
			b, err := read()
			if err != nil {
				return err
			}
			cs.Write(newDir+strings.TrimPrefix(p, oldDir), b)
			moved++
			return nil
		})
		if err != nil {
			return "", err
		}
		if moved == 0 {
			return "", ErrNotFound
		}
		cs.DeleteDir(oldDir)
		s.relocateMetadata(ctx, head, cs, guid, cur.ParentPath, folder)
		return fmt.Sprintf("Folder %s moved to %s", oldDir, newDir), nil
	})
	if err == nil {
		s.emit(Event{Type: "artifact-moved", GUID: guid, Path: folder})
	}
	return err
}

// relocateMetadata moves links (owned by the source) and comments (owned
// by the subject) into the configuration folder of the new location,
// preserving metadata locality.
func (s *Service) relocateMetadata(ctx context.Context, head plumbing.Hash, cs *gitstore.Changeset, guid, oldFolder, newFolder string) {
	cfgFolder := s.Scanner.Config().ConfigFolders[0]
	links, err := s.DB.LinksFor(ctx, guid)
	if err == nil {
		for _, l := range links {
			if l.Source != guid {
				continue
			}
			row, err := s.DB.GetArtifact(ctx, l.GUID)
			if err != nil || row == nil {
				continue
			}
			newPath := path.Join(newFolder, cfgFolder, "links", l.GUID+".json")
			if row.RepoPath == newPath {
				continue
			}
			if b, ok, err := s.Git.ReadBlob(head, row.RepoPath); err == nil && ok {
				cs.Delete(row.RepoPath)
				cs.Write(newPath, b)
			}
		}
	}
	comments, err := s.DB.CommentsFor(ctx, guid)
	if err == nil {
		for _, c := range comments {
			row, err := s.DB.GetArtifact(ctx, c.GUID)
			if err != nil || row == nil {
				continue
			}
			newPath := path.Join(newFolder, cfgFolder, "comments", c.GUID+".json")
			if row.RepoPath == newPath {
				continue
			}
			if b, ok, err := s.Git.ReadBlob(head, row.RepoPath); err == nil && ok {
				cs.Delete(row.RepoPath)
				cs.Write(newPath, b)
			}
		}
	}
}

// ---- Links ----

// CreateLinkParams describes a new semantic relationship.
type CreateLinkParams struct {
	Type   string         `json:"type"`
	Source string         `json:"source"`
	Target string         `json:"target"`
	Fields map[string]any `json:"fields,omitempty"`
}

// CreateLink creates a typed directed link between two artifacts.
func (s *Service) CreateLink(ctx context.Context, p CreateLinkParams) (string, error) {
	if p.Type == "" {
		return "", validationErr("link type is required")
	}
	source, err := s.DB.GetArtifact(ctx, p.Source)
	if err != nil {
		return "", err
	}
	target, err := s.DB.GetArtifact(ctx, p.Target)
	if err != nil {
		return "", err
	}
	if source == nil {
		return "", validationErr("source artifact %s not found", p.Source)
	}
	if target == nil {
		return "", validationErr("target artifact %s not found", p.Target)
	}
	if err := s.validateRelationship(ctx, p.Type, source, target); err != nil {
		return "", err
	}
	guid := uuid.NewString()
	cfgFolder := s.Scanner.Config().ConfigFolders[0]
	storagePath := path.Join(source.ParentPath, cfgFolder, "links", guid+".json")

	_, err = s.Update(ctx, func(head plumbing.Hash, cs *gitstore.Changeset) (string, error) {
		doc := ojson.NewDoc()
		obj, _ := doc.RootObject()
		obj.Set("guid", guid)
		obj.Set("kind", "link")
		obj.Set("type", p.Type)
		obj.Set("title", fmt.Sprintf("%s → %s", source.Title, target.Title))
		obj.Set("source", p.Source)
		obj.Set("target", p.Target)
		obj.SetAny("fields", normalizeFields(p.Fields))
		cs.Write(storagePath, doc.Bytes())
		return fmt.Sprintf("Link %s from %s to %s created", p.Type, p.Source, p.Target), nil
	})
	if err != nil {
		return "", err
	}
	s.emit(Event{Type: "link-created", GUID: guid})
	return guid, nil
}

// validateRelationship enforces schema relationship constraints where a
// matching definition exists. Undefined link types remain allowed: the
// Foundation supports arbitrary semantic relationship types.
func (s *Service) validateRelationship(ctx context.Context, linkType string, source, target *projection.ArtifactRow) error {
	def := s.findRelationshipDef(ctx, linkType, source)
	if def == nil {
		return nil
	}
	if len(def.SourceTypes) > 0 && !containsStr(def.SourceTypes, source.Type) {
		return validationErr("link type %q does not allow source type %q", linkType, source.Type)
	}
	if len(def.TargetTypes) > 0 && !containsStr(def.TargetTypes, target.Type) {
		return validationErr("link type %q does not allow target type %q", linkType, target.Type)
	}
	// Cardinality: source→target direction.
	sourceLinks, err := s.DB.LinksFor(ctx, source.GUID)
	if err != nil {
		return err
	}
	targetLinks, err := s.DB.LinksFor(ctx, target.GUID)
	if err != nil {
		return err
	}
	outgoing := 0
	for _, l := range sourceLinks {
		if l.Type == linkType && l.Source == source.GUID {
			outgoing++
		}
	}
	incoming := 0
	for _, l := range targetLinks {
		if l.Type == linkType && l.Target == target.GUID {
			incoming++
		}
	}
	switch def.Cardinality {
	case "one-to-one":
		if outgoing > 0 {
			return validationErr("cardinality one-to-one: source already has a %q link", linkType)
		}
		if incoming > 0 {
			return validationErr("cardinality one-to-one: target already has a %q link", linkType)
		}
	case "one-to-many":
		if incoming > 0 {
			return validationErr("cardinality one-to-many: target already has a %q link", linkType)
		}
	case "many-to-one":
		if outgoing > 0 {
			return validationErr("cardinality many-to-one: source already has a %q link", linkType)
		}
	}
	return nil
}

func (s *Service) findRelationshipDef(ctx context.Context, linkType string, source *projection.ArtifactRow) *schemamodel.RelationshipDef {
	if source.Type == "" {
		return nil
	}
	eff, err := resolve.EffectiveSchema(ctx, s.DB, nil, source.ParentPath, source.Type)
	if err != nil || eff == nil {
		return nil
	}
	for i := range eff.Relationships {
		if eff.Relationships[i].LinkType == linkType {
			return &eff.Relationships[i]
		}
	}
	return nil
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// ---- Comments ----

// CreateCommentParams describes a new comment.
type CreateCommentParams struct {
	Subject string          `json:"subject"`
	Parent  string          `json:"parent,omitempty"`
	Author  string          `json:"author,omitempty"`
	Text    string          `json:"text"`
	Anchor  json.RawMessage `json:"anchor,omitempty"`
}

// CreateComment attaches a comment to an artifact (or to another comment
// as a threaded reply via Parent).
func (s *Service) CreateComment(ctx context.Context, p CreateCommentParams) (string, error) {
	if strings.TrimSpace(p.Text) == "" {
		return "", validationErr("comment text is required")
	}
	subject, err := s.DB.GetArtifact(ctx, p.Subject)
	if err != nil {
		return "", err
	}
	if subject == nil {
		return "", validationErr("comment subject %s not found", p.Subject)
	}
	if p.Parent != "" {
		parent, err := s.DB.GetArtifact(ctx, p.Parent)
		if err != nil {
			return "", err
		}
		if parent == nil || parent.Kind != artifact.KindComment {
			return "", validationErr("comment parent %s is not a comment", p.Parent)
		}
	}
	guid := uuid.NewString()
	cfgFolder := s.Scanner.Config().ConfigFolders[0]
	storagePath := path.Join(subject.ParentPath, cfgFolder, "comments", guid+".json")

	_, err = s.Update(ctx, func(head plumbing.Hash, cs *gitstore.Changeset) (string, error) {
		doc := ojson.NewDoc()
		obj, _ := doc.RootObject()
		obj.Set("guid", guid)
		obj.Set("kind", "comment")
		obj.Set("type", "comment")
		obj.Set("subject", p.Subject)
		if p.Parent != "" {
			obj.Set("parent", p.Parent)
		}
		if p.Author != "" {
			obj.Set("author", p.Author)
		}
		obj.Set("text", p.Text)
		if len(p.Anchor) > 0 {
			var anchor any
			if err := json.Unmarshal(p.Anchor, &anchor); err == nil {
				obj.SetAny("anchor", anchor)
			}
		}
		obj.Set("createdAt", time.Now().UTC().Format(time.RFC3339))
		cs.Write(storagePath, doc.Bytes())
		return fmt.Sprintf("Comment %s added to %s", guid, p.Subject), nil
	})
	if err != nil {
		return "", err
	}
	s.emit(Event{Type: "comment-created", GUID: guid, Detail: p.Subject})
	return guid, nil
}

// ---- Workflow transitions ----

// WorkflowTransition moves an artifact to a new state within one of its
// workflows after validating the transition against the workflow
// definition resolved at the artifact location.
func (s *Service) WorkflowTransition(ctx context.Context, guid, workflowName, toState, ifRevision string) error {
	row, err := s.requireArtifact(ctx, guid, ifRevision)
	if err != nil {
		return err
	}
	_, err = s.Update(ctx, func(head plumbing.Hash, cs *gitstore.Changeset) (string, error) {
		cur, err := s.DB.GetArtifact(ctx, guid)
		if err != nil {
			return "", err
		}
		if cur == nil {
			return "", ErrNotFound
		}
		wd, err := resolve.Workflow(ctx, s.DB, nil, cur.ParentPath, workflowName)
		if err != nil {
			return "", validationErr("%v", err)
		}
		if !wd.HasState(toState) {
			return "", validationErr("workflow %q has no state %q", workflowName, toState)
		}
		doc, af, err := s.loadArtifactAt(head, cur.RepoPath, cur.Kind)
		if err != nil {
			return "", err
		}
		fromState, participates := af.Workflows[workflowName]
		if !participates {
			fromState = wd.Initial
		}
		if fromState == toState {
			return "", nil
		}
		if !wd.CanTransition(fromState, toState) {
			return "", validationErr("workflow %q does not allow transition %s → %s", workflowName, fromState, toState)
		}
		obj, err := doc.RootObject()
		if err != nil {
			return "", err
		}
		wfObj := obj.GetObject("workflows")
		if wfObj == nil {
			obj.SetAny("workflows", map[string]any{})
			wfObj = obj.GetObject("workflows")
		}
		wfObj.Set(workflowName, toState)
		filePath := cur.RepoPath
		if cur.Kind == artifact.KindEntry || cur.Kind == artifact.KindDocument {
			filePath = path.Join(cur.RepoPath, s.guidFileName())
		}
		cs.Write(filePath, doc.Bytes())
		return fmt.Sprintf("Workflow transition: Item %s transitioned from %s to %s", guid, fromState, toState), nil
	})
	if err == nil {
		s.emit(Event{Type: "workflow-transition", GUID: guid, Detail: workflowName + ":" + toState})
		_ = row
	}
	return err
}

// ---- Folder operations ----

// CreateFolder creates an empty repository folder by materializing its
// configuration directory.
func (s *Service) CreateFolder(ctx context.Context, folder string) error {
	f, err := cleanFolder(folder)
	if err != nil {
		return err
	}
	if f == "" {
		return validationErr("folder path is required")
	}
	cfgFolder := s.Scanner.Config().ConfigFolders[0]
	_, err = s.Update(ctx, func(head plumbing.Hash, cs *gitstore.Changeset) (string, error) {
		marker := path.Join(f, cfgFolder, ".gitkeep")
		if _, ok, err := s.Git.ReadBlob(head, marker); err != nil {
			return "", err
		} else if ok {
			return "", nil
		}
		cs.Write(marker, []byte{})
		return fmt.Sprintf("Folder %s created", f), nil
	})
	if err == nil {
		s.emit(Event{Type: "folder-created", Path: f})
	}
	return err
}

// StructuralImpact estimates the impact of a structural operation.
type StructuralImpact struct {
	AffectedArtifacts int  `json:"affectedArtifacts"`
	AffectedFiles     int  `json:"affectedFiles"`
	Maintenance       bool `json:"maintenance"`
}

// AnalyzeMove estimates the impact of moving a folder.
func (s *Service) AnalyzeMove(ctx context.Context, oldFolder string) (StructuralImpact, error) {
	rows, err := s.DB.ArtifactsUnder(ctx, nil, oldFolder)
	if err != nil {
		return StructuralImpact{}, err
	}
	impact := StructuralImpact{AffectedArtifacts: len(rows)}
	impact.Maintenance = impact.AffectedArtifacts > maintenanceThreshold
	return impact, nil
}

// MoveFolder moves or renames a complete repository subtree in a single
// commit. Large operations temporarily switch the repository into
// maintenance mode (reads stay available, writes are rejected).
func (s *Service) MoveFolder(ctx context.Context, oldFolder, newFolder string) error {
	from, err := cleanFolder(oldFolder)
	if err != nil {
		return err
	}
	to, err := cleanFolder(newFolder)
	if err != nil {
		return err
	}
	if from == "" || to == "" {
		return validationErr("source and destination folders are required")
	}
	if from == to || strings.HasPrefix(to+"/", from+"/") {
		return validationErr("cannot move a folder into itself")
	}
	impact, err := s.AnalyzeMove(ctx, from)
	if err != nil {
		return err
	}
	// A large move enters Maintenance Mode exclusively (draining in-flight
	// writers and blocking new ones); a small one runs as an ordinary
	// concurrent writer.
	exclusive := impact.Maintenance
	if exclusive {
		s.maint.Lock()
		defer s.maint.Unlock()
		s.maintenance.Store(true)
		s.emit(Event{Type: "maintenance", Detail: "enabled"})
		defer func() {
			s.maintenance.Store(false)
			s.emit(Event{Type: "maintenance", Detail: "disabled"})
		}()
	}
	_, err = s.update(ctx, func(head plumbing.Hash, cs *gitstore.Changeset) (string, error) {
		moved := 0
		err := s.Git.WalkTree(head, func(p string, read func() ([]byte, error)) error {
			if p != from && !strings.HasPrefix(p, from+"/") {
				return nil
			}
			b, err := read()
			if err != nil {
				return err
			}
			cs.Write(to+strings.TrimPrefix(p, from), b)
			moved++
			return nil
		})
		if err != nil {
			return "", err
		}
		if moved == 0 {
			return "", validationErr("folder %q is empty or does not exist", from)
		}
		cs.DeleteDir(from)
		return fmt.Sprintf("Folder %s moved to %s", from, to), nil
	}, exclusive)
	if err == nil {
		s.emit(Event{Type: "folder-moved", Path: to, Detail: from})
	}
	return err
}

// DeleteFolder removes a folder subtree including all contained artifacts.
func (s *Service) DeleteFolder(ctx context.Context, folder string) error {
	f, err := cleanFolder(folder)
	if err != nil {
		return err
	}
	if f == "" {
		return validationErr("refusing to delete the repository root")
	}
	_, err = s.Update(ctx, func(head plumbing.Hash, cs *gitstore.Changeset) (string, error) {
		found := false
		err := s.Git.WalkTree(head, func(p string, read func() ([]byte, error)) error {
			if p == f || strings.HasPrefix(p, f+"/") {
				found = true
			}
			return nil
		})
		if err != nil {
			return "", err
		}
		if !found {
			return "", validationErr("folder %q does not exist", f)
		}
		cs.DeleteDir(f)
		return fmt.Sprintf("Folder %s deleted", f), nil
	})
	if err == nil {
		s.emit(Event{Type: "folder-deleted", Path: f})
	}
	return err
}
