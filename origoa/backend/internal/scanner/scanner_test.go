package scanner

import "testing"

const guid = "11111111-2222-3333-4444-555555555555"

func TestParseConfigDefaults(t *testing.T) {
	c, err := ParseConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.GUIDFiles[0] != ".origoa.json" || c.ConfigFolders[0] != ".origoa" || c.Indexers[0] != "foundation" {
		t.Fatalf("empty config did not fall back to defaults: %+v", c)
	}
	// Partial config keeps supplied values and fills the rest.
	c, err = ParseConfig([]byte(`{"guid_files":["x.json"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.GUIDFiles[0] != "x.json" || len(c.ConfigFolders) == 0 || len(c.Indexers) == 0 {
		t.Fatalf("partial config not merged with defaults: %+v", c)
	}
	if _, err := ParseConfig([]byte(`{not json`)); err == nil {
		t.Error("expected error for malformed config")
	}
}

func TestClassify(t *testing.T) {
	cfg := DefaultConfig()
	cases := []struct {
		path     string
		class    PathClass
		scope    string
		category string
		dir      string
		parent   string
	}{
		{"specs/" + guid + "/.origoa.json", ArtifactFile, "", "", "specs/" + guid, "specs"},
		{guid + "/.origoa.json", ArtifactFile, "", "", guid, ""},
		{"a/b/" + guid + "/attachment.png", AttachmentFile, "", "", "a/b/" + guid, "a/b"},
		{"specs/.origoa/schemas/req.json", ConfigObjectFile, "specs", "schemas", "", ""},
		{".origoa/workflows/review.json", ConfigObjectFile, "", "workflows", "", ""},
		{"team/.origoa/links/" + guid + ".json", ConfigObjectFile, "team", "links", "", ""},
		{"README.md", Irrelevant, "", "", "", ""},
		{"specs/notes.txt", Irrelevant, "", "", "", ""},
		{"", Irrelevant, "", "", "", ""},
		// Malformed paths are normalized and never yield an empty category.
		{".origoa//0", ConfigObjectFile, "", "misc", "", ""},
		{"a/../" + guid + "/.origoa.json", ArtifactFile, "", "", guid, ""},
	}
	for _, c := range cases {
		got := cfg.Classify(c.path)
		if got.Class != c.class {
			t.Errorf("Classify(%q).Class = %v, want %v", c.path, got.Class, c.class)
			continue
		}
		switch c.class {
		case ArtifactFile, AttachmentFile:
			if got.ArtifactDir != c.dir || got.ParentDir != c.parent {
				t.Errorf("Classify(%q) dir=%q parent=%q, want %q/%q", c.path, got.ArtifactDir, got.ParentDir, c.dir, c.parent)
			}
		case ConfigObjectFile:
			if got.ScopePath != c.scope || got.Category != c.category {
				t.Errorf("Classify(%q) scope=%q category=%q, want %q/%q", c.path, got.ScopePath, got.Category, c.scope, c.category)
			}
			if got.Category == "" {
				t.Errorf("Classify(%q) produced empty category", c.path)
			}
		}
	}
}
