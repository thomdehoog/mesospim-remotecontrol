// Package ojson implements order-preserving JSON documents.
//
// JSON serialization forms part of the Origoa repository format and must
// remain stable: repeated loading and writing of an unchanged artifact
// produces a byte-identical file, and logically modified documents keep
// their existing property order, indentation style, line-ending style and
// trailing-newline behavior. New properties are appended without
// reordering existing content.
package ojson

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Value is one of: *Object, *Array, string, json.Number, bool, nil.
type Value any

// Object is a JSON object that preserves property order.
type Object struct {
	keys   []string
	values map[string]Value
	dirty  *bool
}

// Array is a JSON array.
type Array struct {
	items []Value
	dirty *bool
}

// Style captures the serialization conventions of a source document so a
// rewrite stays close to the original bytes.
type Style struct {
	Indent          string // one indentation level, e.g. "  " or "\t"
	Newline         string // "\n" or "\r\n"
	TrailingNewline bool
}

// DefaultStyle is used for documents created in memory.
var DefaultStyle = Style{Indent: "  ", Newline: "\n", TrailingNewline: true}

// Doc is a parsed JSON document plus its formatting conventions and the
// original raw bytes. While the document is logically unmodified, Bytes
// returns the original bytes verbatim.
type Doc struct {
	root  Value
	style Style
	raw   []byte
	dirty bool
}

// NewDoc creates an empty object document with default style.
func NewDoc() *Doc {
	d := &Doc{style: DefaultStyle, dirty: true}
	d.root = newObject(&d.dirty)
	return d
}

func newObject(dirty *bool) *Object {
	return &Object{values: map[string]Value{}, dirty: dirty}
}

func newArray(dirty *bool) *Array {
	return &Array{dirty: dirty}
}

// Parse reads a JSON document, preserving object property order and
// detecting the formatting style of the source.
func Parse(b []byte) (*Doc, error) {
	d := &Doc{raw: append([]byte(nil), b...), style: detectStyle(b)}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	v, err := parseValue(dec, &d.dirty)
	if err != nil {
		return nil, err
	}
	// Reject trailing garbage.
	if _, err := dec.Token(); err == nil {
		return nil, fmt.Errorf("ojson: trailing content after JSON value")
	}
	d.root = v
	return d, nil
}

func parseValue(dec *json.Decoder, dirty *bool) (Value, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	return parseFromToken(dec, tok, dirty)
}

func parseFromToken(dec *json.Decoder, tok json.Token, dirty *bool) (Value, error) {
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			obj := newObject(dirty)
			for dec.More() {
				keyTok, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyTok.(string)
				if !ok {
					return nil, fmt.Errorf("ojson: object key is not a string")
				}
				val, err := parseValue(dec, dirty)
				if err != nil {
					return nil, err
				}
				if _, exists := obj.values[key]; !exists {
					obj.keys = append(obj.keys, key)
				}
				obj.values[key] = val
			}
			if _, err := dec.Token(); err != nil { // consume '}'
				return nil, err
			}
			return obj, nil
		case '[':
			arr := newArray(dirty)
			for dec.More() {
				val, err := parseValue(dec, dirty)
				if err != nil {
					return nil, err
				}
				arr.items = append(arr.items, val)
			}
			if _, err := dec.Token(); err != nil { // consume ']'
				return nil, err
			}
			return arr, nil
		}
		return nil, fmt.Errorf("ojson: unexpected delimiter %v", t)
	default:
		return tok, nil // string, json.Number, bool, nil
	}
}

func detectStyle(b []byte) Style {
	s := DefaultStyle
	if bytes.Contains(b, []byte("\r\n")) {
		s.Newline = "\r\n"
	}
	s.TrailingNewline = len(b) > 0 && b[len(b)-1] == '\n'
	// First indented line determines the unit.
	for _, line := range bytes.Split(b, []byte("\n")) {
		trimmed := bytes.TrimLeft(line, " \t")
		if len(trimmed) == 0 || len(trimmed) == len(line) {
			continue
		}
		s.Indent = string(line[:len(line)-len(trimmed)])
		break
	}
	return s
}

// Root returns the top-level value of the document.
func (d *Doc) Root() Value { return d.root }

// RootObject returns the top-level object, or an error if the document
// root is not an object.
func (d *Doc) RootObject() (*Object, error) {
	obj, ok := d.root.(*Object)
	if !ok {
		return nil, fmt.Errorf("ojson: document root is not an object")
	}
	return obj, nil
}

// Modified reports whether the document was logically changed since parse.
func (d *Doc) Modified() bool { return d.dirty }

