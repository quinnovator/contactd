package wire_test

import (
	"bytes"
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"testing"

	"github.com/quinnovator/contactd/internal/tui/mods/wire"
	"github.com/quinnovator/contactd/internal/tui/tuitest"
)

func TestMain(m *testing.M) { tuitest.Main(m) }

// fullRequest sets every field of a request, so the golden file names every
// key of the contract.
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

// fullReply sets every field of a reply. No real reply uses them all at
// once; the golden file is the list of keys.
func fullReply() wire.Reply {
	return wire.Reply{
		ABI:   wire.ABI,
		Text:  "cache avg 61%",
		Short: "61%",
		Tone:  "warn",
		Lines: []wire.Line{
			{{T: "11 Oct", Tone: "dim"}, {T: "  14 calls  "}, {T: "61%", Tone: "ok", Bold: true}},
			{},
			{{T: "<&> stays as written"}},
		},
		Insert: "Summarize what we did yesterday (Sun 11 Oct) for the team: done, next, blocked.",
		Note:   "7 days",
		Pass:   true,
		Error:  "no calls yet",
	}
}

// encode writes a value as the golden files hold it: indented, and with
// nothing escaped for HTML, as a guest that writes JSON by hand would.
func encode(t *testing.T, v any) []byte {
	t.Helper()
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestGoldenRequest(t *testing.T) {
	t.Parallel()
	in := fullRequest()
	data := encode(t, in)
	tuitest.Golden(t, "request.golden.json", data)

	var back wire.Request
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, in) {
		t.Errorf("a request did not survive the round trip:\n got: %+v\nwant: %+v", back, in)
	}
}

func TestGoldenReply(t *testing.T) {
	t.Parallel()
	in := fullReply()
	data := encode(t, in)
	tuitest.Golden(t, "reply.golden.json", data)

	var back wire.Reply
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, in) {
		t.Errorf("a reply did not survive the round trip:\n got: %+v\nwant: %+v", back, in)
	}
}

// The full values really are full: a field added to the contract and left
// out of them would be missing from the golden files without this.
func TestFullValuesSetEveryField(t *testing.T) {
	t.Parallel()
	for name, v := range map[string]any{"request": fullRequest(), "reply": fullReply()} {
		if path := firstZero(reflect.ValueOf(v), name); path != "" {
			t.Errorf("%s is not set", path)
		}
	}
}

// firstZero walks a value and returns the path of the first field that is
// zero, or "". Across a slice of structs each field must be set in at least
// one element, since no single record uses every field.
func firstZero(v reflect.Value, path string) string {
	switch v.Kind() { //nolint:exhaustive // only the kinds the contract uses need a rule; the rest are compared with zero
	case reflect.Pointer:
		if v.IsNil() {
			return path
		}
		return firstZero(v.Elem(), path)
	case reflect.Struct:
		for i := range v.NumField() {
			if p := firstZero(v.Field(i), path+"."+v.Type().Field(i).Name); p != "" {
				return p
			}
		}
		return ""
	case reflect.Slice:
		if v.Len() == 0 {
			return path
		}
		if v.Type().Elem().Kind() != reflect.Struct {
			return anySet(v, path+"[]")
		}
		for i := range v.Type().Elem().NumField() {
			fields := reflect.MakeSlice(reflect.SliceOf(v.Type().Elem().Field(i).Type), 0, v.Len())
			for j := range v.Len() {
				fields = reflect.Append(fields, v.Index(j).Field(i))
			}
			if p := anySet(fields, path+"[]."+v.Type().Elem().Field(i).Name); p != "" {
				return p
			}
		}
		return ""
	default:
		if v.IsZero() {
			return path
		}
		return ""
	}
}

// anySet returns "" when one element of a slice has nothing left zero, and
// otherwise what the first element lacks.
func anySet(v reflect.Value, path string) string {
	first := path
	for i := range v.Len() {
		p := firstZero(v.Index(i), path)
		if p == "" {
			return ""
		}
		if i == 0 {
			first = p
		}
	}
	return first
}

