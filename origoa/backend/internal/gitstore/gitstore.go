// Package gitstore manages the authoritative Git repository of an Origoa
// Foundation instance.
//
// All modifications are performed through plumbing-level object
// construction: blobs and trees are written directly to the object
// database, a commit object is created without touching any working
// directory, and the branch reference is finally advanced with a
// compare-and-swap update. Git remains the single source of truth; every
// other data structure is a rebuildable projection.
package gitstore

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/utils/merkletrie"
)

// Branch is the repository branch maintained by the Foundation. Multi-
// branching support is outside the MVP scope.
const Branch = "refs/heads/main"

// ErrConcurrentUpdate is returned by PublishCommit when the branch moved
// between BuildCommit and PublishCommit. The caller re-reads HEAD,
// rebuilds the changeset on top of the new revision and retries.
var ErrConcurrentUpdate = errors.New("gitstore: branch was updated concurrently")

// Store wraps a bare Git repository.
type Store struct {
	repo *git.Repository
	dir  string
}

// Open opens (or initializes) a bare repository at dir.
func Open(dir string) (*Store, error) {
	repo, err := git.PlainOpen(dir)
	if errors.Is(err, git.ErrRepositoryNotExists) {
		if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
			return nil, mkErr
		}
		repo, err = git.PlainInit(dir, true)
	}
	if err != nil {
		return nil, fmt.Errorf("gitstore: open %s: %w", dir, err)
	}
	return &Store{repo: repo, dir: dir}, nil
}

// Dir returns the repository directory.
func (s *Store) Dir() string { return s.dir }

// Head returns the current commit of the managed branch. A zero hash is
// returned for an unborn branch.
func (s *Store) Head() (plumbing.Hash, error) {
	ref, err := s.repo.Reference(plumbing.ReferenceName(Branch), true)
	if errors.Is(err, plumbing.ErrReferenceNotFound) {
		return plumbing.ZeroHash, nil
	}
	if err != nil {
		return plumbing.ZeroHash, err
	}
	return ref.Hash(), nil
}

// Commit returns the commit object for a hash.
func (s *Store) Commit(h plumbing.Hash) (*object.Commit, error) {
	return s.repo.CommitObject(h)
}

