package projection

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestEncodePath(t *testing.T) {
	cases := map[string]string{
		"":        "r",
		"/":       "r",
		"a":       "r.a",
		"a/b/c":   "r.a.b.c",
		"/a/b/":   "r.a.b",
		"Under_1": "r.Under_1",
	}
	for in, want := range cases {
		if got := EncodePath(in); got != want {
			t.Errorf("EncodePath(%q) = %q, want %q", in, got, want)
		}
	}
	// ltree-unsafe segments are hex-escaped behind x_, deterministically and
	// reversibly enough to avoid collisions with plain segments.
	a := EncodePath("with space")
	b := EncodePath("with-dash")
	if !strings.HasPrefix(strings.Split(a, ".")[1], "x_") || a == b {
		t.Fatalf("unsafe segments not distinctly escaped: %q vs %q", a, b)
	}
	// A segment that literally begins with x_ is itself escaped, so it can
	// never be confused with an escaped unsafe segment.
	if seg := strings.Split(EncodePath("x_plain"), ".")[1]; seg == "x_plain" {
		t.Fatalf("x_-prefixed segment not escaped: %q", seg)
	}
	// Every emitted label is ltree-safe.
	for _, label := range strings.Split(EncodePath("a b/c-d/e.f"), ".") {
		if !ltreeSafe.MatchString(label) {
			t.Fatalf("emitted non-ltree-safe label %q", label)
		}
	}
}

func TestSanitizeStripsNUL(t *testing.T) {
	if got := sanitize("a\x00b\x00c"); got != "abc" {
		t.Fatalf("sanitize did not strip NUL: %q", got)
	}
	if got := sanitize("clean"); got != "clean" {
		t.Fatalf("sanitize altered clean input: %q", got)
	}
}

func TestClampFTS(t *testing.T) {
	if got := clampFTS("hi\x00there"); strings.IndexByte(got, 0) >= 0 {
		t.Fatal("clampFTS retained NUL")
	}
	big := strings.Repeat("é", ftsCap) // 2 bytes each, well over the cap
	got := clampFTS(big)
	if len(got) > ftsCap {
		t.Fatalf("clampFTS output %d bytes exceeds cap %d", len(got), ftsCap)
	}
	if !utf8.ValidString(got) {
		t.Fatal("clampFTS truncated mid-rune, producing invalid UTF-8")
	}
}