// The check above can fail: a value with one field left out is caught.
func TestFirstZeroFindsWhatIsMissing(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		change func(*wire.Request, *wire.Reply)
		want   string
	}{
		{name: "a string", change: func(r *wire.Request, _ *wire.Reply) { r.Name = "" }, want: "request.Name"},
		{name: "a nested string", change: func(r *wire.Request, _ *wire.Reply) { r.Term.Glyphs = "" }, want: "request.Term.Glyphs"},
		{name: "a nil pointer", change: func(r *wire.Request, _ *wire.Reply) { r.Data.Route = nil }, want: "request.Data.Route"},
		{name: "behind a pointer", change: func(r *wire.Request, _ *wire.Reply) { r.Item.Kind = "" }, want: "request.Item.Kind"},
		{name: "an empty slice", change: func(r *wire.Request, _ *wire.Reply) { r.Args = nil }, want: "request.Args"},
		{
			name: "a field no record sets",
			change: func(r *wire.Request, _ *wire.Reply) {
				for i := range r.Data.ModelCalls {
					r.Data.ModelCalls[i].Cached = 0
				}
			},
			want: "request.Data.ModelCalls[].Cached",
		},
		{name: "a bool", change: func(_ *wire.Request, r *wire.Reply) { r.Pass = false }, want: "reply.Pass"},
		{
			name: "a field no span sets",
			change: func(_ *wire.Request, r *wire.Reply) {
				for _, line := range r.Lines {
					for i := range line {
						line[i].Bold = false
					}
				}
			},
			want: "reply.Lines[][].Bold",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req, rep := fullRequest(), fullReply()
			tc.change(&req, &rep)
			got := firstZero(reflect.ValueOf(req), "request")
			if got == "" {
				got = firstZero(reflect.ValueOf(rep), "reply")
			}
			if got != tc.want {
				t.Errorf("firstZero = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestWhatIsLeftOut(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   any
		want string
	}{
		{
			name: "a hello carries the version, the terminal and no data",
			in:   wire.Request{ABI: wire.ABI, Op: "hello", Term: wire.Term{Color: "none", Glyphs: "ascii", Background: "unknown"}},
			want: `{"abi":1,"op":"hello","term":{"color":"none","glyphs":"ascii","background":"unknown"},"data":{}}`,
		},
		{
			name: "a grant that was not given leaves no key",
			in: wire.Request{
				ABI: wire.ABI, Op: "segment", Name: "branch", Width: 46,
				Data: wire.Data{Workspace: &wire.Workspace{Dir: "contactd"}},
			},
			want: `{"abi":1,"op":"segment","name":"branch","width":46,"term":{"color":"","glyphs":"","background":""},"data":{"workspace":{"dir":"contactd","branch":""}}}`,
		},
		{
			name: "a reply always carries the version",
			in:   wire.Reply{},
			want: `{"abi":0}`,
		},
		{
			name: "a segment that hides itself",
			in:   wire.Reply{ABI: wire.ABI},
			want: `{"abi":1}`,
		},
		{
			name: "a renderer that passes",
			in:   wire.Reply{ABI: wire.ABI, Pass: true},
			want: `{"abi":1,"pass":true}`,
		},
		{
			name: "a plain span is its text alone",
			in:   wire.Reply{ABI: wire.ABI, Lines: []wire.Line{{{T: "main"}}, {{T: ""}}}},
			want: `{"abi":1,"lines":[[{"t":"main"}],[{"t":""}]]}`,
		},
		{
			name: "a model call keeps its zeros",
			in:   wire.ModelCall{Seq: 1},
			want: `{"seq":1,"ts":0,"step":0,"route":"","input":0,"cached":0,"output":0,"reasoning":0,"ms":0,"error":""}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := json.Marshal(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Errorf("encoded as\n%s\nwant\n%s", got, tc.want)
			}
		})
	}
}

// Unknown fields are ignored both ways, so a newer host can talk to an older
// mod and a newer mod to an older host.
func TestUnknownFieldsAreIgnored(t *testing.T) {
	t.Parallel()

	var req wire.Request
	in := `{"abi":1,"op":"segment","name":"cache","later":{"x":1},"term":{"color":"256","hyperlinks":true},
		"data":{"route":{"provider":"p","model":"m","region":"eu"},"turn":8}}`
	if err := json.Unmarshal([]byte(in), &req); err != nil {
		t.Fatal(err)
	}
	want := wire.Request{
		ABI: 1, Op: "segment", Name: "cache", Term: wire.Term{Color: "256"},
		Data: wire.Data{Route: &wire.Route{Provider: "p", Model: "m"}},
	}
	if !reflect.DeepEqual(req, want) {
		t.Errorf("request = %+v, want %+v", req, want)
	}

	var rep wire.Reply
	in = `{"abi":1,"text":"61%","panel":"side","lines":[[{"t":"a","tone":"dim","italic":true}]]}`
	if err := json.Unmarshal([]byte(in), &rep); err != nil {
		t.Fatal(err)
	}
	wantRep := wire.Reply{ABI: 1, Text: "61%", Lines: []wire.Line{{{T: "a", Tone: "dim"}}}}
	if !reflect.DeepEqual(rep, wantRep) {
		t.Errorf("reply = %+v, want %+v", rep, wantRep)
	}
}

// A reply that leaves out "abi" decodes as version 0, which the host refuses.
func TestMissingVersionIsNotTheCurrentOne(t *testing.T) {
	t.Parallel()
	var rep wire.Reply
	if err := json.Unmarshal([]byte(`{"text":"61%"}`), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.ABI == wire.ABI {
		t.Errorf("a reply without abi decoded as version %d", rep.ABI)
	}
	if wire.ABI != 1 {
		t.Errorf("ABI = %d, want 1", wire.ABI)
	}
}

// The keys of ABI 1, written out. Renaming, adding or removing one changes
// the contract with every mod already built, and -update alone must not
// accept that.
func TestKeysOfTheContract(t *testing.T) {
	t.Parallel()
	want := map[string][]string{
		"request": {
			"abi", "args", "background", "branch", "cached", "color", "conn", "contact", "data", "dir", "error",
			"glyphs", "id", "input", "item", "kind", "messages", "model", "model_calls", "ms", "name", "now", "op",
			"output", "provider", "reasoning", "replying", "role", "route", "running", "seq", "state", "step",
			"term", "text", "ts", "width", "workspace",
		},
		"reply": {"abi", "bold", "error", "insert", "lines", "note", "pass", "short", "t", "text", "tone"},
	}
	values := map[string]any{"request": fullRequest(), "reply": fullReply()}
	for name, keys := range want {
		var decoded any
		if err := json.Unmarshal(encode(t, values[name]), &decoded); err != nil {
			t.Fatal(err)
		}
		seen := map[string]bool{}
		collectKeys(decoded, seen)
		got := slices.Sorted(maps.Keys(seen))
		if !slices.Equal(got, keys) {
			t.Errorf("the %s has the keys\n%q\nwant\n%q", name, got, keys)
		}
	}
}

// collectKeys gathers every object key in a decoded JSON value.
func collectKeys(v any, into map[string]bool) {
	switch v := v.(type) {
	case map[string]any:
		for k, child := range v {
			into[k] = true
			collectKeys(child, into)
		}
	case []any:
		for _, child := range v {
			collectKeys(child, into)
		}
	}
}
