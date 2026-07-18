package ojson

import (
	"bytes"
	"testing"
)

// FuzzRoundTripStable: any input that parses must re-serialize to a stable
// form — parsing our own output and serializing again yields identical bytes.
// This is the load/write stability the repository format depends on.
func FuzzRoundTripStable(f *testing.F) {
	for _, seed := range []string{
		`{}`, `{"a":1}`, `{"z":1,"a":{"n":[1,2,{"d":null}]}}`,
		`{"u":" x"}`, `{"":""}`, `[1,2]`, `"str"`, "  { \"a\" : 1 }\n",
		"{\r\n\t\"a\": 1.50\r\n}\r\n", `{"a":1e999}`, `{"neg":-0.0}`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		d, err := Parse(in)
		if err != nil {
			return // only well-formed JSON is contracted
		}
		// An unmodified parse re-emits the original bytes verbatim.
		if got := d.Bytes(); !bytes.Equal(got, in) {
			t.Fatalf("unmodified round trip changed bytes:\n in: %q\nout: %q", in, got)
		}
		// Re-parsing our own output and re-emitting is a fixed point.
		d2, err := Parse(d.Bytes())
		if err != nil {
			t.Fatalf("re-parse of own output failed: %v", err)
		}
		if !bytes.Equal(d.Bytes(), d2.Bytes()) {
			t.Fatalf("serialization is not a fixed point:\n%q\nvs\n%q", d.Bytes(), d2.Bytes())
		}
	})
}

// FuzzForcedRewriteStable: after a logical modification forces a rewrite, the
// output must still be canonical and idempotent under re-parse.
func FuzzForcedRewriteStable(f *testing.F) {
	for _, seed := range []string{
		`{"a":1}`, `{"z":1,"a":2}`, "{\n  \"a\": 1\n}\n", `{"nested":{"x":1}}`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		d, err := Parse(in)
		if err != nil {
			return
		}
		obj, err := d.RootObject()
		if err != nil {
			return // only object roots carry the append contract
		}
		obj.Set("forced", "rewrite")
		out := d.Bytes()
		d2, err := Parse(out)
		if err != nil {
			t.Fatalf("rewrite produced unparseable output: %q: %v", out, err)
		}
		if !bytes.Equal(out, d2.Bytes()) {
			t.Fatalf("rewritten form is not a fixed point:\n%q\nvs\n%q", out, d2.Bytes())
		}
		if v := obj.GetString("forced"); v != "rewrite" {
			t.Fatalf("appended property lost: %q", v)
		}
	})
}
