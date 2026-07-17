package ojson

import (
	"bytes"
	"testing"
)

// FuzzRoundTrip: any object that parses must re-encode to a stable canonical
// form — encoding twice yields identical bytes and identical key order.
func FuzzRoundTrip(f *testing.F) {
	for _, seed := range []string{
		`{}`, `{"a":1}`, `{"z":1,"a":{"n":[1,2,{"d":null}]}}`,
		`{"dup":1,"dup":2}`, `{"u":"\u0000 x"}`, `{"":""}`,
		`[1,2]`, `"str"`, `{"a":`, `{"a":1e999}`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		o, err := Parse(in)
		if err != nil {
			return
		}
		enc1, err := o.Encode()
		if err != nil {
			t.Fatalf("Encode after Parse(%q): %v", in, err)
		}
		o2, err := Parse(enc1)
		if err != nil {
			t.Fatalf("re-Parse of own encoding failed: %q -> %q: %v", in, enc1, err)
		}
		enc2, err := o2.Encode()
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(enc1, enc2) {
			t.Fatalf("unstable canonical form:\n%q\nvs\n%q", enc1, enc2)
		}
	})
}
