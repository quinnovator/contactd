package toml_test

import (
	"errors"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/quinnovator/contactd/internal/tui/toml"
)

// entry is one key and its value, as the getters report it.
type entry struct {
	Key string
	Val any
}

// table is what a test compares of a toml.Table: all of it but the line.
type table struct {
	Name    string
	Array   bool
	Entries []entry
}

// snapshot reads a whole document back through the exported API. It fails
// the test unless exactly one getter accepts each key.
func snapshot(t *testing.T, d *toml.Doc) []table {
	t.Helper()
	out := make([]table, 0, len(d.Tables))
	for i := range d.Tables {
		tb := &d.Tables[i]
		snap := table{Name: tb.Name, Array: tb.Array}
		for _, key := range tb.Keys() {
			var vals []any
			if v, ok, err := tb.String(key); ok && err == nil {
				vals = append(vals, v)
			}
			if v, ok, err := tb.Int(key); ok && err == nil {
				vals = append(vals, v)
			}
			if v, ok, err := tb.Bool(key); ok && err == nil {
				vals = append(vals, v)
			}
			if v, ok, err := tb.Strings(key); ok && err == nil {
				vals = append(vals, v)
			}
			if len(vals) != 1 {
				t.Fatalf("table %q key %q: %d getters accept it, want 1", tb.Name, key, len(vals))
			}
			snap.Entries = append(snap.Entries, entry{key, vals[0]})
		}
		out = append(out, snap)
	}
	return out
}

func root(entries ...entry) table { return table{Entries: entries} }

type acceptCase struct {
	name string
	src  string
	want []table
}

