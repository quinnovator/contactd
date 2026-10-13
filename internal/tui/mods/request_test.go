package mods

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/quinnovator/contactd/internal/tui/mods/wire"
)

// fullInput is an input with every field set.
func fullInput() Input {
	return Input{
		Term:  wire.Term{Color: "truecolor", Glyphs: "unicode", Background: "dark"},
		Width: 100,
		Args:  []string{"7", "two words"},
		Item:  &wire.Item{Kind: "code:mermaid", Text: "graph TD"},
		Data: wire.Data{
			Contact:    &wire.Contact{ID: 3, Name: "ada"},
			Route:      &wire.Route{Provider: "deepseek", Model: "deepseek-flash"},
			State:      &wire.State{Conn: "live", Replying: true, Running: "python"},
			ModelCalls: []wire.ModelCall{{Seq: 1, Input: 100, Cached: 61}, {Seq: 2, Input: 50}},
			Messages:   []wire.Message{{Seq: 4, Role: "user", Text: "hello"}, {Seq: 5, Role: "agent", Text: "hi"}},
			Workspace:  &wire.Workspace{Dir: "contactd", Branch: "main"},
			Now:        1760270640000,
		},
	}
}

// with is a manifest that holds some grants and declares everything a call
// in these tests names.
func with(grants ...Grant) Manifest {
	return Manifest{
		Name: "m", Read: grants,
		Segments:  []SegmentDecl{{Name: "seg", Refresh: RefreshTurn}},
		Commands:  []CommandDecl{{Name: "go"}},
		Renderers: []RendererDecl{{Kind: "code:mermaid"}},
	}
}

func TestGrantFilter(t *testing.T) {
	t.Parallel()
	in := fullInput()
	all := in.Data
	noMessages := all
	noMessages.Messages = nil
	cases := []struct {
		name string
		man  Manifest
		op   string
		want wire.Data
	}{
		{"no grant, nothing", with(), OpCommand, wire.Data{}},
		{"contact", with(GrantContact), OpSegment, wire.Data{Contact: all.Contact}},
		{"route", with(GrantRoute), OpSegment, wire.Data{Route: all.Route}},
		{"state", with(GrantState), OpSegment, wire.Data{State: all.State}},
		{"model_calls", with(GrantModelCalls), OpSegment, wire.Data{ModelCalls: all.ModelCalls}},
		{"workspace", with(GrantWorkspace), OpSegment, wire.Data{Workspace: all.Workspace}},
		{"clock", with(GrantClock), OpSegment, wire.Data{Now: all.Now}},
		{"messages reach a command", with(GrantMessages), OpCommand, wire.Data{Messages: all.Messages}},
		{"messages do not reach a segment", with(GrantMessages), OpSegment, wire.Data{}},
		{"messages do not reach a renderer", with(GrantMessages), OpRender, wire.Data{}},
		{"every grant, for a command", with(Grants()...), OpCommand, all},
		{"every grant, for a segment", with(Grants()...), OpSegment, noMessages},
		{"every grant, for a renderer", with(Grants()...), OpRender, noMessages},
		{"every grant but messages, for a command", with(GrantContact, GrantRoute, GrantState, GrantModelCalls, GrantWorkspace, GrantClock), OpCommand, noMessages},
		{"hello carries no data", with(Grants()...), OpHello, wire.Data{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := buildRequest(tc.man, tc.op, "x", in)
			if !reflect.DeepEqual(got.Data, tc.want) {
				t.Errorf("Data:\n got %+v\nwant %+v", got.Data, tc.want)
			}
			if got.ABI != wire.ABI || got.Op != tc.op || got.Name != "x" || got.Width != 100 || got.Term != in.Term {
				t.Errorf("the request's own fields: %+v", got)
			}
		})
	}
}

func TestArgsAndItemGoOnlyWhereTheyBelong(t *testing.T) {
	t.Parallel()
	in := fullInput()
	cases := []struct {
		name     string
		man      Manifest
		op       string
		args     bool
		wantItem bool
	}{
		{"a command gets its arguments", with(Grants()...), OpCommand, true, false},
		{"a segment gets neither", with(Grants()...), OpSegment, false, false},
		{"a renderer gets the item", with(Grants()...), OpRender, false, true},
		{"a renderer without the messages grant gets no item", with(GrantClock), OpRender, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := buildRequest(tc.man, tc.op, "x", in)
			if (got.Args != nil) != tc.args {
				t.Errorf("Args = %v", got.Args)
			}
			if (got.Item != nil) != tc.wantItem {
				t.Errorf("Item = %+v", got.Item)
			}
		})
	}
}

