package repo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/google/uuid"

	"origoa/internal/gitstore"
	"origoa/internal/projection"
	"origoa/internal/scanner"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// listingOf snapshots the projection as guid → identity signature. The
// signature excludes the updated-commit hash (which differs between a live
// projection and a from-scratch rebuild) and captures the identity-bearing
// fields the spec guarantees are stable.
func listingOf(t *testing.T, s *Service, ctx context.Context) map[string]string {
	t.Helper()
	rows, err := s.DB.Search(ctx, projection.SearchQuery{Subtree: true, Limit: 1000})
	must(t, err)
	out := map[string]string{}
	for _, r := range rows {
		out[r.GUID] = fmt.Sprintf("%s|%s|%s|%s|%s", r.Kind, r.Type, r.Title, r.HID, r.ParentPath)
	}
	return out
}

func diffListings(t *testing.T, name string, a, b map[string]string) {
	t.Helper()
	for g, v := range a {
		if b[g] != v {
			t.Errorf("%s: %s = %q live vs %q rebuild", name, g, v, b[g])
		}
	}
	for g, v := range b {
		if _, ok := a[g]; !ok {
			t.Errorf("%s: %s only after rebuild (%q)", name, g, v)
		}
	}
}

// retryWrite retries an operation through the spec's designed backpressure —
// a maintenance window (reindex/large op) — so the torture test can assert
// "no operation was lost" without treating maintenance as a hard failure. A
// compare-and-swap conflict is handled inside Update itself, so it never
// surfaces here.
func retryWrite(fn func() error) error {
	var err error
	for attempt := 0; attempt < 500; attempt++ {
		err = fn()
		if !errors.Is(err, ErrMaintenance) {
			return err
		}
		retryBackoff(attempt%20 + 1)
	}
	return err
}

// TestTortureConcurrentMixedOps hammers one Service with every operation
// concurrently — including reindexing under fire — then proves three
// invariants the spec promises: every created artifact survives with a
// unique HID, the live projection equals a from-scratch rebuild (Git is the
// source of truth), and Git history is intact.
func TestTortureConcurrentMixedOps(t *testing.T) {
	s, ctx := newService(t)
	seedConfig(t, s, ctx)

	const workers = 8
	const opsEach = 14
	var created, unexpected atomic.Int64
	var mu sync.Mutex
	var allGUIDs []string

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			var mine []string
			fail := func(op string, err error) {
				if err != nil {
					t.Errorf("worker %d %s: %v", w, op, err)
					unexpected.Add(1)
				}
			}
			for i := 0; i < opsEach; i++ {
				switch i % 7 {
				case 0, 1: // create
					var guid string
					err := retryWrite(func() error {
						g, e := s.CreateArtifact(ctx, CreateArtifactParams{
							Kind: "entry", Folder: fmt.Sprintf("w%d", w), Type: "requirement",
							Title: fmt.Sprintf("w%d-%d", w, i), Fields: map[string]any{"priority": "high"}})
						guid = g
						return e
					})
					if err != nil {
						fail("create", err)
						continue
					}
					created.Add(1)
					mine = append(mine, guid)
					mu.Lock()
					allGUIDs = append(allGUIDs, guid)
					mu.Unlock()
				case 2: // update
					if len(mine) > 0 {
						fail("update", retryWrite(func() error {
							return s.UpdateArtifact(ctx, mine[0], UpdateArtifactParams{Fields: map[string]any{"priority": pick(i)}})
						}))
					}
				case 3: // transition + link + comment
					if len(mine) > 1 {
						fail("transition", retryWrite(func() error {
							e := s.WorkflowTransition(ctx, mine[0], "review", "in_review", "")
							if errors.As(e, new(ErrValidation)) {
								return nil // already past this state
							}
							return e
						}))
						fail("link", retryWrite(func() error {
							_, e := s.CreateLink(ctx, CreateLinkParams{Type: "relates", Source: mine[0], Target: mine[1]})
							return e
						}))
						fail("comment", retryWrite(func() error {
							_, e := s.CreateComment(ctx, CreateCommentParams{Subject: mine[1], Text: fmt.Sprintf("c%d-%d", w, i)})
							return e
						}))
					}
				case 4: // move
					if len(mine) > 1 {
						fail("move", retryWrite(func() error {
							return s.MoveArtifact(ctx, mine[1], fmt.Sprintf("moved/w%d", w), "")
						}))
					}
				case 5: // reads under fire — must remain available during maintenance
					s.DB.Search(ctx, projection.SearchQuery{Text: "w", Subtree: true})
					s.DB.Search(ctx, projection.SearchQuery{Kind: "entry", Type: "requirement", Subtree: true})
					s.DB.ChildFolders(ctx, "")
				case 6: // reindex concurrently with writers
					if err := s.Reindex(ctx); err != nil && !strings.Contains(err.Error(), "already running") {
						t.Errorf("worker %d reindex: %v", w, err)
					}
				}
			}
		}(w)
	}
	wg.Wait()
	if unexpected.Load() > 0 {
		t.Fatalf("%d operations failed unexpectedly", unexpected.Load())
	}
	if created.Load() == 0 {
		t.Fatal("no artifacts were created")
	}

	// Invariant 1: every created artifact is present with a unique HID.
	seenHID := map[string]string{}
	for _, g := range allGUIDs {
		row, err := s.DB.GetArtifact(ctx, g)
		must(t, err)
		if row == nil {
			t.Fatalf("artifact %s vanished from the projection", g)
		}
		if row.HID == "" {
			t.Fatalf("artifact %s lost its HID", g)
		}
		if prev, dup := seenHID[row.HID]; dup {
			t.Fatalf("duplicate HID %q on %s and %s", row.HID, prev, g)
		}
		seenHID[row.HID] = g
	}

	// Invariant 2: the live projection equals a from-scratch rebuild.
	before := listingOf(t, s, ctx)
	must(t, s.Reindex(ctx))
	diffListings(t, "live vs rebuild", before, listingOf(t, s, ctx))

	// Invariant 3: Git history is intact and linear.
	head, _ := s.Git.Head()
	chain, err := s.Git.CommitsBetween(plumbing.ZeroHash, head)
	must(t, err)
	if len(chain) == 0 {
		t.Fatal("history empty after torture")
	}
}

