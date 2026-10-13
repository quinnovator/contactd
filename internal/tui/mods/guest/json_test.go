package guest

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/quinnovator/contactd/internal/tui/mods/wire"
)

// byHand decodes a request with this package's decoder, new for the call.
func byHand(data []byte) (wire.Request, bool) {
	var req wire.Request
	var d decoder
	ok := d.decode(data, &req)
	return req, ok
}

// byReflection decodes a request with encoding/json, which is what the
// decoder replaced and what it must agree with.
func byReflection(data []byte) (wire.Request, bool) {
	var req wire.Request
	err := json.Unmarshal(data, &req)
	return req, err == nil
}

// agree fails the test unless both decoders say the same of data: both
// refuse it, or both accept it with the same request.
func agree(t *testing.T, data []byte) (wire.Request, bool) {
	t.Helper()
	got, ok := byHand(data)
	want, wantOK := byReflection(data)
	if ok != wantOK {
		t.Fatalf("decode accepted: %v; encoding/json accepted: %v\ninput: %s", ok, wantOK, clip(data))
	}
	if ok && !reflect.DeepEqual(got, want) {
		t.Fatalf("decode and encoding/json differ\n got %+v\nwant %+v\ninput: %s", show(got), show(want), clip(data))
	}
	return got, ok
}

// show writes a request with its pointers followed, for a failure message.
func show(r wire.Request) string {
	b, err := json.Marshal(r)
	if err != nil {
		return err.Error()
	}
	return string(b)
}

// clip shortens an input for a failure message.
func clip(data []byte) string {
	if len(data) > 600 {
		return strconv.Quote(string(data[:600])) + "..."
	}
	return strconv.Quote(string(data))
}

// fullRequest sets every field of a request.
func fullRequest() wire.Request {
	return wire.Request{
		ABI:   wire.ABI,
		Op:    "command",
		Name:  "stats",
		Args:  []string{"7", "two words"},
		Width: 100,
		Term:  wire.Term{Color: "truecolor", Glyphs: "unicode", Background: "dark"},
		Data: wire.Data{
			Contact: &wire.Contact{ID: 42, Name: "ada"},
			Route:   &wire.Route{Provider: "deepseek", Model: "deepseek-flash"},
			State:   &wire.State{Conn: "live", Replying: true, Running: "python"},
			ModelCalls: []wire.ModelCall{
				{
					Seq: 30, TS: 1_760_270_700_000, Step: 1, Route: "deepseek/deepseek-flash",
					Input: 3200, Cached: 1536, Output: 212, Reasoning: 64, MS: 5400,
				},
				{Seq: 33, TS: 1_760_270_760_000, Step: 2, Route: "deepseek/deepseek-flash", MS: 900, Error: "rate_limit"},
			},
			Messages: []wire.Message{
				{Seq: 29, TS: 1_760_270_690_000, Role: "user", Text: "plot last week's latency"},
				{Seq: 31, TS: 1_760_270_710_000, Role: "agent", Text: "p99 peaked on Thursday at 7.2 s."},
			},
			Workspace: &wire.Workspace{Dir: "contactd", Branch: "tui-frame"},
			Now:       1_760_270_765_000,
		},
		Item: &wire.Item{Kind: "code:mermaid", Text: "graph TD; a-->b"},
	}
}

// manyCalls is a request with n model-call records, as a segment that holds
// the model_calls grant is sent.
func manyCalls(n int) wire.Request {
	req := wire.Request{
		ABI: wire.ABI, Op: "segment", Name: "cache", Width: 100,
		Term: wire.Term{Color: "truecolor", Glyphs: "unicode", Background: "dark"},
	}
	for i := range n {
		req.Data.ModelCalls = append(req.Data.ModelCalls, wire.ModelCall{
			Seq: uint64(100 + 3*i), TS: 1_791_806_640_000 + int64(i)*3_600_000, Step: 1 + i%3,
			Route: "deepseek/deepseek-flash",
			Input: 3000 + 211*i, Cached: 1500 + 97*i, Output: 200 + i, MS: 5000 + int64(i),
		})
	}
	return req
}

// awkward is a request whose strings and numbers are the ones a decoder gets
// wrong: every escape, characters of every length, bytes that are not UTF-8,
// and numbers at the ends of their types.
func awkward() wire.Request {
	return wire.Request{
		ABI:   -1,
		Op:    "quote \" backslash \\ slash / tab \t newline \n return \r bell \a null \x00 del \x7f",
		Name:  "é ⎇ 日本語 🙂 \u2028 \u2029 <b>&amp;</b>",
		Args:  []string{"", " ", "\xff", "ok\xc3", "\xed\xa0\x80", strings.Repeat("é", 300), strings.Repeat("a\"", 40), "\\u0041"},
		Width: math.MinInt64,
		Term:  wire.Term{Color: "", Glyphs: "0123456", Background: "01234567"},
		Data: wire.Data{
			Contact: &wire.Contact{ID: math.MaxUint64, Name: "012345678"},
			Route:   &wire.Route{},
			State:   &wire.State{Replying: false, Running: "\x1b[31mred\x1b[0m"},
			ModelCalls: []wire.ModelCall{
				{Seq: 0, TS: math.MinInt64, Step: math.MaxInt64, Input: -1, Cached: 99_999_999, Output: 100_000_000},
				{Seq: 1 << 63, TS: math.MaxInt64, Step: -0, Reasoning: 999_999_999_999_999, MS: 1_000_000_000_000_000},
				{Route: strings.Repeat("r", 256), Error: strings.Repeat("e", 255) + "\n"},
			},
			Messages:  []wire.Message{{Text: strings.Repeat("long text, said once. ", 300)}, {}},
			Workspace: &wire.Workspace{Dir: "a", Branch: "feature/ünïcode"},
			Now:       -1,
		},
		Item: &wire.Item{Kind: "tool:python", Text: "line one\nline two\n\ttabbed \\n"},
	}
}

// written is a request as the host writes it.
func written(tb testing.TB, req wire.Request) []byte {
	tb.Helper()
	data, err := json.Marshal(req)
	if err != nil {
		tb.Fatal(err)
	}
	return data
}

// requests are the requests the tests write and read back.
func requests() map[string]wire.Request {
	return map[string]wire.Request{
		"every field":   fullRequest(),
		"nothing":       {},
		"hello":         {ABI: wire.ABI, Op: "hello"},
		"one call":      manyCalls(1),
		"fifty calls":   manyCalls(50),
		"more than 50":  manyCalls(131),
		"awkward":       awkward(),
		"empty lists":   {Args: []string{}, Data: wire.Data{ModelCalls: []wire.ModelCall{}, Messages: []wire.Message{}}},
		"empty structs": {Data: wire.Data{Contact: &wire.Contact{}, Workspace: &wire.Workspace{}}, Item: &wire.Item{}},
	}
}