func TestListsAreCutToTheNewest(t *testing.T) {
	t.Parallel()
	var in Input
	for i := range 120 {
		in.Data.ModelCalls = append(in.Data.ModelCalls, wire.ModelCall{Seq: uint64(i)})
		in.Data.Messages = append(in.Data.Messages, wire.Message{Seq: uint64(i), Text: strconv.Itoa(i)})
	}
	got := buildRequest(with(Grants()...), OpCommand, "go", in).Data
	if len(got.ModelCalls) != 50 || got.ModelCalls[0].Seq != 70 || got.ModelCalls[49].Seq != 119 {
		t.Errorf("model calls: %d, from %d to %d; want the newest 50, oldest first",
			len(got.ModelCalls), got.ModelCalls[0].Seq, got.ModelCalls[len(got.ModelCalls)-1].Seq)
	}
	if len(got.Messages) != 20 || got.Messages[0].Seq != 100 || got.Messages[19].Seq != 119 {
		t.Errorf("messages: %d, from %d; want the newest 20, oldest first", len(got.Messages), got.Messages[0].Seq)
	}
	if len(in.Data.ModelCalls) != 120 || len(in.Data.Messages) != 120 {
		t.Error("the caller's lists were changed")
	}
}

func TestModelCallTextIsCut(t *testing.T) {
	t.Parallel()
	page := strings.Repeat("<html>語", 2000) // a provider's error page
	in := Input{Data: wire.Data{ModelCalls: []wire.ModelCall{
		{Seq: 1, Route: "deepseek/deepseek-flash", Error: "rate_limit"},
		{Seq: 2, Route: page, Error: page, Input: 7},
		{Seq: 3},
	}}}
	got := buildRequest(with(GrantModelCalls), OpSegment, "seg", in).Data.ModelCalls
	if len(got) != 3 || got[0] != in.Data.ModelCalls[0] || got[2] != in.Data.ModelCalls[2] {
		t.Fatalf("records that were short enough were changed: %+v", got)
	}
	cut := got[1]
	for what, s := range map[string]string{"route": cut.Route, "error": cut.Error} {
		if len(s) == 0 || len(s) > MaxCallText || !utf8.ValidString(s) || !strings.HasPrefix(page, s) {
			t.Errorf("the %s is %d bytes, valid %v; want the start of it, whole characters, at most %d bytes",
				what, len(s), utf8.ValidString(s), MaxCallText)
		}
	}
	if cut.Seq != 2 || cut.Input != 7 {
		t.Errorf("the rest of the record changed: %+v", cut)
	}
	if in.Data.ModelCalls[1].Error != page || in.Data.ModelCalls[1].Route != page {
		t.Error("the caller's record was changed")
	}
}

func TestMessagesFitInEightKiB(t *testing.T) {
	t.Parallel()
	msg := func(seq uint64, n int) wire.Message {
		return wire.Message{Seq: seq, Text: strings.Repeat("m", n)}
	}
	cases := []struct {
		name     string
		in       []wire.Message
		wantSeqs []uint64
		wantLast int // bytes of the newest message kept
	}{
		{"none", nil, nil, 0},
		{"all fit", []wire.Message{msg(1, 4096), msg(2, 4096)}, []uint64{1, 2}, 4096},
		{"the oldest is dropped", []wire.Message{msg(1, 1), msg(2, 4096), msg(3, 4096)}, []uint64{2, 3}, 4096},
		{"an old one that does not fit ends the list", []wire.Message{msg(1, 10), msg(2, 8000), msg(3, 10), msg(4, 300)}, []uint64{3, 4}, 300},
		{"the newest alone is too long, and is cut", []wire.Message{msg(1, 10), msg(2, 9000)}, []uint64{2}, MaxMessagesBytes},
		{"empty messages are kept", []wire.Message{msg(1, 0), msg(2, 0)}, []uint64{1, 2}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := capMessages(tc.in)
			var seqs []uint64
			total := 0
			for _, m := range got {
				seqs = append(seqs, m.Seq)
				total += len(m.Text)
			}
			if !reflect.DeepEqual(seqs, tc.wantSeqs) {
				t.Errorf("kept %v, want %v", seqs, tc.wantSeqs)
			}
			if total > MaxMessagesBytes {
				t.Errorf("kept %d bytes of text, over the limit", total)
			}
			if len(got) > 0 && len(got[len(got)-1].Text) != tc.wantLast {
				t.Errorf("the newest message has %d bytes, want %d", len(got[len(got)-1].Text), tc.wantLast)
			}
		})
	}
}

