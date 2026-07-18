package artifact

import "testing"

const guid = "11111111-2222-3333-4444-555555555555"

func TestIsGUID(t *testing.T) {
	valid := []string{guid, "00000000-0000-0000-0000-000000000000"}
	invalid := []string{"", "not-a-guid", guid + "x", "11111111222233334444555555555555",
		"11111111-2222-3333-4444-55555555555G", ".origoa"}
	for _, s := range valid {
		if !IsGUID(s) {
			t.Errorf("IsGUID(%q) = false, want true", s)
		}
	}
	for _, s := range invalid {
		if IsGUID(s) {
			t.Errorf("IsGUID(%q) = true, want false", s)
		}
	}
}

func TestParseValidatesGUIDAndKind(t *testing.T) {
	if _, err := Parse([]byte(`{"kind":"entry","title":"x"}`)); err == nil {
		t.Error("expected error for missing guid")
	}
	if _, err := Parse([]byte(`{"guid":"` + guid + `","kind":"bogus"}`)); err == nil {
		t.Error("expected error for unknown kind")
	}
	// A missing kind defaults to entry.
	f, err := Parse([]byte(`{"guid":"` + guid + `","title":"x"}`))
	if err != nil || f.Kind != KindEntry {
		t.Fatalf("default kind: %v %+v", err, f)
	}
}

func TestSearchTextGathersHumanText(t *testing.T) {
	f, err := Parse([]byte(`{
		"guid":"` + guid + `","kind":"entry","type":"requirement","title":"Boot fast","hid":"REQ-1",
		"fields":{"description":"<b>must</b> boot","tags":["speed","safety"]}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	got := f.SearchText()
	for _, want := range []string{"Boot fast", "REQ-1", "requirement", "must", "boot", "speed", "safety"} {
		if !contains(got, want) {
			t.Errorf("SearchText missing %q in %q", want, got)
		}
	}
	// HTML tags are stripped, not indexed as markup.
	if contains(got, "<b>") {
		t.Errorf("SearchText retained HTML markup: %q", got)
	}
}

func TestSearchTextWalksDocumentSections(t *testing.T) {
	f, _ := Parse([]byte(`{
		"guid":"` + guid + `","kind":"document","title":"Spec",
		"sections":[{"heading":"Intro","blocks":[{"type":"text","text":"hello world"}],
		  "children":[{"heading":"Sub","blocks":[{"type":"text","text":"nested body"}]}]}]
	}`))
	got := f.SearchText()
	for _, want := range []string{"Intro", "hello world", "Sub", "nested body"} {
		if !contains(got, want) {
			t.Errorf("SearchText missing %q in %q", want, got)
		}
	}
}

func TestReferencedEntries(t *testing.T) {
	other := "99999999-8888-7777-6666-555555555555"
	f, _ := Parse([]byte(`{
		"guid":"` + guid + `","kind":"document","title":"Doc",
		"sections":[{"blocks":[
			{"type":"entryRef","guid":"` + other + `"},
			{"type":"text","text":"x"},
			{"type":"entryRef","guid":"` + other + `"},
			{"type":"entryRef","guid":"not-a-guid"}
		]}]
	}`))
	refs := f.ReferencedEntries()
	if len(refs) != 1 || refs[0] != other {
		t.Fatalf("ReferencedEntries = %v, want [%s] (deduped, valid only)", refs, other)
	}
}

func TestFieldStrings(t *testing.T) {
	cases := []struct {
		in   any
		want int
	}{
		{"single", 1},
		{true, 1},
		{[]any{"a", "b", "c"}, 3},
		{[]any{"a", []any{"b", "c"}}, 3},
		{nil, 0},
	}
	for _, c := range cases {
		if got := FieldStrings(c.in); len(got) != c.want {
			t.Errorf("FieldStrings(%v) = %v, want %d values", c.in, got, c.want)
		}
	}
}

func contains(haystack, needle string) bool {
	return len(needle) == 0 || indexOf(haystack, needle) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