// TestDecodeReadsWhatTheHostWrites is the decoder's main job: a request
// written by encoding/json, as the host writes it, is read back the same,
// and without one key being searched for.
func TestDecodeReadsWhatTheHostWrites(t *testing.T) {
	t.Parallel()
	for name, req := range requests() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			data := written(t, req)
			var got wire.Request
			var d decoder
			if !d.decode(data, &got) {
				t.Fatalf("decode refused what the host wrote: %s", clip(data))
			}
			want, ok := byReflection(data)
			if !ok {
				t.Fatal("encoding/json refused its own output")
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("decode and encoding/json differ\n got %s\nwant %s", show(got), show(want))
			}
			// This is the gate on speed. Each member of what the host
			// writes is where the decoder first looks, so no key is ever
			// searched for. A key misspelled in the decoder would still
			// be read, the slow way, and only this would show it.
			if d.searched != 0 {
				t.Errorf("the decoder searched for %d keys of a request the host wrote; want 0", d.searched)
			}
		})
	}
}

// TestDecodeReadsTheGoldenRequest reads the contract's own example, which is
// indented, so every key is found the slow way.
func TestDecodeReadsTheGoldenRequest(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join("..", "wire", "testdata", "request.golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	got, ok := agree(t, data)
	if !ok {
		t.Fatal("the golden request was refused")
	}
	if got.Op != "command" || len(got.Data.ModelCalls) != 2 || got.Item == nil || got.Data.Workspace.Branch != "tui-frame" {
		t.Errorf("the golden request was read as %s", show(got))
	}
}

// decodeCase is one input and whether it is a request.
type decodeCase struct {
	name, in string
	ok       bool
}

// deep is n arrays inside each other, as the value of a key no request has.
func deep(n int) string {
	return `{"x":` + strings.Repeat("[", n) + strings.Repeat("]", n) + `}`
}

func decodeCases() []decodeCase {
	return []decodeCase{
		// The top-level value.
		{"empty input", ``, false},
		{"white space only", " \n", false},
		{"empty object", `{}`, true},
		{"null", `null`, true},
		{"null in white space", " \t\r\n null \n", true},
		{"array", `[]`, false},
		{"number", `1`, false},
		{"string", `"abi"`, false},
		{"true", `true`, false},
		{"unclosed", `{`, false},
		{"unclosed after a member", `{"abi":1`, false},
		{"text after the value", `{} x`, false},
		{"two values", `{}{}`, false},
		{"a NUL after the value", "{}\x00", false},
		{"a NUL before the value", "\x00{}", false},
		{"form feed is not white space", "\f{}", false},

		// Members.
		{"white space everywhere", " { \"abi\" : 1 , \"op\" : \"x\" , \"data\" : { \"now\" : 5 } } ", true},
		{"trailing comma", `{"abi":1,}`, false},
		{"leading comma", `{,"abi":1}`, false},
		{"two commas", `{"abi":1,,"op":"x"}`, false},
		{"no comma", `{"abi":1 "op":"x"}`, false},
		{"no colon", `{"abi" 1}`, false},
		{"no value", `{"abi":}`, false},
		{"a key that is not a string", `{1:1}`, false},
		{"a key in single quotes", `{'abi':1}`, false},
		{"a key cut short", `{"abi`, false},
		{"the empty key", `{"":1}`, true},
		{"a key written with escapes", `{"\u0061bi":7,"o\u0070":"x"}`, true},
		{"a key with a bad escape", `{"a\qbi":7}`, false},
		{"a key that only starts like one", `{"abix":1,"ab":2,"a":3}`, true},
		{"a key twice, the last wins", `{"abi":1,"abi":2,"op":"a","op":"b"}`, true},
		{"members in another order", `{"data":{"now":3,"model_calls":[{"error":"e","seq":4}]},"term":{"background":"dark"},"op":"x","abi":1}`, true},
		{"one member out of order", `{"abi":1,"name":"n","op":"o","width":3}`, true},
		{"unknown keys of every kind", `{"a":1,"b":"s","c":true,"d":false,"e":null,"f":[1,[2,{"g":[]}]],"h":{"i":{"j":-1.5e+3}},"abi":4}`, true},
		{"an unknown key between known ones", `{"abi":1,"zzz":[1,2,3],"op":"x"}`, true},
		{"an unknown value that is not JSON", `{"x":[1,2,}`, false},
		{"an unknown number that is not JSON", `{"x":01}`, false},
		{"an unknown number cut short", `{"x":1.}`, false},
		{"an unknown exponent cut short", `{"x":1e}`, false},
		{"an unknown minus alone", `{"x":-}`, false},
		{"an unknown word", `{"x":nul}`, false},
		{"an unknown word in capitals", `{"x":TRUE}`, false},
		{"unknown numbers of every form", `{"x":[0,-0,1.5,1e5,1E-5,-1.25e+10,0.0,123456789012345678901234567890]}`, true},
		{"an unknown string with a control character", "{\"x\":\"a\nb\"}", false},
		{"an unknown string with a bad escape", `{"x":"\x41"}`, false},

		// Nesting.
		{"10,000 deep", deep(maxDepth - 1), true},
		{"10,001 deep", deep(maxDepth), false},

		// Strings.
		{"every escape", `{"op":"\" \\ \/ \b \f \n \r \t \u00e9 \u0000 \u2028"}`, true},
		{"an escape in capitals", `{"op":"\u00E9\u00e9"}`, true},
		{"a pair of surrogates", `{"op":"\ud83d\ude42"}`, true},
		{"half a pair", `{"op":"\ud83d"}`, true},
		{"the second half alone", `{"op":"\ude42 x"}`, true},
		{"a first half and a letter", `{"op":"\ud83d\u0041"}`, true},
		{"two first halves", `{"op":"\ud83d\ud83d\ude42"}`, true},
		{"a first half and text", `{"op":"\ud83dabc"}`, true},
		{"a first half at the end", `{"op":"\ud83d\`, false},
		{"an escape cut short", `{"op":"\u00e"}`, false},
		{"an escape that is not hex", `{"op":"\u00zz"}`, false},
		{"an unknown escape", `{"op":"\q"}`, false},
		{"a single quote escaped", `{"op":"\'"}`, false},
		{"a backslash at the end", `{"op":"\`, false},
		{"a string cut short", `{"op":"abc`, false},
		{"a control character", "{\"op\":\"a\tb\"}", false},
		{"a newline", "{\"op\":\"a\nb\"}", false},
		{"DEL is an ordinary character", "{\"op\":\"a\x7fb\"}", true},
		{"characters of two, three and four bytes", `{"op":"é ⎇ 日本語 🙂"}`, true},
		{"a byte that is not UTF-8", "{\"op\":\"a\xffb\"}", true},
		{"a character cut short", "{\"op\":\"a\xe6\x97\"}", true},
		{"a character cut short before an escape", "{\"op\":\"a\xe6\x97\\n\xa5\"}", true},
		{"an overlong character", "{\"op\":\"\xc0\xaf\"}", true},
		{"a surrogate written as bytes", "{\"op\":\"\xed\xa0\x80\"}", true},
		{"a string of 7, 8, 9, 15, 16 and 17", `{"args":["1234567","12345678","123456789","123456789012345","1234567890123456","12345678901234567"]}`, true},
		{"a quote at every place in a word", `{"args":["\"1234567","1\"234567","12\"34567","123\"4567","1234\"567","12345\"67","123456\"7","1234567\""]}`, true},
		{"escapes one after another", `{"op":"\n\n\"\"\\\\\/\b\f\r\t","name":"x"}`, true},
		{"long escapes one after another", `{"op":"\u0001\u001f\u007f\u0080\u07ff\u0800\uffff\u0000","name":"x"}`, true},
		{"long and short escapes mixed with text", `{"op":"a\u0001b\nc\u00e9\"d\u2028\\e\u001b[31m","name":"x"}`, true},
		{"long escapes in capitals and not", `{"op":"\u00AB\u00ab\uABCD\uabcd\uAbCd\u00Ff","name":"x"}`, true},
		{"a long escape, then half a pair, then a pair", `{"op":"\u0041\ud83d\u0042\ud83d\ude42\u0043","name":"x"}`, true},
		{"the second half of a pair after a long escape", `{"op":"\u0041\ude42\u0042","name":"x"}`, true},
		{"an escape as the last thing in the input", `{"op":"\n"}`, true},
		{"a long escape as the last thing in the input", `{"op":"\u0041"}`, true},
		{"a long escape cut off by the end of the input", `{"op":"\u004`, false},
		{"a long escape with a bad first digit", `{"op":"\ug041","name":"x"}`, false},
		{"a long escape with a bad second digit", `{"op":"\u0g41","name":"x"}`, false},
		{"a long escape with a bad third digit", `{"op":"\u00g1","name":"x"}`, false},
		{"a long escape with a bad fourth digit", `{"op":"\u004g","name":"x"}`, false},
		{"a long escape with a digit outside ASCII", "{\"op\":\"\\u00\xb1\xb1\",\"name\":\"x\"}", false},
		{"a long escape with a quote in it", `{"op":"\u00"1","name":"x"}`, false},
		{"a long escape in capitals", `{"op":"\U0041","name":"x"}`, false},
		{"an unknown escape after a known one", `{"op":"\n\q","name":"x"}`, false},
		{"an escaped byte outside ASCII", "{\"op\":\"\\\xe9\",\"name\":\"x\"}", false},
		{"an escaped NUL", "{\"op\":\"\\\x00\",\"name\":\"x\"}", false},
		{"text outside ASCII between escapes", `{"op":"é\né\u0001é\"é","name":"日本語\t日本語"}`, true},
		{"a character cut short before a long escape", "{\"op\":\"a\xe6\x97\\u0041\xa5\",\"name\":\"x\"}", true},
		{"a string where a number belongs", `{"abi":"1"}`, false},
		{"a number where a string belongs", `{"op":1}`, false},
		{"a list where a string belongs", `{"op":["x"]}`, false},
		{"an object where a string belongs", `{"op":{}}`, false},
		{"true where a string belongs", `{"op":true}`, false},

		// Numbers.
		{"zero", `{"abi":0,"width":0,"data":{"now":0,"contact":{"id":0}}}`, true},
		{"minus zero", `{"abi":-0,"data":{"now":-0}}`, true},
		{"minus zero without a sign to have", `{"data":{"contact":{"id":-0}}}`, false},
		{"minus one without a sign to have", `{"data":{"contact":{"id":-1}}}`, false},
		{"below zero", `{"abi":-12,"width":-1234567890,"data":{"now":-1}}`, true},
		{"1 to 17 digits", `{"args":[],"data":{"model_calls":[{"seq":1,"ts":12,"step":123,"input":1234,"cached":12345,"output":123456,"reasoning":1234567,"ms":12345678},{"seq":123456789,"ts":1234567890,"step":12345678901,"input":123456789012,"cached":1234567890123,"output":12345678901234,"reasoning":123456789012345,"ms":1234567890123456},{"seq":12345678901234567}]}}`, true},
		{"eight nines and the next", `{"data":{"model_calls":[{"seq":99999999,"ts":100000000,"step":9999999,"input":10000000}]}}`, true},
		{"the largest of each type", `{"abi":9223372036854775807,"data":{"now":9223372036854775807,"contact":{"id":18446744073709551615}}}`, true},
		{"the smallest of each type", `{"abi":-9223372036854775808,"data":{"now":-9223372036854775808}}`, true},
		{"one more than an int holds", `{"abi":9223372036854775808}`, false},
		{"one less than an int holds", `{"abi":-9223372036854775809}`, false},
		{"one more than an int64 holds", `{"data":{"now":9223372036854775808}}`, false},
		{"one more than a uint64 holds", `{"data":{"contact":{"id":18446744073709551616}}}`, false},
		{"twenty digits that wrap", `{"data":{"contact":{"id":28446744073709551615}}}`, false},
		{"thirty digits", `{"abi":123456789012345678901234567890}`, false},
		{"a leading zero", `{"abi":01}`, false},
		{"two zeros", `{"abi":00}`, false},
		{"a leading zero on a long number", `{"data":{"now":0123456789}}`, false},
		{"minus and a leading zero", `{"abi":-01}`, false},
		{"a fraction", `{"abi":1.0}`, false},
		{"a fraction on a long number", `{"data":{"now":123456789.5}}`, false},
		{"an exponent", `{"abi":1e2}`, false},
		{"an exponent in capitals", `{"abi":1E2}`, false},
		{"an exponent on a long number", `{"data":{"now":123456789e1}}`, false},
		{"a plus sign", `{"abi":+1}`, false},
		{"minus alone", `{"abi":-}`, false},
		{"hex", `{"abi":0x10}`, false},
		{"a letter after the digits", `{"abi":12a}`, false},
		{"a slash after the digits", `{"abi":12/}`, false},
		{"a colon after the digits", `{"abi":12:}`, false},
		{"a number cut short at the end", `{"abi":12`, false},
		{"white space before a number", `{"abi": 12,"data":{"now":	34,"contact":{"id":
56}}}`, true},
		{"white space after a number", `{"abi":12 ,"width":34
}`, true},
		{"true where a number belongs", `{"abi":true}`, false},
		{"a list where a number belongs", `{"abi":[1]}`, false},

		// true, false and null.
		{"true and false", `{"data":{"state":{"replying":true}}}`, true},
		{"false after true", `{"data":{"state":{"replying":true,"replying":false}}}`, true},
		{"a number where true belongs", `{"data":{"state":{"replying":1}}}`, false},
		{"a string where true belongs", `{"data":{"state":{"replying":"true"}}}`, false},
		{"true cut short", `{"data":{"state":{"replying":tru}}}`, false},
		{"true run on", `{"data":{"state":{"replying":truex}}}`, false},
		{"null for every kind of value", `{"abi":null,"op":null,"name":null,"args":null,"width":null,"term":null,"data":null,"item":null}`, true},
		{"null inside data", `{"data":{"contact":null,"route":null,"state":null,"model_calls":null,"messages":null,"workspace":null,"now":null}}`, true},
		{"null after a value leaves a number and a string", `{"abi":3,"abi":null,"op":"x","op":null}`, true},
		{"null after a value clears a pointer and a list", `{"item":{"kind":"k"},"item":null,"args":["a"],"args":null}`, true},
		{"null as an element", `{"args":["a",null,"c"],"data":{"model_calls":[null,{"seq":1},null]}}`, true},
		{"null for the fields of a record", `{"data":{"model_calls":[{"seq":null,"ts":null,"step":null,"route":null,"error":null}],"state":{"replying":null,"conn":null}}}`, true},
		{"null cut short", `{"abi":nul}`, false},
		{"null run on", `{"abi":nullx}`, false},

		// Objects and lists of the wrong kind.
		{"a list where an object belongs", `{"term":[]}`, false},
		{"a string where an object belongs", `{"data":"x"}`, false},
		{"an object where a list belongs", `{"args":{}}`, false},
		{"a number where a pointer belongs", `{"item":5}`, false},
		{"a number as an element", `{"args":[1]}`, false},
		{"a string as a record", `{"data":{"model_calls":["x"]}}`, false},

		// Lists.
		{"an empty list is not nil", `{"args":[],"data":{"model_calls":[],"messages":[]}}`, true},
		{"an empty list with white space", `{"args":[ ],"data":{"model_calls":[
]}}`, true},
		{"white space in a list", `{"args":[ "a" , "b" ],"data":{"model_calls":[ {"seq":1} , {"seq":2} ]}}`, true},
		{"a trailing comma in a list", `{"args":["a",]}`, false},
		{"a leading comma in a list", `{"args":[,"a"]}`, false},
		{"no comma in a list", `{"args":["a" "b"]}`, false},
		{"a list cut short", `{"args":["a"`, false},
		{"a list closed as an object", `{"args":["a"}}`, false},
		{"more elements than there is room for", `{"args":["a","b","c","d","e","f","g","h","i","j"]}`, true},

		// A key that comes again, where encoding/json reads the second
		// value over the first, part by part.
		{"an object again keeps what the second leaves out", `{"term":{"color":"16","glyphs":"ascii"},"term":{"color":"256"}}`, true},
		{"a pointer again keeps what the second leaves out", `{"item":{"kind":"k","text":"t"},"item":{"text":"u"}}`, true},
		{"a list again is as long as the second", `{"args":["a","b","c"],"args":["d"]}`, true},
		{"a longer list again", `{"args":["a"],"args":["b","c","d","e","f","g"]}`, true},
		{"records again keep what the second leaves out", `{"data":{"model_calls":[{"seq":1,"route":"r"},{"seq":2,"error":"e"}],"model_calls":[{"ts":5}]}}`, true},
		{"what a longer list left behind is read over", `{"args":["a","b","c"],"args":["d"],"args":[null,null,null,null]}`, true},
		{"records left behind are read over", `{"data":{"model_calls":[{"seq":1},{"seq":2,"route":"r"},{"seq":3}],"model_calls":[{"seq":4}],"model_calls":[{"ts":5},{"ts":6},{"ts":7},{"ts":8}]}}`, true},
		{"an empty list forgets what was left behind", `{"args":["a","b"],"args":[],"args":[null,null]}`, true},
		{"null forgets what was left behind", `{"args":["a","b"],"args":null,"args":[null,null]}`, true},
		{"a list that outgrows its room, again", `{"args":["a","b","c","d","e","f"],"args":["g"],"args":[null,null,null,null,null,null,null,null]}`, true},
	}
}

// TestDecode puts each input through the decoder and through encoding/json:
// they must agree, and on what the table says.
func TestDecode(t *testing.T) {
	t.Parallel()
	for _, tc := range decodeCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, ok := agree(t, []byte(tc.in)); ok != tc.ok {
				t.Errorf("accepted: %v, want %v\ninput: %s", ok, tc.ok, clip([]byte(tc.in)))
			}
		})
	}
}

