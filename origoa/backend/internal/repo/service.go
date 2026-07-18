// Package repo implements the Origoa repository services: the
// transactional update procedure that keeps Git (the source of truth)
// and the PostgreSQL projection synchronized, artifact CRUD, structural
// operations, workflow transitions, overlay resolution, recovery and
// reindexing.
package repo

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"origoa/internal/gitstore"
	"origoa/internal/projection"
	"origoa/internal/scanner"
)

// ErrMaintenance is returned for write operations while the repository is
// in maintenance mode.
var ErrMaintenance = errors.New("repository is in maintenance mode")

// ErrConflict is returned when optimistic concurrency validation fails:
// the artifact was modified by another user since it was loaded.
var ErrConflict = errors.New("artifact was modified concurrently")

// ErrNotFound is returned when a GUID cannot be resolved.
var ErrNotFound = errors.New("artifact not found")

// ErrValidation wraps user-input validation failures.
type ErrValidation struct{ Msg string }

func (e ErrValidation) Error() string { return e.Msg }

func validationErr(format string, args ...any) error {
	return ErrValidation{Msg: fmt.Sprintf(format, args...)}
}

// maintenanceThreshold is the number of affected artifacts above which a
// structural operation switches the repository into maintenance mode.
const maintenanceThreshold = 100

// Event is broadcast to connected clients (WebSocket session service).
type Event struct {
	Type    string `json:"type"`
	GUID    string `json:"guid,omitempty"`
	Path    string `json:"path,omitempty"`
	Detail  string `json:"detail,omitempty"`
	Percent int    `json:"percent,omitempty"`
}

