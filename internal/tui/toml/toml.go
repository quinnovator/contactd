// Package toml reads and writes the part of TOML that the terminal client's
// own files use: chat.toml, statusline.toml, mod.toml and mods.toml.
//
// Accepted: comments; bare keys; basic strings with the escapes \" \\ \n \t
// and \uXXXX; literal strings; decimal integers; booleans; arrays of
// strings, which may span lines and end with a comma; and the table headers
// [a], [a.b] and [[a]]. The rest of TOML is refused with a line number:
// floats, dates, inline tables, multi-line strings, dotted keys and quoted
// keys. What is accepted means what TOML says it means, so any TOML parser
// reads an accepted file the same way. A table name has at most 32 parts.
//
// A document is a flat list of tables in file order. A key this package does
// not know is not an error: the caller knows which keys it wants, and
// reports the rest.
package toml

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// MaxSize is the largest source Parse reads, in bytes.
const MaxSize = 1 << 20

// Error is a refusal: a file that could not be read, or a value of the wrong
// type.
type Error struct {
	// Line counts from 1. It is 0 when no line is to blame: the file is too
	// large, or the value was set by the program.
	Line int
	// Msg says what is wrong, without the line.
	Msg string
}

// Error returns the message, with the line in front when there is one.
func (e *Error) Error() string {
	if e.Line <= 0 {
		return e.Msg
	}
	return "line " + strconv.Itoa(e.Line) + ": " + e.Msg
}

// Doc is a parsed file, or one being built. Tables[0] is the root, which
// holds the keys above the first header; the rest follow in file order.
type Doc struct{ Tables []Table }

// Table is one table of a document: the root, a [name] or one [[name]].
//
// A Table shares its keys with its copies, so a pointer from Add or Named
// stays good for Set after the document has grown.
type Table struct {
	// Name is "" for the root, and otherwise the header as written, without
	// spaces: "segment", "mod.cache".
	Name string
	// Array says the table was declared with [[name]].
	Array bool
	// Line is where the header is. It is 0 for the root and for a table made
	// with Add.
	Line int

	e *entries
}

// entries are a table's keys in file order, and their values.
type entries struct {
	keys []string
	vals map[string]value
}

type kind uint8

const (
	kindString kind = iota + 1
	kindInt
	kindBool
	kindStrings
)

// value is one key's value. Only the field its kind names is set.
type value struct {
	kind kind
	s    string
	i    int64
	b    bool
	list []string
	// line is where the key is, or 0 for a value the program set.
	line int
}

func (k kind) String() string {
	switch k {
	case kindString:
		return "a string"
	case kindInt:
		return "an integer"
	case kindBool:
		return "true or false"
	case kindStrings:
		return "an array of strings"
	default:
		return "nothing"
	}
}

// NewDoc returns an empty document: a root table with no keys.
func NewDoc() *Doc {
	return &Doc{Tables: []Table{{e: &entries{}}}}
}

// Root returns the table of the keys above the first header.
func (d *Doc) Root() *Table {
	if len(d.Tables) == 0 {
		d.Tables = append(d.Tables, Table{e: &entries{}})
	}
	return &d.Tables[0]
}

// Named returns every table with this name, in file order: one for a [name],
// one for each [[name]], none when the file has no such header.
func (d *Doc) Named(name string) []*Table {
	var out []*Table
	for i := range d.Tables {
		if d.Tables[i].Name == name && (i > 0) == (name != "") {
			out = append(out, &d.Tables[i])
		}
	}
	return out
}

// Add appends a table and returns it. array makes it one [[name]] of
// several. Add panics when name is not bare keys joined by dots, or has more
// parts than Parse reads, since such a header could not be read back.
func (d *Doc) Add(name string, array bool) *Table {
	if strings.Count(name, ".") >= maxParts {
		panic("toml: Add: the table name has more than " + strconv.Itoa(maxParts) + " parts")
	}
	for part := range strings.SplitSeq(name, ".") {
		if !isBareKey(part) {
			panic("toml: Add: " + strconv.Quote(name) + " is not a table name")
		}
	}
	d.Root()
	d.Tables = append(d.Tables, Table{Name: name, Array: array, e: &entries{}})
	return &d.Tables[len(d.Tables)-1]
}