// TestDecodeValues checks what was read, not only that encoding/json reads
// the same.
func TestDecodeValues(t *testing.T) {
	t.Parallel()
	in := `{"abi":1,"op":"a\"b\\c\n\u00e9\ud83d\ude42\ud83d","name":"x` + "\xff" + `y","args":["a",null,""],"width":-5,` +
		`"term":{"color":"256"},"term":{"glyphs":"ascii"},` +
		`"data":{"contact":{"id":18446744073709551615,"name":"ada"},"state":{"replying":true},` +
		`"model_calls":[{"seq":12345678,"ts":-9223372036854775808,"step":0,"input":123456789012345,"route":"r"}],` +
		`"messages":[],"workspace":null,"now":1760270765000},"item":{"kind":"k"},"later":{"abi":2}}`
	want := wire.Request{
		ABI: 1, Op: "a\"b\\c\né🙂\ufffd", Name: "x\ufffdy", Args: []string{"a", "", ""}, Width: -5,
		Term: wire.Term{Color: "256", Glyphs: "ascii"},
		Data: wire.Data{
			Contact: &wire.Contact{ID: math.MaxUint64, Name: "ada"},
			State:   &wire.State{Replying: true},
			ModelCalls: []wire.ModelCall{
				{Seq: 12345678, TS: math.MinInt64, Input: 123456789012345, Route: "r"},
			},
			Messages: []wire.Message{},
			Now:      1760270765000,
		},
		Item: &wire.Item{Kind: "k"},
	}
	got, ok := agree(t, []byte(in))
	if !ok || !reflect.DeepEqual(got, want) {
		t.Errorf("decode() = %s, %v\nwant %s", show(got), ok, show(want))
	}
}

