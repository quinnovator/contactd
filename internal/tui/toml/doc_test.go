package toml_test

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/quinnovator/contactd/internal/tui/toml"
)

// The four files the client reads, as the design writes them.
const (
	statusLineFile = `[[segment]]
name = "cache"
from = "core"          # core | mod:<mod> | shell
on = true
side = "left"          # left | right
format = "cache $pct%"
short = "$pct%"
warn_below = 50
drop = 4               # 1 is dropped first when narrow; 0 is never dropped

[[segment]]
name = "branch"
from = "mod:git"
side = "right"

[[segment]]
name = "load"
from = "shell"
on = false
side = "right"
command = "uptime"
match = 'load averages?: ([0-9.]+)'   # optional RE2; $1..$9 in format
format = "load $1"                     # $out is the first line of output
interval = "10s"                       # at least 1s
timeout = "1s"                         # at most the interval
`
	modFile = `abi = 1
name = "cache"                  # ^[a-z][a-z0-9-]{0,31}$, equal to the directory name
version = "0.3.1"
description = "How much of each prompt the provider served from its cache."
module = "cache.wasm"           # a bare file name; at most 16 MiB
sha256 = ""                     # optional pin
read = ["model_calls"]          # closed list; anything else is a load error
budget = "5ms"                  # may only lower the host's

[[segment]]
name = "cache"
refresh = "model_call"          # model_call | turn | state | a duration of at least 1s

[[command]]
name = "stats"
usage = "[days]"
summary = "token use and cache share by day"

[[renderer]]
kind = "code:mermaid"           # code:<language> | tool:<tool name>; requires "messages" in read
`
	modsFile = `[mod.cache]
on = true
source = "/home/ada/contactd/dist/mods/cache"   # where it was installed from
sha256 = "..."      # of the module when it was switched on; a file that no longer matches is off until switched on again
disabled = ""       # "over budget 3 times in a row, 2026-10-12 12:04"
`
	chatFile = `color  = "auto"    # auto | truecolor | 256 | 16 | none
glyphs = "auto"    # auto | unicode | ascii
theme  = "auto"    # auto | dark | light
images = "auto"    # auto | pixels | blocks | text
probe  = true
mods   = true

[glyph]            # overrides for the Unicode set, by the names in section 3
tool  = "●"
elbow = "└"
`
)

func parse(t *testing.T, src string) *toml.Doc {
	t.Helper()
	doc, err := toml.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return doc
}

func TestTheClientsFiles(t *testing.T) {
	t.Parallel()
	seg := func(entries ...entry) table { return table{Name: "segment", Array: true, Entries: entries} }
	cases := []struct {
		name string
		src  string
		want []table
	}{
		{"statusline.toml", statusLineFile, []table{
			root(),
			seg(
				entry{"name", "cache"}, entry{"from", "core"}, entry{"on", true}, entry{"side", "left"},
				entry{"format", "cache $pct%"}, entry{"short", "$pct%"}, entry{"warn_below", int64(50)}, entry{"drop", int64(4)},
			),
			seg(entry{"name", "branch"}, entry{"from", "mod:git"}, entry{"side", "right"}),
			seg(
				entry{"name", "load"}, entry{"from", "shell"}, entry{"on", false}, entry{"side", "right"},
				entry{"command", "uptime"}, entry{"match", "load averages?: ([0-9.]+)"}, entry{"format", "load $1"},
				entry{"interval", "10s"}, entry{"timeout", "1s"},
			),
		}},
		{"mod.toml", modFile, []table{
			root(
				entry{"abi", int64(1)}, entry{"name", "cache"}, entry{"version", "0.3.1"},
				entry{"description", "How much of each prompt the provider served from its cache."},
				entry{"module", "cache.wasm"}, entry{"sha256", ""}, entry{"read", []string{"model_calls"}}, entry{"budget", "5ms"},
			),
			seg(entry{"name", "cache"}, entry{"refresh", "model_call"}),
			{Name: "command", Array: true, Entries: []entry{
				{"name", "stats"}, {"usage", "[days]"}, {"summary", "token use and cache share by day"},
			}},
			{Name: "renderer", Array: true, Entries: []entry{{"kind", "code:mermaid"}}},
		}},
		{"mods.toml", modsFile, []table{
			root(),
			{Name: "mod.cache", Entries: []entry{
				{"on", true}, {"source", "/home/ada/contactd/dist/mods/cache"}, {"sha256", "..."}, {"disabled", ""},
			}},
		}},
		{"chat.toml", chatFile, []table{
			root(
				entry{"color", "auto"}, entry{"glyphs", "auto"}, entry{"theme", "auto"}, entry{"images", "auto"},
				entry{"probe", true}, entry{"mods", true},
			),
			{Name: "glyph", Entries: []entry{{"tool", "●"}, {"elbow", "└"}}},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			doc := parse(t, tc.src)
			if got := snapshot(t, doc); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got  %#v\nwant %#v", got, tc.want)
			}
			// Written out and read again, it is the same document.
			if got := snapshot(t, parse(t, string(doc.Encode()))); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("after Encode:\ngot  %#v\nwant %#v", got, tc.want)
			}
		})
	}
}

