// Package resolve performs lexical configuration resolution: effective
// schemas and workflow definitions are composed from the configuration
// objects found along the repository hierarchy between the root and an
// artifact location.
package resolve

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"origoa/internal/projection"
	"origoa/internal/schemamodel"
)

// EffectiveSchema composes the effective schema for an artifact type at a
// repository folder. Returns nil when no schema matches.
func EffectiveSchema(ctx context.Context, db *projection.DB, tx pgx.Tx, folder, artifactType string) (*schemamodel.EffectiveSchema, error) {
	objs, err := db.ConfigObjectsAlongPath(ctx, tx, "schemas", folder)
	if err != nil {
		return nil, err
	}
	var contributions []schemamodel.Contribution
	for _, o := range objs {
		sf, err := schemamodel.ParseSchemaFile(o.Content)
		if err != nil {
			continue // invalid definitions never break resolution
		}
		if sf.ArtifactType != artifactType {
			continue
		}
		contributions = append(contributions, schemamodel.Contribution{StoragePath: o.StoragePath, Schema: sf})
	}
	return schemamodel.Compose(artifactType, contributions), nil
}

// AvailableTypes lists every artifact type defined at a folder, composed
// through lexical inheritance.
func AvailableTypes(ctx context.Context, db *projection.DB, tx pgx.Tx, folder string) ([]*schemamodel.EffectiveSchema, error) {
	objs, err := db.ConfigObjectsAlongPath(ctx, tx, "schemas", folder)
	if err != nil {
		return nil, err
	}
	byType := map[string][]schemamodel.Contribution{}
	var order []string
	for _, o := range objs {
		sf, err := schemamodel.ParseSchemaFile(o.Content)
		if err != nil {
			continue
		}
		if _, seen := byType[sf.ArtifactType]; !seen {
			order = append(order, sf.ArtifactType)
		}
		byType[sf.ArtifactType] = append(byType[sf.ArtifactType],
			schemamodel.Contribution{StoragePath: o.StoragePath, Schema: sf})
	}
	out := make([]*schemamodel.EffectiveSchema, 0, len(order))
	for _, t := range order {
		if eff := schemamodel.Compose(t, byType[t]); eff != nil {
			out = append(out, eff)
		}
	}
	return out, nil
}

// Workflow resolves a workflow definition by name at a folder. The
// nearest matching definition wins.
func Workflow(ctx context.Context, db *projection.DB, tx pgx.Tx, folder, name string) (*schemamodel.WorkflowDef, error) {
	objs, err := db.ConfigObjectsAlongPath(ctx, tx, "workflows", folder)
	if err != nil {
		return nil, err
	}
	var found *schemamodel.WorkflowDef
	for _, o := range objs { // ordered root → folder; keep the deepest match
		wd, err := schemamodel.ParseWorkflowDef(o.Content)
		if err != nil {
			continue
		}
		if wd.Name == name {
			found = wd
		}
	}
	if found == nil {
		return nil, fmt.Errorf("workflow %q not defined at %q", name, folder)
	}
	return found, nil
}