// TestDecodeKeysAreExact pins the one place the decoder is stricter than
// encoding/json, which matches a key in any case.
func TestDecodeKeysAreExact(t *testing.T) {
	t.Parallel()
	for _, in := range []string{
		`{"ABI":7,"Op":"x","DATA":{"now":5}}`,
		`{"ABi":7,"aBi":1E700}`,
		`{"data":{"Now":5,"model_calls":[{"SEQ":3,"\u212aey":1}]},"abI":7,"oP":"x"}`,
	} {
		got, ok := byHand([]byte(in))
		if !ok {
			t.Errorf("%s was refused; a key in another case is one the decoder does not know", in)
			continue
		}
		got.Data.ModelCalls = nil
		if !reflect.DeepEqual(got, wire.Request{}) {
			t.Errorf("%s was read as %s; want nothing read", in, show(got))
		}
		if folded, _ := byReflection([]byte(in)); folded.ABI != 7 {
			t.Errorf("encoding/json read %s as %s: the test no longer shows a difference", in, show(folded))
		}
	}
}

// TestDecoderIsKeptBetweenRequests reads different requests with one decoder,
// as a module does: what it remembers of one must not show in the next.
func TestDecoderIsKeptBetweenRequests(t *testing.T) {
	t.Parallel()
	var d decoder
	// Strings that fall in the same place in the decoder's memory of
	// strings: equally long, with the same first and last byte.
	for n := 1; n <= 40; n++ {
		for at := range n {
			a := []byte(strings.Repeat("a", n))
			b := []byte(strings.Repeat("a", n))
			b[at] = 'b'
			if at == 0 || at == n-1 {
				// The ends decide the place; change a second byte so
				// that both ends differ and still collide sometimes.
				b[n-1-at] = 'c'
			}
			for _, s := range [][]byte{a, b, a} {
				var req wire.Request
				in := `{"op":"` + string(s) + `","name":"` + string(s) + `"}`
				if !d.decode([]byte(in), &req) || req.Op != string(s) || req.Name != string(s) {
					t.Fatalf("after other requests, %s was read as op %q, name %q", in, req.Op, req.Name)
				}
			}
		}
	}
	for range 3 {
		for name, want := range requests() {
			data := written(t, want)
			var got wire.Request
			if !d.decode(data, &got) {
				t.Fatalf("%s: refused", name)
			}
			if fresh, _ := byReflection(data); !reflect.DeepEqual(got, fresh) {
				t.Fatalf("%s: a decoder that had read other requests read\n got %s\nwant %s", name, show(got), show(fresh))
			}
		}
	}
	// A refused request leaves the decoder fit for the next.
	var req wire.Request
	if d.decode([]byte(`{"op":"x`), &req) {
		t.Fatal("a string cut short was accepted")
	}
	req = wire.Request{}
	if !d.decode([]byte(`{"op":"y"}`), &req) || req.Op != "y" {
		t.Fatalf("after a refused request: %+v", req)
	}
}