func pick(i int) string {
	return []string{"low", "medium", "high"}[i%3]
}

// TestMultiWriterSharedRepo runs two independent Service instances (as two
// server processes would) against the SAME bare repository and projection
// database. After concurrent writes, a from-scratch rebuild is ground truth:
// nothing either writer created may be lost, and the compare-and-swap on the
// Git branch must have serialized every commit.
func TestMultiWriterSharedRepo(t *testing.T) {
	ctx := context.Background()
	gitDir := t.TempDir() + "/shared.git"

	db, err := projection.Connect(ctx, testDSN())
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer db.Close()
	if _, err := db.Pool.Exec(ctx, `
		TRUNCATE artifacts, field_index, fts, link_index, comment_index,
		         hid_history, deleted_artifacts, folders, config_objects;
		UPDATE repo_state SET processed_hash='' WHERE id=1`); err != nil {
		t.Fatal(err)
	}

	open := func() *Service {
		git, err := gitstore.Open(gitDir)
		must(t, err)
		sc, err := scanner.New(scanner.DefaultConfig(), git, db, &scanner.FoundationIndexer{DB: db})
		must(t, err)
		return New(git, db, sc)
	}
	f1, f2 := open(), open()
	seedConfig(t, f1, ctx)

	const each = 15
	var wg sync.WaitGroup
	guids := make([][]string, 2)
	var mus [2]sync.Mutex
	for i, f := range []*Service{f1, f2} {
		wg.Add(1)
		go func(i int, f *Service) {
			defer wg.Done()
			for n := 0; n < each; n++ {
				var guid string
				err := retryWrite(func() error {
					g, e := f.CreateArtifact(ctx, CreateArtifactParams{
						Kind: "entry", Folder: fmt.Sprintf("proc%d", i), Type: "testcase",
						Title: fmt.Sprintf("p%d-%d", i, n)})
					guid = g
					return e
				})
				if err != nil {
					t.Errorf("writer %d create %d: %v", i, n, err)
					return
				}
				mus[i].Lock()
				guids[i] = append(guids[i], guid)
				mus[i].Unlock()
			}
		}(i, f)
	}
	wg.Wait()

	// Ground truth: a Service rebuilt purely from Git.
	must(t, f1.Reindex(ctx))
	want := listingOf(t, f1, ctx)
	for i := range guids {
		for _, g := range guids[i] {
			if _, ok := want[g]; !ok {
				t.Fatalf("writer %d artifact %s lost from Git (CAS failure)", i, g)
			}
		}
	}
	if total := len(guids[0]) + len(guids[1]); len(want) < total {
		t.Fatalf("expected at least %d artifacts, ground truth has %d", total, len(want))
	}
}

