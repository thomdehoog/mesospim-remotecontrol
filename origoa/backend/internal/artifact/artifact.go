// Package artifact defines the persisted JSON form of the four native
// repository artifacts (entries, documents, links, comments) and helpers
// to read them tolerantly.
package artifact

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Kinds of native repository artifacts.
const (
	KindEntry    = "entry"
	KindDocument = "document"
	KindLink     = "link"
	KindComment  = "comment"
)

// GUIDPattern matches the folder / identity format used for artifact
// GUIDs (lower-case UUID).
var GUIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// IsGUID reports whether s is a valid artifact GUID.
func IsGUID(s string) bool { return GUIDPattern.MatchString(s) }

// Block is one content block of a document section.
type Block struct {
	Type       string `json:"type"` // text | image | entryRef
	Text       string `json:"text,omitempty"`
	Attachment string `json:"attachment,omitempty"`
	GUID       string `json:"guid,omitempty"` // entryRef target
}

// Section is one hierarchical document section.
type Section struct {
	ID       string    `json:"id"`
	Heading  string    `json:"heading,omitempty"`
	Blocks   []Block   `json:"blocks,omitempty"`
	Children []Section `json:"children,omitempty"`
}

// File is the parsed form of an artifact JSON file. Unknown properties
// are preserved only in the raw bytes; File carries the fields the
// Foundation itself interprets.
type File struct {
	GUID  string `json:"guid"`
	Kind  string `json:"kind"`
	Type  string `json:"type,omitempty"`
	Title string `json:"title,omitempty"`
	HID   string `json:"hid,omitempty"`

	// Entries
	Base   string         `json:"base,omitempty"` // overlay base entry
	Fields map[string]any `json:"fields,omitempty"`

	// Workflow participation: workflow name → current state.
	Workflows map[string]string `json:"workflows,omitempty"`

	// Documents
	Sections []Section `json:"sections,omitempty"`

	// Links
	Source     string         `json:"source,omitempty"`
	Target     string         `json:"target,omitempty"`
	LinkFields map[string]any `json:"linkFields,omitempty"`

	// Comments
	Subject   string          `json:"subject,omitempty"`
	Parent    string          `json:"parent,omitempty"`
	Author    string          `json:"author,omitempty"`
	Text      string          `json:"text,omitempty"`
	Anchor    json.RawMessage `json:"anchor,omitempty"`
	CreatedAt string          `json:"createdAt,omitempty"`
}

// Parse decodes an artifact file.
func Parse(b []byte) (*File, error) {
	var f File
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("artifact: %w", err)
	}
	if !IsGUID(f.GUID) {
		return nil, fmt.Errorf("artifact: missing or invalid guid")
	}
	switch f.Kind {
	case KindEntry, KindDocument, KindLink, KindComment:
	case "":
		f.Kind = KindEntry
	default:
		return nil, fmt.Errorf("artifact: unknown kind %q", f.Kind)
	}
	return &f, nil
}

// SearchText collects the human-readable text of the artifact for
// full-text indexing.
func (f *File) SearchText() string {
	var sb strings.Builder
	add := func(s string) {
		if s != "" {
			sb.WriteString(s)
			sb.WriteString(" ")
		}
	}
	add(f.Title)
	add(f.HID)
	add(f.Type)
	add(f.Text)
	for _, v := range f.Fields {
		addFieldValue(&sb, v)
	}
	var walk func(secs []Section)
	walk = func(secs []Section) {
		for _, s := range secs {
			add(s.Heading)
			for _, b := range s.Blocks {
				add(stripTags(b.Text))
			}
			walk(s.Children)
		}
	}
	walk(f.Sections)
	return strings.TrimSpace(sb.String())
}

func addFieldValue(sb *strings.Builder, v any) {
	switch t := v.(type) {
	case string:
		if t != "" {
			sb.WriteString(stripTags(t))
			sb.WriteString(" ")
		}
	case []any:
		for _, item := range t {
			addFieldValue(sb, item)
		}
	}
}

var tagPattern = regexp.MustCompile(`<[^>]*>`)

func stripTags(s string) string {
	if !strings.Contains(s, "<") {
		return s
	}
	return tagPattern.ReplaceAllString(s, " ")
}

// FieldStrings converts a field value into its indexable string values.
func FieldStrings(v any) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case bool:
		return []string{fmt.Sprintf("%v", t)}
	case float64:
		return []string{trimFloat(t)}
	case json.Number:
		return []string{t.String()}
	case []any:
		var out []string
		for _, item := range t {
			out = append(out, FieldStrings(item)...)
		}
		return out
	}
	return nil
}

func trimFloat(f float64) string {
	s := fmt.Sprintf("%v", f)
	return s
}

// ReferencedEntries lists the entry GUIDs referenced from document
// sections (entryRef blocks).
func (f *File) ReferencedEntries() []string {
	var out []string
	seen := map[string]bool{}
	var walk func(secs []Section)
	walk = func(secs []Section) {
		for _, s := range secs {
			for _, b := range s.Blocks {
				if b.Type == "entryRef" && IsGUID(b.GUID) && !seen[b.GUID] {
					seen[b.GUID] = true
					out = append(out, b.GUID)
				}
			}
			walk(s.Children)
		}
	}
	walk(f.Sections)
	return out
}