// TestDecodedStringsAreCopies makes sure nothing read keeps a hold on the
// input, which is a buffer the host writes the next request into.
func TestDecodedStringsAreCopies(t *testing.T) {
	t.Parallel()
	want := awkward()
	data := written(t, want)
	var d decoder
	var got wire.Request
	if !d.decode(data, &got) {
		t.Fatal("refused")
	}
	before := show(got)
	for i := range data {
		data[i] = 'x'
	}
	var other wire.Request
	d.decode(written(t, fullRequest()), &other)
	if after := show(got); after != before {
		t.Errorf("the request changed when its input was overwritten\n was %s\n now %s", before, after)
	}
}

// naiveScan is scan a byte at a time.
func naiveScan(b []byte, i int, high uint64) int {
	for ; i < len(b); i++ {
		if c := b[i]; c < 0x20 || c == '"' || c == '\\' || c >= 0x80 && high != 0 {
			return i
		}
	}
	return i
}

// TestScan puts every byte value at every place of a word, among plain
// characters and among bytes that are nearly special.
func TestScan(t *testing.T) {
	t.Parallel()
	for _, fill := range []byte{'a', 0x7f, 0x20, 0x21, 0x23, 0x5b, 0x5d} {
		for n := range 20 {
			for at := range n {
				for c := range 256 {
					b := []byte(strings.Repeat(string(fill), n))
					b[at] = byte(c)
					for _, high := range []uint64{0, highs} {
						for from := range n {
							if got, want := scan(b, from, high), naiveScan(b, from, high); got != want {
								t.Fatalf("scan(%q, %d, %#x) = %d, want %d", b, from, high, got, want)
							}
						}
					}
				}
			}
		}
	}
	// Two special bytes in one word: the first one is found, whatever
	// follows it.
	for first := range 8 {
		for _, a := range []byte{'"', '\\', 0, 0x1f, 0x80, 0xff} {
			for _, z := range []byte{'"', '\\', 0, 0x1f, 0x80, 0xff, 0x21, 0x23} {
				b := []byte("aaaaaaaaaaaaaaaa")
				b[first] = a
				for i := first + 1; i < 8; i++ {
					b[i] = z
				}
				if got := scan(b, 0, highs); got != first {
					t.Fatalf("scan(%q) = %d, want %d", b, got, first)
				}
			}
		}
	}
}