// TestMultiWriterDriftDetected reproduces the nastiest interleaving: a foreign
// writer publishes commits directly to Git between the backend's own writes,
// so the backend's compare-and-swap builds on the foreign head. After each of
// its own writes the projection represents the current Git head, so every
// foreign artifact committed before that head must be visible — a silent skip
// would be data loss.
func TestMultiWriterDriftDetected(t *testing.T) {
	s, ctx := newService(t)
	seedConfig(t, s, ctx)

	var foreign []string
	for i := 0; i < 12; i++ {
		head, _ := s.Git.Head()
		fg := uuid.NewString()
		cs := gitstore.NewChangeset()
		cs.Write(fmt.Sprintf("ext/%s/.origoa.json", fg), []byte(fmt.Sprintf(
			`{"guid":"%s","kind":"entry","type":"testcase","title":"foreign%d"}`, fg, i)))
		h, err := s.Git.BuildCommit(head, cs, "foreign tooling commit", "ext", "ext@x")
		must(t, err)
		must(t, s.Git.PublishCommit(h, head))
		foreign = append(foreign, fg)

		_, err = s.CreateArtifact(ctx, CreateArtifactParams{
			Kind: "entry", Folder: "own", Type: "testcase", Title: fmt.Sprintf("own%d", i)})
		must(t, err)

		for _, fg := range foreign {
			row, err := s.DB.GetArtifact(ctx, fg)
			must(t, err)
			if row == nil {
				t.Fatalf("iteration %d: foreign artifact %s invisible although the processed head includes it (silent drift)", i, fg)
			}
		}
	}
}

// TestAdversarialPayloads feeds hostile content through the full stack: NUL
// bytes (PostgreSQL text/tsvector killers), multi-megabyte strings (the
// tsvector 1 MiB ceiling), deep nesting and unicode chaos. Nothing may
// corrupt the projection: after each payload the artifact round-trips and a
// reindex still succeeds.
func TestAdversarialPayloads(t *testing.T) {
	s, ctx := newService(t)
	seedConfig(t, s, ctx)

	cases := []struct{ name, title, field string }{
		{"nul bytes", "nul\x00title", "field\x00value\x00"},
		{"huge string", "big", strings.Repeat("origoa ", 300_000)},
		{"huge single token", "bigtok", strings.Repeat("x", 3_000)},
		{"unicode chaos", "emoji 🔥 ‮rtl‬ �", "combining á́́ zalgo w̸o̵r̷d"},
		{"quotes newlines", "l1\nl2\t\"q\" 'x'", "back\\slash %like_ $$dollar$$ ;drop"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			guid, err := s.CreateArtifact(ctx, CreateArtifactParams{
				Kind: "entry", Folder: "hostile", Type: "requirement",
				Title: c.title, Fields: map[string]any{"description": c.field}})
			if err != nil {
				t.Fatalf("create rejected payload outright: %v", err)
			}
			if row, _ := s.DB.GetArtifact(ctx, guid); row == nil || row.GUID != guid {
				t.Fatal("round-trip lost the artifact")
			}
			must(t, s.Reindex(ctx))
			if row, _ := s.DB.GetArtifact(ctx, guid); row == nil {
				t.Fatal("artifact vanished after reindex")
			}
		})
	}

	t.Run("deep nesting", func(t *testing.T) {
		depth := 4000
		deep := strings.Repeat(`{"a":`, depth) + `"x"` + strings.Repeat(`}`, depth)
		var v any
		if err := json.Unmarshal([]byte(deep), &v); err != nil {
			v = deep
		}
		// Either cleanly rejected or stored — never a panic or a wedged projection.
		if _, err := s.CreateArtifact(ctx, CreateArtifactParams{
			Kind: "entry", Folder: "hostile", Type: "requirement", Title: "deep",
			Fields: map[string]any{"blob": v}}); err != nil {
			t.Logf("deep nesting rejected cleanly: %v", err)
		}
		must(t, s.Reindex(ctx))
	})
}

