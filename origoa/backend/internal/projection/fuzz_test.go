package projection

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzSanitizeFTS: whatever hostile bytes reach the projection layer, the
// values written to PostgreSQL text and tsvector columns must never contain a
// NUL byte (SQLSTATE 22021), the full-text input must stay under the tsvector
// size ceiling, and truncation must preserve valid UTF-8.
func FuzzSanitizeFTS(f *testing.F) {
	for _, seed := range []string{
		"", "plain", "nul\x00here", "\x00\x00", "unicode ☃ é 🔥",
		strings.Repeat("x", ftsCap+100), strings.Repeat("é", ftsCap),
		strings.Repeat("word ", 500_000),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in string) {
		if s := sanitize(in); strings.IndexByte(s, 0) >= 0 {
			t.Fatalf("sanitize(%q) still contains NUL", in)
		}
		c := clampFTS(in)
		if strings.IndexByte(c, 0) >= 0 {
			t.Fatalf("clampFTS retained a NUL byte")
		}
		if len(c) > ftsCap {
			t.Fatalf("clampFTS output %d bytes exceeds cap %d", len(c), ftsCap)
		}
		if !utf8.ValidString(c) && utf8.ValidString(sanitize(in)) {
			t.Fatalf("clampFTS truncated mid-rune, producing invalid UTF-8")
		}
	})
}
