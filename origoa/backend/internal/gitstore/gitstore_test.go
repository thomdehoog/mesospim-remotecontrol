package gitstore

import (
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir() + "/repo.git")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func commitFiles(t *testing.T, s *Store, parent plumbing.Hash, msg string, files map[string]string) plumbing.Hash {
	t.Helper()
	cs := NewChangeset()
	for p, c := range files {
		cs.Write(p, []byte(c))
	}
	h, err := s.BuildCommit(parent, cs, msg, "test", "test@origoa")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PublishCommit(h, parent); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestBuildAndPublish(t *testing.T) {
	s := newStore(t)
	head, _ := s.Head()
	if !head.IsZero() {
		t.Fatal("expected unborn branch")
	}
	h1 := commitFiles(t, s, head, "Datapod 1 created", map[string]string{
		"specs/g1/.origoa.json": `{"guid":"g1"}`,
		"specs/.origoa/schemas/req.json": `{"artifactType":"req"}`,
	})
	b, ok, err := s.ReadBlob(h1, "specs/g1/.origoa.json")
	if err != nil || !ok || string(b) != `{"guid":"g1"}` {
		t.Fatalf("read blob: %v %v %s", err, ok, b)
	}
	entries, err := s.ListTree(h1, "specs")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("want 2 entries got %v", entries)
	}
}

func TestCommitIsInvisibleUntilPublished(t *testing.T) {
	s := newStore(t)
	h1 := commitFiles(t, s, plumbing.ZeroHash, "init", map[string]string{"a.json": "{}"})
	cs2 := NewChangeset()
	cs2.Write("b.json", []byte("{}"))
	h2, err := s.BuildCommit(h1, cs2, "add b", "t", "t@o")
	if err != nil {
		t.Fatal(err)
	}
	head, _ := s.Head()
	if head != h1 {
		t.Fatal("unpublished commit moved the branch")
	}
	if err := s.PublishCommit(h2, h1); err != nil {
		t.Fatal(err)
	}
	head, _ = s.Head()
	if head != h2 {
		t.Fatal("publish did not move branch")
	}
}

func TestPublishCASConflict(t *testing.T) {
	s := newStore(t)
	h1 := commitFiles(t, s, plumbing.ZeroHash, "init", map[string]string{"a.json": "{}"})
	// Two competing commits on top of h1.
	csA := NewChangeset()
	csA.Write("a1.json", []byte("{}"))
	hA, err := s.BuildCommit(h1, csA, "A", "t", "t@o")
	if err != nil {
		t.Fatal(err)
	}
	csB := NewChangeset()
	csB.Write("b1.json", []byte("{}"))
	hB, err := s.BuildCommit(h1, csB, "B", "t", "t@o")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PublishCommit(hA, h1); err != nil {
		t.Fatal(err)
	}
	if err := s.PublishCommit(hB, h1); !errors.Is(err, ErrConcurrentUpdate) {
		t.Fatalf("expected ErrConcurrentUpdate, got %v", err)
	}
}

func TestDeleteAndPruneEmptyTrees(t *testing.T) {
	s := newStore(t)
	h1 := commitFiles(t, s, plumbing.ZeroHash, "init", map[string]string{
		"dir/sub/file.json": "{}",
		"other.json":        "{}",
	})
	cs := NewChangeset()
	cs.Delete("dir/sub/file.json")
	h2, err := s.BuildCommit(h1, cs, "delete", "t", "t@o")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PublishCommit(h2, h1); err != nil {
		t.Fatal(err)
	}
	entries, _ := s.ListTree(h2, "")
	for _, e := range entries {
		if e.Name == "dir" {
			t.Fatal("empty tree was not pruned")
		}
	}
}

func TestDiffAndReplayChain(t *testing.T) {
	s := newStore(t)
	h1 := commitFiles(t, s, plumbing.ZeroHash, "c1", map[string]string{"a.json": "1"})
	h2 := commitFiles(t, s, h1, "c2", map[string]string{"b.json": "1"})
	h3 := commitFiles(t, s, h2, "c3", map[string]string{"a.json": "2"})

	chain, err := s.CommitsBetween(h1, h3)
	if err != nil {
		t.Fatal(err)
	}
	if len(chain) != 2 || chain[0] != h2 || chain[1] != h3 {
		t.Fatalf("bad chain %v", chain)
	}
	full, err := s.CommitsBetween(plumbing.ZeroHash, h3)
	if err != nil || len(full) != 3 {
		t.Fatalf("bad full chain %v %v", full, err)
	}

	changes, err := s.DiffCommit(h3)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].Path != "a.json" || changes[0].Action != Modified {
		t.Fatalf("bad diff %v", changes)
	}
	changes, _ = s.DiffCommit(h1) // root commit diffs against empty tree
	if len(changes) != 1 || changes[0].Action != Added {
		t.Fatalf("bad root diff %v", changes)
	}
}

func TestWalkTree(t *testing.T) {
	s := newStore(t)
	h := commitFiles(t, s, plumbing.ZeroHash, "init", map[string]string{
		"x/one.json": "1", "y/two.json": "2",
	})
	var seen []string
	err := s.WalkTree(h, func(p string, read func() ([]byte, error)) error {
		b, err := read()
		if err != nil {
			return err
		}
		seen = append(seen, p+"="+string(b))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 {
		t.Fatalf("walk saw %v", seen)
	}
}

// TestNativeGitCanReadRepository verifies real git accepts our objects.
func TestNativeGitCanReadRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	s := newStore(t)
	h := commitFiles(t, s, plumbing.ZeroHash, "Datapod g1 created", map[string]string{
		"a/g1/.origoa.json": `{"guid":"g1"}`,
	})
	out, err := exec.Command("git", "-C", s.Dir(), "fsck", "--strict").CombinedOutput()
	if err != nil {
		t.Fatalf("git fsck failed: %v: %s", err, out)
	}
	out, err = exec.Command("git", "-C", s.Dir(), "ls-tree", "-r", h.String()).CombinedOutput()
	if err != nil || !strings.Contains(string(out), ".origoa.json") {
		t.Fatalf("git ls-tree: %v: %s", err, out)
	}
}

func TestFileHistory(t *testing.T) {
	s := newStore(t)
	h1 := commitFiles(t, s, plumbing.ZeroHash, "c1", map[string]string{"a/f.json": "1", "b/g.json": "1"})
	h2 := commitFiles(t, s, h1, "c2", map[string]string{"b/g.json": "2"})
	h3 := commitFiles(t, s, h2, "c3", map[string]string{"a/f.json": "3"})
	hist, err := s.FileHistory(h3, "a", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 2 || hist[0].Hash != h3 || hist[1].Hash != h1 {
		t.Fatalf("bad history %v", hist)
	}
}
