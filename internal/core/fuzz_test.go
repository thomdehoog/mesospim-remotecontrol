package core

import (
	"strings"
	"testing"
)

// FuzzCleanFolder: whatever comes in, an accepted folder must be safe — no
// traversal, no metadata dirs, no GUID segments, no control chars, no
// pathspec magic, bounded size.
func FuzzCleanFolder(f *testing.F) {
	for _, seed := range []string{"", "a/b", "../x", ":team", "a\nb", ".origoa",
		"uni-код", strings.Repeat("d/", 40), "a*b", "a\\b", "a//b", " /x/ "} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in string) {
		out, err := CleanFolder(in)
		if err != nil {
			return
		}
		if out == "" {
			return
		}
		if len(out) > maxFolderLen || out[0] == ':' || out[0] == '/' {
			t.Fatalf("CleanFolder(%q) = %q: unsafe shape", in, out)
		}
		for _, seg := range strings.Split(out, "/") {
			if seg == "" || seg == "." || seg == ".." || seg == MetaDir || IsGUID(seg) {
				t.Fatalf("CleanFolder(%q) = %q: unsafe segment %q", in, out, seg)
			}
		}
		for _, r := range out {
			if r < 0x20 || r == 0x7f {
				t.Fatalf("CleanFolder(%q) = %q: control character", in, out)
			}
		}
	})
}

// FuzzClassify: arbitrary repository content must never panic the classifier
// and must never produce text with NUL bytes or beyond the cap (both would
// wedge the PostgreSQL projection).
func FuzzClassify(f *testing.F) {
	guid := NewGUID()
	f.Add(guid+"/.origoa.json", []byte(`{"guid":"`+guid+`","kind":"entry","type":"t","title":"x"}`))
	f.Add(".origoa/links/"+guid+".json", []byte(`{"guid":"`+guid+`","kind":"link","source":"a","target":"b"}`))
	f.Add(".origoa/schemas/t.json", []byte(`{"artifactType":"t"}`))
	f.Add(".origoa/workflows/w.json", []byte(`{"id":"w","initial":"a","states":["a"]}`))
	f.Add(guid+"/.origoa.json", []byte("{\"guid\":\""+guid+"\",\"kind\":\"entry\",\"title\":\"\x00\xff\"}"))
	f.Add("x/.origoa.json", []byte(`[1,2,3]`))
	f.Fuzz(func(t *testing.T, path string, content []byte) {
		rec := classify(path, "0000000000000000000000000000000000000000", content)
		if rec == nil {
			return
		}
		if strings.ContainsRune(rec.text, 0) {
			t.Fatalf("classify(%q) produced NUL in search text", path)
		}
		if len(rec.text) > searchTextCap+1024 {
			t.Fatalf("classify(%q) text exceeds cap: %d", path, len(rec.text))
		}
		if rec.meta != nil {
			for _, s := range []string{rec.meta.Title, rec.meta.Type, rec.meta.HID} {
				if strings.ContainsRune(s, 0) {
					t.Fatalf("classify(%q) produced NUL in meta", path)
				}
			}
		}
	})
}