// ReindexProgress describes the state of a running reindex.
type ReindexProgress struct {
	Running bool   `json:"running"`
	Phase   string `json:"phase,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

// Service exposes the repository operations.
type Service struct {
	Git     *gitstore.Store
	DB      *projection.DB
	Scanner *scanner.Scanner

	AuthorName  string
	AuthorEmail string

	// EventSink receives repository events for the session service.
	EventSink func(Event)

	// syncMu is the repository synchronization mutex the spec names: it
	// protects only the Git branch update and database revision tracking,
	// minimizing contention between concurrent writers.
	syncMu sync.Mutex

	// maint implements the spec's Maintenance Mode. Writers hold it shared
	// (they remain concurrent with one another); a reindex or large
	// structural operation holds it exclusively, so the repository is
	// effectively read-only for the duration while ordinary reads continue.
	maint sync.RWMutex

	maintenance atomic.Bool
	reindexing  atomic.Bool
	progress    atomic.Value // ReindexProgress
}

// New creates the service.
func New(git *gitstore.Store, db *projection.DB, sc *scanner.Scanner) *Service {
	s := &Service{Git: git, DB: db, Scanner: sc, AuthorName: "origoa", AuthorEmail: "origoa@localhost"}
	s.progress.Store(ReindexProgress{})
	return s
}

func (s *Service) emit(e Event) {
	if s.EventSink != nil {
		s.EventSink(e)
	}
}

// Maintenance reports whether the repository is in maintenance mode.
func (s *Service) Maintenance() bool { return s.maintenance.Load() }

// Progress returns the current reindex progress.
func (s *Service) Progress() ReindexProgress {
	p, _ := s.progress.Load().(ReindexProgress)
	return p
}

func (s *Service) setProgress(phase, detail string) {
	running := phase != ""
	s.progress.Store(ReindexProgress{Running: running, Phase: phase, Detail: detail})
	if running {
		s.emit(Event{Type: "reindex", Detail: phase + ": " + detail})
	}
}

// Sync brings the projection up to date with the current Git HEAD by
// replaying missing commits. Called at startup and before every update.
//
// processed_hash is read before HEAD (see Replay) so a writer advancing the
// repository between the two reads can only make HEAD newer than the stored
// hash, never the reverse.
func (s *Service) Sync(ctx context.Context) error {
	stored, err := s.DB.ProcessedHash(ctx, nil)
	if err != nil {
		return err
	}
	head, err := s.Git.Head()
	if err != nil {
		return err
	}
	if (head.IsZero() && stored == "") || head.String() == stored {
		return nil
	}
	tx, err := s.DB.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := s.Scanner.Replay(ctx, tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// SyncOrReindex synchronizes the projection incrementally, falling back to a
// full reindex when replay is impossible (for example a Git history rewrite).
// Called at startup, where holding no maintenance lock makes the reindex
// safe; the in-request write path never triggers a reindex from Sync.
func (s *Service) SyncOrReindex(ctx context.Context) error {
	if err := s.Sync(ctx); err != nil {
		log.Printf("repo: incremental sync failed (%v); performing full reindex", err)
		return s.Reindex(ctx)
	}
	return nil
}

// BuildFunc constructs the changeset of one logical repository operation
// on top of the given head revision and returns the structured commit
// message. It is re-invoked when a concurrent update forces a retry.
type BuildFunc func(head plumbing.Hash, cs *gitstore.Changeset) (string, error)

// Update executes the repository update transaction:
//
//  1. Check processed_hash against Git HEAD; replay missing commits.
//  2. Construct the Git commit (plumbing, branch untouched).
//  3. Begin the PostgreSQL transaction.
//  4. Update the database projection (excluding processed_hash).
//  5. Acquire the repository mutex.
//  6. Publish the Git commit (update-ref compare-and-swap).
//     On failure: release mutex, roll back, rebuild, retry.
//  7. Update processed_hash.
//  8. Release the repository mutex.
//  9. Commit the PostgreSQL transaction.
func (s *Service) Update(ctx context.Context, build BuildFunc) (plumbing.Hash, error) {
	return s.update(ctx, build, false)
}

func (s *Service) update(ctx context.Context, build BuildFunc, exclusiveHeld bool) (plumbing.Hash, error) {
	// Enter Maintenance Mode as a reader: many writers proceed concurrently,
	// but a reindex or large structural operation holding the lock
	// exclusively makes writes return "Temporarily Unavailable". A caller
	// that already holds the exclusive lock (a large structural operation)
	// skips this to avoid self-deadlock.
	if !exclusiveHeld {
		if !s.maint.TryRLock() {
			return plumbing.ZeroHash, ErrMaintenance
		}
		defer s.maint.RUnlock()
	}
	// A branch that moved under a concurrent writer means the transaction is
	// simply restarted against the new head (spec step 7); the bound is a
	// livelock backstop, generous relative to any realistic writer count.
	const maxRetries = 50
	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			retryBackoff(attempt)
		}
		// (1) synchronize projection with HEAD
		if err := s.Sync(ctx); err != nil {
			return plumbing.ZeroHash, err
		}
		head, err := s.Git.Head()
		if err != nil {
			return plumbing.ZeroHash, err
		}

		// (2) construct the Git commit without touching the branch
		cs := gitstore.NewChangeset()
		message, err := build(head, cs)
		if err != nil {
			return plumbing.ZeroHash, err
		}
		if cs.Empty() {
			return head, nil
		}
		newCommit, err := s.Git.BuildCommit(head, cs, message, s.AuthorName, s.AuthorEmail)
		if err != nil {
			return plumbing.ZeroHash, err
		}

		// (3) + (4) project the new commit inside a DB transaction
		tx, err := s.DB.Pool.Begin(ctx)
		if err != nil {
			return plumbing.ZeroHash, err
		}
		if err := s.Scanner.ProjectCommit(ctx, tx, newCommit); err != nil {
			tx.Rollback(ctx)
			if isUniqueViolation(err) {
				// A concurrent writer claimed the same generated value
				// (e.g. an HID). Rebuild against fresh state and retry.
				continue
			}
			return plumbing.ZeroHash, err
		}

		// (5) acquire the repository synchronization mutex
		s.syncMu.Lock()

		// (6) publish via compare-and-swap
		if err := s.Git.PublishCommit(newCommit, head); err != nil {
			s.syncMu.Unlock()
			tx.Rollback(ctx)
			if errors.Is(err, gitstore.ErrConcurrentUpdate) {
				continue // rebuild on top of the new head and retry
			}
			return plumbing.ZeroHash, err
		}

		// (7) record the published revision inside the transaction
		if err := s.DB.SetProcessedHash(ctx, tx, newCommit.String()); err != nil {
			s.syncMu.Unlock()
			tx.Rollback(ctx)
			return plumbing.ZeroHash, err
		}

		// (8) release the mutex, (9) commit the DB transaction
		s.syncMu.Unlock()
		if err := tx.Commit(ctx); err != nil {
			// Git now contains a newer revision than the projection.
			// Git is authoritative: replay the missing commit.
			log.Printf("repo: projection commit failed after publish (%v); resynchronizing", err)
			if syncErr := s.Sync(ctx); syncErr != nil {
				return newCommit, fmt.Errorf("projection failed (%v) and resync failed: %w", err, syncErr)
			}
		}
		return newCommit, nil
	}
	return plumbing.ZeroHash, fmt.Errorf("repository update failed after %d retries: %w", maxRetries, gitstore.ErrConcurrentUpdate)
}

// retryBackoff sleeps for a short, jittered, escalating interval that spreads
// competing writers apart so they stop colliding on the branch CAS.
func retryBackoff(attempt int) {
	time.Sleep(time.Duration(attempt)*2*time.Millisecond + time.Duration(rand.IntN(8))*time.Millisecond)
}

// Reindex performs a complete repository rebuild from Git:
//
//	Phase 1 — GUID recognition: rebuild the GUID → path translation.
//	Phase 2 — Field indexing: metadata and schema-defined field index.
//	Phase 3 — Full-text rebuild.
//	Phase 4 — History scan: record deleted artifacts and their commits.
//
// Phases 1–3 share one tree traversal (the foundation indexer projects
// identity, fields and text per artifact); the history scan then walks
// backwards through Git history.
func (s *Service) Reindex(ctx context.Context) error {
	if !s.reindexing.CompareAndSwap(false, true) {
		return errors.New("reindex already running")
	}
	defer s.reindexing.Store(false)

	// Enter Maintenance Mode exclusively: this waits for every in-flight
	// writer to finish and blocks new ones, so the rebuild runs against a
	// stable HEAD with no concurrent projection writes. Reads continue.
	s.maint.Lock()
	defer s.maint.Unlock()

	s.maintenance.Store(true)
	s.emit(Event{Type: "maintenance", Detail: "enabled"})
	defer func() {
		s.maintenance.Store(false)
		s.setProgress("", "")
		s.emit(Event{Type: "maintenance", Detail: "disabled"})
	}()

	head, err := s.Git.Head()
	if err != nil {
		return err
	}

	tx, err := s.DB.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	s.setProgress("reset", "clearing derived data")
	if err := s.DB.Reset(ctx, tx); err != nil {
		return err
	}

	s.setProgress("guid-recognition", "rebuilding GUID translation table")
	s.setProgress("field-indexing", "rebuilding metadata and field index")
	s.setProgress("fulltext", "rebuilding full-text indexes")
	if !head.IsZero() {
		if err := s.Scanner.ProjectTree(ctx, tx, head); err != nil {
			return err
		}
	}

	s.setProgress("history-scan", "recording deleted artifacts")
	if err := s.historyScan(ctx, tx, head); err != nil {
		return err
	}

	if err := s.DB.SetProcessedHash(ctx, tx, headString(head)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	s.setProgress("", "")
	return nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func headString(h plumbing.Hash) string {
	if h.IsZero() {
		return ""
	}
	return h.String()
}

// historyScan traverses Git history to find artifacts that were deleted
// and records the commit in which each artifact disappeared.
func (s *Service) historyScan(ctx context.Context, tx pgx.Tx, head plumbing.Hash) error {
	if head.IsZero() {
		return nil
	}
	chain, err := s.Git.CommitsBetween(plumbing.ZeroHash, head)
	if err != nil {
		return err
	}
	// Walk oldest → newest, remembering the latest content of every GUID
	// file; a deletion without a same-commit re-add marks the artifact
	// deleted in that commit (unless it exists again at HEAD).
	cfg := s.Scanner.Config()
	for _, h := range chain {
		changes, err := s.Git.DiffCommit(h)
		if err != nil {
			return err
		}
		commit, err := s.Git.Commit(h)
		if err != nil {
			return err
		}
		parent := plumbing.ZeroHash
		if commit.NumParents() > 0 {
			parent = commit.ParentHashes[0]
		}
		for _, ch := range changes {
			if ch.Action != gitstore.Deleted {
				continue
			}
			cl := cfg.Classify(ch.Path)
			isArtifact := cl.Class == scanner.ArtifactFile ||
				(cl.Class == scanner.ConfigObjectFile && (cl.Category == "links" || cl.Category == "comments"))
			if !isArtifact {
				continue
			}
			content, ok, err := s.Git.ReadBlob(parent, ch.Path)
			if err != nil || !ok {
				continue
			}
			af, err := parseArtifact(content)
			if err != nil {
				continue
			}
			// Skip when the artifact still exists in the projection
			// (it was moved, or re-created later).
			var exists int
			if err := tx.QueryRow(ctx,
				`SELECT count(*) FROM artifacts WHERE guid=$1`, af.GUID).Scan(&exists); err != nil {
				return err
			}
			if exists > 0 {
				continue
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO deleted_artifacts (guid, kind, type, title, hid, last_path, deleted_in_commit)
				VALUES ($1,$2,$3,$4,$5,$6,$7)
				ON CONFLICT (guid) DO UPDATE SET deleted_in_commit=$7, last_path=$6`,
				af.GUID, af.Kind, af.Type, af.Title, af.HID, ch.Path, h.String()); err != nil {
				return err
			}
		}
	}
	return nil
}
