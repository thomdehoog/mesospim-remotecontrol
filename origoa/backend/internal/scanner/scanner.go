// Package scanner synchronizes the Git repository with the PostgreSQL
// projection.
//
// The scanner does not analyze every file: it searches for a configurable
// set of repository markers (GUID files and configuration folders) and
// delegates processing to configured indexers. Commit messages are never
// interpreted — the repository state and the Git diff are the only
// authoritative description of a change.
package scanner

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/jackc/pgx/v5"

	"origoa/internal/artifact"
	"origoa/internal/gitstore"
	"origoa/internal/projection"
)

// Config is the scanner configuration (origoa-scanner.json).
type Config struct {
	GUIDFiles     []string `json:"guid_files"`
	ConfigFolders []string `json:"config_folders"`
	Indexers      []string `json:"indexers"`
}

// DefaultConfig returns the standard Foundation scanner configuration.
func DefaultConfig() Config {
	return Config{
		GUIDFiles:     []string{".origoa.json"},
		ConfigFolders: []string{".origoa"},
		Indexers:      []string{"foundation"},
	}
}

// ParseConfig reads a scanner configuration file.
func ParseConfig(b []byte) (Config, error) {
	c := DefaultConfig()
	if len(b) == 0 {
		return c, nil
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("scanner config: %w", err)
	}
	if len(c.GUIDFiles) == 0 {
		c.GUIDFiles = []string{".origoa.json"}
	}
	if len(c.ConfigFolders) == 0 {
		c.ConfigFolders = []string{".origoa"}
	}
	if len(c.Indexers) == 0 {
		c.Indexers = []string{"foundation"}
	}
	return c, nil
}

// PathClass describes how the scanner classifies one repository path.
type PathClass int

// Path classifications.
const (
	Irrelevant PathClass = iota
	ArtifactFile          // a configured GUID file inside a GUID directory
	ConfigObjectFile      // a file inside a configured configuration folder
	AttachmentFile        // other file inside a GUID directory
)

// Classified is the result of classifying a repository path.
type Classified struct {
	Class PathClass

	// ArtifactFile / AttachmentFile
	ArtifactDir string // GUID directory path
	ParentDir   string // folder containing the GUID directory

	// ConfigObjectFile
	ScopePath string // folder owning the configuration folder
	Category  string // first path segment inside the config folder (schemas, workflows, links, comments, ...)
	Name      string // file name without extension
}

// Classify determines what a repository path means to the Foundation.
func (c Config) Classify(p string) Classified {
	segs := strings.Split(p, "/")
	// Configuration folder?
	for i, seg := range segs[:len(segs)-1] {
		if c.isConfigFolder(seg) {
			scope := strings.Join(segs[:i], "/")
			rest := segs[i+1:]
			category := "misc"
			if len(rest) > 1 {
				category = rest[0]
			}
			name := strings.TrimSuffix(rest[len(rest)-1], path.Ext(rest[len(rest)-1]))
			return Classified{Class: ConfigObjectFile, ScopePath: scope, Category: category, Name: name}
		}
	}
	base := segs[len(segs)-1]
	if len(segs) >= 2 && c.isGUIDFile(base) && artifact.IsGUID(segs[len(segs)-2]) {
		return Classified{
			Class:       ArtifactFile,
			ArtifactDir: strings.Join(segs[:len(segs)-1], "/"),
			ParentDir:   strings.Join(segs[:len(segs)-2], "/"),
		}
	}
	if len(segs) >= 2 && artifact.IsGUID(segs[len(segs)-2]) {
		return Classified{
			Class:       AttachmentFile,
			ArtifactDir: strings.Join(segs[:len(segs)-1], "/"),
			ParentDir:   strings.Join(segs[:len(segs)-2], "/"),
		}
	}
	return Classified{Class: Irrelevant}
}

func (c Config) isConfigFolder(name string) bool {
	for _, f := range c.ConfigFolders {
		if f == name {
			return true
		}
	}
	return false
}

func (c Config) isGUIDFile(name string) bool {
	for _, f := range c.GUIDFiles {
		if f == name {
			return true
		}
	}
	return false
}

// Indexer processes classified repository changes. The Foundation ships
// the built-in "foundation" indexer; applications may register more.
type Indexer interface {
	Name() string
	// Upsert is called for added or modified repository content.
	Upsert(ctx context.Context, tx pgx.Tx, cl Classified, repoPath string, content []byte, commit string) error
	// Delete is called for removed repository content; content holds the
	// last version of the file (from the parent commit).
	Delete(ctx context.Context, tx pgx.Tx, cl Classified, repoPath string, content []byte, commit string, movedGUIDs map[string]bool) error
}

// Scanner projects Git commits into the database.
type Scanner struct {
	cfg      Config
	git      *gitstore.Store
	db       *projection.DB
	indexers []Indexer
}

