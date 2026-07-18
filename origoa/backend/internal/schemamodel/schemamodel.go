// Package schemamodel implements the Origoa schema system: declarative,
// lexically inherited artifact type definitions.
//
// Schemas define structure rather than behavior. The effective schema of
// an artifact is composed from all schema definitions with the same
// artifact type encountered while traversing the repository hierarchy
// from the root towards the artifact location; the most specialized
// definition always takes precedence, and `"inheritance": "off"`
// terminates composition at that schema.
package schemamodel

import (
	"encoding/json"
	"fmt"
)

// Field types provided by the Foundation.
const (
	FieldHID        = "hid"
	FieldBoolean    = "boolean"
	FieldInteger    = "integer"
	FieldFloat      = "float"
	FieldCurrency   = "currency"
	FieldDate       = "date"
	FieldTime       = "time"
	FieldDateTime   = "datetime"
	FieldText       = "text"      // single-line
	FieldMultiText  = "multitext" // multi-line
	FieldRichText   = "richtext"
	FieldEnum       = "enum"
	FieldHyperlink  = "hyperlink"
	FieldArtifact   = "artifact"  // single artifact reference
	FieldArtifacts  = "artifacts" // multi artifact reference
	FieldAttachment = "attachment"
	FieldObject     = "object" // JSON object
)

// ValidFieldTypes enumerates the stable set of generic field types the
// Foundation defines. Workflow participation is assigned at the schema level
// (the Workflows list), not through a field type, so "workflow" is
// deliberately absent.
var ValidFieldTypes = map[string]bool{
	FieldHID: true, FieldBoolean: true, FieldInteger: true, FieldFloat: true,
	FieldCurrency: true, FieldDate: true, FieldTime: true, FieldDateTime: true,
	FieldText: true, FieldMultiText: true, FieldRichText: true, FieldEnum: true,
	FieldHyperlink: true, FieldArtifact: true, FieldArtifacts: true,
	FieldAttachment: true, FieldObject: true,
}

// FieldDef describes one schema-defined artifact field.
type FieldDef struct {
	ID           string         `json:"id"`
	DisplayName  string         `json:"displayName,omitempty"`
	Type         string         `json:"type"`
	Multiple     bool           `json:"multiple,omitempty"`
	Required     bool           `json:"required,omitempty"`
	Indexed      bool           `json:"indexed,omitempty"`
	Enum         string         `json:"enum,omitempty"`    // named enumeration reference
	Options      []string       `json:"options,omitempty"` // inline enumeration values
	Presentation map[string]any `json:"presentation,omitempty"`
}

// RelationshipDef constrains semantic links between artifact types.
type RelationshipDef struct {
	LinkType    string     `json:"linkType"`
	DisplayName string     `json:"displayName,omitempty"`
	SourceTypes []string   `json:"sourceTypes,omitempty"` // empty = any
	TargetTypes []string   `json:"targetTypes,omitempty"`
	Cardinality string     `json:"cardinality,omitempty"` // one-to-one | one-to-many | many-to-one | many-to-many
	Fields      []FieldDef `json:"fields,omitempty"`
}

// EnumDef is a named enumeration definition. The MVP supports static,
// user-defined value lists; enumeration values generated from repository
// artifacts or by extensions, and user-extendable enumerations, are part of
// the (non-MVP) extension model and are intentionally not advertised here.
type EnumDef struct {
	Values   []string `json:"values"`
	Multiple bool     `json:"multiple,omitempty"`
}

// SchemaFile is the persisted form of one schema definition inside a
// .origoa/schemas directory.
type SchemaFile struct {
	ArtifactType  string             `json:"artifactType"`
	Kind          string             `json:"kind,omitempty"` // entry | document | link | comment
	DisplayName   string             `json:"displayName,omitempty"`
	Inheritance   string             `json:"inheritance,omitempty"` // "off" terminates composition
	HIDPrefix     string             `json:"hidPrefix,omitempty"`
	Fields        []FieldDef         `json:"fields,omitempty"`
	Workflows     []string           `json:"workflows,omitempty"`
	Relationships []RelationshipDef  `json:"relationships,omitempty"`
	Enums         map[string]EnumDef `json:"enums,omitempty"`
	Presentation  map[string]any     `json:"presentation,omitempty"`
}

// EffectiveSchema is the composed artifact definition for one artifact
// type at one repository location.
type EffectiveSchema struct {
	ArtifactType  string             `json:"artifactType"`
	Kind          string             `json:"kind"`
	DisplayName   string             `json:"displayName"`
	HIDPrefix     string             `json:"hidPrefix,omitempty"`
	Fields        []FieldDef         `json:"fields"`
	Workflows     []string           `json:"workflows"`
	Relationships []RelationshipDef  `json:"relationships"`
	Enums         map[string]EnumDef `json:"enums,omitempty"`
	Presentation  map[string]any     `json:"presentation,omitempty"`
	// Sources lists the storage paths of every schema definition that
	// contributed to this effective schema (root first).
	Sources []string `json:"sources"`
}

// ParseSchemaFile decodes a schema definition.
func ParseSchemaFile(content []byte) (*SchemaFile, error) {
	var sf SchemaFile
	if err := json.Unmarshal(content, &sf); err != nil {
		return nil, fmt.Errorf("schema: %w", err)
	}
	if sf.ArtifactType == "" {
		return nil, fmt.Errorf("schema: missing artifactType")
	}
	if sf.Kind == "" {
		sf.Kind = "entry"
	}
	// Field types must belong to the Foundation's defined vocabulary. An
	// unknown type is a malformed schema; resolution skips such schemas
	// rather than composing a field the UI cannot render.
	if err := validateFieldTypes(sf.Fields); err != nil {
		return nil, err
	}
	for _, r := range sf.Relationships {
		if err := validateFieldTypes(r.Fields); err != nil {
			return nil, err
		}
	}
	return &sf, nil
}