// Keys returns the table's keys in file order.
func (t *Table) Keys() []string {
	if t.e == nil {
		return nil
	}
	return slices.Clone(t.e.keys)
}

// String returns a key's string value. ok is false when the key is absent.
// err is an *Error naming the key's line when its value is not a string.
func (t *Table) String(key string) (v string, ok bool, err error) {
	val, ok := t.lookup(key)
	if !ok {
		return "", false, nil
	}
	if val.kind != kindString {
		return "", true, mismatch(key, val, kindString)
	}
	return val.s, true, nil
}

// Int returns a key's integer value. ok is false when the key is absent.
// err is an *Error naming the key's line when its value is not an integer.
func (t *Table) Int(key string) (v int64, ok bool, err error) {
	val, ok := t.lookup(key)
	if !ok {
		return 0, false, nil
	}
	if val.kind != kindInt {
		return 0, true, mismatch(key, val, kindInt)
	}
	return val.i, true, nil
}

// Bool returns a key's boolean value. ok is false when the key is absent.
// err is an *Error naming the key's line when its value is not a boolean.
func (t *Table) Bool(key string) (v, ok bool, err error) {
	val, ok := t.lookup(key)
	if !ok {
		return false, false, nil
	}
	if val.kind != kindBool {
		return false, true, mismatch(key, val, kindBool)
	}
	return val.b, true, nil
}

// Strings returns a key's array of strings; the slice is the caller's. ok is
// false when the key is absent. err is an *Error naming the key's line when
// its value is not an array.
func (t *Table) Strings(key string) (v []string, ok bool, err error) {
	val, ok := t.lookup(key)
	if !ok {
		return nil, false, nil
	}
	if val.kind != kindStrings {
		return nil, true, mismatch(key, val, kindStrings)
	}
	return slices.Clone(val.list), true, nil
}

// KeyLine returns the line a key was read from, for a caller that refuses the
// value itself. It is 0 when the key is absent or was set by the program.
func (t *Table) KeyLine(key string) int {
	val, _ := t.lookup(key)
	return val.line
}

// Set gives a key a value: a string, an int64 (or an int), a bool or a
// []string. A key already present keeps its place. Bytes that are not UTF-8
// become U+FFFD, since a file must be UTF-8.
//
// Set panics when key is not a bare key or v is of another type: both are
// mistakes in the program, and neither could be written to a file.
func (t *Table) Set(key string, v any) {
	if !isBareKey(key) {
		panic("toml: Set: " + strconv.Quote(key) + " is not a bare key")
	}
	var val value
	switch x := v.(type) {
	case string:
		val = value{kind: kindString, s: valid(x)}
	case int64:
		val = value{kind: kindInt, i: x}
	case int:
		val = value{kind: kindInt, i: int64(x)}
	case bool:
		val = value{kind: kindBool, b: x}
	case []string:
		list := make([]string, len(x))
		for i, s := range x {
			list[i] = valid(s)
		}
		val = value{kind: kindStrings, list: list}
	default:
		panic(fmt.Sprintf("toml: Set: %s cannot hold a %T", key, v))
	}
	if t.e == nil {
		t.e = &entries{}
	}
	t.e.put(key, val)
}

func (t *Table) lookup(key string) (value, bool) {
	if t.e == nil {
		return value{}, false
	}
	val, ok := t.e.vals[key]
	return val, ok
}

func (e *entries) put(key string, val value) {
	if e.vals == nil {
		e.vals = make(map[string]value)
	}
	if _, ok := e.vals[key]; !ok {
		e.keys = append(e.keys, key)
	}
	e.vals[key] = val
}

func mismatch(key string, got value, want kind) *Error {
	return &Error{Line: got.line, Msg: key + " must be " + want.String() + ", not " + got.kind.String()}
}

func valid(s string) string { return strings.ToValidUTF8(s, "\uFFFD") }

// isBareKey reports whether s can be written as a key without quotes.
func isBareKey(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		if !isBareByte(s[i]) {
			return false
		}
	}
	return true
}

func isBareByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || isDigit(c) || c == '_' || c == '-'
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// isControl reports whether c may not appear as itself in a string or a
// comment. A tab may.
func isControl(c byte) bool { return c < 0x20 && c != '\t' || c == 0x7F }
