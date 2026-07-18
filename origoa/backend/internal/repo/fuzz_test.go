package repo

import (
	"strings"
	"testing"

	"origoa/internal/artifact"
)

// FuzzCleanFolder: whatever comes in, an accepted folder path must be safe —
// no path traversal, no empty or dot segments, no configuration-directory or
// GUID segments, and no leading slash — so it can never escape the repository
// or collide with the identity/metadata layout.
func FuzzCleanFolder(f *testing.F) {
	for _, seed := range []string{
		"", "a/b", "../x", "a/../b", "a//b", " /x/ ", ".origoa", "a/.origoa/b",
		"11111111-2222-3333-4444-555555555555", "a/b/.", "./a", "a/.",
		strings.Repeat("d/", 60), "uni-код", "a\x00b",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in string) {
		out, err := cleanFolder(in)
		if err != nil {
			return // rejected inputs carry no guarantee
		}
		if out == "" {
			return // the repository root is always valid
		}
		if strings.HasPrefix(out, "/") {
			t.Fatalf("cleanFolder(%q) = %q: leading slash", in, out)
		}
		for _, seg := range strings.Split(out, "/") {
			if seg == "" || seg == "." || seg == ".." {
				t.Fatalf("cleanFolder(%q) = %q: unsafe segment %q", in, out, seg)
			}
			if strings.HasPrefix(seg, ".") {
				t.Fatalf("cleanFolder(%q) = %q: dotfile segment %q", in, out, seg)
			}
			if artifact.IsGUID(seg) {
				t.Fatalf("cleanFolder(%q) = %q: GUID segment %q", in, out, seg)
			}
		}
	})
}