func TestLines(t *testing.T) {
	t.Parallel()
	doc := parse(t, "# a comment\na = 1\n\n[one]\nb = [\n  'x',\n]\nc = 2\n[[two]]\n\n[[two]]\n")
	lines := make([]int, 0, len(doc.Tables))
	for i := range doc.Tables {
		lines = append(lines, doc.Tables[i].Line)
	}
	if want := []int{0, 4, 9, 11}; !reflect.DeepEqual(lines, want) {
		t.Errorf("table lines %v, want %v", lines, want)
	}
	// A value of the wrong type is reported on its key's line, and a key
	// after an array that spans lines still counts right.
	one := doc.Named("one")[0]
	for key, line := range map[string]int{"b": 5, "c": 8} {
		_, _, err := one.Bool(key)
		if te, ok := errors.AsType[*toml.Error](err); !ok || te.Line != line {
			t.Errorf("Bool(%q) error %v, want an *Error on line %d", key, err, line)
		}
	}
	if _, _, err := doc.Root().String("a"); err == nil || err.Error() != "line 2: a must be a string, not an integer" {
		t.Errorf("String(a) error %v", err)
	}

	// KeyLine gives the same line to a caller that refuses the value itself.
	two := doc.Named("two")[1]
	two.Set("made", "by the program")
	lines = lines[:0]
	for _, at := range []struct {
		t   *toml.Table
		key string
	}{{doc.Root(), "a"}, {one, "b"}, {one, "c"}, {one, "none"}, {doc.Root(), "b"}, {two, "made"}, {&toml.Table{}, "a"}} {
		lines = append(lines, at.t.KeyLine(at.key))
	}
	if want := []int{2, 5, 8, 0, 0, 0, 0}; !reflect.DeepEqual(lines, want) {
		t.Errorf("key lines %v, want %v", lines, want)
	}
	// A key set again by the program no longer has a line.
	one.Set("c", 3)
	if got := one.KeyLine("c"); got != 0 {
		t.Errorf("KeyLine(c) = %d after Set, want 0", got)
	}
}