func validateFieldTypes(fields []FieldDef) error {
	for _, f := range fields {
		if f.ID == "" {
			return fmt.Errorf("schema: field missing id")
		}
		if !ValidFieldTypes[f.Type] {
			return fmt.Errorf("schema: field %q has unknown type %q", f.ID, f.Type)
		}
	}
	return nil
}

// Contribution pairs a parsed schema with its storage path, ordered from
// the repository root towards the artifact location.
type Contribution struct {
	StoragePath string
	Schema      *SchemaFile
}

// Compose builds the effective schema from contributions ordered root →
// artifact location. Definitions closer to the artifact override
// inherited definitions; `"inheritance": "off"` discards everything
// accumulated above that schema.
func Compose(artifactType string, contributions []Contribution) *EffectiveSchema {
	start := 0
	for i, c := range contributions {
		if c.Schema.Inheritance == "off" {
			start = i
		}
	}
	contributions = contributions[start:]
	if len(contributions) == 0 {
		return nil
	}
	eff := &EffectiveSchema{
		ArtifactType: artifactType,
		Kind:         "entry",
		DisplayName:  artifactType,
		Enums:        map[string]EnumDef{},
	}
	for _, c := range contributions {
		s := c.Schema
		eff.Sources = append(eff.Sources, c.StoragePath)
		if s.Kind != "" {
			eff.Kind = s.Kind
		}
		if s.DisplayName != "" {
			eff.DisplayName = s.DisplayName
		}
		if s.HIDPrefix != "" {
			eff.HIDPrefix = s.HIDPrefix
		}
		for _, f := range s.Fields {
			eff.mergeField(f)
		}
		for _, w := range s.Workflows {
			if !contains(eff.Workflows, w) {
				eff.Workflows = append(eff.Workflows, w)
			}
		}
		for _, r := range s.Relationships {
			eff.mergeRelationship(r)
		}
		for name, e := range s.Enums {
			eff.Enums[name] = e // most specialized definition replaces
		}
		// Presentation metadata is replaced completely by a more specialized
		// definition, not merged key-by-key: the spec applies the single
		// "most specialized definition takes precedence" rule uniformly,
		// including presentation. A schema that defines presentation at all
		// supersedes any inherited presentation wholesale.
		if s.Presentation != nil {
			eff.Presentation = s.Presentation
		}
	}
	if eff.Fields == nil {
		eff.Fields = []FieldDef{}
	}
	if eff.Workflows == nil {
		eff.Workflows = []string{}
	}
	if eff.Relationships == nil {
		eff.Relationships = []RelationshipDef{}
	}
	return eff
}

// mergeField replaces an existing field definition with the same
// identifier in place, or appends a new one.
func (e *EffectiveSchema) mergeField(f FieldDef) {
	for i, cur := range e.Fields {
		if cur.ID == f.ID {
			e.Fields[i] = f
			return
		}
	}
	e.Fields = append(e.Fields, f)
}

func (e *EffectiveSchema) mergeRelationship(r RelationshipDef) {
	for i, cur := range e.Relationships {
		if cur.LinkType == r.LinkType {
			e.Relationships[i] = r
			return
		}
	}
	e.Relationships = append(e.Relationships, r)
}

// Field returns a field definition by identifier.
func (e *EffectiveSchema) Field(id string) *FieldDef {
	for i := range e.Fields {
		if e.Fields[i].ID == id {
			return &e.Fields[i]
		}
	}
	return nil
}

// IndexedFieldIDs lists the fields marked for database indexing.
func (e *EffectiveSchema) IndexedFieldIDs() []string {
	var out []string
	for _, f := range e.Fields {
		if f.Indexed {
			out = append(out, f.ID)
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// ---- Workflow definitions ----

// WorkflowState is one state of a workflow definition.
type WorkflowState struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName,omitempty"`
}

// WorkflowTransition connects two workflow states.
type WorkflowTransition struct {
	Name string `json:"name,omitempty"`
	From string `json:"from"`
	To   string `json:"to"`
}

// WorkflowDef is a workflow definition stored in .origoa/workflows.
// Workflow behavior is defined independently of artifact types; schemas
// merely reference workflows by name.
type WorkflowDef struct {
	Name        string               `json:"name"`
	DisplayName string               `json:"displayName,omitempty"`
	Initial     string               `json:"initial"`
	States      []WorkflowState      `json:"states"`
	Transitions []WorkflowTransition `json:"transitions"`
}

// ParseWorkflowDef decodes a workflow definition.
func ParseWorkflowDef(content []byte) (*WorkflowDef, error) {
	var wd WorkflowDef
	if err := json.Unmarshal(content, &wd); err != nil {
		return nil, fmt.Errorf("workflow: %w", err)
	}
	if wd.Name == "" {
		return nil, fmt.Errorf("workflow: missing name")
	}
	return &wd, nil
}

// HasState reports whether the workflow defines a state.
func (w *WorkflowDef) HasState(id string) bool {
	for _, s := range w.States {
		if s.ID == id {
			return true
		}
	}
	return false
}

// CanTransition reports whether from → to is an allowed transition.
func (w *WorkflowDef) CanTransition(from, to string) bool {
	for _, t := range w.Transitions {
		if t.From == from && t.To == to {
			return true
		}
	}
	return false
}

// TransitionsFrom lists the transitions available from a state. The
// result is never nil so it serializes as a JSON array.
func (w *WorkflowDef) TransitionsFrom(state string) []WorkflowTransition {
	out := []WorkflowTransition{}
	for _, t := range w.Transitions {
		if t.From == state {
			out = append(out, t)
		}
	}
	return out
}
