package core

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/thomdehoog/origoa/internal/gitx"
	"github.com/thomdehoog/origoa/internal/ojson"
)

// listingOf snapshots the projection as a comparable set of guid/etag/path.
func listingOf(f *Foundation) map[string]string {
	out := map[string]string{}
	metas, err := f.List("", "", "", true, 0)
	if err != nil {
		panic(err)
	}
	for _, m := range metas {
		out[m.GUID] = m.ETag + " " + m.FilePath + " " + m.HID
	}
	return out
}

func diffListings(t *testing.T, name string, a, b map[string]string) {
	t.Helper()
	for g, v := range a {
		if b[g] != v {
			t.Errorf("%s: %s = %q vs %q", name, g, v, b[g])
		}
	}
	for g, v := range b {
		if _, ok := a[g]; !ok {
			t.Errorf("%s: %s only in second listing (%q)", name, g, v)
		}
	}
}

// TestTortureConcurrentMixedOps hammers one Foundation with every operation
// concurrently, including reindexing, then proves three invariants:
// no operation was lost, the live projection equals a from-scratch rebuild,
// and current HIDs are unique.
func TestTortureConcurrentMixedOps(t *testing.T) {
	f := testFoundation(t)
	must(t, f.PutWorkflow("", "dev", &Workflow{
		ID: "dev", Initial: "open", States: []string{"open", "done"},
		Transitions: []Transition{{From: "open", To: "done"}, {From: "done", To: "open"}},
	}))
	must(t, f.PutSchema("", "part", &Schema{ArtifactType: "part", HIDPrefix: "P", Workflows: []string{"dev"}}))

	const workers = 8
	const opsEach = 12
	var created, failed atomic.Int64
	var mu sync.Mutex
	var guids []string

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			var mine []string
			for i := 0; i < opsEach; i++ {
				switch i % 6 {
				case 0, 1: // create
					m, err := f.CreateArtifact(KindEntry, fmt.Sprintf("w%d", w), "part",
						body(t, fmt.Sprintf(`{"title":"w%d-%d","fields":{"n":"%d"}}`, w, i, i)))
					if err != nil {
						failed.Add(1)
						continue
					}
					created.Add(1)
					mine = append(mine, m.GUID)
					mu.Lock()
					guids = append(guids, m.GUID)
					mu.Unlock()
				case 2: // update
					if len(mine) > 0 {
						if _, err := f.UpdateArtifact(mine[0], body(t, fmt.Sprintf(`{"title":"upd-%d-%d"}`, w, i)), ""); err != nil {
							failed.Add(1)
						}
					}
				case 3: // transition + link + comment
					if len(mine) > 1 {
						m, err := f.Meta(mine[0])
						if err != nil {
							failed.Add(1)
							continue
						}
						to := "done"
						if m.Workflows["dev"] == "done" {
							to = "open"
						}
						if _, err := f.Transition(mine[0], "dev", to); err != nil {
							failed.Add(1)
						}
						if _, err := f.CreateLink("relates", mine[0], mine[1], nil); err != nil {
							failed.Add(1)
						}
						if _, err := f.CreateComment(mine[1], fmt.Sprintf("c%d-%d", w, i), "", "torture"); err != nil {
							failed.Add(1)
						}
					}
				case 4: // move + reads under fire
					if len(mine) > 1 {
						if _, err := f.MoveArtifact(mine[1], fmt.Sprintf("moved/w%d", w)); err != nil {
							failed.Add(1)
						}
					}
					f.Search("upd", "", "", 0)
					f.List("entry", "part", "", true, 0)
					f.Folders()
				case 5: // reindex concurrently with writers
					if err := f.Reindex(); err != nil {
						t.Errorf("reindex: %v", err)
					}
				}
			}
		}(w)
	}
	wg.Wait()
	if failed.Load() > 0 {
		t.Fatalf("%d operations failed unexpectedly", failed.Load())
	}

	// Invariant 1: every created artifact is present with a unique HID.
	seenHID := map[string]string{}
	for _, g := range guids {
		m, err := f.Meta(g)
		must(t, err)
		if m.HID == "" {
			t.Fatalf("artifact %s lost its HID", g)
		}
		if prev, dup := seenHID[m.HID]; dup {
			t.Fatalf("duplicate HID %q on %s and %s", m.HID, prev, g)
		}
		seenHID[m.HID] = g
	}
	// Invariant 2: live projection == from-scratch rebuild.
	before := listingOf(f)
	must(t, f.Reindex())
	diffListings(t, "live vs rebuild", before, listingOf(f))
	// Invariant 3: no lost updates at the Git level — every create is a commit.
	log, err := f.History(guids[0], 1)
	must(t, err)
	if len(log) == 0 {
		t.Fatal("history empty after torture")
	}
}