func TestGetters(t *testing.T) {
	t.Parallel()
	tb := parse(t, "s = \"x\"\ni = 7\nb = true\nl = ['p', 'q']\n").Root()
	type result struct {
		val any
		ok  bool
		err string
	}
	get := func(kind, key string) result {
		var (
			val any
			ok  bool
			err error
		)
		switch kind {
		case "String":
			val, ok, err = tb.String(key)
		case "Int":
			val, ok, err = tb.Int(key)
		case "Bool":
			val, ok, err = tb.Bool(key)
		default:
			val, ok, err = tb.Strings(key)
		}
		if err != nil {
			if _, isError := errors.AsType[*toml.Error](err); !isError {
				t.Errorf("%s(%q) error is %T, want *toml.Error", kind, key, err)
			}
			return result{val, ok, err.Error()}
		}
		return result{val, ok, ""}
	}
	cases := []struct {
		kind, key string
		want      result
	}{
		{"String", "s", result{"x", true, ""}},
		{"Int", "i", result{int64(7), true, ""}},
		{"Bool", "b", result{true, true, ""}},
		{"Strings", "l", result{[]string{"p", "q"}, true, ""}},

		{"String", "none", result{"", false, ""}},
		{"Int", "none", result{int64(0), false, ""}},
		{"Bool", "none", result{false, false, ""}},
		{"Strings", "none", result{[]string(nil), false, ""}},

		{"String", "i", result{"", true, "line 2: i must be a string, not an integer"}},
		{"String", "b", result{"", true, "line 3: b must be a string, not true or false"}},
		{"String", "l", result{"", true, "line 4: l must be a string, not an array of strings"}},
		{"Int", "s", result{int64(0), true, "line 1: s must be an integer, not a string"}},
		{"Int", "b", result{int64(0), true, "line 3: b must be an integer, not true or false"}},
		{"Bool", "s", result{false, true, "line 1: s must be true or false, not a string"}},
		{"Bool", "i", result{false, true, "line 2: i must be true or false, not an integer"}},
		{"Strings", "s", result{[]string(nil), true, "line 1: s must be an array of strings, not a string"}},
		{"Strings", "i", result{[]string(nil), true, "line 2: i must be an array of strings, not an integer"}},
	}
	for _, tc := range cases {
		if got := get(tc.kind, tc.key); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s(%q) = %#v, want %#v", tc.kind, tc.key, got, tc.want)
		}
	}

	// What a getter hands out is the caller's to change.
	list, _, _ := tb.Strings("l")
	list[0] = "changed"
	keys := tb.Keys()
	keys[0] = "changed"
	if again, _, _ := tb.Strings("l"); again[0] != "p" {
		t.Errorf("changing a returned array changed the table: %v", again)
	}
	if again := tb.Keys(); !reflect.DeepEqual(again, []string{"s", "i", "b", "l"}) {
		t.Errorf("Keys() = %v after changing a returned slice", again)
	}
}

func TestError(t *testing.T) {
	t.Parallel()
	cases := []struct {
		err  toml.Error
		want string
	}{
		{toml.Error{Line: 12, Msg: "floats are not supported"}, "line 12: floats are not supported"},
		{toml.Error{Line: 1, Msg: "x"}, "line 1: x"},
		{toml.Error{Msg: "the file is larger than 1 MiB"}, "the file is larger than 1 MiB"},
	}
	for _, tc := range cases {
		if got := tc.err.Error(); got != tc.want {
			t.Errorf("Error() = %q, want %q", got, tc.want)
		}
	}
}

func TestMaxSize(t *testing.T) {
	t.Parallel()
	head := "a = \""
	fits := head + strings.Repeat("x", toml.MaxSize-len(head)-1) + "\""
	if len(fits) != toml.MaxSize {
		t.Fatalf("test input is %d bytes, want %d", len(fits), toml.MaxSize)
	}
	doc, err := toml.Parse([]byte(fits))
	if err != nil {
		t.Fatalf("Parse refused a file of exactly MaxSize: %v", err)
	}
	if v, _, _ := doc.Root().String("a"); len(v) != toml.MaxSize-len(head)-1 {
		t.Errorf("the value is %d bytes long", len(v))
	}

	_, err = toml.Parse([]byte(fits + "\n"))
	if te, ok := errors.AsType[*toml.Error](err); !ok || te.Line != 0 || te.Msg != "the file is larger than 1 MiB" {
		t.Errorf("Parse of MaxSize+1 bytes: %v", err)
	}
}

