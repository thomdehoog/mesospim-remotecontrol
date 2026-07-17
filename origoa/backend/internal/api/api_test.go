package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"

	"origoa/internal/gitstore"
	"origoa/internal/projection"
	"origoa/internal/repo"
	"origoa/internal/scanner"
)

func testDSN() string {
	if dsn := os.Getenv("ORIGOA_TEST_DSN"); dsn != "" {
		return dsn
	}
	return "postgres://origoa:origoa@localhost:5432/origoa_test"
}

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	ctx := context.Background()
	db, err := projection.Connect(ctx, testDSN())
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	t.Cleanup(db.Close)
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
	svc := repo.New(git, db, sc)
	// Seed a schema and workflow.
	_, err = svc.Update(ctx, func(head plumbing.Hash, cs *gitstore.Changeset) (string, error) {
		cs.Write(".origoa/schemas/requirement.json", []byte(`{
			"artifactType": "requirement", "kind": "entry", "displayName": "Requirement",
			"hidPrefix": "REQ",
			"fields": [{"id": "priority", "type": "enum", "options": ["low","high"], "indexed": true}],
			"workflows": ["review"]}`))
		cs.Write(".origoa/workflows/review.json", []byte(`{
			"name": "review", "initial": "draft",
			"states": [{"id":"draft"},{"id":"approved"}],
			"transitions": [{"from":"draft","to":"approved","name":"Approve"}]}`))
		return "Repository configuration created", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(NewServer(svc, "").Handler())
	t.Cleanup(ts.Close)
	return ts
}

func doJSON(t *testing.T, method, url string, body any, wantStatus int) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req, _ := http.NewRequest(method, url, &buf)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != wantStatus {
		t.Fatalf("%s %s: status %d (want %d): %v", method, url, resp.StatusCode, wantStatus, out)
	}
	return out
}

func TestAPIEndToEnd(t *testing.T) {
	ts := newTestServer(t)
	base := ts.URL

	// Create an entry.
	created := doJSON(t, "POST", base+"/api/entries", map[string]any{
		"folder": "specs", "type": "requirement", "title": "Boot in 3 seconds",
		"fields": map[string]any{"priority": "high"},
	}, http.StatusCreated)
	guid := created["guid"].(string)
	if created["hid"] != "REQ-1" {
		t.Fatalf("expected REQ-1: %v", created)
	}

	// Detail view includes schema, workflows and resolved overlay.
	detail := doJSON(t, "GET", base+"/api/artifacts/"+guid, nil, http.StatusOK)
	if detail["schema"] == nil || detail["workflows"] == nil || detail["resolved"] == nil {
		t.Fatalf("detail incomplete: %v", detail)
	}
	wfs := detail["workflows"].([]any)
	if len(wfs) != 1 {
		t.Fatalf("workflows: %v", wfs)
	}
	wf := wfs[0].(map[string]any)
	if wf["state"] != "draft" {
		t.Fatalf("initial state: %v", wf)
	}
	trans := wf["transitions"].([]any)
	if len(trans) != 1 {
		t.Fatalf("transitions: %v", trans)
	}

	// Workflow transition through the API.
	doJSON(t, "POST", base+"/api/artifacts/"+guid+"/workflows/review/transition",
		map[string]any{"to": "approved"}, http.StatusOK)

	// Update with optimistic concurrency: stale revision → 409.
	rev := created["updatedCommit"].(string)
	doJSON(t, "PATCH", base+"/api/artifacts/"+guid,
		map[string]any{"title": "New title", "ifRevision": rev}, http.StatusConflict)
	doJSON(t, "PATCH", base+"/api/artifacts/"+guid,
		map[string]any{"title": "New title"}, http.StatusOK)

	// Tree and search.
	tree := doJSON(t, "GET", base+"/api/tree?path=specs", nil, http.StatusOK)
	if len(tree["artifacts"].([]any)) != 1 {
		t.Fatalf("tree: %v", tree)
	}
	res := doJSON(t, "GET", base+"/api/search?q=title&field.priority=high", nil, http.StatusOK)
	if len(res["results"].([]any)) != 1 {
		t.Fatalf("search: %v", res)
	}

	// Second entry, link, comment.
	second := doJSON(t, "POST", base+"/api/entries", map[string]any{
		"folder": "specs", "type": "requirement", "title": "Second"}, http.StatusCreated)
	doJSON(t, "POST", base+"/api/links", map[string]any{
		"type": "relates", "source": guid, "target": second["guid"]}, http.StatusCreated)
	doJSON(t, "POST", base+"/api/comments", map[string]any{
		"subject": guid, "author": "alice", "text": "please review"}, http.StatusCreated)
	links := doJSON(t, "GET", base+"/api/artifacts/"+guid+"/links", nil, http.StatusOK)
	if len(links["links"].([]any)) != 1 {
		t.Fatalf("links: %v", links)
	}
	comments := doJSON(t, "GET", base+"/api/artifacts/"+guid+"/comments", nil, http.StatusOK)
	if len(comments["comments"].([]any)) != 1 {
		t.Fatalf("comments: %v", comments)
	}

	// History reflects the structured commit messages.
	hist := doJSON(t, "GET", base+"/api/artifacts/"+guid+"/history", nil, http.StatusOK)
	if len(hist["history"].([]any)) < 3 {
		t.Fatalf("history: %v", hist)
	}

	// Effective schema service endpoint.
	eff := doJSON(t, "GET", base+"/api/schemas/effective?path=specs&type=requirement", nil, http.StatusOK)
	if eff["hidPrefix"] != "REQ" {
		t.Fatalf("effective schema: %v", eff)
	}

	// Status endpoint.
	status := doJSON(t, "GET", base+"/api/status", nil, http.StatusOK)
	if status["maintenance"] != false {
		t.Fatalf("status: %v", status)
	}

	// Delete → tombstone → 410 with deletion info.
	doJSON(t, "DELETE", base+"/api/artifacts/"+second["guid"].(string), nil, http.StatusOK)
	gone := doJSON(t, "GET", base+"/api/artifacts/"+second["guid"].(string), nil, http.StatusGone)
	if gone["deleted"] == nil {
		t.Fatalf("tombstone: %v", gone)
	}

	// Validation errors are 400s.
	doJSON(t, "POST", base+"/api/entries", map[string]any{
		"folder": "specs", "type": "", "title": "x"}, http.StatusBadRequest)
}
