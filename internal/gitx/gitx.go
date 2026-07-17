// Package gitx manages a bare Git repository through plumbing commands.
//
// Git is the authoritative store. Commits are constructed without a working
// directory (hash-object, update-index against a private index file,
// write-tree, commit-tree) and published with a compare-and-swap update-ref.
// CommitOnce publishes exactly once against an expected parent and reports
// ErrStale on contention — the retry policy lives in the caller, which must
// re-validate against the new head before retrying (design guide §10.1).
package gitx

import (
	"bytes"
	"crypto/sha1"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const Branch = "refs/heads/main"

// ErrStale reports that the branch moved past the expected parent revision.
var ErrStale = errors.New("branch moved: stale parent revision")

type Repo struct {
	Dir string
}

// Op is a single file change within a commit: either a write or a delete.
type Op struct {
	Path    string
	Content []byte
	Delete  bool
}

// TreeEntry is one blob in the repository tree.
type TreeEntry struct {
	Path string
	SHA  string
}

// LogEntry is one commit touching a path: hash, author date (ISO), subject.
type LogEntry struct {
	Hash    string `json:"hash"`
	Date    string `json:"date"`
	Subject string `json:"subject"`
}

// Init opens dir as a bare repository, creating it if necessary.
func Init(dir string) (*Repo, error) {
	if _, err := os.Stat(filepath.Join(dir, "HEAD")); os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
		cmd := exec.Command("git", "init", "--bare", "-b", "main", dir)
		if out, err := cmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("git init: %v: %s", err, out)
		}
	}
	return &Repo{Dir: dir}, nil
}

func (r *Repo) env() []string {
	return append(os.Environ(),
		"GIT_DIR="+r.Dir,
		"GIT_AUTHOR_NAME=origoa", "GIT_AUTHOR_EMAIL=origoa@localhost",
		"GIT_COMMITTER_NAME=origoa", "GIT_COMMITTER_EMAIL=origoa@localhost",
	)
}

