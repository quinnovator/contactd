package tui

import (
	"fmt"
	"image/color"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/ansi/kitty"

	"github.com/quinnovator/contactd/internal/client"
	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/tui/conf"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
)

// asked is a terminal that is asked everything: 24-bit colour by the look of
// its environment, nothing set, and nothing known about it yet.
func asked() termcaps.Env {
	return termcaps.Env{Term: "xterm-256color", Detected: termcaps.ColorTrue}
}

// The answers a terminal gives, as Bubble Tea hands them to Update.
func background(c color.Color) tea.Msg { return tea.BackgroundColorMsg{Color: c} }

func nearBlack() color.Color { return color.RGBA{R: 0x11, G: 0x14, B: 0x17, A: 0xff} }

func paper() color.Color { return color.RGBA{R: 0xf7, G: 0xf5, B: 0xee, A: 0xff} }

func kittyAnswer(id int, payload string) tea.Msg {
	return uv.KittyGraphicsEvent{Options: kitty.Options{ID: id}, Payload: []byte(payload)}
}

func version(name string) tea.Msg { return tea.TerminalVersionMsg{Name: name} }

func attributes() tea.Msg { return uv.PrimaryDeviceAttributesEvent{62, 4} }

// produced runs a command that finishes at once and returns what it gives:
// for a batch, what each of its commands gives.
func produced(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		return []tea.Msg{msg}
	}
	var out []tea.Msg
	for _, c := range batch {
		out = append(out, produced(c)...)
	}
	return out
}

// clears reports whether a command asks for the screen to be cleared.
func clears(cmd tea.Cmd) bool {
	return slices.Contains(produced(cmd), tea.ClearScreen())
}