func TestBuild(t *testing.T) {
	t.Parallel()
	doc := toml.NewDoc()
	if len(doc.Tables) != 1 || doc.Tables[0].Name != "" || doc.Root() != &doc.Tables[0] {
		t.Fatalf("NewDoc() = %#v, want one root table", doc.Tables)
	}
	if got := doc.Encode(); len(got) != 0 {
		t.Errorf("an empty document encodes as %q", got)
	}

	// Tables are added first and filled afterwards, through pointers taken
	// before the document grew: every Set must still land.
	first := doc.Add("segment", true)
	tables := make([]*toml.Table, 0, 65)
	tables = append(tables, first)
	for range 64 {
		tables = append(tables, doc.Add("segment", true))
	}
	state := doc.Add("mod.cache", false)
	rootTable := doc.Root()
	for i, tb := range tables {
		tb.Set("drop", int64(i))
	}
	first.Set("name", "cache")
	state.Set("on", true)
	rootTable.Set("abi", 1)
	rootTable.Set("read", []string{"model_calls", "clock"})

	if got := doc.Named("segment"); len(got) != 65 {
		t.Fatalf("Named(segment) has %d tables, want 65", len(got))
	}
	for i, tb := range doc.Named("segment") {
		if !tb.Array || tb.Line != 0 {
			t.Errorf("segment %d: Array %v, Line %d", i, tb.Array, tb.Line)
		}
		if v, ok, err := tb.Int("drop"); v != int64(i) || !ok || err != nil {
			t.Errorf("segment %d: drop = %d, %v, %v", i, v, ok, err)
		}
	}
	if v, ok, err := doc.Named("segment")[0].String("name"); v != "cache" || !ok || err != nil {
		t.Errorf("name = %q, %v, %v", v, ok, err)
	}
	if got := doc.Named("mod.cache"); len(got) != 1 || got[0].Array {
		t.Fatalf("Named(mod.cache) = %v", got)
	}
	if v, ok, err := doc.Named("mod.cache")[0].Bool("on"); !v || !ok || err != nil {
		t.Errorf("on = %v, %v, %v", v, ok, err)
	}
	if v, ok, err := doc.Root().Int("abi"); v != 1 || !ok || err != nil {
		t.Errorf("abi = %d, %v, %v", v, ok, err)
	}
	if got := doc.Named(""); len(got) != 1 || got[0] != doc.Root() {
		t.Errorf(`Named("") = %v, want the root alone`, got)
	}
	if got := doc.Named("mod"); got != nil {
		t.Errorf("Named(mod) = %v, want none", got)
	}
	// The longest name Parse reads can be added, and read back.
	long := strings.Repeat("a.", 31) + "a"
	doc.Add(long, false).Set("deep", true)
	if got := parse(t, string(doc.Encode())).Named(long); len(got) != 1 {
		t.Errorf("a table with a name of 32 parts was read back %d times, want once", len(got))
	}

	// A wrong type set by the program is reported without a line.
	if _, _, err := doc.Root().String("abi"); err == nil || err.Error() != "abi must be a string, not an integer" {
		t.Errorf("String(abi) error %v", err)
	}
}

