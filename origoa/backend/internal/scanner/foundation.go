package scanner

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"

	"origoa/internal/artifact"
	"origoa/internal/projection"
	"origoa/internal/resolve"
)

// FoundationIndexer is the built-in indexer responsible for entries,
// documents, links, comments, schemas and workflow definitions.
type FoundationIndexer struct {
	DB *projection.DB
}

// Name implements Indexer.
func (f *FoundationIndexer) Name() string { return "foundation" }

// Upsert implements Indexer.
func (f *FoundationIndexer) Upsert(ctx context.Context, tx pgx.Tx, cl Classified, repoPath string, content []byte, commit string) error {
	switch cl.Class {
	case ArtifactFile:
		af, err := artifact.Parse(content)
		if err != nil {
			return nil // malformed files never break synchronization
		}
		return f.upsertArtifact(ctx, tx, af, cl.ArtifactDir, cl.ParentDir, content, commit)

	case ConfigObjectFile:
		switch cl.Category {
		case "links":
			af, err := artifact.Parse(content)
			if err != nil || af.Kind != artifact.KindLink {
				return nil
			}
			if err := f.upsertArtifact(ctx, tx, af, repoPath, cl.ScopePath, content, commit); err != nil {
				return err
			}
			return f.DB.UpsertLinkIndex(ctx, tx, af.GUID, af.Type, af.Source, af.Target)
		case "comments":
			af, err := artifact.Parse(content)
			if err != nil || af.Kind != artifact.KindComment {
				return nil
			}
			if err := f.upsertArtifact(ctx, tx, af, repoPath, cl.ScopePath, content, commit); err != nil {
				return err
			}
			return f.DB.UpsertCommentIndex(ctx, tx, af.GUID, af.Subject, af.Parent)
		default: // schemas, workflows, other configuration
			if !strings.HasSuffix(repoPath, ".json") {
				return f.DB.EnsureFolders(ctx, tx, cl.ScopePath)
			}
			if err := f.DB.UpsertConfigObject(ctx, tx, repoPath, cl.ScopePath, cl.Category, cl.Name, content); err != nil {
				return err
			}
			return f.DB.EnsureFolders(ctx, tx, cl.ScopePath)
		}

	case AttachmentFile:
		return f.DB.EnsureFolders(ctx, tx, cl.ParentDir)
	}
	return nil
}

func (f *FoundationIndexer) upsertArtifact(ctx context.Context, tx pgx.Tx, af *artifact.File, repoPath, parentDir string, content []byte, commit string) error {
	// HID history: record when the HID differs from the last one recorded
	// for this artifact. Comparing against the recorded history (rather than
	// the projected artifact row) keeps this correct during a reindex, where
	// the artifacts table starts empty but the history has already been
	// reconstructed from Git — so the current HID is not re-recorded against
	// HEAD.
	latestHID, err := f.DB.LatestHID(ctx, tx, af.GUID)
	if err != nil {
		return err
	}
	if af.HID != "" && af.HID != latestHID {
		if err := f.DB.RecordHID(ctx, tx, af.GUID, af.HID, commit); err != nil {
			return err
		}
	}
	if err := f.DB.UpsertArtifact(ctx, tx, projection.Artifact{
		GUID: af.GUID, Kind: af.Kind, Type: af.Type, Title: af.Title, HID: af.HID,
		RepoPath: repoPath, ParentPath: parentDir, Content: content, UpdatedCommit: commit,
	}); err != nil {
		return err
	}
	if err := f.DB.EnsureFolders(ctx, tx, parentDir); err != nil {
		return err
	}
	if err := f.reindexFields(ctx, tx, af, parentDir); err != nil {
		return err
	}
	if af.Kind == artifact.KindLink {
		// Links carry no searchable prose; their synthesized titles
		// would pollute full-text results.
		return nil
	}
	return f.DB.UpsertFTS(ctx, tx, af.GUID, af.SearchText())
}

// reindexFields extracts the schema-defined indexed key/value pairs.
func (f *FoundationIndexer) reindexFields(ctx context.Context, tx pgx.Tx, af *artifact.File, folder string) error {
	fields := map[string][]string{
		"type": {af.Type},
		"kind": {af.Kind},
	}
	if af.HID != "" {
		fields["hid"] = []string{af.HID}
	}
	for wf, state := range af.Workflows {
		fields["workflow."+wf] = []string{state}
	}
	if af.Base != "" {
		fields["base"] = []string{af.Base}
	}
	if af.Type != "" {
		eff, err := resolve.EffectiveSchema(ctx, f.DB, tx, folder, af.Type)
		if err != nil {
			return err
		}
		if eff != nil {
			for _, id := range eff.IndexedFieldIDs() {
				if v, ok := af.Fields[id]; ok {
					if vals := artifact.FieldStrings(v); len(vals) > 0 {
						fields[id] = vals
					}
				}
			}
		}
	}
	return f.DB.ReplaceFieldIndex(ctx, tx, af.GUID, fields)
}

// Delete implements Indexer.
func (f *FoundationIndexer) Delete(ctx context.Context, tx pgx.Tx, cl Classified, repoPath string, content []byte, commit string, movedGUIDs map[string]bool) error {
	switch cl.Class {
	case ArtifactFile:
		af, err := artifact.Parse(content)
		if err != nil {
			return nil
		}
		if movedGUIDs[af.GUID] {
			return nil // the artifact reappeared elsewhere in this commit: a move, not a deletion
		}
		return f.DB.DeleteArtifact(ctx, tx, af.GUID, commit)

	case ConfigObjectFile:
		switch cl.Category {
		case "links", "comments":
			af, err := artifact.Parse(content)
			if err != nil {
				return nil
			}
			if movedGUIDs[af.GUID] {
				return nil
			}
			return f.DB.DeleteArtifact(ctx, tx, af.GUID, commit)
		default:
			return f.DB.DeleteConfigObject(ctx, tx, repoPath)
		}
	}
	return nil
}