// TestDigits reads numbers of every length up to eight, followed by each
// character that could be taken for a digit.
func TestDigits(t *testing.T) {
	t.Parallel()
	numbers := []string{
		"", "0", "7", "9", "10", "42", "99", "100", "999", "1000", "12345", "99999", "123456", "999999",
		"1234567", "9999999", "12345678", "99999999", "00000000", "0000", "09", "10000000", "90000009",
	}
	followers := []byte{',', '}', ']', ' ', '.', 'e', 'E', '-', '/', ':', '"', 0, 0x7f, 0x80, 0xb0, 0xb9, 0xff}
	for _, num := range numbers {
		for _, f := range followers {
			in := num + strings.Repeat(string(f), 8)
			n, v := digits(word([]byte(in)))
			want, _ := strconv.ParseUint("0"+num, 10, 64)
			if n != len(num) || v != want {
				t.Errorf("digits(%q) = %d digits, value %d; want %d, %d", in, n, v, len(num), want)
			}
		}
	}
	// Whatever follows the digits changes nothing.
	for _, tail := range []string{"9999999", ",123456", "\xff\xff\xff\xff\xff\xff\xff", "e5,\"a\":1"} {
		in := "5" + tail[:0] + "," + tail
		if n, v := digits(word([]byte(in)[:8])); n != 1 || v != 5 {
			t.Errorf("digits(%q) = %d, %d; want 1, 5", in, n, v)
		}
	}
}

// TestCount reads whole numbers of 1 to 20 digits at every place in a
// request where the quick reader's room runs out.
func TestCount(t *testing.T) {
	t.Parallel()
	for digitsIn := 1; digitsIn <= 20; digitsIn++ {
		for _, lead := range []string{"1", "9"} {
			num := lead + strings.Repeat("0", digitsIn-1)
			for pad := range 20 {
				// The number is pad bytes from the end of the input.
				in := `{"data":{"contact":{"id":` + num + `}}` + strings.Repeat(" ", pad) + `}`
				got, ok := agree(t, []byte(in))
				want, err := strconv.ParseUint(num, 10, 64)
				if ok != (err == nil) || ok && got.Data.Contact.ID != want {
					t.Fatalf("%s: read %v, %v; want %d, %v", in, got.Data.Contact, ok, want, err)
				}
			}
		}
	}
}

// TestQuick compares quick with comparing the bytes, for patterns of every
// length it takes.
func TestQuick(t *testing.T) {
	t.Parallel()
	const letters = "abcdefghijklmnopqrstuvwxyz"
	for n := 4; n <= 16; n++ {
		pat := letters[:n]
		for at := range n + 1 {
			// at == n is the pattern itself; any other at is the
			// pattern with one byte changed.
			text := []byte(pat)
			if at < n {
				text[at] ^= 0x40
			}
			for _, fresh := range []bool{true, false} {
				for _, comma := range []bool{true, false} {
					for _, room := range []int{0, 1, 20} {
						in := string(text) + strings.Repeat("1", room)
						if comma {
							in = "," + in
						}
						d := decoder{b: []byte(in), fresh: fresh}
						// It wants one byte more than it reads, and after
						// a comma that byte is not always there to need.
						want := at == n && comma != fresh && (room > 0 || comma)
						got := d.quick(pat)
						if got != want {
							t.Fatalf("quick(%q) on %q, fresh %v: %v, want %v", pat, in, fresh, got, want)
						}
						if end := len(in) - room; got && (d.i != end || d.fresh) {
							t.Fatalf("quick(%q) on %q left the decoder at %d, fresh %v; want %d, false", pat, in, d.i, d.fresh, end)
						}
						if !got && (d.i != 0 || d.fresh != fresh) {
							t.Fatalf("quick(%q) on %q read something and reported false", pat, in)
						}
					}
				}
			}
		}
	}
}

// TestTables checks the four tables the decoder reads strings by against
// what each stands for, byte by byte.
func TestTables(t *testing.T) {
	t.Parallel()
	if len(escaped) != 256 || len(hexPlus) != 256 || len(lead) != 256 || len(kinds) != 36 {
		t.Fatalf("the tables are %d, %d, %d and %d bytes; want 256, 256, 256 and 36", len(escaped), len(hexPlus), len(lead), len(kinds))
	}
	stands := map[byte]byte{'"': '"', '\\': '\\', '/': '/', 'b': '\b', 'f': '\f', 'n': '\n', 'r': '\r', 't': '\t'}
	for c := range 256 {
		if got, want := escaped[c], stands[byte(c)]; got != want {
			t.Errorf("escaped[%q] = %q, want %q", byte(c), got, want)
		}
		want := byte(0)
		if v, err := strconv.ParseUint(string(rune(c)), 16, 8); err == nil && c < 0x80 {
			want = byte(v) + 1
		}
		if got := hexPlus[c]; got != want {
			t.Errorf("hexPlus[%q] = %d, want %d", byte(c), got, want)
		}
		// The first byte of a character says how long the character is,
		// and utf8 knows that of the first character it stands for.
		at := int(lead[c])
		if at%4 != 0 || at >= len(kinds) {
			t.Fatalf("lead[%#x] = %d: not a place in kinds", c, at)
		}
		size, none := int(kinds[at]), kinds[at+3] == 0x80
		wantSize, wantNone := 1, true
		for _, tail := range []string{"", "\x80", "\xa0", "\x90", "\x80\x80", "\xa0\x80", "\x90\x80\x80", "\x80\x80\x80"} {
			if r, n := utf8.DecodeRuneInString(string([]byte{byte(c)}) + tail); r != utf8.RuneError {
				wantSize, wantNone = n, false
				break
			}
		}
		if size != wantSize || none != wantNone {
			t.Errorf("first byte %#x: %d bytes, starts none: %v; want %d, %v", c, size, none, wantSize, wantNone)
		}
	}
}

// validCase holds valid to utf8.Valid for one run of bytes, wherever in the
// input the run is: at its end, and before a quote with more after it.
func validCase(t *testing.T, run []byte) {
	t.Helper()
	want := utf8.Valid(run)
	for _, before := range []string{"", "ab", "é"} {
		for _, after := range []string{"", `"`, `"}`, `"}}`, `"}}}`, "\\n\"}}}", "\x01\x80\x80\x80"} {
			b := append(append([]byte(before), run...), after...)
			i, j := len(before), len(before)+len(run)
			if got := valid(b, i, j); got != want {
				t.Fatalf("valid(%q, %d, %d) = %v; utf8.Valid(%q) = %v", b, i, j, got, run, want)
			}
		}
	}
}