func TestSet(t *testing.T) {
	t.Parallel()
	tb := toml.NewDoc().Root()
	tb.Set("a", "one")
	tb.Set("b", int64(2))
	tb.Set("c", false)
	tb.Set("a", []string{"now", "a list"})
	if got := tb.Keys(); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Errorf("Keys() = %v: a key set again must keep its place", got)
	}
	if _, ok, err := tb.String("a"); !ok || err == nil {
		t.Errorf("String(a) after Set with a list: ok %v, err %v", ok, err)
	}
	if v, _, err := tb.Strings("a"); err != nil || !reflect.DeepEqual(v, []string{"now", "a list"}) {
		t.Errorf("Strings(a) = %v, %v", v, err)
	}

	// The table keeps its own copy of a list.
	list := []string{"x", "y"}
	tb.Set("list", list)
	list[0] = "changed"
	if v, _, _ := tb.Strings("list"); v[0] != "x" {
		t.Errorf("changing the slice passed to Set changed the table: %v", v)
	}
	tb.Set("none", []string(nil))
	if v, ok, err := tb.Strings("none"); len(v) != 0 || !ok || err != nil {
		t.Errorf("Strings(none) = %v, %v, %v", v, ok, err)
	}

	// Bytes that are not UTF-8 would make a file Parse refuses.
	tb.Set("bad", "a\xffb")
	tb.Set("bads", []string{"\xc3"})
	if v, _, _ := tb.String("bad"); v != "a\uFFFDb" {
		t.Errorf("String(bad) = %q", v)
	}
	if v, _, _ := tb.Strings("bads"); !reflect.DeepEqual(v, []string{"\uFFFD"}) {
		t.Errorf("Strings(bads) = %q", v)
	}

	// The zero Table and the zero Doc work.
	var zero toml.Table
	if zero.Keys() != nil {
		t.Errorf("zero Table has keys %v", zero.Keys())
	}
	if _, ok, err := zero.String("a"); ok || err != nil {
		t.Errorf("zero Table: String = %v, %v", ok, err)
	}
	zero.Set("a", true)
	if v, ok, err := zero.Bool("a"); !v || !ok || err != nil {
		t.Errorf("zero Table after Set: %v, %v, %v", v, ok, err)
	}
	var doc toml.Doc
	if got := doc.Named(""); got != nil {
		t.Errorf(`zero Doc: Named("") = %v`, got)
	}
	doc.Add("t", false).Set("k", 1)
	doc.Root().Set("r", 2)
	if got, want := string(doc.Encode()), "r = 2\n\n[t]\nk = 1\n"; got != want {
		t.Errorf("zero Doc encodes as %q, want %q", got, want)
	}
}

func TestProgramMistakesPanic(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		do   func(d *toml.Doc)
	}{
		{"Set with an empty key", func(d *toml.Doc) { d.Root().Set("", 1) }},
		{"Set with a dotted key", func(d *toml.Doc) { d.Root().Set("a.b", 1) }},
		{"Set with a space in the key", func(d *toml.Doc) { d.Root().Set("a b", 1) }},
		{"Set with a key that would start a table", func(d *toml.Doc) { d.Root().Set("[a]", 1) }},
		{"Set with a newline in the key", func(d *toml.Doc) { d.Root().Set("a\nb", 1) }},
		{"Set with a float", func(d *toml.Doc) { d.Root().Set("a", 1.5) }},
		{"Set with nil", func(d *toml.Doc) { d.Root().Set("a", nil) }},
		{"Set with an int32", func(d *toml.Doc) { d.Root().Set("a", int32(1)) }},
		{"Set with a list of integers", func(d *toml.Doc) { d.Root().Set("a", []int{1}) }},
		{"Add with an empty name", func(d *toml.Doc) { d.Add("", false) }},
		{"Add with an empty part", func(d *toml.Doc) { d.Add("mod.", false) }},
		{"Add with a bracket", func(d *toml.Doc) { d.Add("a]\n[b", true) }},
		{"Add with a space", func(d *toml.Doc) { d.Add("mod cache", false) }},
		{"Add with more parts than Parse reads", func(d *toml.Doc) { d.Add(strings.Repeat("a.", 32)+"a", false) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			doc := toml.NewDoc()
			defer func() {
				if recover() == nil {
					t.Error("no panic")
				}
				if got := doc.Encode(); len(got) != 0 {
					t.Errorf("the document changed: %q", got)
				}
			}()
			tc.do(doc)
		})
	}
}

