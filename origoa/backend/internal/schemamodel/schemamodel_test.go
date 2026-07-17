package schemamodel

import "testing"

func mustParse(t *testing.T, s string) *SchemaFile {
	t.Helper()
	sf, err := ParseSchemaFile([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return sf
}

func TestComposeNearestWins(t *testing.T) {
	root := mustParse(t, `{
		"artifactType": "requirement",
		"displayName": "Requirement",
		"hidPrefix": "REQ",
		"fields": [
			{"id": "priority", "type": "enum", "options": ["low", "high"], "indexed": true},
			{"id": "text", "type": "richtext"}
		],
		"workflows": ["review"]
	}`)
	local := mustParse(t, `{
		"artifactType": "requirement",
		"fields": [
			{"id": "priority", "type": "enum", "options": ["p1", "p2", "p3"], "indexed": true},
			{"id": "safety", "type": "boolean"}
		],
		"workflows": ["release"]
	}`)
	eff := Compose("requirement", []Contribution{
		{StoragePath: ".origoa/schemas/req.json", Schema: root},
		{StoragePath: "sub/.origoa/schemas/req.json", Schema: local},
	})
	if eff.DisplayName != "Requirement" || eff.HIDPrefix != "REQ" {
		t.Fatalf("inherited metadata lost: %+v", eff)
	}
	// priority replaced in place, order preserved, safety appended.
	if len(eff.Fields) != 3 || eff.Fields[0].ID != "priority" || eff.Fields[1].ID != "text" || eff.Fields[2].ID != "safety" {
		t.Fatalf("bad field composition: %+v", eff.Fields)
	}
	if len(eff.Fields[0].Options) != 3 || eff.Fields[0].Options[0] != "p1" {
		t.Fatalf("nearest definition did not win: %+v", eff.Fields[0])
	}
	if len(eff.Workflows) != 2 {
		t.Fatalf("workflows not accumulated: %v", eff.Workflows)
	}
	if len(eff.Sources) != 2 {
		t.Fatalf("sources missing: %v", eff.Sources)
	}
}

func TestInheritanceOff(t *testing.T) {
	root := mustParse(t, `{"artifactType": "x", "displayName": "Root X", "fields": [{"id": "a", "type": "text"}]}`)
	isolated := mustParse(t, `{"artifactType": "x", "inheritance": "off", "fields": [{"id": "b", "type": "text"}]}`)
	eff := Compose("x", []Contribution{
		{StoragePath: "root", Schema: root},
		{StoragePath: "sub", Schema: isolated},
	})
	if len(eff.Fields) != 1 || eff.Fields[0].ID != "b" {
		t.Fatalf("inheritance: off did not terminate composition: %+v", eff.Fields)
	}
	if eff.DisplayName != "x" {
		t.Fatalf("inherited display name survived inheritance off: %q", eff.DisplayName)
	}
}

func TestWorkflowDef(t *testing.T) {
	wd, err := ParseWorkflowDef([]byte(`{
		"name": "review", "initial": "draft",
		"states": [{"id": "draft"}, {"id": "in_review"}, {"id": "approved"}],
		"transitions": [
			{"from": "draft", "to": "in_review", "name": "Submit"},
			{"from": "in_review", "to": "approved", "name": "Approve"},
			{"from": "in_review", "to": "draft", "name": "Reject"}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if !wd.CanTransition("draft", "in_review") || wd.CanTransition("draft", "approved") {
		t.Fatal("transition validation wrong")
	}
	if got := wd.TransitionsFrom("in_review"); len(got) != 2 {
		t.Fatalf("TransitionsFrom: %v", got)
	}
}