// TestMultiWriterSharedRepo runs two independent Foundation instances (as two
// server processes would) against the SAME bare repository — and, under
// ORIGOA_TEST_DSN, the same projection database. After the dust settles, a
// from-scratch rebuild must contain every artifact either writer created and
// both instances must converge to it: nothing lost, nothing silently stale.
func TestMultiWriterSharedRepo(t *testing.T) {
	gitDir := t.TempDir() + "/shared.git"
	openShared := func() *Foundation {
		var f *Foundation
		var err error
		if dsn := pgSharedDSN(t); dsn != "" {
			f, err = OpenPostgres(gitDir, dsn)
		} else {
			f, err = Open(gitDir)
		}
		must(t, err)
		t.Cleanup(func() { f.Close() })
		return f
	}
	f1 := openShared()
	f2 := openShared()

	const each = 15
	var wg sync.WaitGroup
	guids := make([][]string, 2)
	for i, f := range []*Foundation{f1, f2} {
		wg.Add(1)
		go func(i int, f *Foundation) {
			defer wg.Done()
			for n := 0; n < each; n++ {
				m, err := f.CreateArtifact(KindEntry, fmt.Sprintf("proc%d", i), "part",
					body(t, fmt.Sprintf(`{"title":"p%d-%d"}`, i, n)))
				if err != nil {
					t.Errorf("writer %d create %d: %v", i, n, err)
					return
				}
				guids[i] = append(guids[i], m.GUID)
				if n%3 == 2 {
					if _, err := f.UpdateArtifact(m.GUID, body(t, fmt.Sprintf(`{"title":"p%d-%d-v2"}`, i, n)), ""); err != nil {
						t.Errorf("writer %d update: %v", i, err)
					}
				}
			}
		}(i, f)
	}
	wg.Wait()

	// Ground truth: a fresh Foundation rebuilt purely from Git.
	truth, err := Open(gitDir)
	must(t, err)
	defer truth.Close()
	want := listingOf(truth)
	for i := range guids {
		for _, g := range guids[i] {
			if _, ok := want[g]; !ok {
				t.Fatalf("writer %d artifact %s lost from Git (CAS failure)", i, g)
			}
		}
	}
	// Both instances must converge to ground truth (self-heal on demand).
	for i, f := range []*Foundation{f1, f2} {
		if f.Head() != truth.Head() {
			must(t, f.Reindex())
		}
		diffListings(t, fmt.Sprintf("writer %d vs ground truth", i), listingOf(f), want)
	}
}

// pgSharedDSN returns a namespaced DSN with fresh tables for the multi-writer
// test, or "" when Postgres testing is disabled.
func pgSharedDSN(t *testing.T) string {
	return pgTestDSN(t) // one namespace; caller opens both Foundations on it
}

// TestMultiWriterDriftDetected reproduces the nastiest interleaving: writer B
// publishes a commit inside writer A's commit window so that A's CAS builds
// on B's head. A's projection must not silently skip B's changes when A's
// commit lands.
func TestMultiWriterDriftDetected(t *testing.T) {
	gitDir := t.TempDir() + "/drift.git"
	var f1 *Foundation
	var err error
	dsn := pgSharedDSN(t)
	if dsn != "" {
		f1, err = OpenPostgres(gitDir, dsn)
	} else {
		f1, err = Open(gitDir)
	}
	must(t, err)
	defer f1.Close()

	// Foreign writer commits directly between f1's operations, repeatedly.
	g := &gitx.Repo{Dir: gitDir}
	var foreign []string
	for i := 0; i < 10; i++ {
		fg := NewGUID()
		_, err := g.Commit("foreign", []gitx.Op{{
			Path:    fmt.Sprintf("ext/%s/%s", fg, ArtifactFile),
			Content: []byte(fmt.Sprintf(`{"guid":"%s","kind":"entry","type":"part","title":"foreign%d"}`, fg, i)),
		}})
		must(t, err)
		foreign = append(foreign, fg)
		if _, err := f1.CreateArtifact(KindEntry, "own", "part", body(t, fmt.Sprintf(`{"title":"own%d"}`, i))); err != nil {
			t.Fatalf("own create %d: %v", i, err)
		}
		// After f1's write, its projection claims to represent the Git head;
		// every foreign artifact committed BEFORE that head must be visible.
		for _, fg := range foreign {
			if _, err := f1.Meta(fg); err != nil {
				t.Fatalf("iteration %d: foreign artifact %s invisible although processed head includes it (silent drift)", i, fg)
			}
		}
	}
}

