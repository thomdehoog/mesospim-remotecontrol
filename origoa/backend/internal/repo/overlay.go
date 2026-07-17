package repo

import (
	"context"
	"encoding/json"
	"fmt"

	"origoa/internal/artifact"
)

// OverlayLevel describes one level of an overlay composition chain.
type OverlayLevel struct {
	GUID   string         `json:"guid"`
	Title  string         `json:"title"`
	HID    string         `json:"hid,omitempty"`
	Fields map[string]any `json:"fields"`
}

// ResolvedEntry is an entry with its overlay composition applied.
type ResolvedEntry struct {
	GUID string `json:"guid"`
	// Fields is the effective field set: values from the overlay replace
	// values inherited from the base chain.
	Fields map[string]any `json:"fields"`
	// FieldOrigin maps each effective field to the GUID that supplied it.
	FieldOrigin map[string]string `json:"fieldOrigin"`
	// Chain lists the composition levels, the entry itself first, its
	// base next, and so on up to the root base.
	Chain []OverlayLevel `json:"chain"`
}

// ResolveOverlay computes the effective fields of an entry by walking its
// base chain. Only fields explicitly contained within an overlay replace
// values inherited from its base; all remaining fields are resolved
// dynamically from the referenced base. Composition may span multiple
// inheritance levels.
func (s *Service) ResolveOverlay(ctx context.Context, guid string) (*ResolvedEntry, error) {
	var chain []OverlayLevel
	seen := map[string]bool{}
	cur := guid
	for cur != "" {
		if seen[cur] {
			return nil, fmt.Errorf("overlay cycle detected at %s", cur)
		}
		seen[cur] = true
		row, err := s.DB.GetArtifact(ctx, cur)
		if err != nil {
			return nil, err
		}
		if row == nil {
			if cur == guid {
				return nil, ErrNotFound
			}
			break // dangling base reference: stop resolution
		}
		af, err := artifact.Parse(row.Content)
		if err != nil {
			return nil, err
		}
		chain = append(chain, OverlayLevel{GUID: cur, Title: af.Title, HID: af.HID, Fields: normalizeFields(af.Fields)})
		cur = af.Base
	}
	resolved := &ResolvedEntry{GUID: guid, Fields: map[string]any{}, FieldOrigin: map[string]string{}, Chain: chain}
	// Apply from the root base downwards so nearer levels override.
	for i := len(chain) - 1; i >= 0; i-- {
		for k, v := range chain[i].Fields {
			resolved.Fields[k] = v
			resolved.FieldOrigin[k] = chain[i].GUID
		}
	}
	return resolved, nil
}

// checkOverlayCycle verifies that setting newBase on guid does not create
// a cycle in the overlay graph.
func (s *Service) checkOverlayCycle(ctx context.Context, guid, newBase string) error {
	cur := newBase
	for i := 0; cur != "" && i < 64; i++ {
		if cur == guid {
			return validationErr("overlay base would create a cycle through %s", cur)
		}
		row, err := s.DB.GetArtifact(ctx, cur)
		if err != nil {
			return err
		}
		if row == nil {
			return nil
		}
		var f struct {
			Base string `json:"base"`
		}
		if err := json.Unmarshal(row.Content, &f); err != nil {
			return nil
		}
		cur = f.Base
	}
	return nil
}