func acceptCases() []acceptCase {
	return []acceptCase{
		{"empty file", "", []table{root()}},
		{"blank lines and spaces", " \n\t\n\n   ", []table{root()}},
		{"comments only", "# one\n  # two\n#", []table{root()}},
		{"a comment may hold a tab and any text", "#\ta = \"1\" [x] é ●\n", []table{root()}},
		{
			"bare keys",
			"a = 1\nA-b_9 = 2\n123 = 3\n-_ = 4\n",
			[]table{root(entry{"a", int64(1)}, entry{"A-b_9", int64(2)}, entry{"123", int64(3)}, entry{"-_", int64(4)})},
		},
		{
			"no space around the equals sign, and a lot of it",
			"a=1\nb\t =  \t2\n   c = 3   \n",
			[]table{root(entry{"a", int64(1)}, entry{"b", int64(2)}, entry{"c", int64(3)})},
		},
		{
			"a comment after a value",
			"a = 1# one\nb = \"x\" # two\nc = true#\n",
			[]table{root(entry{"a", int64(1)}, entry{"b", "x"}, entry{"c", true})},
		},
		{"no newline at the end", "a = \"x\"", []table{root(entry{"a", "x"})}},
		{
			"basic string escapes",
			`a = "q\"b\\n\nt\tu\u00e9\u25CFz"`,
			[]table{root(entry{"a", "q\"b\\n\nt\tu\u00e9\u25cfz"})},
		},
		{"an escaped NUL", `a = "\u0000"`, []table{root(entry{"a", "\x00"})}},
		{
			"a basic string keeps a hash, quotes and a raw tab",
			"a = \"# not 'a' comment\there\"\n",
			[]table{root(entry{"a", "# not 'a' comment\there"})},
		},
		{"UTF-8 in a string", "tool = \"●\"\nelbow = '└'\n", []table{root(entry{"tool", "●"}, entry{"elbow", "└"})}},
		{"an empty string of each kind", "a = \"\"\nb = ''\n", []table{root(entry{"a", ""}, entry{"b", ""})}},
		{
			"a literal string takes a backslash as it is",
			`match = 'load averages?: ([0-9.]+)\s"x" \u0041 \'`,
			[]table{root(entry{"match", `load averages?: ([0-9.]+)\s"x" \u0041 \`})},
		},
		{
			"integers",
			"a = 0\nb = +7\nc = -7\nd = 1_000_000\ne = -0\nf = 9223372036854775807\ng = -9223372036854775808\n",
			[]table{root(
				entry{"a", int64(0)}, entry{"b", int64(7)}, entry{"c", int64(-7)}, entry{"d", int64(1_000_000)},
				entry{"e", int64(0)}, entry{"f", int64(math.MaxInt64)}, entry{"g", int64(math.MinInt64)},
			)},
		},
		{"booleans", "a = true\nb = false\n", []table{root(entry{"a", true}, entry{"b", false})}},
		{
			"arrays of strings",
			"a = []\nb = [\"x\"]\nc = [ 'x' , \"y\",]\nd=[\"a\",\"b\"]\n",
			[]table{root(
				entry{"a", []string{}}, entry{"b", []string{"x"}},
				entry{"c", []string{"x", "y"}}, entry{"d", []string{"a", "b"}},
			)},
		},
		{
			"an array over several lines, with comments and a last comma",
			"read = [ # grants\n  \"model_calls\",\n\n  'clock', # the time\n\t\"]\",\n]  # done\nafter = 1\n",
			[]table{root(entry{"read", []string{"model_calls", "clock", "]"}}, entry{"after", int64(1)})},
		},
		{
			"a table",
			"top = 1\n[glyph]\ntool = \"*\"\n",
			[]table{root(entry{"top", int64(1)}), {Name: "glyph", Entries: []entry{{"tool", "*"}}}},
		},
		{
			"a dotted table name, with spaces",
			"[mod.cache]\non = true\n[ mod . git ] # second\non = false\n",
			[]table{
				root(),
				{Name: "mod.cache", Entries: []entry{{"on", true}}},
				{Name: "mod.git", Entries: []entry{{"on", false}}},
			},
		},
		{
			"an array of tables keeps file order and empty tables",
			"[[segment]]\nname = \"a\"\n[[segment]]\n[[segment]]\nname = \"c\"\n",
			[]table{
				root(),
				{Name: "segment", Array: true, Entries: []entry{{"name", "a"}}},
				{Name: "segment", Array: true},
				{Name: "segment", Array: true, Entries: []entry{{"name", "c"}}},
			},
		},
		{
			"the same key in different tables",
			"name = \"r\"\n[a]\nname = \"a\"\n[b]\nname = \"b\"\n",
			[]table{
				root(entry{"name", "r"}),
				{Name: "a", Entries: []entry{{"name", "a"}}},
				{Name: "b", Entries: []entry{{"name", "b"}}},
			},
		},
		{
			"a table defined after one beneath it",
			"[a.b]\nx = 1\n[a]\ny = 2\n",
			[]table{root(), {Name: "a.b", Entries: []entry{{"x", int64(1)}}}, {Name: "a", Entries: []entry{{"y", int64(2)}}}},
		},
		{
			"a table beneath each element of an array",
			"[[a]]\n[a.b]\nx = 1\n[[a]]\n[a.b]\nx = 2\n",
			[]table{
				root(),
				{Name: "a", Array: true},
				{Name: "a.b", Entries: []entry{{"x", int64(1)}}},
				{Name: "a", Array: true},
				{Name: "a.b", Entries: []entry{{"x", int64(2)}}},
			},
		},
		{
			"an array of tables beneath a name that gets its header later",
			"[[a.b]]\n[a]\nb2 = 1\n[[a.b]]\n",
			[]table{root(), {Name: "a.b", Array: true}, {Name: "a", Entries: []entry{{"b2", int64(1)}}}, {Name: "a.b", Array: true}},
		},
		{
			"CRLF line endings",
			"a = 1\r\n[t] # c\r\nb = [\r\n 'x',\r\n]\r\n",
			[]table{root(entry{"a", int64(1)}), {Name: "t", Entries: []entry{{"b", []string{"x"}}}}},
		},
		{"a byte order mark", "\uFEFFa = 1\n", []table{root(entry{"a", int64(1)})}},
		{"a key named like a value", "true = false\ninf = 1\n", []table{root(entry{"true", false}, entry{"inf", int64(1)})}},
		{
			"a table name of the most parts there may be",
			"[" + strings.Repeat("a.", 31) + "a]\nx = 1\n",
			[]table{root(), {Name: strings.Repeat("a.", 31) + "a", Entries: []entry{{"x", int64(1)}}}},
		},
	}
}

func TestParseAccepts(t *testing.T) {
	t.Parallel()
	for _, tc := range acceptCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			doc, err := toml.Parse([]byte(tc.src))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got := snapshot(t, doc); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Parse(%.200q)\n got %#v\nwant %#v", tc.src, got, tc.want)
			}
		})
	}
}

type refuseCase struct {
	name string
	src  string
	line int
	msg  string // part of the message
}

func refuseCases() []refuseCase {
	return []refuseCase{
		// The constructs the subset leaves out.
		{"float", "a = 1\nb = 1.5\n", 2, "floats are not supported"},
		{"float with an exponent", "\n\na = 1e6\n", 3, "floats are not supported"},
		{"float with both", "a = -6.02E+23", 1, "floats are not supported"},
		{"infinity", "a = 1\nb = +inf", 2, "floats are not supported"},
		{"not a number", "a = nan", 1, "floats are not supported"},
		{"date", "a = 1\n\nb = 2026-10-12\n", 3, "dates and times are not supported"},
		{"date and time", "a = 1979-05-27T07:32:00Z\n", 1, "dates and times are not supported"},
		{"date, a space, a time", "# c\na = 1979-05-27 07:32:00\n", 2, "dates and times are not supported"},
		{"time", "a = 07:32:00\n", 1, "dates and times are not supported"},
		{"inline table", "a = 1\nb = { x = 1 }\n", 2, "inline tables are not supported"},
		{"multi-line basic string", "a = 1\nb = 2\nc = \"\"\"\nx\n\"\"\"\n", 3, "multi-line strings are not supported"},
		{"multi-line literal string", "\nc = '''x'''\n", 2, "multi-line strings are not supported"},
		{"multi-line string in an array", "a = [\n  \"\"\"x\"\"\",\n]\n", 2, "multi-line strings are not supported"},
		{"dotted key", "a = 1\nb.c = 2\n", 2, "dotted keys are not supported"},
		{"dotted key with spaces", "[t]\nb . c = 2\n", 2, "dotted keys are not supported"},
		{"quoted key", "a = 1\n\"b\" = 2\n", 2, "quoted keys are not supported"},
		{"literal quoted key", "'b' = 2\n", 1, "quoted keys are not supported"},
		{"quoted table name", "\n[\"a\"]\n", 2, "quoted keys are not supported"},
		{"quoted part of a table name", "[a.'b']\n", 1, "quoted keys are not supported"},
		{"a key set twice", "a = 1\nb = 2\na = 3\n", 3, "key a is set twice (first on line 1)"},
		{"a key set twice in a table", "a = 1\n[t]\na = 2\n# c\na = 3\n", 5, "key a is set twice (first on line 3)"},
		{"a key set twice in one array element", "[[s]]\nn = 1\n[[s]]\nn = 2\nn = 3\n", 5, "key n is set twice (first on line 4)"},

		// Other values that are not in the subset.
		{"hexadecimal", "a = 0xFF\n", 1, "only decimal integers are supported"},
		{"octal", "a = 0o17\n", 1, "only decimal integers are supported"},
		{"binary", "a = 0b11\n", 1, "only decimal integers are supported"},
		{"an integer in an array", "a = [1, 2]\n", 1, "an array may hold only strings"},
		{"a boolean in an array", "a = [\n \"x\",\n true,\n]\n", 3, "an array may hold only strings"},
		{"an array in an array", "a = [[\"x\"]]\n", 1, "an array may hold only strings"},
		{"an inline table in an array", "a = [{}]\n", 1, "an array may hold only strings"},
		{"a bare word", "color = auto\n", 1, `"auto" is not a value; a string needs quotes`},
		{"capitalised boolean", "a = True\n", 1, `"True" is not a value`},
		{"a leading zero", "a = 007\n", 1, "may not start with 0"},
		{"a leading underscore", "a = _1\n", 1, "underscore"},
		{"a trailing underscore", "a = 1_\n", 1, "underscore"},
		{"a doubled underscore", "a = 1__0\n", 1, "underscore"},
		{"two signs", "a = +-1\n", 1, "is not a value"},
		{"a sign alone", "a = -\n", 1, "is not a value"},
		{"too large an integer", "\na = 9223372036854775808\n", 2, "does not fit in 64 bits"},
		{"too small an integer", "a = -9223372036854775809\n", 1, "does not fit in 64 bits"},

		// Strings.
		{"unclosed basic string", "a = 1\nb = \"x\nc = 2\n", 2, "the string is not closed"},
		{"unclosed literal string", "b = 'x", 1, "the string is not closed"},
		{"unclosed string at a backslash", "b = \"x\\", 1, "the string is not closed"},
		{"an escape the subset lacks", "a = \"x\\ry\"\n", 1, `the escape \r is not supported`},
		{"the long unicode escape", "a = \"\\U0001F600\"\n", 1, `the escape \U is not supported`},
		{"a short unicode escape", "a = \"\\u12\"\n", 1, "four hex digits"},
		{"a unicode escape that is not hex", "a = \"\\u12G4\"\n", 1, "four hex digits"},
		{"a unicode escape cut off by the file", "a = \"\\u12", 1, "four hex digits"},
		{"a surrogate", "a = \"\\uD83D\\uDE00\"\n", 1, "surrogate"},
		{"a control character in a basic string", "a = 1\nb = \"x\x1by\"\n", 2, "control character in a string"},
		{"a control character in a literal string", "b = 'x\x00y'\n", 1, "control character in a string"},
		{"a delete character in a string", "b = \"x\x7fy\"\n", 1, "control character in a string"},
		{"a control character in a comment", "a = 1\n# x\x1b[2J\n", 2, "control character in a comment"},
		{"an escape with a hint for a pattern", "match = \"(\\d+)\"\n", 1, `the escape \d is not supported; there are \" \\ \n \t and \uXXXX, or write the string in single quotes`},
		{"a backslash before a control character", "a = 1\nb = \"x\\\x1b[2J\"\n", 2, `the escape of '\x1b' is not supported`},
		{"a backslash before a C1 control character", "b = \"x\\\u009b2J\"\n", 1, `the escape of '\u009b' is not supported`},
		{"a backslash before a character that reverses the line", "b = \"x\\\u202e\"\n", 1, `the escape of '\u202e' is not supported`},

		// Arrays.
		{"unclosed array", "a = 1\nb = [\n \"x\",\n \"y\"\n", 2, "the array is not closed"},
		{"unclosed empty array", "b = [", 1, "the array is not closed"},
		{"a missing comma", "b = [\n \"x\"\n \"y\"\n]\n", 3, "expected , or ] in the array"},
		{"a comma with nothing before it", "b = [,]\n", 1, "expected a string before the comma"},
		{"two commas", "b = [\"x\",,]\n", 1, "expected a string before the comma"},

		// Keys and lines.
		{"no value", "a = 1\nb =\n", 2, "expected a value after ="},
		{"no value before a comment", "b = # none\n", 1, "expected a value after ="},
		{"no value at the end of the file", "b =", 1, "expected a value after ="},
		{"no equals sign", "a = 1\nb\n", 2, "expected = after b, found the end of the line"},
		{"no equals sign at the end of the file", "b", 1, "expected = after b, found the end of the file"},
		{"two keys", "a b = 1\n", 1, "expected = after a, found 'b'"},
		{"no key", "\n= 1\n", 2, "expected a key, found '='"},
		{"a key with a character that is not bare", "a = 1\nb = 2\ncafé = 3\n", 3, "expected = after caf, found 'é'"},
		{"two values", "a = 1 2\n", 1, "expected the end of the line, found '2'"},
		{"two pairs on a line", "a = \"x\" b = \"y\"\n", 1, "expected the end of the line, found 'b'"},
		{"text after a header", "[a] b = 1\n", 1, "expected the end of the line, found 'b'"},
		{"a lone carriage return", "a = 1\rb = 2\n", 1, "carriage return"},
		{"a lone carriage return on a blank line", "a = 1\n\r", 2, "carriage return"},

		// Headers.
		{"an empty table name", "a = 1\n[]\n", 2, "expected a table name"},
		{"an empty array name", "[[]]\n", 1, "expected a table name"},
		{"an unclosed header", "\n[a\n", 2, "expected ] after the table name, found the end of the line"},
		{"a half-closed array header", "[[a]\n", 1, "expected ]] to close [[a"},
		{"a name that ends with a dot", "[a.]\n", 1, "expected a key, found ']'"},
		{"a name that starts with a dot", "[.a]\n", 1, "expected a key, found '.'"},
		{"a space in a name", "[a b]\n", 1, "expected ] after the table name, found 'b'"},
		{"a space between the brackets", "[ [a]]\n", 1, "expected a key, found '['"},
		{"a table defined twice", "[a]\nx = 1\n[b]\n[a]\n", 4, "table a is defined twice (first on line 1)"},
		{"a nested table defined twice", "[a.b]\n[a]\n[a.b]\n", 3, "table a.b is defined twice (first on line 1)"},
		{"a table, then an array of that name", "[a]\n\n[[a]]\n", 3, "a is already a table, defined on line 1"},
		{"an array, then a table of that name", "[[a]]\n[[a]]\n[a]\n", 3, "a is already an array of tables, begun on line 1"},
		{"an array where a table was implied", "[a.b]\n[[a]]\n", 2, "a is already a table, named on line 1"},
		{"a table named like a key", "a = 1\n[a]\n", 2, "a is already a key, set on line 1"},
		{"an array named like a key", "a = 1\n[[a]]\n", 2, "a is already a key, set on line 1"},
		{"a table beneath a key", "[t]\na = 1\n[t.a.b]\n", 3, "t.a is already a key, set on line 2"},
		{"a key named like a table beneath", "[a.b]\n[a]\nc = 1\nb = 1\n", 4, "b is already a table, named on line 1"},
		{"a table beneath a key of an array element", "[[s]]\n[s.w]\n[[s]]\n[s.w]\nv = 1\n[s.w.v]\n", 6, "s.w.v is already a key, set on line 5"},

		// The file as a whole.
		{"not UTF-8", "a = 1\nb = \"\xff\"\n", 2, "the file is not UTF-8"},
		{"not UTF-8 in a comment", "# \xc3\n", 1, "the file is not UTF-8"},
		{"a NUL outside a string", "a = 1\n\x00\n", 2, `expected a key, found '\x00'`},

		// A file made to cost more than its size: the message stays short,
		// and a name with many parts is refused before it is taken apart.
		{"a table name of too many parts", "a = 1\n[" + strings.Repeat("a.", 32) + "a]\n", 2, "a table name may have at most 32 parts"},
		{"a table name as long as a file", "[" + strings.Repeat("a.", 200_000) + "a]\n", 1, "a table name may have at most 32 parts"},
		{"a long bare word", "a = " + strings.Repeat("é", 5000) + "\n", 1, `"` + strings.Repeat("é", 24) + `..." is not a value`},
		{"a long word of control characters", "a = 0" + strings.Repeat("\x7f", 5000) + "\n", 1, `"0\x7f\x7f`},
		{"a long key with no equals sign", strings.Repeat("k", 5000) + "\n", 1, "expected = after " + strings.Repeat("k", 48) + "..., found the end of the line"},
		{"a long key set twice", "\n" + strings.Repeat("k", 5000) + " = 1\n" + strings.Repeat("k", 5000) + " = 2\n", 3, "key " + strings.Repeat("k", 48) + "... is set twice (first on line 2)"},
		{"a long table name defined twice", "[" + strings.Repeat("t", 5000) + "]\n[" + strings.Repeat("t", 5000) + "]\n", 2, "table " + strings.Repeat("t", 48) + "... is defined twice (first on line 1)"},
		{"a long name, half closed", "[[" + strings.Repeat("t", 5000) + "]\n", 1, "expected ]] to close [[" + strings.Repeat("t", 48) + "..."},
	}
}

// maxMessage is longer than any message Parse writes: the longest holds one
// clipped name and two line numbers.
const maxMessage = 200

// checkMessage fails the test unless a refusal can be shown as it is: one
// short line, with nothing from the file that a terminal would act on.
func checkMessage(t *testing.T, te *toml.Error) {
	t.Helper()
	if te.Msg == "" || len(te.Msg) > maxMessage {
		t.Errorf("the message is %d bytes long, want 1 to %d: %.80q", len(te.Msg), maxMessage, te.Msg)
	}
	for _, r := range te.Msg {
		if !strconv.IsPrint(r) {
			t.Errorf("the message holds %U, which cannot be printed: %q", r, te.Msg)
			return
		}
	}
}

func TestParseRefuses(t *testing.T) {
	t.Parallel()
	for _, tc := range refuseCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			doc, err := toml.Parse([]byte(tc.src))
			if err == nil {
				t.Fatalf("Parse accepted it: %#v", snapshot(t, doc))
			}
			if doc != nil {
				t.Error("Parse returned a document with its error")
			}
			te, ok := errors.AsType[*toml.Error](err)
			if !ok {
				t.Fatalf("Parse error is %T, want *toml.Error", err)
			}
			if te.Line != tc.line || !strings.Contains(te.Msg, tc.msg) {
				t.Errorf("got line %d: %s\nwant line %d: ...%s...", te.Line, te.Msg, tc.line, tc.msg)
			}
			checkMessage(t, te)
		})
	}
}