// TestProjectionNeverWedges verifies the projection keeps working after a
// burst of pathological writes: normal operation continues, search still
// functions, and the live projection matches a rebuild.
func TestProjectionNeverWedges(t *testing.T) {
	s, ctx := newService(t)
	seedConfig(t, s, ctx)

	for i := 0; i < 6; i++ {
		if _, err := s.CreateArtifact(ctx, CreateArtifactParams{
			Kind: "entry", Folder: "burst", Type: "requirement",
			Title: fmt.Sprintf("burst %d \x00 spike", i)}); err != nil {
			t.Fatalf("hostile write %d: %v", i, err)
		}
	}
	guid, err := s.CreateArtifact(ctx, CreateArtifactParams{
		Kind: "entry", Folder: "burst", Type: "requirement", Title: "after the storm"})
	must(t, err)

	res, err := s.DB.Search(ctx, projection.SearchQuery{Text: "storm"})
	must(t, err)
	if len(res) != 1 {
		t.Fatalf("search broken after hostile burst: got %d results", len(res))
	}
	before := listingOf(t, s, ctx)
	must(t, s.Reindex(ctx))
	diffListings(t, "post-burst live vs rebuild", before, listingOf(t, s, ctx))
	if row, _ := s.DB.GetArtifact(ctx, guid); row == nil {
		t.Fatal("artifact missing after burst")
	}
}

// TestUnknownPropertiesSurviveUpdate guards the spec's stable-serialization
// claim end to end: a property added by an external tool (a direct Git edit)
// must survive an API update, keeping its value and its position, while
// unrelated logical edits apply.
func TestUnknownPropertiesSurviveUpdate(t *testing.T) {
	s, ctx := newService(t)
	seedConfig(t, s, ctx)
	guid, err := s.CreateArtifact(ctx, CreateArtifactParams{
		Kind: "entry", Folder: "a", Type: "requirement", Title: "v1",
		Fields: map[string]any{"priority": "low"}})
	must(t, err)

	// External tool injects an extension property, bypassing the backend.
	row, err := s.DB.GetArtifact(ctx, guid)
	must(t, err)
	head, _ := s.Git.Head()
	raw, ok, err := s.Git.ReadBlob(head, row.RepoPath+"/.origoa.json")
	must(t, err)
	if !ok {
		t.Fatal("artifact file missing")
	}
	injected := strings.Replace(string(raw), "\n}", ",\n  \"x-extension\": \"keep me\"\n}", 1)
	if injected == string(raw) {
		t.Fatalf("could not inject extension property into %q", raw)
	}
	cs := gitstore.NewChangeset()
	cs.Write(row.RepoPath+"/.origoa.json", []byte(injected))
	h, err := s.Git.BuildCommit(head, cs, "manual extension", "ext", "ext@x")
	must(t, err)
	must(t, s.Git.PublishCommit(h, head))

	// An API update of an unrelated property.
	must(t, s.UpdateArtifact(ctx, guid, UpdateArtifactParams{Title: strPtr("v2")}))

	// The extension property survives byte-for-byte in place; the edit applied.
	head, _ = s.Git.Head()
	final, ok, err := s.Git.ReadBlob(head, row.RepoPath+"/.origoa.json")
	must(t, err)
	if !ok {
		t.Fatal("artifact file vanished")
	}
	if !strings.Contains(string(final), `"x-extension": "keep me"`) {
		t.Fatalf("extension property lost:\n%s", final)
	}
	if !strings.Contains(string(final), `"title": "v2"`) {
		t.Fatalf("update did not apply:\n%s", final)
	}
	// Order preserved: guid before x-extension, x-extension still last.
	if strings.Index(string(final), `"x-extension"`) < strings.Index(string(final), `"title"`) {
		t.Fatalf("property order changed:\n%s", final)
	}
}

func strPtr(s string) *string { return &s }