func TestEncode(t *testing.T) {
	t.Parallel()
	doc := toml.NewDoc()
	doc.Root().Set("abi", int64(1))
	doc.Root().Set("name", "cache")
	doc.Root().Set("read", []string{"model_calls", "it's \"quoted\""})
	doc.Root().Set("none", []string{})
	doc.Add("glyph", false)
	seg := doc.Add("segment", true)
	seg.Set("on", false)
	seg.Set("drop", int64(-4))
	seg.Set("match", `load averages?: (\d+)`)
	seg.Set("format", `say "$1"`)
	seg.Set("both", `it's a \d`)
	seg.Set("lines", "one\ntwo\tthree")
	seg.Set("hidden", "\x00\x1b[2J\x7f\u0085\u009b0m")
	seg.Set("wide", "● 🙂 é")
	doc.Add("segment", true).Set("name", "")
	doc.Add("mod.cache", false).Set("on", true)

	want := `abi = 1
name = "cache"
read = ["model_calls", "it's \"quoted\""]
none = []

[glyph]

[[segment]]
on = false
drop = -4
match = 'load averages?: (\d+)'
format = 'say "$1"'
both = "it's a \\d"
lines = "one\ntwo\tthree"
hidden = "\u0000\u001B[2J\u007F\u0085\u009B0m"
wide = "● 🙂 é"

[[segment]]
name = ""

[mod.cache]
on = true
`
	got := doc.Encode()
	if string(got) != want {
		t.Errorf("Encode()\n got:\n%s\nwant:\n%s", got, want)
	}
	if again := doc.Encode(); !bytes.Equal(got, again) {
		t.Errorf("Encode() gave different bytes the second time")
	}
	// No control character reaches the file as itself, bar the line ends.
	for i, c := range got {
		if c < 0x20 && c != '\n' || c == 0x7F {
			t.Errorf("byte %d of the file is the control character %#x", i, c)
		}
	}
	// Read back, every value is what was set.
	back := parse(t, string(got))
	if a, b := snapshot(t, back), snapshot(t, doc); !reflect.DeepEqual(a, b) {
		t.Errorf("read back:\n got %#v\nwant %#v", a, b)
	}
}

// A file begins with its first line: no blank line stands in for a root
// that has no keys. mods.toml is such a file.
func TestEncodeWithoutRootKeys(t *testing.T) {
	t.Parallel()
	doc := toml.NewDoc()
	doc.Add("mod.cache", false).Set("on", true)
	doc.Add("mod.git", false).Set("on", false)
	if got, want := string(doc.Encode()), "[mod.cache]\non = true\n\n[mod.git]\non = false\n"; got != want {
		t.Errorf("Encode() = %q, want %q", got, want)
	}
}

// roundTrip checks that a source Parse accepts is still the same document
// after Encode and Parse, and that Encode has settled by then.
func roundTrip(t *testing.T, src []byte) {
	t.Helper()
	doc, err := toml.Parse(src)
	if err != nil {
		te, ok := errors.AsType[*toml.Error](err)
		if !ok {
			t.Fatalf("Parse error is %T, want *toml.Error", err)
		}
		if lines := bytes.Count(src, []byte{'\n'}) + 1; te.Line < 0 || te.Line > lines {
			t.Fatalf("Parse error %q names line %d of %d", te.Msg, te.Line, lines)
		}
		checkMessage(t, te)
		if doc != nil {
			t.Fatal("Parse returned a document with its error")
		}
		return
	}
	out := doc.Encode()
	if len(out) > toml.MaxSize {
		return // Encode spaces a file out, so one at the limit can pass it
	}
	back, err := toml.Parse(out)
	if err != nil {
		t.Fatalf("Parse(Encode(doc)): %v\nsource  %q\nencoded %q", err, src, out)
	}
	if a, b := snapshot(t, back), snapshot(t, doc); !reflect.DeepEqual(a, b) {
		t.Fatalf("Parse(Encode(doc)) differs\n got %#v\nwant %#v\nsource  %q\nencoded %q", a, b, src, out)
	}
	if again := back.Encode(); !bytes.Equal(again, out) {
		t.Fatalf("Encode is not stable\nfirst  %q\nsecond %q", out, again)
	}
}

func TestRoundTrip(t *testing.T) {
	t.Parallel()
	for _, tc := range acceptCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			roundTrip(t, []byte(tc.src))
		})
	}
}

func FuzzParse(f *testing.F) {
	for _, tc := range acceptCases() {
		f.Add([]byte(tc.src))
	}
	for _, tc := range refuseCases() {
		f.Add([]byte(tc.src))
	}
	for _, src := range []string{statusLineFile, modFile, modsFile, chatFile} {
		f.Add([]byte(src))
	}
	f.Fuzz(func(t *testing.T, src []byte) {
		roundTrip(t, src)
	})
}