// New creates a scanner with the given configuration and indexers.
func New(cfg Config, git *gitstore.Store, db *projection.DB, available ...Indexer) (*Scanner, error) {
	s := &Scanner{cfg: cfg, git: git, db: db}
	byName := map[string]Indexer{}
	for _, ix := range available {
		byName[ix.Name()] = ix
	}
	for _, name := range cfg.Indexers {
		ix, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("scanner: unknown indexer %q", name)
		}
		s.indexers = append(s.indexers, ix)
	}
	return s, nil
}

// Config returns the scanner configuration.
func (s *Scanner) Config() Config { return s.cfg }

// ProjectCommit applies the changes introduced by one commit to the
// projection, inside the caller's transaction.
func (s *Scanner) ProjectCommit(ctx context.Context, tx pgx.Tx, commit plumbing.Hash) error {
	changes, err := s.git.DiffCommit(commit)
	if err != nil {
		return err
	}
	c, err := s.git.Commit(commit)
	if err != nil {
		return err
	}
	parent := plumbing.ZeroHash
	if c.NumParents() > 0 {
		parent = c.ParentHashes[0]
	}

	// Pass 1: adds and modifications (config objects first so schema
	// resolution sees current definitions while indexing artifacts).
	var upserts []gitstore.Change
	var deletes []gitstore.Change
	for _, ch := range changes {
		if ch.Action == gitstore.Deleted {
			deletes = append(deletes, ch)
		} else {
			upserts = append(upserts, ch)
		}
	}
	movedGUIDs := map[string]bool{}
	order := func(list []gitstore.Change, wantConfig bool) []gitstore.Change {
		var out []gitstore.Change
		for _, ch := range list {
			isCfg := s.cfg.Classify(ch.Path).Class == ConfigObjectFile
			if isCfg == wantConfig {
				out = append(out, ch)
			}
		}
		return out
	}
	for _, group := range [][]gitstore.Change{order(upserts, true), order(upserts, false)} {
		for _, ch := range group {
			cl := s.cfg.Classify(ch.Path)
			if cl.Class == Irrelevant {
				continue
			}
			content, ok, err := s.git.ReadBlob(commit, ch.Path)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			if cl.Class == ArtifactFile {
				if f, err := artifact.Parse(content); err == nil {
					movedGUIDs[f.GUID] = true
				}
			}
			if cl.Class == ConfigObjectFile && (cl.Category == "links" || cl.Category == "comments") {
				if f, err := artifact.Parse(content); err == nil {
					movedGUIDs[f.GUID] = true
				}
			}
			for _, ix := range s.indexers {
				if err := ix.Upsert(ctx, tx, cl, ch.Path, content, commit.String()); err != nil {
					return err
				}
			}
		}
	}

	// Pass 2: deletions. The previous file content is read from the
	// parent commit so indexers can identify the removed object.
	pruneNeeded := false
	for _, ch := range deletes {
		cl := s.cfg.Classify(ch.Path)
		if cl.Class == Irrelevant {
			continue
		}
		content, _, err := s.git.ReadBlob(parent, ch.Path)
		if err != nil {
			return err
		}
		for _, ix := range s.indexers {
			if err := ix.Delete(ctx, tx, cl, ch.Path, content, commit.String(), movedGUIDs); err != nil {
				return err
			}
		}
		pruneNeeded = true
	}
	if pruneNeeded {
		if err := s.db.PruneFolders(ctx, tx); err != nil {
			return err
		}
	}
	return nil
}

// ProjectTree projects the complete tree of a commit (used by reindex).
func (s *Scanner) ProjectTree(ctx context.Context, tx pgx.Tx, commit plumbing.Hash) error {
	// Config objects first, then artifacts, for the same reason as above.
	for _, wantConfig := range []bool{true, false} {
		err := s.git.WalkTree(commit, func(p string, read func() ([]byte, error)) error {
			cl := s.cfg.Classify(p)
			if cl.Class == Irrelevant {
				return nil
			}
			if (cl.Class == ConfigObjectFile) != wantConfig {
				return nil
			}
			content, err := read()
			if err != nil {
				return err
			}
			for _, ix := range s.indexers {
				if err := ix.Upsert(ctx, tx, cl, p, content, commit.String()); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// Replay projects all commits between the stored processed hash and the
// given head, updating processed_hash. Used for recovery after crashes,
// external Git pushes, and interrupted synchronization.
func (s *Scanner) Replay(ctx context.Context, tx pgx.Tx, head plumbing.Hash) error {
	stored, err := s.db.ProcessedHash(ctx, tx)
	if err != nil {
		return err
	}
	from := plumbing.ZeroHash
	if stored != "" {
		from = plumbing.NewHash(stored)
	}
	if from == head {
		return nil
	}
	chain, err := s.git.CommitsBetween(from, head)
	if err != nil {
		return err
	}
	for _, h := range chain {
		if err := s.ProjectCommit(ctx, tx, h); err != nil {
			return fmt.Errorf("replay %s: %w", h, err)
		}
	}
	return s.db.SetProcessedHash(ctx, tx, head.String())
}