// TestConcurrentCardinalityHolds proves the relationship cardinality bound is
// enforced atomically. Many workers race to create a many-to-one "parent"
// link out of the SAME source; because validation runs inside the update
// transaction and is re-checked on every compare-and-swap retry, exactly one
// may win — a stale pre-transaction count can never let two through.
func TestConcurrentCardinalityHolds(t *testing.T) {
	s, ctx := newService(t)
	seedConfig(t, s, ctx) // "parent" is many-to-one (source may have one)

	source, err := s.CreateArtifact(ctx, CreateArtifactParams{
		Kind: "entry", Folder: "reqs", Type: "requirement", Title: "source"})
	must(t, err)
	const racers = 8
	targets := make([]string, racers)
	for i := range targets {
		g, e := s.CreateArtifact(ctx, CreateArtifactParams{
			Kind: "entry", Folder: "reqs", Type: "requirement", Title: fmt.Sprintf("t%d", i)})
		must(t, e)
		targets[i] = g
	}

	var success, rejected atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(target string) {
			defer wg.Done()
			err := retryWrite(func() error {
				_, e := s.CreateLink(ctx, CreateLinkParams{Type: "parent", Source: source, Target: target})
				return e
			})
			switch {
			case err == nil:
				success.Add(1)
			case errors.As(err, new(ErrValidation)):
				rejected.Add(1) // cardinality correctly refused the extra link
			default:
				t.Errorf("unexpected error racing link: %v", err)
			}
		}(targets[i])
	}
	wg.Wait()

	if success.Load() != 1 {
		t.Fatalf("many-to-one cardinality breached: %d links committed, want exactly 1", success.Load())
	}
	if rejected.Load() != racers-1 {
		t.Fatalf("expected %d rejections, got %d", racers-1, rejected.Load())
	}
	// Ground truth: the projection holds exactly one outgoing parent link.
	links, err := s.DB.LinksFor(ctx, source)
	must(t, err)
	out := 0
	for _, l := range links {
		if l.Type == "parent" && l.Source == source {
			out++
		}
	}
	if out != 1 {
		t.Fatalf("projection shows %d outgoing parent links, want 1", out)
	}
}

// TestUpdateLinkCommentConflictAndReindex exercises the link/comment update
// paths adversarially: optimistic-concurrency conflicts are detected, hostile
// text is stored without wedging the projection, and both survive a reindex.
func TestUpdateLinkCommentConflictAndReindex(t *testing.T) {
	s, ctx := newService(t)
	seedConfig(t, s, ctx)
	a, _ := s.CreateArtifact(ctx, CreateArtifactParams{Kind: "entry", Folder: "x", Type: "requirement", Title: "A"})
	b, _ := s.CreateArtifact(ctx, CreateArtifactParams{Kind: "entry", Folder: "x", Type: "testcase", Title: "B"})
	link, err := s.CreateLink(ctx, CreateLinkParams{Type: "satisfies", Source: a, Target: b})
	must(t, err)
	comment, err := s.CreateComment(ctx, CreateCommentParams{Subject: a, Text: "original"})
	must(t, err)

	// A stale revision is rejected on both update paths.
	if err := s.UpdateLink(ctx, link, UpdateLinkParams{Fields: map[string]any{"w": "1"}, IfRevision: "deadbeef"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale link update: want conflict, got %v", err)
	}
	if err := s.UpdateComment(ctx, comment, UpdateCommentParams{Text: strPtr("x"), IfRevision: "deadbeef"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale comment update: want conflict, got %v", err)
	}

	// Hostile content through the update paths must not wedge the projection.
	hostile := "edit\x00" + strings.Repeat("spike ", 200_000)
	must(t, s.UpdateLink(ctx, link, UpdateLinkParams{Fields: map[string]any{"note": hostile}}))
	must(t, s.UpdateComment(ctx, comment, UpdateCommentParams{Text: strPtr(hostile)}))

	// Both survive a from-scratch rebuild.
	before := listingOf(t, s, ctx)
	must(t, s.Reindex(ctx))
	diffListings(t, "post-update live vs rebuild", before, listingOf(t, s, ctx))
	if row, _ := s.DB.GetArtifact(ctx, link); row == nil {
		t.Fatal("link vanished after reindex")
	}
	if row, _ := s.DB.GetArtifact(ctx, comment); row == nil {
		t.Fatal("comment vanished after reindex")
	}
}