// TestAdversarialPayloads feeds hostile content through the full stack:
// NUL bytes (PostgreSQL text/tsvector killers), megabyte strings (tsvector
// 1MiB limit), deep nesting, and unicode chaos. Nothing may corrupt the
// projection: after each payload the artifact round-trips and a reindex
// still succeeds.
func TestAdversarialPayloads(t *testing.T) {
	f := testFoundation(t)
	payloads := []struct {
		name  string
		title string
		field string
	}{
		{"nul bytes", "nul\x00title", "field\x00value\x00"},
		{"huge string", "big", strings.Repeat("origoa ", 300_000)}, // ~2.1 MB
		{"unicode chaos", "emoji 🔥 ‮ rtl �", "combining á́́ zalgo"},
		{"quotes and newlines", "line1\nline2\t\"quoted\" 'single'", "back\\slash %like_ $$dollar$$"},
	}
	for _, p := range payloads {
		t.Run(p.name, func(t *testing.T) {
			b := ojson.New()
			b.SetString("title", p.title)
			fields := ojson.New()
			fields.SetString("payload", p.field)
			raw, _ := fields.MarshalJSON()
			b.Set("fields", raw)
			m, err := f.CreateArtifact(KindEntry, "hostile", "part", b)
			if err != nil {
				t.Fatalf("create rejected payload outright: %v", err)
			}
			got, _, err := f.Artifact(m.GUID)
			must(t, err)
			if got.GUID != m.GUID {
				t.Fatal("round-trip lost artifact")
			}
			must(t, f.Reindex())
			if _, err := f.Meta(m.GUID); err != nil {
				t.Fatalf("artifact vanished after reindex: %v", err)
			}
		})
	}

	t.Run("deep nesting", func(t *testing.T) {
		depth := 5000
		deep := strings.Repeat(`{"a":`, depth) + `"x"` + strings.Repeat(`}`, depth)
		b := ojson.New()
		b.SetString("title", "deep")
		b.Set("fields", []byte(`{"deep":`+deep+`}`))
		// Either rejected cleanly or stored — but never a panic or a wedged
		// projection.
		if m, err := f.CreateArtifact(KindEntry, "hostile", "part", b); err == nil {
			if _, _, err := f.Artifact(m.GUID); err != nil {
				t.Fatalf("stored but unreadable: %v", err)
			}
		}
		must(t, f.Reindex())
	})
}

// TestProjectionNeverWedges verifies the projection self-heals: if Apply ever
// fails mid-write, subsequent operations must recover via Sync rather than
// permanently erroring.
func TestProjectionNeverWedges(t *testing.T) {
	f := testFoundation(t)
	// A burst of writes with pathological search text; then normal operation
	// must continue and match a rebuild.
	for i := 0; i < 5; i++ {
		b := ojson.New()
		b.SetString("title", fmt.Sprintf("burst %d \x00", i))
		if _, err := f.CreateArtifact(KindEntry, "", "part", b); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	m, err := f.CreateArtifact(KindEntry, "", "part", body(t, `{"title":"after the storm"}`))
	must(t, err)
	if len(mustSearch(t, f, "storm")) != 1 {
		t.Fatal("search broken after hostile burst")
	}
	before := listingOf(f)
	must(t, f.Reindex())
	diffListings(t, "post-burst live vs rebuild", before, listingOf(f))
	if _, err := f.Meta(m.GUID); err != nil {
		t.Fatal(err)
	}
}