func TestTerminalReply(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		msg  tea.Msg
		want termcaps.Reply
		ok   bool
	}{
		{name: "the deadline", msg: probeDeadlineMsg{}, want: termcaps.Reply{Kind: termcaps.ReplyDeadline}, ok: true},
		{name: "a dark background", msg: background(nearBlack()), want: termcaps.Reply{Kind: termcaps.ReplyBackground, Dark: true}, ok: true},
		{name: "a light background", msg: background(paper()), want: termcaps.Reply{Kind: termcaps.ReplyBackground}, ok: true},
		{name: "the name", msg: version("ghostty 1.2.0"), want: termcaps.Reply{Kind: termcaps.ReplyVersion, Text: "ghostty 1.2.0"}, ok: true},
		{name: "kitty graphics", msg: kittyAnswer(31, "OK"), want: termcaps.Reply{Kind: termcaps.ReplyKitty, ID: 31, OK: true}, ok: true},
		{name: "kitty graphics through tmux", msg: kittyAnswer(32, "OK"), want: termcaps.Reply{Kind: termcaps.ReplyKitty, ID: 32, OK: true}, ok: true},
		{
			name: "kitty graphics refused", msg: kittyAnswer(31, "ENOTSUPPORTED:no"),
			want: termcaps.Reply{Kind: termcaps.ReplyKitty, ID: 31}, ok: true,
		},
		{name: "the device attributes", msg: attributes(), want: termcaps.Reply{Kind: termcaps.ReplyDA1}, ok: true},
		{
			// The terminal writes ESC [ 6 ; 32 ; 16 t, the height first.
			name: "the cell size", msg: uv.CellSizeEvent{Width: 16, Height: 32},
			want: termcaps.Reply{Kind: termcaps.ReplyCell, W: 16, H: 32}, ok: true,
		},
		{
			name: "clusters, switched on", msg: tea.ModeReportMsg{Mode: ansi.ModeUnicodeCore, Value: ansi.ModeSet},
			want: termcaps.Reply{Kind: termcaps.ReplyMode2027, OK: true}, ok: true,
		},
		{
			name: "clusters, switched off", msg: tea.ModeReportMsg{Mode: ansi.ModeUnicodeCore, Value: ansi.ModeReset},
			want: termcaps.Reply{Kind: termcaps.ReplyMode2027, OK: true}, ok: true,
		},
		{
			name: "clusters, always on", msg: tea.ModeReportMsg{Mode: ansi.ModeUnicodeCore, Value: ansi.ModePermanentlySet},
			want: termcaps.Reply{Kind: termcaps.ReplyMode2027, OK: true}, ok: true,
		},
		{
			name: "clusters, never", msg: tea.ModeReportMsg{Mode: ansi.ModeUnicodeCore, Value: ansi.ModePermanentlyReset},
			want: termcaps.Reply{Kind: termcaps.ReplyMode2027}, ok: true,
		},
		{
			name: "clusters, not known", msg: tea.ModeReportMsg{Mode: ansi.ModeUnicodeCore, Value: ansi.ModeNotRecognized},
			want: termcaps.Reply{Kind: termcaps.ReplyMode2027}, ok: true,
		},
		{name: "another mode", msg: tea.ModeReportMsg{Mode: ansi.ModeSynchronizedOutput, Value: ansi.ModeReset}},
		{
			name: "key enhancements", msg: tea.KeyboardEnhancementsMsg{Flags: 3},
			want: termcaps.Reply{Kind: termcaps.ReplyKeyboard, Flags: 3}, ok: true,
		},
		{name: "a control string nobody knows", msg: uv.UnknownDcsEvent("\x1bP1$r0m\x1b\\")},
		{name: "an application command nobody knows", msg: uv.UnknownApcEvent("\x1b_Zfoo\x1b\\")},
		{name: "a control sequence nobody knows", msg: uv.UnknownCsiEvent("\x1b[?9999z")},
		{name: "an operating system command nobody knows", msg: uv.UnknownOscEvent("\x1b]777;x\x07")},
		{name: "the other device attributes", msg: uv.SecondaryDeviceAttributesEvent{1, 10, 0}},
		{name: "the window's size in pixels", msg: uv.PixelSizeEvent{Width: 1600, Height: 960}},
		{name: "the colour profile Bubble Tea starts with", msg: tea.ColorProfileMsg{Profile: colorprofile.TrueColor}},
		{name: "the window gaining the focus", msg: tea.FocusMsg{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := terminalReply(tc.msg)
			if got != tc.want || ok != tc.ok {
				t.Errorf("terminalReply(%#v) = %+v, %v; want %+v, %v", tc.msg, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// Nothing waits for the terminal. The client draws at once for what the
// environment says, and each answer that changes what the terminal can do
// has everything on screen drawn again: the transcript, the bar, the box
// being typed in and the hint line.
func TestTheTerminalsAnswersChangeTheScreen(t *testing.T) {
	t.Parallel()
	h := newHarnessWith(t, Options{Env: asked()})
	start := termcaps.Caps{
		Color: termcaps.ColorTrue, Background: termcaps.BgUnknown, Glyphs: termcaps.GlyphsUnicode,
		Images: termcaps.ImagesBlocks, Width: termcaps.WidthWc, CellW: 8, CellH: 16,
	}
	if h.m.caps != start || h.m.plan != termcaps.ProbeFull {
		t.Fatalf("the client starts with\n%+v and the plan %v, want\n%+v and the full plan", h.m.caps, h.m.plan, start)
	}
	h.size(100, 24)
	sess := h.openChat()
	h.from(sess, live(), client.Update{Kind: client.UpdateEvents, Events: []core.Event{
		logged(t, 1, fixedNow(), core.KindUserMsg, core.MsgBody{Text: "plot last week's latency"}),
		logged(t, 2, fixedNow(), core.KindAgentMsg, core.MsgBody{Text: "p99 peaked on Thursday at 7.2 s."}),
	}})
	h.typeText("compare")
	// With the background unknown the colours are the ones that read on
	// both, from the 256, and the bar has a rule in place of a fill.
	h.wantOnScreen("❯ plot last week's latency", "│ ❯ compare", "ctrl+j new line")
	raw := h.m.View().Content
	if !strings.Contains(raw, "\x1b[1;38;5;30m") || strings.Contains(raw, "38;2;") || strings.Contains(raw, "48;") {
		t.Errorf("before any answer the screen is not drawn for an unknown background:\n%q", raw)
	}
	c := h.m.chat
	if c.rows.BarRule != 1 {
		t.Errorf("before any answer the bar has no rule above it: %+v", c.rows)
	}

	// The background arrives: the theme, the transcript and the box follow.
	drawn := c.win.Stats().Rendered
	if cmd := h.send(background(nearBlack())); !clears(cmd) {
		t.Error("a background answer did not ask for the screen to be cleared")
	}
	if h.m.caps.Background != termcaps.BgDark {
		t.Fatalf("the background is %v, want dark", h.m.caps.Background)
	}
	raw = h.m.View().Content
	for name, seq := range map[string]string{
		"the accent of the message and the prompt": "\x1b[1;38;2;94;196;200m",
		"the bar's fill":   "48;2;27;33;38",
		"the box's border": "38;2;58;68;76",
	} {
		if !strings.Contains(raw, seq) {
			t.Errorf("after a dark background %s (%q) is not on screen:\n%q", name, seq, raw)
		}
	}
	if strings.Contains(raw, "38;5;") {
		t.Errorf("after a dark background something is still drawn in the old colours:\n%q", raw)
	}
	if c.rows.BarRule != 0 || c.rows.Body != 24-1-1-1-3-1 {
		t.Errorf("after a dark background the rows are %+v, want a filled bar with no rule", c.rows)
	}
	if got := c.win.Stats().Rendered - drawn; got < 2 {
		t.Errorf("after a dark background %d blocks were drawn again, want every one in view", got)
	}
	if got := c.input.Value(); got != "compare" {
		t.Errorf("the draft became %q", got)
	}
	// The same answer again changes nothing, and asks for nothing.
	if cmd := h.send(background(nearBlack())); cmd != nil {
		t.Errorf("the same answer again gave %v", produced(cmd))
	}
	// A terminal can change its mind, long after the start.
	h.send(probeDeadlineMsg{})
	if cmd := h.send(background(paper())); !clears(cmd) || h.m.caps.Background != termcaps.BgLight {
		t.Errorf("a light background after the deadline left %v", h.m.caps.Background)
	}
	if raw := h.m.View().Content; !strings.Contains(raw, "\x1b[1;38;2;15;111;119m") {
		t.Errorf("after a light background the accent is not the light one:\n%q", raw)
	}

	// Key enhancements change the key the hint line names, and how cells are
	// counted follows the terminal: both whenever they are reported.
	if cmd := h.send(tea.KeyboardEnhancementsMsg{Flags: 1}); !clears(cmd) {
		t.Error("key enhancements did not ask for the screen to be cleared")
	}
	h.wantOnScreen("shift+enter new line")
	h.wantNotOnScreen("ctrl+j")
	h.send(tea.ModeReportMsg{Mode: ansi.ModeUnicodeCore, Value: ansi.ModeReset})
	if h.m.caps.Width != termcaps.WidthGrapheme || h.m.th.Caps.Width != termcaps.WidthGrapheme {
		t.Errorf("after mode 2027 was recognised cells are counted by %v", h.m.caps.Width)
	}

	// The list and its forms are drawn for the same terminal.
	h.send(h.key(ctrl('p'))())
	h.typeText("n")
	h.wantOnScreen("New contact", "shift+enter new line")
	if raw := h.m.View().Content; !strings.Contains(raw, "\x1b[1;38;2;15;111;119m") {
		t.Errorf("the form's prompt is not the light accent:\n%q", raw)
	}
	h.send(background(nearBlack()))
	if raw := h.m.View().Content; !strings.Contains(raw, "\x1b[1;38;2;94;196;200m") {
		t.Errorf("a late answer did not restyle the form's box:\n%q", raw)
	}
}

// Pixels need two answers: kitty graphics said OK, and the terminal named
// itself as one of the two that keep an image through a redraw.
func TestPixelsNeedTheTerminalsWord(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		env     termcaps.Env
		over    termcaps.Overrides
		answers []tea.Msg
		want    termcaps.Caps
	}{
		{
			name:    "kitty graphics and ghostty",
			answers: []tea.Msg{kittyAnswer(31, "OK"), version("ghostty 1.2.0"), uv.CellSizeEvent{Width: 10, Height: 22}, attributes()},
			want: termcaps.Caps{
				Images: termcaps.ImagesPixels, Terminal: "ghostty 1.2.0", CellW: 10, CellH: 22, Settled: true,
			},
		},
		{
			name:    "kitty graphics from a terminal that is neither",
			answers: []tea.Msg{kittyAnswer(31, "OK"), version("WezTerm 20240203"), attributes()},
			want:    termcaps.Caps{Images: termcaps.ImagesBlocks, Terminal: "wezterm 20240203", CellW: 8, CellH: 16, Settled: true},
		},
		{
			name:    "ghostty that refuses the query",
			answers: []tea.Msg{kittyAnswer(31, "EINVAL"), version("ghostty 1.2.0"), attributes()},
			want:    termcaps.Caps{Images: termcaps.ImagesBlocks, Terminal: "ghostty 1.2.0", CellW: 8, CellH: 16, Settled: true},
		},
		{
			name:    "the answer to something else, such as an image being sent",
			answers: []tea.Msg{kittyAnswer(7, "OK"), version("kitty 0.42.1"), attributes()},
			want:    termcaps.Caps{Images: termcaps.ImagesBlocks, Terminal: "kitty 0.42.1", CellW: 8, CellH: 16, Settled: true},
		},
		{
			name:    "everything after the deadline",
			answers: []tea.Msg{probeDeadlineMsg{}, kittyAnswer(31, "OK"), version("ghostty 1.2.0"), uv.CellSizeEvent{Width: 10, Height: 22}, attributes()},
			want:    termcaps.Caps{Images: termcaps.ImagesBlocks, CellW: 8, CellH: 16, Settled: true},
		},
		{
			name:    "no answer but the deadline",
			answers: []tea.Msg{probeDeadlineMsg{}},
			want:    termcaps.Caps{Images: termcaps.ImagesBlocks, CellW: 8, CellH: 16, Settled: true},
		},
		{
			name:    "through tmux, both answer",
			env:     termcaps.Env{Tmux: "/tmp/tmux-501/default,1,0"},
			answers: []tea.Msg{version("tmux 3.5a"), attributes(), kittyAnswer(32, "OK"), version("ghostty 1.2.0"), attributes()},
			want: termcaps.Caps{
				Images: termcaps.ImagesPixels, Terminal: "ghostty 1.2.0", Tmux: true, CellW: 8, CellH: 16, Settled: true,
			},
		},
		{
			name:    "through tmux with passthrough off",
			env:     termcaps.Env{Tmux: "/tmp/tmux-501/default,1,0"},
			answers: []tea.Msg{version("tmux 3.5a"), attributes()},
			want:    termcaps.Caps{Images: termcaps.ImagesBlocks, Tmux: true, CellW: 8, CellH: 16},
		},
		{
			name:    "asked nothing, and answering anyway",
			over:    termcaps.Overrides{NoProbe: true},
			answers: []tea.Msg{kittyAnswer(31, "OK"), version("ghostty 1.2.0"), uv.CellSizeEvent{Width: 10, Height: 22}, attributes()},
			want:    termcaps.Caps{Images: termcaps.ImagesBlocks, CellW: 8, CellH: 16, Settled: true},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := asked()
			env.Tmux = tc.env.Tmux
			h := newHarnessWith(t, Options{Env: env, Overrides: tc.over})
			for _, answer := range tc.answers {
				h.send(answer)
			}
			want := tc.want
			want.Color, want.Background, want.Glyphs, want.Width = termcaps.ColorTrue, termcaps.BgUnknown, termcaps.GlyphsUnicode, termcaps.WidthWc
			if h.m.caps != want {
				t.Errorf("the terminal can do\n%+v, want\n%+v", h.m.caps, want)
			}
			if h.m.th.Caps != want {
				t.Errorf("the theme was made for\n%+v, want\n%+v", h.m.th.Caps, want)
			}
		})
	}
}

// The wait for the terminal's answers is two seconds, and starts with the
// questions. A terminal that is asked nothing is not waited for.
func TestTheDeadlineIsSetWithTheQuestions(t *testing.T) {
	t.Parallel()
	h := newHarnessWith(t, Options{Env: asked()})
	if h.m.probeWait != 2*time.Second {
		t.Errorf("the terminal has %v to answer, want 2s", h.m.probeWait)
	}
	h.m.probeWait = time.Nanosecond
	if got := produced(h.m.probe()); !slices.Contains(got, tea.Msg(probeDeadlineMsg{})) {
		t.Errorf("the questions gave %#v, with no deadline among them", got)
	}
	if h.m.caps.Settled {
		t.Error("the terminal counts as having answered before anything came")
	}
	h.send(probeDeadlineMsg{})
	if !h.m.caps.Settled {
		t.Error("the deadline did not end the wait")
	}

	for name, opt := range map[string]Options{
		"probing switched off": {Env: asked(), Overrides: termcaps.Overrides{NoProbe: true}},
		"no terminal named":    {},
		"GNU screen":           {Env: termcaps.Env{Term: "screen-256color", Detected: termcaps.Color256}},
	} {
		h := newHarnessWith(t, opt)
		if h.m.probe() != nil || !h.m.caps.Settled || h.m.plan != termcaps.ProbeOff {
			t.Errorf("%s: the plan is %v and the caps %+v, want nothing asked and nothing awaited", name, h.m.plan, h.m.caps)
		}
	}
	// The zero Options are the safest terminal.
	if got := newHarnessWith(t, Options{}).m.caps; got.Glyphs != termcaps.GlyphsASCII || got.Color != termcaps.ColorNone {
		t.Errorf("with nothing known the terminal is drawn with %v glyphs and %v colours", got.Glyphs, got.Color)
	}
}

// A tmux that the environment hid gives itself away when it answers with its
// name. The terminal behind it is then asked, once, and never after the
// deadline.
func TestATmuxFoundFromItsAnswerIsAskedOnce(t *testing.T) {
	t.Parallel()
	raws := func(cmd tea.Cmd) []string {
		var out []string
		for _, msg := range produced(cmd) {
			if raw, ok := msg.(tea.RawMsg); ok {
				out = append(out, fmt.Sprint(raw.Msg))
			}
		}
		return out
	}
	h := newHarnessWith(t, Options{Env: asked()})
	if got := raws(h.send(version("ghostty 1.2.0"))); len(got) != 0 {
		t.Errorf("a terminal's own name had %q written", got)
	}
	cmd := h.send(version("tmux 3.5a"))
	if got := raws(cmd); !slices.Equal(got, []string{termcaps.TmuxRoundBytes()}) {
		t.Errorf("tmux's name had %q written, want the round through it", got)
	}
	if !h.m.caps.Tmux || !clears(cmd) {
		t.Errorf("after tmux's name the caps are %+v", h.m.caps)
	}
	for _, again := range []tea.Msg{version("tmux 3.5a"), attributes(), kittyAnswer(32, "OK"), version("ghostty 1.2.0"), attributes()} {
		if got := raws(h.send(again)); len(got) != 0 {
			t.Errorf("%#v had %q written: the round goes out once", again, got)
		}
	}
	if h.m.caps.Images != termcaps.ImagesPixels || !h.m.caps.Settled {
		t.Errorf("after the round was answered the caps are %+v", h.m.caps)
	}

	// Under the plan for tmux the round went out with the first questions.
	in := asked()
	in.Tmux = "/tmp/tmux-501/default,1,0"
	h = newHarnessWith(t, Options{Env: in})
	if got := raws(h.send(version("tmux 3.5a"))); len(got) != 0 {
		t.Errorf("in a tmux the environment names, its answer had %q written", got)
	}

	// After the deadline nothing is asked.
	h = newHarnessWith(t, Options{Env: asked()})
	h.send(probeDeadlineMsg{})
	if got := raws(h.send(version("tmux 3.5a"))); len(got) != 0 || h.m.caps.Tmux {
		t.Errorf("after the deadline tmux's name had %q written and the caps are %+v", got, h.m.caps)
	}
}

// The [glyph] table of chat.toml replaces single glyphs, and what it asks
// for that cannot be drawn is said once, where the client starts.
func TestGlyphOverrides(t *testing.T) {
	t.Parallel()
	env, over := terminal(t, trueDark())
	chat := conf.Chat{Probe: true, Mods: true, Glyph: map[string]string{
		"tool": "●", "elbow": "└", "turn": "8", "prompt": "\x1b[31m>",
	}}
	events := client.Update{Kind: client.UpdateEvents, Events: []core.Event{
		logged(t, 1, fixedNow(), core.KindToolCall, core.ToolCall{ID: "c", Name: "python", Code: "df.plot.bar()"}),
		logged(t, 2, fixedNow(), core.KindToolResult, core.ToolResult{ID: "c", Output: "saved latency.png\n"}),
	}}
	note := `chat.toml: glyph "prompt": "\x1b[31m>" holds a character that is not drawn; glyph "turn": there is no glyph of that name`

	// Started at the list, the list says it, until the next key.
	h := newHarnessWith(t, Options{Env: env, Overrides: over, Chat: chat}, client.Contact{ID: 1, Name: "ada"})
	h.size(140, 24)
	h.m.Init()
	h.send(h.m.showContacts()())
	h.wantOnScreen("▸ ada", note)
	h.key(special(tea.KeyDown))
	h.wantNotOnScreen("chat.toml")
	h.key(enter())
	h.from(h.b.sessions[0], live(), events)
	h.wantOnScreen("● python", "└ saved latency.png", "❯ Message ada")
	h.wantNotOnScreen("⏺", "⎿", "chat.toml")
	// A late answer keeps the replaced glyphs.
	h.m.setCaps(light16())
	h.wantOnScreen("● python", "└ saved latency.png")
	// The ASCII set is not touched by them.
	h.m.setCaps(noneASCII())
	h.wantOnScreen("* python", "|_ saved latency.png")

	// Started in a conversation, the conversation says it.
	h = newHarnessWith(t, Options{Env: env, Overrides: over, Chat: chat, Open: &client.Contact{ID: 1, Name: "ada"}})
	h.size(140, 24)
	h.m.Init()
	h.from(h.b.sessions[0], live(), events)
	h.wantOnScreen("● python", note)
	h.typeText("x")
	h.wantNotOnScreen("chat.toml")
	h.send(h.key(ctrl('p'))())
	h.wantOnScreen("Contacts")
	h.wantNotOnScreen("chat.toml")

	// A table with nothing wrong in it says nothing.
	chat.Glyph = map[string]string{"tool": "●"}
	h = newHarnessWith(t, Options{Env: env, Overrides: over, Chat: chat})
	if h.m.note != "" {
		t.Errorf("a good glyph table left the note %q", h.m.note)
	}
}

func TestProfile(t *testing.T) {
	t.Parallel()
	for c, want := range map[termcaps.Color]colorprofile.Profile{
		termcaps.ColorTrue: colorprofile.TrueColor,
		termcaps.Color256:  colorprofile.ANSI256,
		termcaps.Color16:   colorprofile.ANSI,
		termcaps.ColorNone: colorprofile.ASCII,
		termcaps.Color(0):  colorprofile.ASCII,
	} {
		if got := profile(c); got != want {
			t.Errorf("profile(%v) = %v, want %v", c, got, want)
		}
	}
}
