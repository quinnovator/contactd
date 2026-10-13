package termcaps_test

import (
	"strings"
	"testing"

	"github.com/quinnovator/contactd/internal/tui/termcaps"
)

func TestFold(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   []termcaps.Reply
		want termcaps.Replies
	}{
		{name: "nothing", want: termcaps.Replies{}},
		{
			name: "kitty answers everything, in the order asked",
			in:   []termcaps.Reply{background(true), kittyReply(31, true), version("kitty(0.42.1)"), cell(10, 20), da1()},
			want: termcaps.Replies{
				Background: termcaps.BgDark, Kitty: true, Version: "kitty(0.42.1)", CellW: 10, CellH: 20, DA1: 1,
			},
		},
		{
			name: "tmux answers, then the terminal behind it",
			in: []termcaps.Reply{
				version("tmux 3.7c"), cell(16, 32), da1(), kittyReply(32, true), version("kitty(0.42.1)"), da1(),
			},
			want: termcaps.Replies{
				TmuxSeen: true, CellW: 16, CellH: 32, DA1: 2, KittyOuter: true, Version: "kitty(0.42.1)",
			},
		},
		{
			name: "tmux with passthrough off: one reply, then the deadline",
			in:   []termcaps.Reply{version("tmux 3.7c"), cell(16, 32), da1(), deadline()},
			want: termcaps.Replies{TmuxSeen: true, CellW: 16, CellH: 32, DA1: 1, Deadline: true},
		},
		{
			name: "after the deadline the probe's answers are dropped",
			in: []termcaps.Reply{
				deadline(), version("kitty(0.42.1)"), version("tmux 3.4"), kittyReply(31, true), kittyReply(32, true),
				da1(), cell(10, 20),
			},
			want: termcaps.Replies{Deadline: true},
		},
		{
			name: "after the deadline what Bubble Tea reports on its own still counts",
			in:   []termcaps.Reply{deadline(), background(false), mode2027(true), keyboard(3)},
			want: termcaps.Replies{Deadline: true, Background: termcaps.BgLight, Graphemes: true, KeyFlags: 3},
		},
		{
			name: "what came before the deadline is kept",
			in:   []termcaps.Reply{kittyReply(31, true), version("ghostty 1.2.0"), da1(), deadline(), kittyReply(31, false), da1()},
			want: termcaps.Replies{Kitty: true, Version: "ghostty 1.2.0", DA1: 1, Deadline: true},
		},
		{name: "the deadline twice is the deadline once", in: []termcaps.Reply{deadline(), deadline()}, want: termcaps.Replies{Deadline: true}},
		{name: "a light background", in: []termcaps.Reply{background(false)}, want: termcaps.Replies{Background: termcaps.BgLight}},
		{
			name: "the newest background wins, as when the theme changes",
			in:   []termcaps.Reply{background(false), background(true)}, want: termcaps.Replies{Background: termcaps.BgDark},
		},
		{name: "a name is kept in lower case", in: []termcaps.Reply{version("WezTerm 20240203-110809")}, want: termcaps.Replies{Version: "wezterm 20240203-110809"}},
		{name: "tmux in capitals is tmux", in: []termcaps.Reply{version("TMUX 3.5a")}, want: termcaps.Replies{TmuxSeen: true}},
		{name: "tmux after spaces is tmux", in: []termcaps.Reply{version("  tmux 3.3")}, want: termcaps.Replies{TmuxSeen: true}},
		{
			name: "tmux does not replace the name already known",
			in:   []termcaps.Reply{version("kitty(0.42.1)"), version("tmux 3.4")},
			want: termcaps.Replies{Version: "kitty(0.42.1)", TmuxSeen: true},
		},
		{
			name: "a name loses what is not printable ASCII",
			in:   []termcaps.Reply{version("kitty\x1b[2J(0.42.1)\r\n\u009b\u202E\U0010EEEEé")},
			want: termcaps.Replies{Version: "kitty[2j(0.42.1)"},
		},
		{
			name: "a name keeps every printable ASCII character and no other",
			in:   []termcaps.Reply{version("\x00\x1f !~\x7f\x80\u00a0")},
			want: termcaps.Replies{Version: "!~"},
		},
		{
			name: "a name that mentions tmux later is not tmux",
			in:   []termcaps.Reply{version("WezTerm tmux-cc 1")},
			want: termcaps.Replies{Version: "wezterm tmux-cc 1"},
		},
		{name: "the largest cell believed", in: []termcaps.Reply{cell(4096, 4096)}, want: termcaps.Replies{CellW: 4096, CellH: 4096}},
		{name: "a cell one pixel too wide", in: []termcaps.Reply{cell(4097, 16)}, want: termcaps.Replies{}},
		{name: "a cell one pixel too tall", in: []termcaps.Reply{cell(8, 4097)}, want: termcaps.Replies{}},
		{
			name: "a long name is cut to 64 bytes",
			in:   []termcaps.Reply{version(strings.Repeat("x", 63) + " yz")},
			want: termcaps.Replies{Version: strings.Repeat("x", 63)},
		},
		{
			name: "an empty name changes nothing",
			in:   []termcaps.Reply{version("foot(1.16)"), version(""), version(" \x1b ")},
			want: termcaps.Replies{Version: "foot(1.16)"},
		},
		{name: "a kitty error is not OK", in: []termcaps.Reply{kittyReply(31, false)}, want: termcaps.Replies{}},
		{
			name: "the newest kitty answer for an id wins",
			in:   []termcaps.Reply{kittyReply(31, true), kittyReply(32, true), kittyReply(31, false)},
			want: termcaps.Replies{KittyOuter: true},
		},
		{
			name: "a kitty reply with another id is someone else's",
			in:   []termcaps.Reply{kittyReply(0, true), kittyReply(7, true), kittyReply(33, true)},
			want: termcaps.Replies{},
		},
		{name: "every device-attributes reply is counted", in: []termcaps.Reply{da1(), da1(), da1()}, want: termcaps.Replies{DA1: 3}},
		{
			name: "a cell size that makes no sense is dropped",
			in:   []termcaps.Reply{cell(9, 18), cell(0, 18), cell(9, 0), cell(-9, 18), cell(5000, 18), cell(9, 5000)},
			want: termcaps.Replies{CellW: 9, CellH: 18},
		},
		{name: "the newest cell size wins", in: []termcaps.Reply{cell(9, 18), cell(10, 20)}, want: termcaps.Replies{CellW: 10, CellH: 20}},
		{name: "mode 2027 can be taken back", in: []termcaps.Reply{mode2027(true), mode2027(false)}, want: termcaps.Replies{}},
		{name: "key enhancements can be taken back", in: []termcaps.Reply{keyboard(5), keyboard(0)}, want: termcaps.Replies{}},
		{
			name: "a reply of no known kind changes nothing",
			in:   []termcaps.Reply{{}, {Kind: 99, Dark: true, Text: "x", ID: 31, OK: true, W: 9, H: 18, Flags: 1}},
			want: termcaps.Replies{},
		},
		{
			name: "a field that belongs to another kind is not read",
			in: []termcaps.Reply{
				{Kind: termcaps.ReplyBackground, Text: "tmux", OK: true, W: 9, H: 18, Flags: 1},
				{Kind: termcaps.ReplyDA1, Dark: true, Text: "kitty", ID: 31, OK: true},
			},
			want: termcaps.Replies{Background: termcaps.BgLight, DA1: 1},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := fold(tc.in...); got != tc.want {
				t.Errorf("Fold:\n got %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

// Fold takes and returns a value: the Replies it was given is not changed.
func TestFoldLeavesItsArgumentAlone(t *testing.T) {
	t.Parallel()
	before := fold(version("kitty(0.42.1)"), da1())
	kept := before
	after := termcaps.Fold(before, cell(10, 20))
	if before != kept {
		t.Errorf("Fold changed its argument: %+v", before)
	}
	if after == before {
		t.Error("Fold returned what it was given")
	}
}

// The life of a session as the client sees it: Caps is resolved after every
// reply, and compared with the one before to decide whether to redraw.
func TestCapsFollowReplies(t *testing.T) {
	t.Parallel()
	type step struct {
		in      termcaps.Reply
		changes bool
		check   func(termcaps.Caps) bool
	}
	run := func(t *testing.T, env termcaps.Env, steps []step) {
		t.Helper()
		var r termcaps.Replies
		caps := termcaps.Resolve(env, termcaps.Overrides{}, r)
		for i, s := range steps {
			r = termcaps.Fold(r, s.in)
			next := termcaps.Resolve(env, termcaps.Overrides{}, r)
			if (next != caps) != s.changes {
				t.Errorf("step %d (%+v): Caps changed = %v, want %v\n was %+v\n now %+v", i, s.in, next != caps, s.changes, caps, next)
			}
			if s.check != nil && !s.check(next) {
				t.Errorf("step %d (%+v): Caps = %+v", i, s.in, next)
			}
			caps = next
		}
	}

	t.Run("kitty: half blocks become pixels when the evidence arrives", func(t *testing.T) {
		t.Parallel()
		run(t, kitty(), []step{
			{in: background(true), changes: true, check: func(c termcaps.Caps) bool { return c.Background == termcaps.BgDark }},
			{in: kittyReply(31, true), changes: true, check: func(c termcaps.Caps) bool { return c.Images == termcaps.ImagesPixels && !c.Settled }},
			{in: version("kitty(0.42.1)"), changes: true, check: func(c termcaps.Caps) bool { return c.Terminal == "kitty(0.42.1)" }},
			{in: cell(10, 20), changes: true, check: func(c termcaps.Caps) bool { return c.CellW == 10 && c.CellH == 20 }},
			{in: da1(), changes: true, check: func(c termcaps.Caps) bool { return c.Settled }},
			{in: da1(), changes: false},
			{in: deadline(), changes: false},
			{in: kittyReply(31, false), changes: false, check: func(c termcaps.Caps) bool { return c.Images == termcaps.ImagesPixels }},
		})
	})

	t.Run("a terminal that never answers settles at the deadline, and late answers change nothing", func(t *testing.T) {
		t.Parallel()
		run(t, kitty(), []step{
			{in: deadline(), changes: true, check: func(c termcaps.Caps) bool { return c.Settled && c.Images == termcaps.ImagesBlocks }},
			{in: kittyReply(31, true), changes: false},
			{in: version("kitty(0.42.1)"), changes: false},
			{in: cell(10, 20), changes: false},
			{in: da1(), changes: false},
			{in: background(false), changes: true, check: func(c termcaps.Caps) bool { return c.Background == termcaps.BgLight }},
			{in: keyboard(1), changes: true, check: func(c termcaps.Caps) bool { return c.ShiftEnter }},
		})
	})

	t.Run("tmux found from its version reply: one reply no longer settles", func(t *testing.T) {
		t.Parallel()
		run(t, xterm(), []step{
			{in: version("tmux 3.4"), changes: true, check: func(c termcaps.Caps) bool { return c.Tmux && c.Terminal == "" }},
			{in: da1(), changes: false, check: func(c termcaps.Caps) bool { return !c.Settled }},
			{in: kittyReply(32, true), changes: false},
			{in: version("ghostty 1.2.0"), changes: true, check: func(c termcaps.Caps) bool { return c.Images == termcaps.ImagesPixels }},
			{in: da1(), changes: true, check: func(c termcaps.Caps) bool { return c.Settled }},
		})
	})
}