// run executes git; stderr is captured for errors and the raw *exec.ExitError
// is preserved in the error chain so callers can inspect exit codes.
// core.quotepath=false keeps non-ASCII paths literal in all output — quoted
// paths would silently corrupt move/delete ops built from listings.
func (r *Repo) run(stdin []byte, extraEnv []string, args ...string) ([]byte, error) {
	args = append([]string{"-c", "core.quotepath=false"}, args...)
	cmd := exec.Command("git", args...)
	cmd.Env = append(r.env(), extraEnv...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", args[2], err, strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), nil
}

func (r *Repo) git(stdin []byte, args ...string) ([]byte, error) {
	return r.run(stdin, nil, args...)
}

// Head returns the current commit of the main branch, or "" if the branch is
// unborn. Other failures (I/O, corruption, resource exhaustion) are real
// errors — they must never be mistaken for an empty repository, or the
// projection would happily rebuild itself empty.
func (r *Repo) Head() (string, error) {
	out, err := r.git(nil, "rev-parse", "--verify", "--quiet", Branch)
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 {
			return "", nil // --verify --quiet exits 1 iff the ref does not resolve
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// ListTree returns every blob under prefix ("" for the whole tree) at rev.
// The prefix is matched literally — no pathspec magic or wildcards.
func (r *Repo) ListTree(rev, prefix string) ([]TreeEntry, error) {
	if rev == "" {
		return nil, nil
	}
	args := []string{"ls-tree", "-r", "-z", "--format=%(objectname) %(path)", rev}
	if prefix != "" {
		args = append(args, "--", ":(literal)"+prefix)
	}
	out, err := r.git(nil, args...)
	if err != nil {
		return nil, err
	}
	var entries []TreeEntry
	for _, line := range strings.Split(string(out), "\x00") {
		if line == "" {
			continue
		}
		sha, path, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		entries = append(entries, TreeEntry{Path: path, SHA: sha})
	}
	return entries, nil
}

// ReadBlobs fetches many blobs by SHA in a single cat-file --batch call.
func (r *Repo) ReadBlobs(shas []string) (map[string][]byte, error) {
	if len(shas) == 0 {
		return map[string][]byte{}, nil
	}
	stdin := strings.Join(shas, "\n") + "\n"
	out, err := r.git([]byte(stdin), "cat-file", "--batch")
	if err != nil {
		return nil, err
	}
	res := make(map[string][]byte, len(shas))
	buf := out
	for len(buf) > 0 {
		nl := bytes.IndexByte(buf, '\n')
		if nl < 0 {
			break
		}
		header := string(buf[:nl])
		buf = buf[nl+1:]
		parts := strings.Fields(header)
		if len(parts) < 3 || parts[1] != "blob" {
			return nil, fmt.Errorf("gitx: unexpected batch header %q", header)
		}
		var size int
		fmt.Sscanf(parts[2], "%d", &size)
		if size > len(buf) {
			return nil, fmt.Errorf("gitx: truncated batch output")
		}
		res[parts[0]] = append([]byte(nil), buf[:size]...)
		buf = buf[size:]
		if len(buf) > 0 && buf[0] == '\n' {
			buf = buf[1:]
		}
	}
	return res, nil
}

// Log returns commit history for a pathspec (whole repo if empty). The
// caller supplies any pathspec magic (e.g. ":(glob)**/<guid>/**").
func (r *Repo) Log(pathspec string, limit int) ([]LogEntry, error) {
	head, err := r.Head()
	if err != nil || head == "" {
		return nil, err
	}
	args := []string{"log", fmt.Sprintf("--max-count=%d", limit), "--format=%H%x1f%aI%x1f%s%x1e", head}
	if pathspec != "" {
		args = append(args, "--", pathspec)
	}
	out, err := r.git(nil, args...)
	if err != nil {
		return nil, err
	}
	var log []LogEntry
	for _, rec := range strings.Split(string(out), "\x1e") {
		rec = strings.TrimSpace(rec)
		if rec == "" {
			continue
		}
		f := strings.Split(rec, "\x1f")
		if len(f) == 3 {
			log = append(log, LogEntry{Hash: f[0], Date: f[1], Subject: f[2]})
		}
	}
	return log, nil
}

// BlobSHA computes the Git blob object name for content without touching the
// repository. Used as the optimistic-concurrency ETag.
func BlobSHA(content []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(content))
	h.Write(content)
	return fmt.Sprintf("%x", h.Sum(nil))
}

// Commit publishes ops with blind CAS retry on contention. Only safe when
// the ops carry no cross-commit invariants (tooling, tests, simulated
// foreign writers) — server writes go through the Foundation's validated
// retry loop, which re-checks preconditions against the new head instead.
func (r *Repo) Commit(message string, ops []Op) (string, error) {
	for attempt := 0; attempt < 8; attempt++ {
		head, err := r.Head()
		if err != nil {
			return "", err
		}
		commit, err := r.CommitOnce(head, message, ops)
		if errors.Is(err, ErrStale) {
			continue
		}
		return commit, err
	}
	return "", fmt.Errorf("gitx: commit failed after retries (concurrent updates)")
}

// CommitOnce builds ops on top of parent ("" for an unborn branch) and
// publishes with one compare-and-swap update-ref. If the branch has moved
// past parent it returns ErrStale and publishes nothing — the caller must
// resynchronize, re-validate, and rebuild its ops before trying again.
func (r *Repo) CommitOnce(parent, message string, ops []Op) (string, error) {
	commit, err := r.buildCommit(parent, message, ops)
	if err != nil {
		return "", err
	}
	// CAS publish: old value "" asserts the ref must not exist yet.
	_, casErr := r.git(nil, "update-ref", Branch, commit, parent)
	if casErr == nil {
		return commit, nil
	}
	// Distinguish contention from real failures (permissions, disk, ...).
	if now, herr := r.Head(); herr == nil && now != parent {
		return "", fmt.Errorf("%w (expected %.12s, found %.12s)", ErrStale, parent, now)
	}
	return "", casErr
}

func (r *Repo) buildCommit(parent, message string, ops []Op) (string, error) {
	idx, err := os.CreateTemp("", "origoa-index-*")
	if err != nil {
		return "", err
	}
	idx.Close()
	defer os.Remove(idx.Name())
	idxEnv := []string{"GIT_INDEX_FILE=" + idx.Name()}

	if parent != "" {
		if _, err := r.run(nil, idxEnv, "read-tree", parent); err != nil {
			return "", err
		}
	} else {
		if _, err := r.run(nil, idxEnv, "read-tree", "--empty"); err != nil {
			return "", err
		}
	}
	// All index mutations go through one NUL-terminated --index-info stream:
	// mode 0 removes an entry, mode 100644 adds/replaces one. NUL termination
	// makes every legal filename safe, including newlines.
	var info bytes.Buffer
	for _, op := range ops {
		if op.Delete {
			fmt.Fprintf(&info, "0 %040d\t%s\x00", 0, op.Path)
			continue
		}
		sha, err := r.git(op.Content, "hash-object", "-w", "--stdin")
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&info, "100644 %s 0\t%s\x00", strings.TrimSpace(string(sha)), op.Path)
	}
	if info.Len() > 0 {
		if _, err := r.run(info.Bytes(), idxEnv, "update-index", "-z", "--index-info"); err != nil {
			return "", err
		}
	}
	tree, err := r.run(nil, idxEnv, "write-tree")
	if err != nil {
		return "", err
	}
	args := []string{"commit-tree", strings.TrimSpace(string(tree)), "-m", message}
	if parent != "" {
		args = append(args, "-p", parent)
	}
	commit, err := r.git(nil, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(commit)), nil
}