// Bytes serializes the document. An unmodified parsed document returns the
// original bytes verbatim, guaranteeing byte-stable round-trips.
func (d *Doc) Bytes() []byte {
	if !d.dirty && d.raw != nil {
		return append([]byte(nil), d.raw...)
	}
	var sb strings.Builder
	writeValue(&sb, d.root, d.style, 0)
	if d.style.TrailingNewline {
		sb.WriteString(d.style.Newline)
	}
	return []byte(sb.String())
}

func writeValue(sb *strings.Builder, v Value, st Style, depth int) {
	switch t := v.(type) {
	case *Object:
		if len(t.keys) == 0 {
			sb.WriteString("{}")
			return
		}
		sb.WriteString("{")
		for i, k := range t.keys {
			if i > 0 {
				sb.WriteString(",")
			}
			sb.WriteString(st.Newline)
			sb.WriteString(strings.Repeat(st.Indent, depth+1))
			kb, _ := json.Marshal(k)
			sb.Write(kb)
			sb.WriteString(": ")
			writeValue(sb, t.values[k], st, depth+1)
		}
		sb.WriteString(st.Newline)
		sb.WriteString(strings.Repeat(st.Indent, depth))
		sb.WriteString("}")
	case *Array:
		if len(t.items) == 0 {
			sb.WriteString("[]")
			return
		}
		sb.WriteString("[")
		for i, item := range t.items {
			if i > 0 {
				sb.WriteString(",")
			}
			sb.WriteString(st.Newline)
			sb.WriteString(strings.Repeat(st.Indent, depth+1))
			writeValue(sb, item, st, depth+1)
		}
		sb.WriteString(st.Newline)
		sb.WriteString(strings.Repeat(st.Indent, depth))
		sb.WriteString("]")
	case string:
		b, _ := json.Marshal(t)
		sb.Write(b)
	case json.Number:
		sb.WriteString(t.String())
	case bool:
		if t {
			sb.WriteString("true")
		} else {
			sb.WriteString("false")
		}
	case nil:
		sb.WriteString("null")
	default:
		// Fall back to encoding/json for foreign values set via SetAny.
		b, err := json.Marshal(t)
		if err != nil {
			sb.WriteString("null")
			return
		}
		sb.Write(b)
	}
}

// ---- Object accessors ----

// Len returns the number of properties.
func (o *Object) Len() int { return len(o.keys) }

// Keys returns the property names in document order.
func (o *Object) Keys() []string { return append([]string(nil), o.keys...) }

// Get returns the value of a property.
func (o *Object) Get(key string) (Value, bool) {
	v, ok := o.values[key]
	return v, ok
}

