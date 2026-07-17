package core

import (
	"strings"
	"testing"

	"github.com/thomdehoog/origoa/internal/gitx"
)

// TestUnknownPropertiesSurviveUpdate guards the flagship stable-serialization
// claim end to end: a property added by an external tool (direct Git edit)
// must survive an API update byte-for-byte, in its original position.
func TestUnknownPropertiesSurviveUpdate(t *testing.T) {
	dir := t.TempDir() + "/repo.git"
	f, err := openTest(t, dir)
	must(t, err)
	defer f.Close()
	m, err := f.CreateArtifact(KindEntry, "a", "part", body(t, `{"title":"v1","fields":{"x":"1"}}`))
	must(t, err)

	// External tool injects an extension property between existing keys.
	_, obj, err := f.Artifact(m.GUID)
	must(t, err)
	obj.SetString("x-extension", "keep me")
	content, err := obj.Encode()
	must(t, err)
	g := &gitx.Repo{Dir: dir}
	_, err = g.Commit("manual extension", []gitx.Op{{Path: m.FilePath, Content: content}})
	must(t, err)

	// API update of an unrelated property.
	_, err = f.UpdateArtifact(m.GUID, body(t, `{"title":"v2"}`), "")
	must(t, err)
	m2, obj2, err := f.Artifact(m.GUID)
	must(t, err)
	if obj2.GetString("x-extension") != "keep me" {
		t.Fatalf("extension property lost: keys=%v", obj2.Keys())
	}
	if obj2.GetString("title") != "v2" || m2.Title != "v2" {
		t.Fatal("update did not apply")
	}
	// Key order: guid, kind, type, title, fields, x-extension (appended when
	// injected) — order must be unchanged apart from the value edit.
	want := obj.Keys()
	got := obj2.Keys()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("key order changed: %v -> %v", want, got)
	}
}

// TestHistorySurvivesMove: moving an artifact must not orphan its commit
// history (GUID directories are globally unique, so history follows the GUID).
func TestHistorySurvivesMove(t *testing.T) {
	f := testFoundation(t)
	m, err := f.CreateArtifact(KindEntry, "old/place", "part", body(t, `{"title":"h1"}`))
	must(t, err)
	_, err = f.UpdateArtifact(m.GUID, body(t, `{"title":"h2"}`), "")
	must(t, err)
	_, err = f.MoveArtifact(m.GUID, "new/place")
	must(t, err)

	log, err := f.History(m.GUID, 50)
	must(t, err)
	if len(log) != 3 {
		t.Fatalf("history after move = %d commits, want 3 (create, update, move): %+v", len(log), log)
	}
	if !strings.Contains(log[len(log)-1].Subject, "created") {
		t.Fatalf("oldest history entry = %q, want the create commit", log[len(log)-1].Subject)
	}
}

// TestHostileFolderNames: folder names that are dangerous as git pathspecs
// are either rejected up front or fully functional — never a silent no-op
// delete or an unmovable artifact.
func TestHostileFolderNames(t *testing.T) {
	f := testFoundation(t)
	// Rejected outright.
	for _, p := range []string{":team", ":!x", "a\nb", "a\tb", strings.Repeat("d/", 40) + "x",
		strings.Repeat("s", 200), "a/" + strings.Repeat("s", 600)} {
		if _, err := f.CreateArtifact(KindEntry, p, "part", body(t, `{}`)); err == nil {
			t.Fatalf("folder %q accepted", p)
		}
	}
	// Glob metacharacters are legal folder names and must behave literally.
	for _, p := range []string{"a*b", "q?x", "br[ack]et", "spa ced", "uni-код"} {
		m, err := f.CreateArtifact(KindEntry, p, "part", body(t, `{"title":"in `+p+`"}`))
		must(t, err)
		if len(mustList(t, f, KindEntry, "", p, false)) != 1 {
			t.Fatalf("folder %q: artifact not listed", p)
		}
		moved, err := f.MoveArtifact(m.GUID, p+"/sub")
		must(t, err)
		if moved.Folder != p+"/sub" {
			t.Fatalf("folder %q: move failed: %+v", p, moved)
		}
		hist, err := f.History(m.GUID, 10)
		must(t, err)
		if len(hist) != 2 {
			t.Fatalf("folder %q: history = %d commits, want 2", p, len(hist))
		}
		must(t, f.DeleteArtifact(m.GUID))
		if _, err := f.Meta(m.GUID); err == nil {
			t.Fatalf("folder %q: delete was a silent no-op", p)
		}
		// The projection must agree with a rebuild (nothing half-deleted).
		must(t, f.Reindex())
		if _, err := f.Meta(m.GUID); err == nil {
			t.Fatalf("folder %q: artifact resurrected by rebuild", p)
		}
	}
}

// TestCommentThreads: replies attach to parents on the same subject and come
// back in chronological order.
func TestCommentThreads(t *testing.T) {
	f := testFoundation(t)
	m, err := f.CreateArtifact(KindEntry, "", "part", body(t, `{"title":"discussed"}`))
	must(t, err)
	c1, err := f.CreateComment(m.GUID, "first", "", "alice")
	must(t, err)
	c2, err := f.CreateComment(m.GUID, "reply", c1.GUID, "bob")
	must(t, err)

	got := mustComments(t, f, m.GUID)
	if len(got) != 2 || got[0].GUID != c1.GUID || got[1].GUID != c2.GUID {
		t.Fatalf("comments order = %+v", got)
	}
	_, obj, err := f.Artifact(c2.GUID)
	must(t, err)
	if obj.GetString("parent") != c1.GUID {
		t.Fatal("reply lost its parent")
	}
}

