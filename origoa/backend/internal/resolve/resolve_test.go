package resolve

import (
	"context"
	"os"
	"testing"

	"origoa/internal/projection"
)

func testDSN() string {
	if dsn := os.Getenv("ORIGOA_TEST_DSN"); dsn != "" {
		return dsn
	}
	return "postgres://origoa:origoa@localhost:5432/origoa_test"
}

// newDB connects to the scratch projection database and clears it. Resolve
// reads configuration objects directly from the projection, so these tests
// seed config_objects rather than driving the full repository service.
func newDB(t *testing.T) (*projection.DB, context.Context) {
	t.Helper()
	ctx := context.Background()
	db, err := projection.Connect(ctx, testDSN())
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	t.Cleanup(db.Close)
	if _, err := db.Pool.Exec(ctx, `TRUNCATE config_objects`); err != nil {
		t.Fatal(err)
	}
	return db, ctx
}

func putConfig(t *testing.T, db *projection.DB, ctx context.Context, storagePath, scope, category, name, content string) {
	t.Helper()
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err := db.UpsertConfigObject(ctx, tx, storagePath, scope, category, name, []byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestEffectiveSchemaLexicalComposition(t *testing.T) {
	db, ctx := newDB(t)
	// Root schema, then a more specialized one deeper in the hierarchy.
	putConfig(t, db, ctx, ".origoa/schemas/req.json", "", "schemas", "req", `{
		"artifactType":"req","displayName":"Requirement","hidPrefix":"REQ",
		"fields":[{"id":"priority","type":"enum","options":["low","high"]}],
		"workflows":["review"]}`)
	putConfig(t, db, ctx, "eng/safety/.origoa/schemas/req.json", "eng/safety", "schemas", "req", `{
		"artifactType":"req",
		"fields":[{"id":"sil","type":"enum","options":["SIL-1","SIL-2"]}]}`)

	// At the root only the base definition applies.
	eff, err := EffectiveSchema(ctx, db, nil, "", "req")
	if err != nil || eff == nil {
		t.Fatalf("root schema: %v %v", eff, err)
	}
	if eff.HIDPrefix != "REQ" || len(eff.Fields) != 1 {
		t.Fatalf("root effective schema wrong: %+v", eff)
	}

	// Deeper location composes both, nearest last, inheriting the prefix.
	eff, err = EffectiveSchema(ctx, db, nil, "eng/safety", "req")
	if err != nil || eff == nil {
		t.Fatal(err)
	}
	if eff.HIDPrefix != "REQ" {
		t.Fatalf("inherited prefix lost: %+v", eff)
	}
	if len(eff.Fields) != 2 {
		t.Fatalf("expected composed fields [priority, sil], got %+v", eff.Fields)
	}
	if len(eff.Sources) != 2 {
		t.Fatalf("expected two contributing sources, got %v", eff.Sources)
	}

	// An unrelated location sees no such type.
	if eff, _ := EffectiveSchema(ctx, db, nil, "other", "req"); eff != nil && len(eff.Fields) != 1 {
		t.Fatalf("unrelated location composed specialized fields: %+v", eff)
	}
	// An undefined type resolves to nil.
	if eff, _ := EffectiveSchema(ctx, db, nil, "", "nonexistent"); eff != nil {
		t.Fatalf("undefined type resolved to %+v", eff)
	}
}

func TestWorkflowResolution(t *testing.T) {
	db, ctx := newDB(t)
	putConfig(t, db, ctx, ".origoa/workflows/review.json", "", "workflows", "review", `{
		"name":"review","initial":"draft",
		"states":[{"id":"draft"},{"id":"done"}],
		"transitions":[{"from":"draft","to":"done"}]}`)

	wd, err := Workflow(ctx, db, nil, "any/where", "review")
	if err != nil {
		t.Fatal(err)
	}
	if wd.Initial != "draft" || !wd.CanTransition("draft", "done") {
		t.Fatalf("workflow resolved wrong: %+v", wd)
	}
	if _, err := Workflow(ctx, db, nil, "", "missing"); err == nil {
		t.Error("expected error for undefined workflow")
	}
}

func TestAvailableTypes(t *testing.T) {
	db, ctx := newDB(t)
	putConfig(t, db, ctx, ".origoa/schemas/req.json", "", "schemas", "req", `{"artifactType":"req","displayName":"Requirement"}`)
	putConfig(t, db, ctx, ".origoa/schemas/tc.json", "", "schemas", "tc", `{"artifactType":"testcase","kind":"entry","displayName":"Test Case"}`)
	types, err := AvailableTypes(ctx, db, nil, "some/folder")
	if err != nil {
		t.Fatal(err)
	}
	if len(types) != 2 {
		t.Fatalf("expected 2 available types, got %d", len(types))
	}
}