func TestACutMessageIsStillText(t *testing.T) {
	t.Parallel()
	// 3 bytes a character, and 8192 is not a multiple of 3.
	long := wire.Message{Seq: 1, Text: strings.Repeat("語", 3000)}
	got := capMessages([]wire.Message{long})
	if len(got) != 1 || !utf8.ValidString(got[0].Text) || len(got[0].Text) != 8190 {
		t.Errorf("kept %d bytes, valid %v; want 8190 bytes of whole characters",
			len(got[0].Text), utf8.ValidString(got[0].Text))
	}
	if long.Text != strings.Repeat("語", 3000) {
		t.Error("the caller's message was changed")
	}
}

func TestCleanReply(t *testing.T) {
	t.Parallel()
	const esc = "\x1b"
	many := make([]wire.Line, 250)
	for i := range many {
		many[i] = wire.Line{{T: strconv.Itoa(i)}}
	}
	cases := []struct {
		name  string
		in    wire.Reply
		ascii bool
		want  wire.Reply
	}{
		{
			name: "a plain reply is kept as it is",
			in: wire.Reply{
				ABI: 1, Text: "cache avg 61%", Short: "61%", Tone: "warn", Insert: "Summarize", Note: "7 days",
				Pass: true, Error: "no", Lines: []wire.Line{{{T: "11 Oct", Tone: "dim"}, {T: " 61%", Tone: "ok", Bold: true}}, {}},
			},
			want: wire.Reply{
				ABI: 1, Text: "cache avg 61%", Short: "61%", Tone: "warn", Insert: "Summarize", Note: "7 days",
				Pass: true, Error: "no", Lines: []wire.Line{{{T: "11 Oct", Tone: "dim"}, {T: " 61%", Tone: "ok", Bold: true}}, {}},
			},
		},
		{
			name: "escape sequences lose what makes them work",
			in: wire.Reply{
				Text: esc + "[31mred" + esc + "[0m", Short: esc + "]0;title\x07", Insert: esc + "[2J", Note: "\u009b31m",
				Error: esc + "P1$r" + esc + "\\", Lines: []wire.Line{{{T: esc + "_Gi=1;AAAA" + esc + "\\"}}},
			},
			want: wire.Reply{
				Text: "[31mred[0m", Short: "]0;title", Insert: "[2J", Note: "31m",
				Error: "P1$r\\", Lines: []wire.Line{{{T: "_Gi=1;AAAA\\"}}},
			},
		},
		{
			name: "every string is one line",
			in:   wire.Reply{Text: "a\nb", Insert: "rm -rf /\r\nyes\n", Note: "x y", Lines: []wire.Line{{{T: "one\ntwo"}}}},
			want: wire.Reply{Text: "a b", Insert: "rm -rf / yes", Note: "x y", Lines: []wire.Line{{{T: "one two"}}}},
		},
		{
			name: "a tone the client does not know is plain",
			in:   wire.Reply{Tone: "blink", Lines: []wire.Line{{{T: "x", Tone: "\x1b[5m"}, {T: "y", Tone: "mod"}, {T: "z", Tone: "DIM"}}}},
			want: wire.Reply{Lines: []wire.Line{{{T: "x"}, {T: "y", Tone: "mod"}, {T: "z"}}}},
		},
		{
			name:  "ASCII glyphs get ASCII",
			in:    wire.Reply{Text: "⎇ main", Short: "café", Lines: []wire.Line{{{T: "→ 61%"}}}},
			ascii: true,
			want:  wire.Reply{Text: "? main", Short: "caf?", Lines: []wire.Line{{{T: "? 61%"}}}},
		},
		{
			name: "Unicode glyphs keep theirs",
			in:   wire.Reply{Text: "⎇ main"},
			want: wire.Reply{Text: "⎇ main"},
		},
		{
			name: "a string is cut to 512 runes",
			in:   wire.Reply{Text: strings.Repeat("é", 600), Lines: []wire.Line{{{T: strings.Repeat("x", 513)}}}},
			want: wire.Reply{Text: strings.Repeat("é", 512), Lines: []wire.Line{{{T: strings.Repeat("x", 512)}}}},
		},
		{
			name: "a reply is cut to 200 lines",
			in:   wire.Reply{Lines: many},
			want: wire.Reply{Lines: many[:200]},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := cleanReply(tc.in, tc.ascii)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("cleanReply:\n got %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func TestCleanReplyLeavesTheModsReplyAlone(t *testing.T) {
	t.Parallel()
	in := wire.Reply{Lines: []wire.Line{{{T: "\x1b[31m", Tone: "blink"}}}}
	cleanReply(in, true)
	if in.Lines[0][0].T != "\x1b[31m" || in.Lines[0][0].Tone != "blink" {
		t.Errorf("the reply passed in was changed: %+v", in)
	}
}

func FuzzCleanReplyIsSafeToDraw(f *testing.F) {
	f.Add("\x1b]0;x\x07", "\x1b[31m", true)
	f.Add("⎇ main\n", "warn", false)
	f.Add("\u009b31m\xff\xfe", "dim", true)
	f.Fuzz(func(t *testing.T, text, tone string, ascii bool) {
		got := cleanReply(wire.Reply{
			Text: text, Short: text, Tone: tone, Insert: text, Note: text, Error: text,
			Lines: []wire.Line{{{T: text, Tone: tone}}},
		}, ascii)
		for _, s := range []string{got.Text, got.Short, got.Tone, got.Insert, got.Note, got.Error, got.Lines[0][0].T, got.Lines[0][0].Tone} {
			if !utf8.ValidString(s) {
				t.Fatalf("%q is not UTF-8", s)
			}
			for _, r := range s {
				if r < 0x20 || r == 0x7F || (r >= 0x80 && r <= 0x9F) || (ascii && r > 0x7E) {
					t.Fatalf("%q holds %U", s, r)
				}
			}
			if utf8.RuneCountInString(s) > MaxSpanRunes {
				t.Fatalf("%d runes", utf8.RuneCountInString(s))
			}
		}
	})
}

// TestTheRequestIsWrittenPlainly pins how the host writes a request: as
// encoding/json would, except that <, > and & are left as they are. A mod
// reads a string with no escape in it much faster, and an error page is
// mostly those characters.
func TestTheRequestIsWrittenPlainly(t *testing.T) {
	t.Parallel()
	in := fullInput()
	in.Args = []string{"<b>bold</b> & more", "a \"quote\"\nand a line", "é ⎇ 日本語 🙂", "not \xff UTF-8", "\u2028"}
	in.Data.ModelCalls[1].Error = "<html><h1>502 Bad Gateway</h1></html>"
	req := buildRequest(with(Grants()...), OpCommand, "go", in)
	got, err := encodeRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	for _, want := range []string{`"<b>bold</b> & more"`, `"<html><h1>502 Bad Gateway</h1></html>"`, `"a \"quote\"\nand a line"`, `"é ⎇ 日本語 🙂"`} {
		if !strings.Contains(text, want) {
			t.Errorf("the request does not hold %s as written:\n%s", want, text)
		}
	}
	for _, escape := range []string{`\u003c`, `\u003e`, `\u0026`} {
		if strings.Contains(text, escape) {
			t.Errorf("the request holds the escape %s", escape)
		}
	}
	if strings.ContainsAny(text, "\n\r") || !utf8.ValidString(text) {
		t.Errorf("the request is more than one line, or not UTF-8: %q", text)
	}
	// It reads back as what json.Marshal writes reads back.
	plain, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var back, want wire.Request
	if err := json.Unmarshal(got, &back); err != nil {
		t.Fatalf("the request is not JSON: %v", err)
	}
	if err := json.Unmarshal(plain, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, want) {
		t.Errorf("the request reads back as\n got %+v\nwant %+v", back, want)
	}
}