// TestMultiFileArtifactMoveDelete: artifact directories may carry extra files
// (attachments via direct Git); move and delete must carry all of them.
func TestMultiFileArtifactMoveDelete(t *testing.T) {
	dir := t.TempDir() + "/repo.git"
	f, err := openTest(t, dir)
	must(t, err)
	defer f.Close()
	m, err := f.CreateArtifact(KindDocument, "docs", "spec", body(t, `{"title":"with attachment"}`))
	must(t, err)
	g := &gitx.Repo{Dir: dir}
	attach := "docs/" + m.GUID + "/attachment.bin"
	_, err = g.Commit("attach", []gitx.Op{{Path: attach, Content: []byte("BLOB")}})
	must(t, err)

	moved, err := f.MoveArtifact(m.GUID, "archive")
	must(t, err)
	head, err := g.Head()
	must(t, err)
	entries, err := g.ListTree(head, "archive/"+m.GUID+"/")
	must(t, err)
	if len(entries) != 2 {
		t.Fatalf("moved artifact has %d files, want 2 (attachment lost)", len(entries))
	}
	must(t, f.DeleteArtifact(moved.GUID))
	head, err = g.Head()
	must(t, err)
	entries, err = g.ListTree(head, "archive/"+m.GUID+"/")
	must(t, err)
	if len(entries) != 0 {
		t.Fatalf("delete left %d files behind", len(entries))
	}
}

// TestDocumentContentUpdate: content round-trips through PUT.
func TestDocumentContentUpdate(t *testing.T) {
	f := testFoundation(t)
	m, err := f.CreateArtifact(KindDocument, "", "spec", body(t, `{"title":"d","content":[{"type":"text","text":"v1"}]}`))
	must(t, err)
	_, err = f.UpdateArtifact(m.GUID, body(t, `{"content":[{"type":"text","text":"v2"},{"type":"entryRef","guid":"`+NewGUID()+`"}]}`), "")
	must(t, err)
	_, obj, err := f.Artifact(m.GUID)
	must(t, err)
	raw, _ := obj.Get("content")
	if !strings.Contains(string(raw), `"v2"`) || !strings.Contains(string(raw), "entryRef") {
		t.Fatalf("content = %s", raw)
	}
	// Entries must reject content.
	e, err := f.CreateArtifact(KindEntry, "", "part", body(t, `{}`))
	must(t, err)
	if _, err := f.UpdateArtifact(e.GUID, body(t, `{"content":[]}`), ""); err == nil {
		t.Fatal("entry accepted content")
	}
}

// TestNoOpUpdateCreatesNoCommit: an update that changes nothing must not
// pollute Git history with an empty-diff commit.
func TestNoOpUpdateCreatesNoCommit(t *testing.T) {
	f := testFoundation(t)
	m, err := f.CreateArtifact(KindEntry, "", "part", body(t, `{"title":"same"}`))
	must(t, err)
	before, err := f.History(m.GUID, 10)
	must(t, err)
	m2, err := f.UpdateArtifact(m.GUID, body(t, `{"title":"same"}`), "")
	must(t, err)
	after, err := f.History(m.GUID, 10)
	must(t, err)
	if len(after) != len(before) {
		t.Fatalf("no-op update created a commit (%d -> %d)", len(before), len(after))
	}
	if m2.ETag != m.ETag {
		t.Fatal("no-op update changed the ETag")
	}
}

// TestLinkFields: custom fields on links round-trip.
func TestLinkFields(t *testing.T) {
	f := testFoundation(t)
	a, err := f.CreateArtifact(KindEntry, "", "part", body(t, `{"title":"a"}`))
	must(t, err)
	b, err := f.CreateArtifact(KindEntry, "", "part", body(t, `{"title":"b"}`))
	must(t, err)
	l, err := f.CreateLink("relates", a.GUID, b.GUID, []byte(`{"weight":3,"note":"strong"}`))
	must(t, err)
	_, obj, err := f.Artifact(l.GUID)
	must(t, err)
	raw, _ := obj.Get("fields")
	if !strings.Contains(string(raw), `"weight"`) {
		t.Fatalf("link fields = %s", raw)
	}
	if _, err := f.CreateLink("bad", a.GUID, b.GUID, []byte(`[1,2]`)); err == nil {
		t.Fatal("non-object link fields accepted")
	}
}

// TestConfigNameMustMatchID prevents silent same-scope shadowing.
func TestConfigNameMustMatchID(t *testing.T) {
	f := testFoundation(t)
	if err := f.PutSchema("", "alias", &Schema{ArtifactType: "requirement"}); err == nil {
		t.Fatal("schema name != artifactType accepted")
	}
	if err := f.PutWorkflow("", "alias", &Workflow{ID: "dev", Initial: "open", States: []string{"open"}}); err == nil {
		t.Fatal("workflow name != id accepted")
	}
}