// ReadBlob reads a file at a repository path in the given commit. The
// second return value reports whether the path exists.
func (s *Store) ReadBlob(commit plumbing.Hash, p string) ([]byte, bool, error) {
	if commit.IsZero() {
		return nil, false, nil
	}
	c, err := s.repo.CommitObject(commit)
	if err != nil {
		return nil, false, err
	}
	f, err := c.File(norm(p))
	if errors.Is(err, object.ErrFileNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	r, err := f.Reader()
	if err != nil {
		return nil, false, err
	}
	defer r.Close()
	b, err := io.ReadAll(r)
	return b, err == nil, err
}

// TreeEntry describes one child of a tree.
type TreeEntry struct {
	Name  string
	IsDir bool
}

// ListTree lists the direct children of a directory at the given commit.
// A missing directory yields an empty list.
func (s *Store) ListTree(commit plumbing.Hash, dir string) ([]TreeEntry, error) {
	if commit.IsZero() {
		return nil, nil
	}
	c, err := s.repo.CommitObject(commit)
	if err != nil {
		return nil, err
	}
	tree, err := c.Tree()
	if err != nil {
		return nil, err
	}
	dir = norm(dir)
	if dir != "" {
		tree, err = tree.Tree(dir)
		if errors.Is(err, object.ErrDirectoryNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
	}
	out := make([]TreeEntry, 0, len(tree.Entries))
	for _, e := range tree.Entries {
		out = append(out, TreeEntry{Name: e.Name, IsDir: e.Mode == filemode.Dir})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// WalkFile is invoked for every file during a full tree walk.
type WalkFile func(path string, read func() ([]byte, error)) error

// WalkTree traverses every file reachable from the commit.
func (s *Store) WalkTree(commit plumbing.Hash, fn WalkFile) error {
	if commit.IsZero() {
		return nil
	}
	c, err := s.repo.CommitObject(commit)
	if err != nil {
		return err
	}
	tree, err := c.Tree()
	if err != nil {
		return err
	}
	iter := tree.Files()
	defer iter.Close()
	for {
		f, err := iter.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		file := f
		if err := fn(file.Name, func() ([]byte, error) {
			r, err := file.Reader()
			if err != nil {
				return nil, err
			}
			defer r.Close()
			return io.ReadAll(r)
		}); err != nil {
			return err
		}
	}
}

// Changeset collects file modifications for one logical repository
// operation. Every logical operation produces exactly one commit.
type Changeset struct {
	writes  map[string][]byte
	deletes map[string]bool
	// deleteDirs removes complete subtrees (folder deletion / move).
	deleteDirs map[string]bool
}

// NewChangeset creates an empty changeset.
func NewChangeset() *Changeset {
	return &Changeset{writes: map[string][]byte{}, deletes: map[string]bool{}, deleteDirs: map[string]bool{}}
}

// Write stages file content at a repository path.
func (cs *Changeset) Write(p string, content []byte) {
	p = norm(p)
	delete(cs.deletes, p)
	cs.writes[p] = content
}

// Delete stages removal of a file.
func (cs *Changeset) Delete(p string) {
	p = norm(p)
	delete(cs.writes, p)
	cs.deletes[p] = true
}

// DeleteDir stages removal of a complete directory subtree.
func (cs *Changeset) DeleteDir(p string) {
	cs.deleteDirs[norm(p)] = true
}

// Empty reports whether the changeset stages no modifications.
func (cs *Changeset) Empty() bool {
	return len(cs.writes) == 0 && len(cs.deletes) == 0 && len(cs.deleteDirs) == 0
}

// Paths returns all file paths staged for writing.
func (cs *Changeset) Paths() []string {
	out := make([]string, 0, len(cs.writes))
	for p := range cs.writes {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func norm(p string) string {
	p = strings.TrimPrefix(path.Clean("/"+p), "/")
	if p == "." {
		p = ""
	}
	return p
}

// treeNode is a mutable in-memory tree used while constructing the new
// repository revision.
type treeNode struct {
	children map[string]*nodeEntry
}

type nodeEntry struct {
	mode filemode.FileMode
	hash plumbing.Hash // valid when sub == nil
	sub  *treeNode     // loaded / modified subtree
}

// BuildCommit constructs the new revision: blobs and trees are written to
// the object database and a commit object is created on top of parent.
// The branch reference is NOT updated; the commit exists but is not yet
// visible in the repository history until PublishCommit succeeds.
func (s *Store) BuildCommit(parent plumbing.Hash, cs *Changeset, message, authorName, authorEmail string) (plumbing.Hash, error) {
	root := &treeNode{children: map[string]*nodeEntry{}}
	if !parent.IsZero() {
		c, err := s.repo.CommitObject(parent)
		if err != nil {
			return plumbing.ZeroHash, err
		}
		t, err := c.Tree()
		if err != nil {
			return plumbing.ZeroHash, err
		}
		for _, e := range t.Entries {
			root.children[e.Name] = &nodeEntry{mode: e.Mode, hash: e.Hash}
		}
	}

	for dir := range cs.deleteDirs {
		if err := s.removePath(root, strings.Split(dir, "/")); err != nil {
			return plumbing.ZeroHash, err
		}
	}
	for p := range cs.deletes {
		if err := s.removePath(root, strings.Split(p, "/")); err != nil {
			return plumbing.ZeroHash, err
		}
	}
	for p, content := range cs.writes {
		blobHash, err := s.writeBlob(content)
		if err != nil {
			return plumbing.ZeroHash, err
		}
		if err := s.setPath(root, strings.Split(p, "/"), blobHash); err != nil {
			return plumbing.ZeroHash, err
		}
	}

	treeHash, err := s.writeTree(root)
	if err != nil {
		return plumbing.ZeroHash, err
	}

	now := time.Now()
	sig := object.Signature{Name: authorName, Email: authorEmail, When: now}
	commit := &object.Commit{
		Author:    sig,
		Committer: sig,
		Message:   message,
		TreeHash:  treeHash,
	}
	if !parent.IsZero() {
		commit.ParentHashes = []plumbing.Hash{parent}
	}
	obj := s.repo.Storer.NewEncodedObject()
	if err := commit.Encode(obj); err != nil {
		return plumbing.ZeroHash, err
	}
	return s.repo.Storer.SetEncodedObject(obj)
}

// loadNode materializes a stored subtree for modification.
func (s *Store) loadNode(e *nodeEntry) error {
	if e.sub != nil {
		return nil
	}
	sub := &treeNode{children: map[string]*nodeEntry{}}
	if !e.hash.IsZero() {
		t, err := s.repo.TreeObject(e.hash)
		if err != nil {
			return err
		}
		for _, te := range t.Entries {
			sub.children[te.Name] = &nodeEntry{mode: te.Mode, hash: te.Hash}
		}
	}
	e.sub = sub
	return nil
}

func (s *Store) setPath(n *treeNode, segments []string, blob plumbing.Hash) error {
	name := segments[0]
	if len(segments) == 1 {
		n.children[name] = &nodeEntry{mode: filemode.Regular, hash: blob}
		return nil
	}
	e, ok := n.children[name]
	if !ok || e.mode != filemode.Dir {
		e = &nodeEntry{mode: filemode.Dir, sub: &treeNode{children: map[string]*nodeEntry{}}}
		n.children[name] = e
	}
	if err := s.loadNode(e); err != nil {
		return err
	}
	return s.setPath(e.sub, segments[1:], blob)
}

func (s *Store) removePath(n *treeNode, segments []string) error {
	name := segments[0]
	e, ok := n.children[name]
	if !ok {
		return nil
	}
	if len(segments) == 1 {
		delete(n.children, name)
		return nil
	}
	if e.mode != filemode.Dir {
		return nil
	}
	if err := s.loadNode(e); err != nil {
		return err
	}
	if err := s.removePath(e.sub, segments[1:]); err != nil {
		return err
	}
	if len(e.sub.children) == 0 {
		delete(n.children, name) // prune empty trees
	}
	return nil
}

func (s *Store) writeBlob(content []byte) (plumbing.Hash, error) {
	obj := s.repo.Storer.NewEncodedObject()
	obj.SetType(plumbing.BlobObject)
	w, err := obj.Writer()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	if _, err := w.Write(content); err != nil {
		w.Close()
		return plumbing.ZeroHash, err
	}
	if err := w.Close(); err != nil {
		return plumbing.ZeroHash, err
	}
	return s.repo.Storer.SetEncodedObject(obj)
}

func (s *Store) writeTree(n *treeNode) (plumbing.Hash, error) {
	entries := make([]object.TreeEntry, 0, len(n.children))
	for name, e := range n.children {
		if e.sub != nil {
			if len(e.sub.children) == 0 {
				continue // git does not store empty trees
			}
			h, err := s.writeTree(e.sub)
			if err != nil {
				return plumbing.ZeroHash, err
			}
			entries = append(entries, object.TreeEntry{Name: name, Mode: filemode.Dir, Hash: h})
			continue
		}
		entries = append(entries, object.TreeEntry{Name: name, Mode: e.mode, Hash: e.hash})
	}
	sortTreeEntries(entries)
	tree := &object.Tree{Entries: entries}
	obj := s.repo.Storer.NewEncodedObject()
	if err := tree.Encode(obj); err != nil {
		return plumbing.ZeroHash, err
	}
	return s.repo.Storer.SetEncodedObject(obj)
}

// sortTreeEntries orders entries the way Git requires: byte order over
// names, with directory names compared as if they had a trailing slash.
func sortTreeEntries(entries []object.TreeEntry) {
	sort.Slice(entries, func(i, j int) bool {
		return treeSortName(entries[i]) < treeSortName(entries[j])
	})
}

func treeSortName(e object.TreeEntry) string {
	if e.Mode == filemode.Dir {
		return e.Name + "/"
	}
	return e.Name
}

// PublishCommit atomically advances the branch reference from expectedOld
// to newCommit (compare-and-swap, equivalent to `git update-ref` with an
// expected old value). ErrConcurrentUpdate is returned when the branch no
// longer points at expectedOld.
func (s *Store) PublishCommit(newCommit, expectedOld plumbing.Hash) error {
	cur, err := s.Head()
	if err != nil {
		return err
	}
	if cur != expectedOld {
		return ErrConcurrentUpdate
	}
	newRef := plumbing.NewHashReference(plumbing.ReferenceName(Branch), newCommit)
	if expectedOld.IsZero() {
		// Unborn branch: plain set (dotgit has no CAS for absent refs).
		return s.repo.Storer.SetReference(newRef)
	}
	oldRef := plumbing.NewHashReference(plumbing.ReferenceName(Branch), expectedOld)
	if err := s.repo.Storer.CheckAndSetReference(newRef, oldRef); err != nil {
		if isRefMismatch(err) {
			return ErrConcurrentUpdate
		}
		return err
	}
	return nil
}

func isRefMismatch(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "reference has changed")
}

// Change describes one file-level difference introduced by a commit.
type Change struct {
	Path    string
	OldPath string // set for renames/moves detected as delete+add pairs
	Action  ChangeAction
}

// ChangeAction enumerates diff actions.
type ChangeAction int

// Diff actions.
const (
	Added ChangeAction = iota
	Modified
	Deleted
)

// DiffCommit lists the file changes a commit introduces relative to its
// first parent (or relative to the empty tree for a root commit). Commit
// messages are never interpreted: the diff is the only authoritative
// description of a commit.
func (s *Store) DiffCommit(h plumbing.Hash) ([]Change, error) {
	c, err := s.repo.CommitObject(h)
	if err != nil {
		return nil, err
	}
	newTree, err := c.Tree()
	if err != nil {
		return nil, err
	}
	var oldTree *object.Tree
	if c.NumParents() > 0 {
		p, err := c.Parent(0)
		if err != nil {
			return nil, err
		}
		oldTree, err = p.Tree()
		if err != nil {
			return nil, err
		}
	}
	changes, err := object.DiffTree(oldTree, newTree)
	if err != nil {
		return nil, err
	}
	out := make([]Change, 0, len(changes))
	for _, ch := range changes {
		action, err := ch.Action()
		if err != nil {
			return nil, err
		}
		switch action {
		case merkletrie.Insert:
			out = append(out, Change{Path: ch.To.Name, Action: Added})
		case merkletrie.Delete:
			out = append(out, Change{Path: ch.From.Name, Action: Deleted})
		case merkletrie.Modify:
			out = append(out, Change{Path: ch.To.Name, OldPath: ch.From.Name, Action: Modified})
		}
	}
	return out, nil
}

// CommitsBetween returns the first-parent chain (oldest first) of commits
// reachable from `to` and newer than `from`. A zero `from` yields the
// complete chain.
func (s *Store) CommitsBetween(from, to plumbing.Hash) ([]plumbing.Hash, error) {
	if to.IsZero() {
		return nil, nil
	}
	var chain []plumbing.Hash
	cur := to
	for !cur.IsZero() && cur != from {
		chain = append(chain, cur)
		c, err := s.repo.CommitObject(cur)
		if err != nil {
			return nil, err
		}
		if c.NumParents() == 0 {
			cur = plumbing.ZeroHash
			break
		}
		cur = c.ParentHashes[0]
	}
	if !from.IsZero() && cur != from {
		return nil, fmt.Errorf("gitstore: %s is not an ancestor of %s", from, to)
	}
	// reverse to oldest-first
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain, nil
}

// FileHistory returns the commits (newest first, first-parent order) that
// touched paths below prefix, up to limit entries.
type HistoryEntry struct {
	Hash    plumbing.Hash
	Message string
	Author  string
	When    time.Time
}

// FileHistory lists commits that changed any path with the given prefix.
func (s *Store) FileHistory(head plumbing.Hash, prefix string, limit int) ([]HistoryEntry, error) {
	if head.IsZero() {
		return nil, nil
	}
	prefix = norm(prefix)
	var out []HistoryEntry
	cur := head
	for !cur.IsZero() && (limit <= 0 || len(out) < limit) {
		changes, err := s.DiffCommit(cur)
		if err != nil {
			return nil, err
		}
		touched := false
		for _, ch := range changes {
			if ch.Path == prefix || strings.HasPrefix(ch.Path, prefix+"/") {
				touched = true
				break
			}
		}
		c, err := s.repo.CommitObject(cur)
		if err != nil {
			return nil, err
		}
		if touched {
			out = append(out, HistoryEntry{Hash: cur, Message: c.Message, Author: c.Author.Name, When: c.Author.When})
		}
		if c.NumParents() == 0 {
			break
		}
		cur = c.ParentHashes[0]
	}
	return out, nil
}
