package repo

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"

	"origoa/internal/gitstore"
	"origoa/internal/projection"
	"origoa/internal/scanner"
)

func testDSN() string {
	if dsn := os.Getenv("ORIGOA_TEST_DSN"); dsn != "" {
		return dsn
	}
	return "postgres://origoa:origoa@localhost:5432/origoa_test"
}

func newService(t *testing.T) (*Service, context.Context) {
	t.Helper()
	ctx := context.Background()
	db, err := projection.Connect(ctx, testDSN())
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	t.Cleanup(db.Close)
	// Isolate the test run.
	if _, err := db.Pool.Exec(ctx, `
		TRUNCATE artifacts, field_index, fts, link_index, comment_index,
		         hid_history, deleted_artifacts, folders, config_objects;
		UPDATE repo_state SET processed_hash='' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	git, err := gitstore.Open(t.TempDir() + "/repo.git")
	if err != nil {
		t.Fatal(err)
	}
	sc, err := scanner.New(scanner.DefaultConfig(), git, db, &scanner.FoundationIndexer{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	return New(git, db, sc), ctx
}

// seedConfig writes the demo schemas and workflow definitions.
func seedConfig(t *testing.T, s *Service, ctx context.Context) {
	t.Helper()
	_, err := s.Update(ctx, func(head plumbing.Hash, cs *gitstore.Changeset) (string, error) {
		cs.Write(".origoa/schemas/requirement.json", []byte(`{
  "artifactType": "requirement",
  "kind": "entry",
  "displayName": "Requirement",
  "hidPrefix": "REQ",
  "fields": [
    {"id": "priority", "type": "enum", "options": ["low", "medium", "high"], "indexed": true},
    {"id": "description", "type": "richtext"}
  ],
  "workflows": ["review"],
  "relationships": [
    {"linkType": "satisfies", "sourceTypes": ["requirement"], "targetTypes": ["testcase"], "cardinality": "many-to-many"},
    {"linkType": "parent", "sourceTypes": ["requirement"], "targetTypes": ["requirement"], "cardinality": "many-to-one"}
  ]
}`))
		cs.Write(".origoa/schemas/testcase.json", []byte(`{
  "artifactType": "testcase", "kind": "entry", "displayName": "Test Case", "hidPrefix": "TC",
  "fields": [{"id": "steps", "type": "multitext"}]
}`))
		cs.Write(".origoa/schemas/spec.json", []byte(`{
  "artifactType": "spec", "kind": "document", "displayName": "Specification", "hidPrefix": "SPEC",
  "workflows": ["review"]
}`))
		cs.Write(".origoa/workflows/review.json", []byte(`{
  "name": "review", "initial": "draft",
  "states": [{"id": "draft"}, {"id": "in_review"}, {"id": "approved"}],
  "transitions": [
    {"from": "draft", "to": "in_review", "name": "Submit"},
    {"from": "in_review", "to": "approved", "name": "Approve"},
    {"from": "in_review", "to": "draft", "name": "Reject"}
  ]
}`))
		return "Repository configuration created", nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCreateEntryWithHIDAndWorkflow(t *testing.T) {
	s, ctx := newService(t)
	seedConfig(t, s, ctx)
	guid, err := s.CreateArtifact(ctx, CreateArtifactParams{
		Kind: "entry", Folder: "specs/core", Type: "requirement", Title: "The system shall start",
		Fields: map[string]any{"priority": "high"},
	})
	if err != nil {
		t.Fatal(err)
	}
	row, err := s.DB.GetArtifact(ctx, guid)
	if err != nil || row == nil {
		t.Fatalf("artifact not projected: %v", err)
	}
	if row.HID != "REQ-1" {
		t.Fatalf("expected generated HID REQ-1, got %q", row.HID)
	}
	var f struct {
		Workflows map[string]string `json:"workflows"`
	}
	json.Unmarshal(row.Content, &f)
	if f.Workflows["review"] != "draft" {
		t.Fatalf("workflow not initialized: %+v", f)
	}
	// Second entry gets REQ-2.
	guid2, err := s.CreateArtifact(ctx, CreateArtifactParams{
		Kind: "entry", Folder: "specs/core", Type: "requirement", Title: "Second"})
	if err != nil {
		t.Fatal(err)
	}
	row2, _ := s.DB.GetArtifact(ctx, guid2)
	if row2.HID != "REQ-2" {
		t.Fatalf("expected REQ-2, got %q", row2.HID)
	}
	// Field index and search.
	res, err := s.DB.Search(ctx, projection.SearchQuery{Fields: map[string]string{"priority": "high"}})
	if err != nil || len(res) != 1 || res[0].GUID != guid {
		t.Fatalf("field search failed: %v %v", res, err)
	}
	res, err = s.DB.Search(ctx, projection.SearchQuery{Text: "start"})
	if err != nil || len(res) != 1 {
		t.Fatalf("fts failed: %v %v", res, err)
	}
}

func TestUpdateIsStableAndConflictChecked(t *testing.T) {
	s, ctx := newService(t)
	seedConfig(t, s, ctx)
	guid, _ := s.CreateArtifact(ctx, CreateArtifactParams{
		Kind: "entry", Folder: "specs", Type: "requirement", Title: "T",
		Fields: map[string]any{"priority": "low"}})
	head1, _ := s.Git.Head()

	// Setting the identical value produces no commit.
	if err := s.UpdateArtifact(ctx, guid, UpdateArtifactParams{Fields: map[string]any{"priority": "low"}}); err != nil {
		t.Fatal(err)
	}
	head2, _ := s.Git.Head()
	if head1 != head2 {
		t.Fatal("no-op update created a commit")
	}

	// A real change advances HEAD and updates the projection.
	if err := s.UpdateArtifact(ctx, guid, UpdateArtifactParams{Fields: map[string]any{"priority": "high"}}); err != nil {
		t.Fatal(err)
	}
	head3, _ := s.Git.Head()
	if head3 == head2 {
		t.Fatal("update did not create a commit")
	}

	// Optimistic concurrency: stale revision is rejected.
	err := s.UpdateArtifact(ctx, guid, UpdateArtifactParams{
		Fields: map[string]any{"priority": "medium"}, IfRevision: head1.String()})
	if err != ErrConflict {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
}

func TestOverlayResolution(t *testing.T) {
	s, ctx := newService(t)
	seedConfig(t, s, ctx)
	base, _ := s.CreateArtifact(ctx, CreateArtifactParams{
		Kind: "entry", Folder: "products", Type: "requirement", Title: "Base product",
		Fields: map[string]any{"priority": "low", "description": "base description"}})
	overlay, err := s.CreateArtifact(ctx, CreateArtifactParams{
		Kind: "entry", Folder: "products", Type: "requirement", Title: "Variant", Base: base,
		Fields: map[string]any{"priority": "high"}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.ResolveOverlay(ctx, overlay)
	if err != nil {
		t.Fatal(err)
	}
	if res.Fields["priority"] != "high" {
		t.Fatalf("overlay field must win: %v", res.Fields)
	}
	if res.Fields["description"] != "base description" {
		t.Fatalf("base field must be inherited: %v", res.Fields)
	}
	if res.FieldOrigin["description"] != base || res.FieldOrigin["priority"] != overlay {
		t.Fatalf("bad origins: %v", res.FieldOrigin)
	}
	if len(res.Chain) != 2 {
		t.Fatalf("bad chain: %v", res.Chain)
	}
	// Cycle protection.
	if err := s.UpdateArtifact(ctx, base, UpdateArtifactParams{Base: &overlay}); err == nil {
		t.Fatal("cycle was not rejected")
	}
}

func TestLinksAndCardinality(t *testing.T) {
	s, ctx := newService(t)
	seedConfig(t, s, ctx)
	r1, _ := s.CreateArtifact(ctx, CreateArtifactParams{Kind: "entry", Folder: "reqs", Type: "requirement", Title: "R1"})
	r2, _ := s.CreateArtifact(ctx, CreateArtifactParams{Kind: "entry", Folder: "reqs", Type: "requirement", Title: "R2"})
	tc, _ := s.CreateArtifact(ctx, CreateArtifactParams{Kind: "entry", Folder: "tests", Type: "testcase", Title: "TC1"})

	if _, err := s.CreateLink(ctx, CreateLinkParams{Type: "satisfies", Source: r1, Target: tc}); err != nil {
		t.Fatal(err)
	}
	// Constraint: satisfies must target a testcase.
	if _, err := s.CreateLink(ctx, CreateLinkParams{Type: "satisfies", Source: r1, Target: r2}); err == nil {
		t.Fatal("target type constraint not enforced")
	}
	// many-to-one: a requirement has at most one parent.
	if _, err := s.CreateLink(ctx, CreateLinkParams{Type: "parent", Source: r1, Target: r2}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateLink(ctx, CreateLinkParams{Type: "parent", Source: r1, Target: r2}); err == nil {
		t.Fatal("many-to-one cardinality not enforced")
	}
	links, err := s.DB.LinksFor(ctx, r1)
	if err != nil || len(links) != 2 {
		t.Fatalf("links: %v %v", links, err)
	}
	// Undefined link types remain allowed.
	if _, err := s.CreateLink(ctx, CreateLinkParams{Type: "relates-to", Source: tc, Target: r2}); err != nil {
		t.Fatalf("arbitrary link type rejected: %v", err)
	}
}

func TestCommentsAndThreads(t *testing.T) {
	s, ctx := newService(t)
	seedConfig(t, s, ctx)
	r1, _ := s.CreateArtifact(ctx, CreateArtifactParams{Kind: "entry", Folder: "reqs", Type: "requirement", Title: "R1"})
	c1, err := s.CreateComment(ctx, CreateCommentParams{Subject: r1, Author: "alice", Text: "Looks wrong"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateComment(ctx, CreateCommentParams{Subject: r1, Parent: c1, Author: "bob", Text: "Agreed"}); err != nil {
		t.Fatal(err)
	}
	comments, err := s.DB.CommentsFor(ctx, r1)
	if err != nil || len(comments) != 2 {
		t.Fatalf("comments: %v %v", comments, err)
	}
}

func TestWorkflowTransitions(t *testing.T) {
	s, ctx := newService(t)
	seedConfig(t, s, ctx)
	guid, _ := s.CreateArtifact(ctx, CreateArtifactParams{Kind: "entry", Folder: "reqs", Type: "requirement", Title: "R1"})

	// draft → approved is not allowed.
	if err := s.WorkflowTransition(ctx, guid, "review", "approved", ""); err == nil {
		t.Fatal("invalid transition accepted")
	}
	if err := s.WorkflowTransition(ctx, guid, "review", "in_review", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.WorkflowTransition(ctx, guid, "review", "approved", ""); err != nil {
		t.Fatal(err)
	}
	row, _ := s.DB.GetArtifact(ctx, guid)
	var f struct {
		Workflows map[string]string `json:"workflows"`
	}
	json.Unmarshal(row.Content, &f)
	if f.Workflows["review"] != "approved" {
		t.Fatalf("state not persisted: %v", f)
	}
	// Workflow state is field-indexed.
	res, err := s.DB.Search(ctx, projection.SearchQuery{Fields: map[string]string{"workflow.review": "approved"}})
	if err != nil || len(res) != 1 {
		t.Fatalf("workflow state search: %v %v", res, err)
	}
}

func TestMoveArtifactRelocatesMetadata(t *testing.T) {
	s, ctx := newService(t)
	seedConfig(t, s, ctx)
	r1, _ := s.CreateArtifact(ctx, CreateArtifactParams{Kind: "entry", Folder: "a", Type: "requirement", Title: "R1"})
	r2, _ := s.CreateArtifact(ctx, CreateArtifactParams{Kind: "entry", Folder: "a", Type: "requirement", Title: "R2"})
	lg, _ := s.CreateLink(ctx, CreateLinkParams{Type: "relates", Source: r1, Target: r2})
	cg, _ := s.CreateComment(ctx, CreateCommentParams{Subject: r1, Text: "note"})

	if err := s.MoveArtifact(ctx, r1, "b/deep", ""); err != nil {
		t.Fatal(err)
	}
	row, _ := s.DB.GetArtifact(ctx, r1)
	if row.ParentPath != "b/deep" {
		t.Fatalf("artifact not moved: %+v", row)
	}
	// GUID unchanged, references intact, metadata relocated with the source.
	linkRow, _ := s.DB.GetArtifact(ctx, lg)
	if linkRow == nil || linkRow.ParentPath != "b/deep" {
		t.Fatalf("link metadata not relocated: %+v", linkRow)
	}
	commentRow, _ := s.DB.GetArtifact(ctx, cg)
	if commentRow == nil || commentRow.ParentPath != "b/deep" {
		t.Fatalf("comment metadata not relocated: %+v", commentRow)
	}
	links, _ := s.DB.LinksFor(ctx, r1)
	if len(links) != 1 {
		t.Fatalf("references broken by move: %v", links)
	}
	// No tombstone for a moved artifact.
	if d, _ := s.DB.GetDeletedArtifact(ctx, r1); d != nil {
		t.Fatalf("move recorded as deletion: %+v", d)
	}
}

func TestDeleteRecordsTombstone(t *testing.T) {
	s, ctx := newService(t)
	seedConfig(t, s, ctx)
	guid, _ := s.CreateArtifact(ctx, CreateArtifactParams{Kind: "entry", Folder: "x", Type: "requirement", Title: "Doomed"})
	if err := s.DeleteArtifact(ctx, guid, ""); err != nil {
		t.Fatal(err)
	}
	if row, _ := s.DB.GetArtifact(ctx, guid); row != nil {
		t.Fatal("artifact still projected")
	}
	d, err := s.DB.GetDeletedArtifact(ctx, guid)
	if err != nil || d == nil {
		t.Fatalf("tombstone missing: %v", err)
	}
	if d.Title != "Doomed" || d.DeletedInCommit == "" {
		t.Fatalf("bad tombstone: %+v", d)
	}
}

func TestExternalGitChangeIsReplayed(t *testing.T) {
	s, ctx := newService(t)
	seedConfig(t, s, ctx)
	// Simulate a direct Git modification bypassing the backend.
	head, _ := s.Git.Head()
	cs := gitstore.NewChangeset()
	cs.Write("ext/11111111-2222-3333-4444-555555555555/.origoa.json", []byte(`{
  "guid": "11111111-2222-3333-4444-555555555555",
  "kind": "entry", "type": "requirement", "title": "Externally created"
}`))
	h, err := s.Git.BuildCommit(head, cs, "external tooling commit", "ext", "ext@x")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Git.PublishCommit(h, head); err != nil {
		t.Fatal(err)
	}
	// The projection catches up during the next synchronization cycle.
	if err := s.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	row, _ := s.DB.GetArtifact(ctx, "11111111-2222-3333-4444-555555555555")
	if row == nil || row.Title != "Externally created" {
		t.Fatalf("external commit not replayed: %+v", row)
	}
}

func TestConcurrentWritersRetry(t *testing.T) {
	s, ctx := newService(t)
	seedConfig(t, s, ctx)
	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = s.CreateArtifact(ctx, CreateArtifactParams{
				Kind: "entry", Folder: "conc", Type: "testcase", Title: fmt.Sprintf("E%d", i)})
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("writer %d failed: %v", i, err)
		}
	}
	rows, err := s.DB.Search(ctx, projection.SearchQuery{Folder: "conc"})
	if err != nil || len(rows) != n {
		t.Fatalf("expected %d artifacts, got %d (%v)", n, len(rows), err)
	}
	// Linear history: n+1 commits (config + n creates).
	head, _ := s.Git.Head()
	chain, _ := s.Git.CommitsBetween(plumbing.ZeroHash, head)
	if len(chain) != n+1 {
		t.Fatalf("expected %d commits, got %d", n+1, len(chain))
	}
}

func TestReindexReconstructsProjection(t *testing.T) {
	s, ctx := newService(t)
	seedConfig(t, s, ctx)
	r1, _ := s.CreateArtifact(ctx, CreateArtifactParams{Kind: "entry", Folder: "a", Type: "requirement", Title: "Alpha", Fields: map[string]any{"priority": "high"}})
	r2, _ := s.CreateArtifact(ctx, CreateArtifactParams{Kind: "entry", Folder: "b", Type: "testcase", Title: "Beta"})
	s.CreateLink(ctx, CreateLinkParams{Type: "relates", Source: r1, Target: r2})
	s.CreateComment(ctx, CreateCommentParams{Subject: r1, Text: "hello world"})
	doomed, _ := s.CreateArtifact(ctx, CreateArtifactParams{Kind: "entry", Folder: "a", Type: "testcase", Title: "Gone"})
	s.DeleteArtifact(ctx, doomed, "")

	before, err := s.DB.GetStats(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Wipe everything derived and rebuild from Git alone.
	if _, err := s.DB.Pool.Exec(ctx, `
		TRUNCATE artifacts, field_index, fts, link_index, comment_index,
		         hid_history, deleted_artifacts, folders, config_objects;
		UPDATE repo_state SET processed_hash=''`); err != nil {
		t.Fatal(err)
	}
	if err := s.Reindex(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := s.DB.GetStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("reindex diverged:\nbefore %+v\nafter  %+v", before, after)
	}
	// Deleted artifact history was reconstructed from Git history.
	d, _ := s.DB.GetDeletedArtifact(ctx, doomed)
	if d == nil || d.Title != "Gone" {
		t.Fatalf("history scan missed deletion: %+v", d)
	}
	// Search still works after reindex.
	res, _ := s.DB.Search(ctx, projection.SearchQuery{Text: "Alpha"})
	if len(res) != 1 {
		t.Fatalf("fts after reindex: %v", res)
	}
	res, _ = s.DB.Search(ctx, projection.SearchQuery{Fields: map[string]string{"priority": "high"}})
	if len(res) != 1 {
		t.Fatalf("field index after reindex: %v", res)
	}
}

func TestMoveFolderBulk(t *testing.T) {
	s, ctx := newService(t)
	seedConfig(t, s, ctx)
	for i := 0; i < 3; i++ {
		s.CreateArtifact(ctx, CreateArtifactParams{Kind: "entry", Folder: "old/sub", Type: "testcase", Title: fmt.Sprintf("E%d", i)})
	}
	if err := s.MoveFolder(ctx, "old", "renamed"); err != nil {
		t.Fatal(err)
	}
	rows, err := s.DB.Search(ctx, projection.SearchQuery{Folder: "renamed/sub"})
	if err != nil || len(rows) != 3 {
		t.Fatalf("artifacts not moved: %d %v", len(rows), err)
	}
	rows, _ = s.DB.Search(ctx, projection.SearchQuery{Folder: "old/sub"})
	if len(rows) != 0 {
		t.Fatal("old folder still populated")
	}
	// GUID-based references survived: no tombstones.
	for _, r := range rows {
		if d, _ := s.DB.GetDeletedArtifact(ctx, r.GUID); d != nil {
			t.Fatal("move recorded deletions")
		}
	}
}

func TestHIDHistoryTracksChanges(t *testing.T) {
	s, ctx := newService(t)
	seedConfig(t, s, ctx)
	guid, _ := s.CreateArtifact(ctx, CreateArtifactParams{Kind: "entry", Folder: "reqs", Type: "requirement", Title: "R"})
	newHID := "REQ-9999"
	if err := s.UpdateArtifact(ctx, guid, UpdateArtifactParams{HID: &newHID}); err != nil {
		t.Fatal(err)
	}
	hist, err := s.DB.HIDHistory(ctx, guid)
	if err != nil || len(hist) != 2 {
		t.Fatalf("expected 2 historical HIDs, got %v (%v)", hist, err)
	}
	// Duplicate HIDs are rejected.
	other, _ := s.CreateArtifact(ctx, CreateArtifactParams{Kind: "entry", Folder: "reqs", Type: "requirement", Title: "Other"})
	dup := "REQ-9999"
	if err := s.UpdateArtifact(ctx, other, UpdateArtifactParams{HID: &dup}); err == nil {
		t.Fatal("duplicate HID accepted")
	}
}
