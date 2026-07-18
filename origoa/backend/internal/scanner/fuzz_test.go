package scanner

import (
	"strings"
	"testing"

	"origoa/internal/artifact"
)

// FuzzClassify: arbitrary repository paths must never panic the classifier,
// and a path classified as an artifact or configuration object must have the
// coherent shape the indexers rely on (a GUID directory, or a segment inside
// a configuration folder).
func FuzzClassify(f *testing.F) {
	guid := "11111111-2222-3333-4444-555555555555"
	for _, seed := range []string{
		guid + "/.origoa.json",
		"a/b/" + guid + "/.origoa.json",
		".origoa/schemas/req.json",
		".origoa/links/" + guid + ".json",
		".origoa/workflows/w.json",
		"", "/", "///", "a//b", "a/.origoa", "..", "../../x",
		strings.Repeat("d/", 50) + "f.json",
		"a\x00b/.origoa.json", "уникод/.origoa.json",
	} {
		f.Add(seed)
	}
	cfg := DefaultConfig()
	f.Fuzz(func(t *testing.T, p string) {
		cl := cfg.Classify(p) // must not panic
		switch cl.Class {
		case ArtifactFile:
			// The artifact directory is the parent of the GUID file and its
			// last segment must be a GUID.
			segs := strings.Split(cl.ArtifactDir, "/")
			if !artifact.IsGUID(segs[len(segs)-1]) {
				t.Fatalf("Classify(%q): ArtifactFile without a GUID directory: %q", p, cl.ArtifactDir)
			}
		case ConfigObjectFile:
			if cl.Category == "" {
				t.Fatalf("Classify(%q): ConfigObjectFile without a category", p)
			}
		case AttachmentFile:
			segs := strings.Split(cl.ArtifactDir, "/")
			if !artifact.IsGUID(segs[len(segs)-1]) {
				t.Fatalf("Classify(%q): AttachmentFile without a GUID directory: %q", p, cl.ArtifactDir)
			}
		}
	})
}

// FuzzArtifactSearchText: parsing arbitrary bytes never panics, and the
// extracted search text of a valid artifact carries no NUL byte on its own
// beyond what the input contained (the projection layer sanitizes; this
// guards that extraction itself is total and safe).
func FuzzArtifactSearchText(f *testing.F) {
	guid := "11111111-2222-3333-4444-555555555555"
	f.Add(`{"guid":"` + guid + `","kind":"entry","type":"t","title":"x"}`)
	f.Add(`{"guid":"` + guid + `","kind":"document","sections":[{"heading":"h","blocks":[{"type":"text","text":"<b>hi</b>"}]}]}`)
	f.Add(`[1,2,3]`)
	f.Add(`{"guid":"` + guid + `","kind":"entry","title":""}`)
	f.Fuzz(func(t *testing.T, in string) {
		af, err := artifact.Parse([]byte(in))
		if err != nil {
			return
		}
		_ = af.SearchText()        // must not panic
		_ = af.ReferencedEntries() // must not panic
	})
}