// TestValid puts every sequence of one and two bytes that a run can hold
// through valid, and the longer ones with each byte at the edges of what is
// allowed. A run holds no quote, no backslash and no control character.
func TestValid(t *testing.T) {
	t.Parallel()
	inRun := func(c int) bool { return c >= 0x20 && c != '"' && c != '\\' }
	edges := []byte{' ', 0x7f, 0x80, 0x8f, 0x90, 0x9f, 0xa0, 0xbf, 0xc0, 0xc2, 0xe0, 0xed, 0xf0, 0xf4, 0xf5, 0xff}
	validCase(t, nil)
	for a := range 256 {
		if !inRun(a) {
			continue
		}
		validCase(t, []byte{byte(a)})
		for b := range 256 {
			if !inRun(b) {
				continue
			}
			validCase(t, []byte{byte(a), byte(b)})
			if a < 0xc0 {
				continue
			}
			// Every second byte, before a third that is and is not one.
			for _, c := range []byte{0x7f, 0x80, 0xbf, 0xc0} {
				validCase(t, []byte{byte(a), byte(b), c})
			}
		}
		if a < 0xc0 {
			continue
		}
		for _, b := range edges {
			for _, c := range edges {
				validCase(t, []byte{'x', byte(a), b, c, 'y'})
				if a < 0xf0 {
					continue
				}
				for _, d := range edges {
					validCase(t, []byte{byte(a), b, c, d})
					validCase(t, []byte{byte(a), b, c, d, byte(a), b, c, d})
				}
			}
		}
	}
	for _, text := range []string{
		"é ⎇ 日本語 🙂", strings.Repeat("語", 300), strings.Repeat("🙂", 70) + "\xf0\x9f\x99", strings.Repeat("é", 99) + "\xc3",
		"plain", "\xed\xa0\x80", "\xf4\x90\x80\x80", "\xf0\x8f\xbf\xbf", "\xe0\x9f\xbf", "\xc0\xaf", "ok\xff",
	} {
		validCase(t, []byte(text))
	}
}

// TestSame compares same with comparing the strings, for every length it is
// given and a difference at every place.
func TestSame(t *testing.T) {
	t.Parallel()
	for n := range 41 {
		s := strings.Repeat("abcdefg", 6)[:n]
		if !same([]byte(s), s) {
			t.Errorf("same(%q, itself) = false", s)
		}
		for at := range n {
			other := []byte(s)
			other[at] ^= 0x01
			if same(other, s) {
				t.Errorf("same(%q, %q) = true", other, s)
			}
		}
		if n > 0 && (same([]byte(s[:n-1]), s) || same([]byte(s+"x"), s)) {
			t.Errorf("same() of %q and a string of another length = true", s)
		}
	}
}

// replies are the replies the encoder's tests write.
func replies() map[string]wire.Reply {
	lines := make([]wire.Line, 0, 300)
	for i := range 300 {
		lines = append(lines, wire.Line{{T: "line " + strconv.Itoa(i), Tone: "accent", Bold: i%2 == 0}})
	}
	return map[string]wire.Reply{
		"nothing": {},
		"hello":   {ABI: wire.ABI},
		"segment": {ABI: wire.ABI, Text: "cache avg 61%", Short: "61%", Tone: "warn"},
		"every field": {
			ABI: wire.ABI, Text: "t", Short: "s", Tone: "ok",
			Lines:  []wire.Line{{{T: "11 Oct", Tone: "dim"}, {T: "  14 calls  "}, {T: "61%", Tone: "ok", Bold: true}}, {}, nil, {{}}},
			Insert: "i", Note: "n", Pass: true, Error: "e",
		},
		"another abi":  {ABI: -7, Pass: true},
		"empty lines":  {Lines: []wire.Line{}},
		"a nil line":   {Lines: []wire.Line{nil}},
		"many lines":   {Lines: lines},
		"quotes":       {Text: `say "hi" to C:\temp\new`, Short: `\`, Tone: `"`, Note: `""`, Error: `\\`},
		"controls":     {Text: "a\x00b\x01c\x1fd\x7fe", Insert: "one\ntwo\r\nthree\tfour", Note: "\x1b[31mred\x1b[0m\x07"},
		"outside":      {Text: "é ⎇ 日本語 🙂 \u2028\u2029", Short: "<b>&amp;</b>", Note: strings.Repeat("語", 3000)},
		"not UTF-8":    {Text: "a\xffb", Short: "\xff", Note: "ok\xc3", Insert: "\xed\xa0\x80", Error: "é\xffé\xe6\x97"},
		"late failure": {Text: strings.Repeat("語", 2000) + "\xff", Note: strings.Repeat("é", 2000) + "\xc3"},
		"long":         {Text: strings.Repeat("0123456789abcdef", 5000)},
		"word edges": {Lines: []wire.Line{{
			{T: "1234567"}, {T: "12345678"}, {T: "123456789"}, {T: `1234567"`}, {T: `"`}, {T: "1234567\n"}, {T: "1234567é"},
		}}},
	}
}

// checkReply fails the test unless the encoder wrote r as JSON that reads
// back as what encoding/json's own output reads back as.
func checkReply(t *testing.T, r wire.Reply) []byte {
	t.Helper()
	got := appendReply(nil, &r)
	if !utf8.Valid(got) {
		t.Fatalf("the reply is not UTF-8: %q", got)
	}
	if !json.Valid(got) {
		t.Fatalf("the reply is not JSON: %s", clip(got))
	}
	for _, c := range got {
		if c < 0x20 {
			t.Fatalf("the reply holds the control character %#x: %s", c, clip(got))
		}
	}
	reference, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var back, want wire.Reply
	if err := json.Unmarshal(got, &back); err != nil {
		t.Fatalf("the host cannot read the reply: %v\n%s", err, clip(got))
	}
	if err := json.Unmarshal(reference, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, want) {
		t.Fatalf("the reply reads back as\n got %+v\nwant %+v\nfrom %s", back, want, clip(got))
	}
	return got
}

func TestAppendReply(t *testing.T) {
	t.Parallel()
	for name, r := range replies() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := checkReply(t, r)
			// It appends, and leaves what was there.
			if again := appendReply([]byte("kept"), &r); string(again) != "kept"+string(got) {
				t.Errorf("appendReply onto %q gave %s", "kept", clip(again))
			}
		})
	}
}

// TestAppendReplyBytes pins the bytes of the usual replies: they are what
// encoding/json writes too.
func TestAppendReplyBytes(t *testing.T) {
	t.Parallel()
	for _, r := range []wire.Reply{
		{ABI: 1},
		{ABI: 1, Text: "cache avg 61%", Short: "61%", Tone: "warn"},
		{ABI: 1, Text: "⎇ main", Short: "main"},
		{ABI: 1, Insert: "Summarize what we did yesterday (Sun 11 Oct) for the team: done, next, blocked."},
		{ABI: 1, Lines: []wire.Line{{{T: "Sat 10 Oct", Tone: "dim"}, {T: "  1 call  9.0k in  "}, {T: "cache 20%", Tone: "warn", Bold: true}}, {}, nil}},
		{ABI: 1, Pass: true},
		{ABI: 1, Note: "no model calls to count yet", Error: "usage: /stats [days]"},
		{ABI: 1, Text: "a \"quoted\" back\\slash\nnew line\ttab \x01"},
	} {
		want, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if got := appendReply(nil, &r); string(got) != string(want) {
			t.Errorf("appendReply() = %s\n   encoding/json: %s", got, want)
		}
	}
}

