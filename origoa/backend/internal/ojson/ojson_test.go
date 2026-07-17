package ojson

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestUnmodifiedRoundTripIsByteIdentical(t *testing.T) {
	inputs := []string{
		"{\n  \"b\": 1,\n  \"a\": [1, 2,   3],\n  \"c\": {\"x\":true}\n}\n",
		"{\r\n\t\"title\": \"x\",\r\n\t\"n\": 1.50\r\n}\r\n",
		"{\"compact\":true,\"z\":null}",
		"[1, \"two\", {\"three\": 3}]\n",
	}
	for _, in := range inputs {
		d, err := Parse([]byte(in))
		if err != nil {
			t.Fatalf("parse %q: %v", in, err)
		}
		if got := d.Bytes(); !bytes.Equal(got, []byte(in)) {
			t.Errorf("round trip changed bytes:\n in: %q\nout: %q", in, got)
		}
	}
}

func TestSettingEqualValueKeepsDocumentClean(t *testing.T) {
	in := "{\n  \"title\": \"Widget\",\n  \"count\": 3\n}\n"
	d, _ := Parse([]byte(in))
	obj, _ := d.RootObject()
	obj.Set("title", "Widget")
	obj.Set("count", json.Number("3"))
	if d.Modified() {
		t.Fatal("setting identical values must not dirty the document")
	}
	if got := string(d.Bytes()); got != in {
		t.Fatalf("bytes changed: %q", got)
	}
}

func TestModificationPreservesOrderAndStyle(t *testing.T) {
	in := "{\n\t\"z\": 1,\n\t\"a\": 2\n}\n"
	d, _ := Parse([]byte(in))
	obj, _ := d.RootObject()
	obj.Set("a", json.Number("5"))
	obj.Set("new", "appended")
	want := "{\n\t\"z\": 1,\n\t\"a\": 5,\n\t\"new\": \"appended\"\n}\n"
	if got := string(d.Bytes()); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestDeletePreservesRemainingOrder(t *testing.T) {
	in := "{\n  \"a\": 1,\n  \"b\": 2,\n  \"c\": 3\n}\n"
	d, _ := Parse([]byte(in))
	obj, _ := d.RootObject()
	obj.Delete("b")
	want := "{\n  \"a\": 1,\n  \"c\": 3\n}\n"
	if got := string(d.Bytes()); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestNumbersAreNotReformatted(t *testing.T) {
	in := "{\n  \"f\": 1.50,\n  \"e\": 1e3\n}\n"
	d, _ := Parse([]byte(in))
	obj, _ := d.RootObject()
	obj.Set("x", "y") // force rewrite
	out := string(d.Bytes())
	if !bytes.Contains([]byte(out), []byte("1.50")) || !bytes.Contains([]byte(out), []byte("1e3")) {
		t.Fatalf("number formatting lost: %q", out)
	}
}

func TestMarshalJSONPreservesOrder(t *testing.T) {
	in := "{\"z\": 1, \"a\": {\"y\": 2, \"b\": 3}}"
	d, _ := Parse([]byte(in))
	obj, _ := d.RootObject()
	b, err := json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"z":1,"a":{"y":2,"b":3}}`
	if string(b) != want {
		t.Fatalf("got %s want %s", b, want)
	}
}

func TestNewDocDeterministicOutput(t *testing.T) {
	d := NewDoc()
	obj, _ := d.RootObject()
	obj.Set("guid", "123")
	obj.SetAny("fields", map[string]any{"b": 1, "a": "x"})
	want := "{\n  \"guid\": \"123\",\n  \"fields\": {\n    \"a\": \"x\",\n    \"b\": 1\n  }\n}\n"
	if got := string(d.Bytes()); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestRejectsTrailingGarbage(t *testing.T) {
	if _, err := Parse([]byte("{} {}")); err == nil {
		t.Fatal("expected error for trailing content")
	}
}