// GetString returns a string property, or "" if absent or not a string.
func (o *Object) GetString(key string) string {
	if v, ok := o.values[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// GetObject returns an object property, or nil.
func (o *Object) GetObject(key string) *Object {
	if v, ok := o.values[key]; ok {
		if obj, ok := v.(*Object); ok {
			return obj
		}
	}
	return nil
}

// GetArray returns an array property, or nil.
func (o *Object) GetArray(key string) *Array {
	if v, ok := o.values[key]; ok {
		if arr, ok := v.(*Array); ok {
			return arr
		}
	}
	return nil
}

func (o *Object) markDirty() {
	if o.dirty != nil {
		*o.dirty = true
	}
}

// Set assigns a property. Existing properties keep their position; new
// properties are appended at the end. Setting an equal scalar value does
// not mark the document dirty.
func (o *Object) Set(key string, v Value) {
	if cur, exists := o.values[key]; exists {
		if scalarEqual(cur, v) {
			return
		}
		o.values[key] = adopt(v, o.dirty)
		o.markDirty()
		return
	}
	o.keys = append(o.keys, key)
	o.values[key] = adopt(v, o.dirty)
	o.markDirty()
}

// SetAny converts any Go value (maps, slices, numbers, ...) to an ojson
// Value and assigns it.
func (o *Object) SetAny(key string, v any) {
	o.Set(key, FromAny(v, o.dirty))
}

// Delete removes a property. The order of the remaining properties is
// unaffected.
func (o *Object) Delete(key string) {
	if _, exists := o.values[key]; !exists {
		return
	}
	delete(o.values, key)
	for i, k := range o.keys {
		if k == key {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
	o.markDirty()
}

func scalarEqual(a, b Value) bool {
	switch at := a.(type) {
	case string:
		bt, ok := b.(string)
		return ok && at == bt
	case bool:
		bt, ok := b.(bool)
		return ok && at == bt
	case json.Number:
		bt, ok := b.(json.Number)
		return ok && at.String() == bt.String()
	case nil:
		return b == nil
	}
	return false
}

func adopt(v Value, dirty *bool) Value {
	switch t := v.(type) {
	case *Object:
		t.dirty = dirty
		for _, k := range t.keys {
			adopt(t.values[k], dirty)
		}
	case *Array:
		t.dirty = dirty
		for _, item := range t.items {
			adopt(item, dirty)
		}
	}
	return v
}

// ---- Array accessors ----

// Len returns the number of items.
func (a *Array) Len() int { return len(a.items) }

// At returns the item at index i.
func (a *Array) At(i int) Value { return a.items[i] }

// Items returns the underlying items in order.
func (a *Array) Items() []Value { return append([]Value(nil), a.items...) }

func (a *Array) markDirty() {
	if a.dirty != nil {
		*a.dirty = true
	}
}

// Append adds an item at the end.
func (a *Array) Append(v Value) {
	a.items = append(a.items, adopt(v, a.dirty))
	a.markDirty()
}

// SetAt replaces the item at index i.
func (a *Array) SetAt(i int, v Value) {
	a.items[i] = adopt(v, a.dirty)
	a.markDirty()
}

// RemoveAt removes the item at index i.
func (a *Array) RemoveAt(i int) {
	a.items = append(a.items[:i], a.items[i+1:]...)
	a.markDirty()
}

// Clear removes all items.
func (a *Array) Clear() {
	if len(a.items) == 0 {
		return
	}
	a.items = nil
	a.markDirty()
}

// ---- Conversion ----

// FromAny converts a generic Go value into an ojson Value. Map keys are
// emitted in sorted order to keep newly created content deterministic.
func FromAny(v any, dirty *bool) Value {
	switch t := v.(type) {
	case nil:
		return nil
	case string, bool, json.Number:
		return t
	case *Object, *Array:
		return adopt(t, dirty)
	case float64:
		return json.Number(formatFloat(t))
	case float32:
		return json.Number(formatFloat(float64(t)))
	case int:
		return json.Number(fmt.Sprintf("%d", t))
	case int64:
		return json.Number(fmt.Sprintf("%d", t))
	case map[string]any:
		obj := newObject(dirty)
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			obj.keys = append(obj.keys, k)
			obj.values[k] = FromAny(t[k], dirty)
		}
		return obj
	case []any:
		arr := newArray(dirty)
		for _, item := range t {
			arr.items = append(arr.items, FromAny(item, dirty))
		}
		return arr
	default:
		// Round-trip through encoding/json for anything else.
		b, err := json.Marshal(t)
		if err != nil {
			return nil
		}
		d, err := Parse(b)
		if err != nil {
			return nil
		}
		return adopt(d.root, dirty)
	}
}

func formatFloat(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}

// ToAny converts an ojson Value into generic Go values (map[string]any,
// []any, string, json.Number, bool, nil). Property order is lost; use
// MarshalJSON for order-preserving output.
func ToAny(v Value) any {
	switch t := v.(type) {
	case *Object:
		m := make(map[string]any, len(t.keys))
		for _, k := range t.keys {
			m[k] = ToAny(t.values[k])
		}
		return m
	case *Array:
		s := make([]any, len(t.items))
		for i, item := range t.items {
			s[i] = ToAny(item)
		}
		return s
	default:
		return t
	}
}

// MarshalJSON emits the object with its property order preserved.
func (o *Object) MarshalJSON() ([]byte, error) {
	var sb strings.Builder
	writeCompact(&sb, o)
	return []byte(sb.String()), nil
}

// MarshalJSON emits the array.
func (a *Array) MarshalJSON() ([]byte, error) {
	var sb strings.Builder
	writeCompact(&sb, a)
	return []byte(sb.String()), nil
}

func writeCompact(sb *strings.Builder, v Value) {
	switch t := v.(type) {
	case *Object:
		sb.WriteString("{")
		for i, k := range t.keys {
			if i > 0 {
				sb.WriteString(",")
			}
			kb, _ := json.Marshal(k)
			sb.Write(kb)
			sb.WriteString(":")
			writeCompact(sb, t.values[k])
		}
		sb.WriteString("}")
	case *Array:
		sb.WriteString("[")
		for i, item := range t.items {
			if i > 0 {
				sb.WriteString(",")
			}
			writeCompact(sb, item)
		}
		sb.WriteString("]")
	default:
		b, err := json.Marshal(t)
		if err != nil {
			sb.WriteString("null")
			return
		}
		sb.Write(b)
	}
}

// Clone returns a deep copy of the value attached to no document.
func Clone(v Value) Value {
	switch t := v.(type) {
	case *Object:
		obj := newObject(nil)
		for _, k := range t.keys {
			obj.keys = append(obj.keys, k)
			obj.values[k] = Clone(t.values[k])
		}
		return obj
	case *Array:
		arr := newArray(nil)
		for _, item := range t.items {
			arr.items = append(arr.items, Clone(item))
		}
		return arr
	default:
		return t
	}
}