// folds reports whether data has a string that encoding/json would take for
// a key of a request and the decoder would not: the same letters in another
// case. It looks at every string, key or not, since a key that comes twice
// hides its first value from anything that reads the whole input into a
// tree.
func folds(data []byte) bool {
	known := []string{
		"abi", "op", "name", "args", "width", "term", "data", "item", "color", "glyphs", "background",
		"contact", "route", "state", "model_calls", "messages", "workspace", "now", "id", "provider", "model",
		"conn", "replying", "running", "seq", "ts", "step", "input", "cached", "output", "reasoning", "ms",
		"error", "role", "text", "dir", "branch", "kind",
	}
	// Numbers are kept as written: one too large for a float is still
	// JSON.
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	for {
		token, err := dec.Token()
		if err != nil {
			return false
		}
		text, ok := token.(string)
		if !ok {
			continue
		}
		for _, name := range known {
			if text != name && strings.EqualFold(text, name) {
				return true
			}
		}
	}
}

// FuzzDecode holds the decoder to encoding/json on any input: the same
// verdict and the same request, and never a panic.
func FuzzDecode(f *testing.F) {
	for _, tc := range decodeCases() {
		if len(tc.in) < 4096 {
			f.Add([]byte(tc.in))
		}
	}
	f.Add([]byte(`{"ABi":1E700}`))
	f.Add([]byte(`{"data":{"model_calls":[{"routE":0}],"model_calls":[]}}`))
	for _, req := range requests() {
		if data, err := json.Marshal(req); err == nil && len(data) < 4096 {
			f.Add(data)
		}
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		got, ok := byHand(data)
		if folds(data) {
			// The one known difference; see TestDecodeKeysAreExact.
			return
		}
		want, wantOK := byReflection(data)
		if ok != wantOK {
			t.Fatalf("decode accepted: %v; encoding/json accepted: %v\ninput: %q", ok, wantOK, data)
		}
		if !ok {
			return
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("decode and encoding/json differ\n got %s\nwant %s\ninput: %q", show(got), show(want), data)
		}
		// A decoder that has read other requests reads this one the same.
		var d decoder
		var warm, again wire.Request
		d.decode([]byte(`{"op":"segment","name":"cache","term":{"color":"256","glyphs":"ascii","background":"dark"}}`), &warm)
		if !d.decode(data, &again) || !reflect.DeepEqual(again, want) {
			t.Fatalf("a decoder that had read another request differs\n got %s\nwant %s\ninput: %q", show(again), show(want), data)
		}
	})
}

// FuzzAppendReply holds the encoder to encoding/json for any strings: what
// it writes is JSON and reads back the same.
func FuzzAppendReply(f *testing.F) {
	f.Add("cache avg 61%", "61%", "warn", "line", true, false, uint8(3))
	f.Add("a\"b\\c\nd", "\x00\x1f", "\xff", "é⎇🙂", false, true, uint8(0))
	f.Add("1234567\xff", "12345678\"", "語語語\xe6", "\u2028", true, true, uint8(200))
	f.Fuzz(func(t *testing.T, text, short, tone, span string, bold, pass bool, lines uint8) {
		r := wire.Reply{ABI: wire.ABI, Text: text, Short: short, Tone: tone, Insert: span, Note: short + text, Pass: pass, Error: tone}
		for i := range int(lines) {
			r.Lines = append(r.Lines, wire.Line{{T: span, Tone: tone, Bold: bold}, {T: text}, {T: strconv.Itoa(i)}})
		}
		checkReply(t, r)
	})
}

// FuzzScan holds scan to the same search a byte at a time.
func FuzzScan(f *testing.F) {
	f.Add([]byte(`plain text, then a "quote"`), uint8(0))
	f.Add([]byte("12345678\\"), uint8(3))
	f.Add([]byte("é\x00\x1f\x7f\x80\xff"), uint8(1))
	f.Fuzz(func(t *testing.T, b []byte, from uint8) {
		i := min(int(from), len(b))
		for _, high := range []uint64{0, highs} {
			if got, want := scan(b, i, high), naiveScan(b, i, high); got != want {
				t.Fatalf("scan(%q, %d, %#x) = %d, want %d", b, i, high, got, want)
			}
		}
	})
}

// FuzzValid holds valid to utf8.Valid for any run a string can hold: from
// any place, up to the next byte the decoder has to look at.
func FuzzValid(f *testing.F) {
	f.Add([]byte("é ⎇ 日本語 🙂\" and more"), uint8(0))
	f.Add([]byte("ab\xe6\x97\"\xa5"), uint8(2))
	f.Add([]byte("\xf0\x9f\x99\x82\xf4\x90\x80\x80\xed\xa0\x80"), uint8(1))
	f.Fuzz(func(t *testing.T, b []byte, from uint8) {
		i := min(int(from), len(b))
		j := scan(b, i, 0)
		if got, want := valid(b, i, j), utf8.Valid(b[i:j]); got != want {
			t.Fatalf("valid(%q, %d, %d) = %v, want %v", b, i, j, got, want)
		}
	})
}

// BenchmarkDecode times the decoder outside a module, where a jump costs
// nothing. Inside one it is slower and the gap to encoding/json is wider;
// BenchmarkExamples in package mods measures that.
func BenchmarkDecode(b *testing.B) {
	data := written(b, manyCalls(50))
	b.Run("by hand", func(b *testing.B) {
		var d decoder
		b.SetBytes(int64(len(data)))
		b.ReportAllocs()
		for b.Loop() {
			var req wire.Request
			if !d.decode(data, &req) {
				b.Fatal("refused")
			}
		}
	})
	b.Run("encoding/json", func(b *testing.B) {
		b.SetBytes(int64(len(data)))
		b.ReportAllocs()
		for b.Loop() {
			var req wire.Request
			if err := json.Unmarshal(data, &req); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// BenchmarkAppendReply times the encoder outside a module.
func BenchmarkAppendReply(b *testing.B) {
	r := replies()["many lines"]
	var buf []byte
	b.ReportAllocs()
	for b.Loop() {
		buf = appendReply(buf[:0], &r)
	}
}
