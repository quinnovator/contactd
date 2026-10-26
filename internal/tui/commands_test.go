package tui

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/quinnovator/contactd/internal/client"
	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/tui/commands"
	"github.com/quinnovator/contactd/internal/tui/conf"
	"github.com/quinnovator/contactd/internal/tui/scrollback"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
	"github.com/quinnovator/contactd/internal/tui/tuitest"
)

// command types a line into the conversation's box and presses enter. It
// returns the command Update answered the enter with.
func (h *harness) command(line string) tea.Cmd {
	h.t.Helper()
	h.typeText(line)
	return h.key(enter())
}

// then delivers what a command gives, the way the program's loop would.
// Only commands that finish are run this way.
func (h *harness) then(cmd tea.Cmd) {
	h.t.Helper()
	if cmd != nil {
		h.send(cmd())
	}
}

// draft is what the conversation's box holds.
func (h *harness) draft() string { return h.m.chat.input.Value() }

// note is the note the conversation shows.
func (h *harness) note() string { return h.m.chat.note }

// inOrder runs a command that finishes at once and returns what it gives.
// For commands that are run one after another, or together, that is what
// each of them gives, in the order they were put.
func inOrder(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	v := reflect.ValueOf(msg)
	if !v.IsValid() || v.Kind() != reflect.Slice || v.Type().Elem() != reflect.TypeFor[tea.Cmd]() {
		return []tea.Msg{msg}
	}
	var out []tea.Msg
	for i := range v.Len() {
		if c, ok := reflect.TypeAssert[tea.Cmd](v.Index(i)); ok {
			out = append(out, inOrder(c)...)
		}
	}
	return out
}

// mockMods adds the commands of the mods the Commands mock was drawn with:
// four of them, which with the core ones make 24.
func mockMods(reg *commands.Registry) {
	reg.SetMod("standup", []commands.Spec{{Name: "standup", Summary: "summarize yesterday for the team"}})
	reg.SetMod("cache", []commands.Spec{{Name: "stats", Summary: "token use and cache share by day"}})
	reg.SetMod("git", []commands.Spec{
		{Name: "branch", Summary: "the branch this contact works on"},
		{Name: "log", Usage: "[n]", Summary: "the last commits"},
	})
}

// commandsMock brings the interface to the moment the Commands mock draws:
// eleven past noon, a short conversation with ada in which a tool ran, and
// "/st" in the box.
func commandsMock(t *testing.T, caps termcaps.Caps) *harness {
	t.Helper()
	h := newHarness(t, caps)
	h.now = time.Date(2026, 10, 12, 12, 11, 0, 0, time.UTC)
	h.size(100, 30)
	h.m.open(client.Contact{ID: 1, Name: "ada", Provider: "deepseek", Model: "deepseek-flash"})
	at := h.now.Add(-time.Minute)
	h.from(h.b.sessions[0], live(), client.Update{Kind: client.UpdateEvents, Events: []core.Event{
		logged(t, 1, at, core.KindUserMsg, core.MsgBody{Text: "show me the numbers for turn 7"}),
		logged(t, 2, at, core.KindToolCall, core.ToolCall{ID: "c", Name: "python", Code: `print(df[df.turn == 7][["input", "cached", "ms"]])`}),
		logged(t, 3, at, core.KindToolResult, core.ToolResult{ID: "c", Output: "   input  cached    ms\n7   3154    1536  5408\n"}),
		logged(t, 4, at, core.KindAttempt, core.AttemptBody{Route: "deepseek/deepseek-flash", Input: 3700, Cached: 3034, Output: 40, MS: 7200}),
		logged(t, 5, at, core.KindAgentMsg, core.MsgBody{Text: "Turn 7 sent 3,154 tokens and 1,536 of them came from cache: 48%."}),
	}})
	mockMods(h.m.cmds)
	h.typeText("/st")
	return h
}

// The handlers are a map by name, so nothing but this test says that every
// command the client lists can be run, and that nothing can be run that is
// not listed.
func TestEveryCoreCommandHasAHandler(t *testing.T) {
	t.Parallel()
	core := commands.Core()
	want := make([]string, 0, len(core))
	for _, spec := range core {
		want = append(want, spec.Name)
	}
	slices.Sort(want)
	if got := slices.Sorted(maps.Keys(handlers())); !slices.Equal(got, want) {
		t.Errorf("the handlers are for\n%q, and the core commands are\n%q", got, want)
	}
}

// The approved drawing of the palette, at the size it was drawn for. The
// build differs from the drawing where the spec says it does: the header's
// right side is the days in view, the gaps of a few pixels are not rows, and
// the transcript has the rule of its day. The bar has no branch cell: that
// one is a mod's.
func TestCommandsMock(t *testing.T) {
	t.Parallel()
	h := commandsMock(t, trueDark())
	rows := h.rows()
	if len(rows) != 30 {
		t.Fatalf("the screen has %d rows, want 30:\n%s", len(rows), h.screen())
	}
	for i, row := range rows {
		if w := ansi.StringWidth(row); w != 100 && (i < 2 || i > 15) {
			t.Errorf("row %d is %d cells wide: %q", i, w, row)
		}
	}
	tuitest.Golden(t, "commands-100x30.golden", []byte(h.screen()+"\n"))

	spread := func(left, right string) string {
		return left + strings.Repeat(" ", 100-ansi.StringWidth(left)-ansi.StringWidth(right)) + right
	}
	boxed := func(left, right string) string { return spread("│ "+left, right+" │") }
	edge := strings.Repeat("─", 98)
	for _, want := range []struct {
		row  int
		text string
	}{
		{0, spread("ada  deepseek/deepseek-flash", "12 Oct")},
		{1, strings.Repeat("─", 100)},
		{5, "❯ show me the numbers for turn 7"},
		{7, "⏺ python"},
		{8, `    print(df[df.turn == 7][["input", "cached", "ms"]])`},
		{9, "  ⎿    input  cached    ms"},
		{10, "    7   3154    1536  5408"},
		{12, "  Turn 7 sent 3,154 tokens and 1,536 of them came from cache: 48%."},
		{16, "╭" + edge + "╮"},
		{17, boxed("commands matching /st", "5 of 27")},
		{18, boxed("▸ /statusline       choose a preset or edit the status line", "core")},
		{19, boxed("  /status           connection, route and cache for this contact", "core")},
		{20, boxed("  /stop             stop the reply in flight (esc)", "core")},
		{21, boxed("  /standup          summarize yesterday for the team", "mod standup")},
		{22, boxed("  /stats            token use and cache share by day", "mod cache")},
		{23, boxed("↑↓ choose · tab complete · enter run · esc close", "")},
		{24, "╰" + edge + "╯"},
		{25, spread(" ● live │ deepseek-flash │ cache 82% │ 3.7k tok │ 7.2s", "12:11 ")},
		{26, "╭" + edge + "╮"},
		{27, boxed("❯ /statusline  preset | edit", "")},
		{28, "╰" + edge + "╯"},
		{29, spread(" a line that starts with / is a command and is never sent to the contact", "")},
	} {
		if rows[want.row] != want.text {
			t.Errorf("row %d is\n%q, want\n%q", want.row, rows[want.row], want.text)
		}
	}
	if got, want := h.m.chat.rows.Body, 14; got != want {
		t.Errorf("the transcript has %d rows, want %d", got, want)
	}

	// The rest of the command is drawn dim, and the cursor stands before it.
	v := h.m.View()
	if !strings.Contains(v.Content, h.m.th.Style("dim").Render("atusline  preset | edit")) {
		t.Errorf("the rest of the command is not drawn dim:\n%q", rows[27])
	}
	if v.Cursor == nil || v.Cursor.Y != 27 || v.Cursor.X != len("| > /st") {
		t.Errorf("the cursor is at %+v, want the cell after /st on row 27", v.Cursor)
	}
	// The chosen row is the one in the accent tone.
	if !strings.Contains(v.Content, h.m.th.Style("accent").Render("▸ /statusline")) {
		t.Errorf("the chosen command is not in the accent tone:\n%q", v.Content)
	}
}

// The palette from its first key to its last: it opens on a slash, narrows
// as the word is typed, follows the arrows, completes on tab, and closes on
// esc, which only then stops a reply.
func TestThePaletteFlow(t *testing.T) {
	t.Parallel()
	h := newHarness(t, trueDark())
	h.size(100, 30)
	sess := h.openChat()
	h.from(sess, live(), messages(t, 1, 3, fixedNow()), delta(core.Delta{Type: core.DeltaTyping, Typing: true}))
	c := h.m.chat
	n := h.m.cmds.Len()
	body := c.rows.Body
	sentence := "a line that starts with / is a command and is never sent to the contact"
	h.wantOnScreen("enter send")
	h.wantNotOnScreen("│ commands ", sentence)

	// A slash opens it on every command, eight to a screen.
	h.typeText("/")
	h.wantOnScreen("│ commands ", fmt.Sprintf("%d of %d", n, n), "▸ /statusline", "/rename", "↑↓ choose · tab complete · enter run · esc close", sentence)
	h.wantNotOnScreen("enter send", "/details")
	if c.rows.Palette != 12 || c.rows.Body != body-12 {
		t.Errorf("the palette has %d rows and the transcript %d of %d", c.rows.Palette, c.rows.Body, body)
	}
	h.wantOnScreen("message 3")

	// The word narrows it. The names that start with the word are listed in
	// the order the client declares them.
	h.typeText("st")
	h.wantOnScreen("commands matching /st", fmt.Sprintf("3 of %d", n), "▸ /statusline", "  /status ", "  /stop ", "❯ /statusline  preset | edit")
	h.wantNotOnScreen("/help", "/rename")
	if c.rows.Palette != 7 {
		t.Errorf("three commands take %d rows", c.rows.Palette)
	}
	listed := func() []string {
		var names []string
		for _, row := range h.rows() {
			name, ok := strings.CutPrefix(row, "│ ▸ /")
			if !ok {
				name, ok = strings.CutPrefix(row, "│   /")
			}
			if ok {
				names = append(names, strings.Fields(name)[0])
			}
		}
		return names
	}
	if got := listed(); !slices.Equal(got, []string{"statusline", "status", "stop"}) {
		t.Errorf("/st lists %q", got)
	}

	// The arrows choose, and stop at the ends. They do not scroll now.
	h.key(special(tea.KeyDown))
	h.wantOnScreen("  /statusline", "▸ /status ", "❯ /status ")
	h.wantNotOnScreen("preset | edit")
	h.key(special(tea.KeyDown))
	h.key(special(tea.KeyDown))
	h.wantOnScreen("▸ /stop", "❯ /stop ")
	h.key(special(tea.KeyUp))
	h.wantOnScreen("▸ /status ")
	if !c.win.Following() {
		t.Error("the arrows scrolled the conversation while the palette was open")
	}

	// Tab completes to the chosen command and a space, which closes it.
	h.key(special(tea.KeyTab))
	if h.draft() != "/status " || c.pal.Open() || c.rows.Palette != 0 || c.rows.Body != body {
		t.Errorf("after tab the draft is %q, the palette open %v, the rows %+v", h.draft(), c.pal.Open(), c.rows)
	}
	h.wantOnScreen("enter send", "❯ /status ")
	h.wantNotOnScreen("commands matching", sentence)

	// Taking the space away opens it again.
	h.key(special(tea.KeyBackspace))
	h.wantOnScreen("commands matching /status", fmt.Sprintf("2 of %d", n), "▸ /status ")

	// Esc closes it and leaves the draft, and the reply, alone.
	h.key(esc())
	if h.draft() != "/status" || c.pal.Open() || sess.interrupts != 0 {
		t.Errorf("after esc the draft is %q, the palette open %v, and %d interrupts were sent", h.draft(), c.pal.Open(), sess.interrupts)
	}
	h.wantOnScreen("enter send", "replying")
	h.wantNotOnScreen("commands matching", sentence)
	// It stays closed while the draft is still a command, with nothing
	// offered after the cursor.
	h.key(special(tea.KeyBackspace))
	h.wantNotOnScreen("commands matching", "preset")
	// Only now does esc stop the reply.
	h.key(esc())
	if sess.interrupts != 1 {
		t.Errorf("esc with the palette closed sent %d interrupts", sess.interrupts)
	}
	// A draft that stopped being a command lets the next one open it again.
	h.key(ctrl('c'))
	h.typeText("/to")
	h.wantOnScreen("commands matching /to", "▸ /top")

	// After esc nothing is offered for a whole name either, until a space
	// follows it: then what the command takes is.
	h.key(ctrl('c'))
	h.typeText("/theme")
	h.wantOnScreen("❯ /theme  auto | dark | light")
	h.key(esc())
	h.wantNotOnScreen("auto | dark | light")
	h.typeText(" ")
	h.wantOnScreen("❯ /theme auto | dark | light")
	h.wantNotOnScreen("commands matching")
	h.typeText("d")
	h.wantNotOnScreen("auto | dark | light")

	// Nothing typed or chosen here was said to the contact.
	if len(sess.said) != 0 || sess.typings != 0 {
		t.Errorf("the contact was sent %q and %d typing signals", sess.said, sess.typings)
	}
}

// Enter on a command line: the name as typed when it is a command, else the
// chosen one, completed, and run unless it needs an argument first. A slash
// alone is only completed.
func TestEnterOnACommand(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		// typed is the draft, and downs how often the down arrow is pressed.
		typed string
		downs int
		// What is left in the box, the note, and how the reply in flight
		// fared. printed is the title of what the command put in the
		// conversation.
		draft, note string
		interrupts  int
		printed     string
		// editor says the command opened the status line's editor.
		editor bool
	}{
		{name: "a name in full is run", typed: "/stop", interrupts: 1},
		{name: "a name in capitals too", typed: "/STOP", interrupts: 1},
		{name: "the start of a name runs the first match", typed: "/sto", interrupts: 1},
		{name: "the chosen command is run", typed: "/st", downs: 2, interrupts: 1},
		{name: "a name that is a command wins over the first match", typed: "/status", printed: "status"},
		{name: "a match that is not the start of the name", typed: "/tus", editor: true},
		{name: "a slash alone is completed and not run", typed: "/", draft: "/statusline "},
		{name: "a slash alone, another command chosen", typed: "/", downs: 2, draft: "/stop "},
		{name: "a command that needs an argument is completed", typed: "/op", draft: "/open "},
		{name: "and says so when run without one", typed: "/open", draft: "/open", note: "usage: /open <contact>"},
		{name: "a space after it is no argument", typed: "/open ", draft: "/open ", note: "usage: /open <contact>"},
		{name: "nothing matches", typed: "/zzz", draft: "/zzz", note: "no command /zzz; /help lists them; start with // to send it"},
		{name: "a space closes the list: the name must be whole", typed: "/sto ", draft: "/sto ", note: "no command /sto; /help lists them; start with // to send it"},
		{name: "a slash and a space", typed: "/ x", draft: "/ x", note: "no command /; /help lists them; start with // to send it"},
		{name: "an argument for a command that takes none", typed: "/stop now", draft: "/stop now", note: "/stop takes no argument"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, trueDark())
			h.size(100, 30)
			sess := h.openChat()
			h.from(sess, live(), messages(t, 1, 3, fixedNow()), delta(core.Delta{Type: core.DeltaTyping, Typing: true}))
			h.typeText(tc.typed)
			for range tc.downs {
				h.key(special(tea.KeyDown))
			}
			h.key(enter())
			if h.draft() != tc.draft || h.note() != tc.note || sess.interrupts != tc.interrupts {
				t.Errorf("the draft is %q, the note %q, and %d interrupts were sent; want %q, %q and %d",
					h.draft(), h.note(), sess.interrupts, tc.draft, tc.note, tc.interrupts)
			}
			var printed string
			if len(h.m.chat.locals) > 0 {
				printed = h.m.chat.locals[0].title
			}
			if printed != tc.printed {
				t.Errorf("the command printed %q, want %q", printed, tc.printed)
			}
			if got := h.m.screen == screenStatusLine; got != tc.editor {
				t.Errorf("the status line's editor is open: %v, want %v", got, tc.editor)
			}
			if len(sess.said) != 0 || sess.typings != 0 {
				t.Errorf("the contact was sent %q and %d typing signals", sess.said, sess.typings)
			}
			if tc.note != "" {
				h.wantOnScreen(tc.note)
			}
		})
	}
}

// A line that starts with one slash is the client's own. Nothing of it
// reaches the contact: not the line, and not the sign that its human is
// writing. Two slashes make it a message again.
func TestACommandIsNeverSent(t *testing.T) {
	t.Parallel()
	h := newHarness(t, trueDark())
	h.size(100, 30)
	sess := h.openChat()
	h.from(sess, live())
	// slowly types a draft a second a key, so that it is never the limit on
	// typing signals that keeps one from being sent.
	slowly := func(s string) {
		for _, r := range s {
			h.now = h.now.Add(time.Second)
			h.send(tea.KeyPressMsg{Code: r, Text: string(r)})
		}
	}
	for _, line := range []string{"/", "/help", "/help stop", "/nosuch thing", "/ x", "/usr/local/bin is where it lives", "/stop\\"} {
		slowly(line)
		h.key(enter())
		h.key(enter())
		if len(sess.said) != 0 || sess.typings != 0 {
			t.Fatalf("%q: the contact was sent %q and %d typing signals", line, sess.said, sess.typings)
		}
		// A slash alone is completed to the first command, which the second
		// enter runs: the status line's editor, left again here.
		if h.m.screen == screenStatusLine {
			h.key(esc())
		}
		h.key(ctrl('c'))
	}
	// Pasted, it is a command all the same.
	h.now = h.now.Add(time.Second)
	h.send(tea.PasteMsg{Content: "/help"})
	h.send(tea.PasteMsg{Content: " stop"})
	if sess.typings != 0 {
		t.Errorf("a pasted command sent %d typing signals", sess.typings)
	}
	h.key(ctrl('c'))

	// Several lines that start with a slash read as a command and cannot be
	// one: nothing is sent, the note says how to send it, the draft stays.
	slowly("/tmp/x is gone")
	h.key(ctrl('j'))
	slowly("what now?")
	h.key(enter())
	refused := "not sent: a draft that starts with / is a command; start with // to send it"
	if len(sess.said) != 0 || sess.typings != 0 || h.note() != refused || h.draft() != "/tmp/x is gone\nwhat now?" {
		t.Errorf("a draft of two lines: sent %q, %d typing signals, the note %q, the draft %q", sess.said, sess.typings, h.note(), h.draft())
	}
	h.wantOnScreen(refused, "❯ /tmp/x is gone")
	h.wantNotOnScreen("commands matching")
	h.key(ctrl('c'))

	// Two slashes: a message that starts with one. The contact hears of it
	// being written from the second slash on.
	slowly("/")
	if sess.typings != 0 {
		t.Errorf("one slash sent %d typing signals", sess.typings)
	}
	slowly("/x")
	if sess.typings == 0 {
		t.Error("a message that starts with two slashes sent no typing signal")
	}
	h.wantNotOnScreen("commands matching")
	h.key(enter())
	if !slices.Equal(sess.said, []string{"/x"}) || h.draft() != "" {
		t.Errorf("//x sent %q and left the draft %q", sess.said, h.draft())
	}
	h.wantOnScreen("❯ /x")

	// And so may it have several lines.
	h.send(tea.PasteMsg{Content: "//etc/hosts\nhas it"})
	h.key(enter())
	if !slices.Equal(sess.said, []string{"/x", "/etc/hosts\nhas it"}) {
		t.Errorf("a message of two lines that starts with two slashes sent %q", sess.said)
	}
}

// history is a page of the log with a tool call in it and a model call.
func history(t *testing.T) client.Update {
	t.Helper()
	at := fixedNow().Add(-time.Hour)
	var out strings.Builder
	for i := 1; i <= 9; i++ {
		fmt.Fprintf(&out, "row %d\n", i)
	}
	return client.Update{Kind: client.UpdateEvents, Events: []core.Event{
		logged(t, 1, at, core.KindUserMsg, core.MsgBody{Text: "what is 6*7?"}),
		logged(t, 2, at, core.KindToolCall, core.ToolCall{ID: "c1", Name: "python", Code: "print(6*7)"}),
		logged(t, 3, at, core.KindToolResult, core.ToolResult{ID: "c1", Output: out.String()}),
		logged(t, 4, at, core.KindAttempt, core.AttemptBody{Route: "fake/fake-1", Input: 2000, Cached: 1500, Output: 90, MS: 1234}),
		logged(t, 5, at, core.KindAgentMsg, core.MsgBody{Text: "It is 42."}),
	}}
}

// talking is a conversation with ada that is connected and has a little
// history, on a server that also has bob, Bobby and a contact with no name.
func talking(t *testing.T) (*harness, *fakeSession) {
	t.Helper()
	h := newHarness(t, trueDark(),
		client.Contact{ID: 1, Name: "ada", Provider: "fake", Model: "fake-1"},
		client.Contact{ID: 2, Name: "bob", Provider: "fake", Model: "fake-1"},
		client.Contact{ID: 3, Name: "Bobby", Provider: "fake", Model: "fake-1"},
		client.Contact{ID: 4, Provider: "fake", Model: "fake-1"},
	)
	h.size(100, 40)
	sess := h.openChat()
	h.from(sess, live(), history(t))
	return h, sess
}

func TestStatusCommand(t *testing.T) {
	t.Parallel()
	h, sess := talking(t)
	h.size(100, 44) // two answers of 17 rows each, now that each has a row for the mods
	items := len(h.m.chat.tr.Items())
	h.command("/status")
	h.wantOnScreen(
		"\nstatus",
		"  connection   live",
		"  server       /data/contactd.sock",
		"  contact      ada, id 1",
		"  route        fake/fake-1",
		"  last call    cache 75% of 2.0k tokens, 90 out, 1.2s",
		"  color        truecolor (detected from the environment)",
		"  background   dark (",
		"  glyphs       unicode (",
		"  images       text (",
		"  width        wcwidth (",
		"  cell         8x16 (",
		"  shift+enter  no (",
		"  probe        off (",
	)
	// It is the client's own: not in the log's account, and not said.
	if got := len(h.m.chat.tr.Items()); got != items || len(sess.said) != 0 || h.draft() != "" {
		t.Errorf("after /status there are %d items of %d, %q was said, and the draft is %q", got, items, sess.said, h.draft())
	}
	// Every line of what Resolve decided is there.
	for _, line := range termcaps.Explain(h.m.opt.Env, h.m.over, h.m.replies) {
		name, rest, _ := strings.Cut(line, ": ")
		h.wantOnScreen(name, rest)
	}

	// What it says is true of the moment it was asked.
	h.from(sess,
		client.Update{Kind: client.UpdateEvents, Events: []core.Event{
			logged(t, 6, fixedNow(), core.KindAttempt, core.AttemptBody{Error: "rate_limit"}),
		}},
		client.Update{Kind: client.UpdateState, State: client.StateDown, Err: errors.New("connection reset")},
	)
	h.command("/status")
	h.wantOnScreen("  connection   offline, retrying: connection reset", "  last call    failed: rate_limit")
	// The first one still says what was true then.
	h.wantOnScreen("  connection   live")

	// Before any model call.
	h = newHarness(t, noneASCII())
	h.size(100, 40)
	h.openChat()
	h.command("/status")
	h.wantOnScreen("  connection   connecting", "  last call    none yet", "  glyphs       ascii (", "  color        none (")
}

func TestStopCommand(t *testing.T) {
	t.Parallel()
	h, sess := talking(t)
	h.command("/stop")
	if sess.interrupts != 0 || h.note() != "no reply is being written" {
		t.Errorf("with no reply in flight /stop sent %d interrupts and said %q", sess.interrupts, h.note())
	}
	h.from(sess, delta(core.Delta{Type: core.DeltaTyping, Typing: true}))
	h.command("/stop")
	if sess.interrupts != 1 || h.note() != "" || h.draft() != "" {
		t.Errorf("/stop sent %d interrupts, said %q and left %q", sess.interrupts, h.note(), h.draft())
	}
}

func TestHelpCommand(t *testing.T) {
	t.Parallel()
	h, _ := talking(t)
	h.size(100, 70)
	mockMods(h.m.cmds)
	h.command("/help")
	h.wantOnScreen("\ncommands", "\nkeys")
	for _, spec := range h.m.cmds.Match("") {
		h.wantOnScreen("  /"+spec.Name, spec.Summary)
		if spec.Usage != "" {
			h.wantOnScreen("/" + spec.Name + " " + spec.Usage)
		}
	}
	h.wantOnScreen(
		"  /stop                       stop the reply in flight (esc)",
		"  /image pixels | blocks | text | auto | save <n> [path]\n                              how images are shown this session, or save one",
		"  /log [n]                    the last commits  mod git",
		"  enter        send the message, or run the command",
		"  ctrl+j       a new line; so are alt+enter, shift+enter, and a backslash before enter",
		"  esc          close the list of commands; else stop the reply",
		"  ctrl+d       leave, when the box is empty",
	)
	// A command's summary already names its key: it is not said twice.
	_, screen, _ := strings.Cut(h.screen(), "\ncommands")
	for _, key := range []string{"(esc)", "(ctrl+p)", "(ctrl+o)", "(ctrl+l)", "(ctrl+c)"} {
		if n := strings.Count(screen, key); n != 1 {
			t.Errorf("%s is on screen %d times", key, n)
		}
	}
	if n := strings.Count(screen, "esc"); n != 2 {
		t.Errorf("esc is named %d times, want once for /stop and once among the keys:\n%s", n, screen)
	}

	// One command.
	for _, line := range []string{"/help rename", "/help /rename", "/help RENAME"} {
		h, _ := talking(t)
		h.command(line)
		h.wantOnScreen("\nhelp", "  /rename [name]              rename this contact")
		h.wantNotOnScreen("/details", "\nkeys", "\ncommands")
	}
	h.command("/help nosuch")
	if h.note() != "no command /nosuch; /help lists them" || h.draft() != "/help nosuch" {
		t.Errorf("/help nosuch said %q and left %q", h.note(), h.draft())
	}
	h.key(ctrl('c'))
	h.command("/help a b")
	if h.note() != "usage: /help [command]" || h.draft() != "/help a b" {
		t.Errorf("/help a b said %q and left %q", h.note(), h.draft())
	}
}

// What a command prints is shown from its first row: help that is longer
// than the transcript starts at its title, and the rest is a page down.
func TestALongAnswerIsShownFromItsTop(t *testing.T) {
	t.Parallel()
	h, _ := talking(t)
	h.size(80, 24)
	h.command("/help")
	rows := h.rows()
	if rows[2] != "commands" || !strings.HasPrefix(rows[3], "  /statusline preset | edit") {
		t.Errorf("the help starts on the rows\n%q\n%q", rows[2], rows[3])
	}
	h.wantOnScreen("↓ newer")
	h.wantNotOnScreen("\nkeys")
	for range 3 {
		h.key(special(tea.KeyPgDown))
	}
	h.wantOnScreen("\nkeys", "  ctrl+d       leave, when the box is empty")
	h.wantNotOnScreen("↓ newer")

	// A short answer is at the newest line, with what came before it above.
	h.command("/help stop")
	h.wantOnScreen("\nhelp", "  /stop ", "  ctrl+d       leave")
	if !h.m.chat.win.Following() {
		t.Error("after a short answer the view does not follow the newest line")
	}
}

func TestContactsAndNewCommands(t *testing.T) {
	t.Parallel()
	h, sess := talking(t)
	h.then(h.command("/contacts"))
	h.wantOnScreen("Contacts", "▸ ada", "bob")
	if !sess.closed || h.m.chat != nil {
		t.Errorf("after /contacts the session is closed: %v, and the chat is %v", sess.closed, h.m.chat)
	}

	h, sess = talking(t)
	if cmd := h.command("/new"); cmd != nil {
		t.Errorf("/new answered with %v", cmd())
	}
	h.wantOnScreen("New contact", "System prompt")
	if !sess.closed || h.m.chat != nil {
		t.Errorf("after /new the session is closed: %v, and the chat is %v", sess.closed, h.m.chat)
	}
	// The form is the one the list opens: it makes the contact and opens it.
	h.typeText("carol")
	h.key(enter())
	h.typeText("You are Carol.")
	h.then(h.key(enter()))
	h.wantOnScreen("carol", "Message carol")
	if len(h.b.created) != 1 || h.b.created[0].Name != "carol" {
		t.Errorf("created %+v", h.b.created)
	}
}

func TestOpenCommand(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, line string
		// opened is the id of the contact that is open afterwards.
		opened core.ContactID
		note   string
	}{
		{name: "by name", line: "/open bob", opened: 2},
		{name: "by name in another case", line: "/open BOB", opened: 2},
		{name: "by a quoted name", line: `/open "bob"`, opened: 2},
		{name: "by id", line: "/open 3", opened: 3},
		{name: "by id as the list writes it", line: "/open #4", opened: 4},
		{name: "by the start of a name", line: "/open bobb", opened: 3},
		{name: "the whole name wins over a start", line: "/open bob", opened: 2},
		{name: "a start two names share", line: "/open bo", opened: 1, note: `2 contacts match "bo"; open one by its id: 2, 3`},
		{name: "nobody", line: "/open carol", opened: 1, note: `no contact is called "carol"`},
		{name: "an id nobody has", line: "/open 9", opened: 1, note: `no contact is called "9"`},
		{name: "the one that is open", line: "/open ada", opened: 1, note: "ada is the contact that is open"},
		{name: "nothing", line: `/open ""`, opened: 1, note: "usage: /open <contact>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h, sess := talking(t)
			h.then(h.command(tc.line))
			if h.m.chat == nil || h.m.chat.contact.ID != tc.opened || h.note() != tc.note {
				t.Fatalf("the open contact is %+v and the note %q, want contact %d and %q", h.m.chat, h.note(), tc.opened, tc.note)
			}
			if moved := tc.opened != 1; sess.closed != moved || len(h.b.sessions) == 1 == moved {
				t.Errorf("the first session is closed: %v; there are %d sessions", sess.closed, len(h.b.sessions))
			}
			if tc.note != "" {
				h.wantOnScreen(tc.note, "ada  fake/fake-1")
			} else {
				h.wantOnScreen("Message " + h.m.chat.label)
			}
		})
	}

	t.Run("the server does not answer", func(t *testing.T) {
		t.Parallel()
		h, sess := talking(t)
		h.b.err = fmt.Errorf("%w: connection refused", client.ErrUnavailable)
		h.then(h.command("/open bob"))
		if sess.closed || !strings.HasSuffix(h.note(), "connection refused (is contactd running?)") {
			t.Errorf("the session is closed: %v, and the note is %q", sess.closed, h.note())
		}
	})

	t.Run("an answer that comes after the conversation was left", func(t *testing.T) {
		t.Parallel()
		h, _ := talking(t)
		found := h.command("/open bob")()
		h.then(h.key(ctrl('p')))
		h.send(found)
		h.wantOnScreen("Contacts")
		if h.m.chat != nil || len(h.b.sessions) != 1 {
			t.Errorf("a late answer opened %+v", h.m.chat)
		}
		// Nor does it move a conversation opened since.
		h.key(enter())
		h.send(found)
		if h.m.chat.contact.ID != 1 {
			t.Errorf("a late answer moved the conversation to %+v", h.m.chat.contact)
		}
	})
}

func TestRenameCommand(t *testing.T) {
	t.Parallel()

	t.Run("with a name", func(t *testing.T) {
		t.Parallel()
		h, sess := talking(t)
		h.typeText("half a thought")
		h.key(ctrl('c'))
		h.then(h.command(`/rename "grace hopper"`))
		if h.b.contacts[0].Name != "grace hopper" || sess.closed || len(h.b.sessions) != 1 {
			t.Fatalf("the server has %+v; the session is closed: %v", h.b.contacts[0], sess.closed)
		}
		h.wantOnScreen("grace hopper  fake/fake-1", "❯ Message grace hopper", "renamed to grace hopper", "It is 42.")
		h.wantNotOnScreen("ada")
		if title := h.m.View().WindowTitle; title != "contactd: grace hopper" {
			t.Errorf("the window is called %q", title)
		}
		// The words after the name are the name.
		h.then(h.command("/rename ada lovelace"))
		h.wantOnScreen("ada lovelace  fake/fake-1")
	})

	t.Run("the prompt, and back", func(t *testing.T) {
		t.Parallel()
		h, sess := talking(t)
		h.typeText("half a thought")
		h.key(ctrl('c'))
		if cmd := h.command("/rename"); cmd != nil {
			t.Errorf("/rename answered with %v", cmd())
		}
		h.wantOnScreen("Rename ada", "enter rename")
		h.wantNotOnScreen("It is 42.")
		// The conversation goes on under the prompt.
		h.from(sess, delta(core.Delta{Type: core.DeltaUser, Text: "said meanwhile", Seq: 7}))
		h.typeText("x")
		h.key(esc())
		h.wantOnScreen("ada  fake/fake-1", "It is 42.", "said meanwhile", "❯ Message ada")
		if sess.closed || h.b.contacts[0].Name != "ada" {
			t.Errorf("esc closed the session: %v; the server has %+v", sess.closed, h.b.contacts[0])
		}

		h.command("/rename")
		h.typeText(" l")
		h.then(h.key(enter()))
		h.wantOnScreen("ada l  fake/fake-1", "renamed to ada l", "said meanwhile")
		if sess.closed || h.b.contacts[0].Name != "ada l" {
			t.Errorf("renaming closed the session: %v; the server has %+v", sess.closed, h.b.contacts[0])
		}
		// A key in the conversation is the conversation's again.
		h.typeText("hi")
		h.key(enter())
		if !slices.Equal(sess.said, []string{"hi"}) {
			t.Errorf("after the prompt the conversation sent %q", sess.said)
		}
	})

	t.Run("an answer that comes after the conversation was left", func(t *testing.T) {
		t.Parallel()
		for name, leave := range map[string]func(h *harness){
			"for another contact": func(h *harness) { h.then(h.command("/open bob")) },
			"for the new-contact form": func(h *harness) {
				h.command("/new")
				h.typeText("carol")
			},
			"for the list": func(h *harness) { h.then(h.command("/contacts")) },
		} {
			h, _ := talking(t)
			renamed := h.command("/rename grace")()
			failed := openedMsg{err: errors.New("the log is read-only"), back: true, sess: h.b.sessions[0]}
			leave(h)
			before := h.screen()
			for _, late := range []tea.Msg{renamed, failed} {
				if cmd := h.send(late); cmd != nil || h.screen() != before {
					t.Errorf("left %s, a late answer gave %v and the screen\n%s\nwas\n%s", name, cmd, h.screen(), before)
				}
			}
		}
	})

	t.Run("refused", func(t *testing.T) {
		t.Parallel()
		h, _ := talking(t)
		long := strings.Repeat("n", nameLimit+1)
		h.then(h.command("/rename " + long))
		if h.note() != "a name has at most 64 characters" || h.draft() != "/rename "+long || h.b.contacts[0].Name != "ada" {
			t.Errorf("a long name: the note is %q, the draft %q, the server has %+v", h.note(), h.draft(), h.b.contacts[0])
		}
		h.key(ctrl('c'))
		h.b.err = errors.New("the log is read-only")
		h.then(h.command("/rename grace"))
		h.wantOnScreen("the log is read-only", "ada  fake/fake-1")
		// In the prompt the same refusal is the prompt's to show.
		h.command("/rename")
		h.then(h.key(enter()))
		h.wantOnScreen("Rename ada", "the log is read-only")
	})
}

func TestDetailsCommand(t *testing.T) {
	t.Parallel()
	h, _ := talking(t)
	h.wantOnScreen("⎿ row 1", "… 5 more rows (ctrl+o)")
	h.wantNotOnScreen("row 5")
	h.command("/details")
	h.wantOnScreen("row 5", "row 9")
	h.wantNotOnScreen("more rows")
	h.command("/details")
	h.wantNotOnScreen("row 5")
}

// The three setting commands change one setting for the session. What the
// terminal can do is worked out again, the screen follows, and the note
// names the line of chat.toml that would make it last.
func TestSettingCommands(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		line string
		// What the settings and the caps are afterwards, on a truecolor
		// terminal with a dark background set and Unicode glyphs.
		over func(*termcaps.Overrides)
		caps func(*termcaps.Caps)
		note string
		// shown is on screen afterwards.
		shown string
	}{
		{
			line:  "/theme light",
			over:  func(o *termcaps.Overrides) { o.Theme = "light" },
			caps:  func(c *termcaps.Caps) { c.Background = termcaps.BgLight },
			note:  `theme: light for this session; theme = "light" in chat.toml keeps it`,
			shown: "background   light (",
		},
		{
			line:  "/theme AUTO",
			over:  func(o *termcaps.Overrides) { o.Theme = "" },
			caps:  func(c *termcaps.Caps) { c.Background = termcaps.BgUnknown },
			note:  `theme: auto (unknown now) for this session; theme = "auto" in chat.toml keeps it`,
			shown: "background   unknown (",
		},
		{
			line:  "/glyphs ascii",
			over:  func(o *termcaps.Overrides) { o.Glyphs = "ascii" },
			caps:  func(c *termcaps.Caps) { c.Glyphs = termcaps.GlyphsASCII },
			note:  `glyphs: ascii for this session; glyphs = "ascii" in chat.toml keeps it`,
			shown: "> what is 6*7?",
		},
		{
			line:  "/color 16",
			over:  func(o *termcaps.Overrides) { o.Color = "16" },
			caps:  func(c *termcaps.Caps) { c.Color = termcaps.Color16 },
			note:  `color: 16 for this session; color = "16" in chat.toml keeps it`,
			shown: "color        16 (",
		},
		{
			line:  "/color none",
			over:  func(o *termcaps.Overrides) { o.Color = "none" },
			caps:  func(c *termcaps.Caps) { c.Color = termcaps.ColorNone },
			note:  `color: none for this session; color = "none" in chat.toml keeps it`,
			shown: "color        none (",
		},
	} {
		t.Run(tc.line, func(t *testing.T) {
			t.Parallel()
			h, _ := talking(t)
			over, caps := h.m.over, h.m.caps
			tc.over(&over)
			tc.caps(&caps)
			cmd := h.command(tc.line)
			if h.m.over != over || h.m.caps != caps || h.note() != tc.note || h.draft() != "" {
				t.Fatalf("the settings are %+v, the caps %+v, the note %q, the draft %q; want %+v, %+v, %q",
					h.m.over, h.m.caps, h.note(), h.draft(), over, caps, tc.note)
			}
			got := inOrder(cmd)
			want := []tea.Msg{tea.ClearScreen()}
			if strings.HasPrefix(tc.line, "/color") {
				// The renderer hears of the new number of colours first.
				want = []tea.Msg{tea.ColorProfileMsg{Profile: profile(caps.Color)}, tea.ClearScreen()}
			}
			if !slices.Equal(got, want) {
				t.Errorf("the command gives %v, want %v", got, want)
			}
			h.wantOnScreen(tc.note)
			if raw := h.m.View().Content; !strings.Contains(raw, h.m.th.Style(toneDone).Render(tc.note)) {
				t.Errorf("the note is not in the tone of something done:\n%q", raw)
			}
			// The model drops the renderer's message; it is not a key.
			for _, msg := range got {
				h.send(msg)
			}
			h.command("/status")
			h.wantOnScreen(tc.shown)
		})
	}

	// The same choice again changes nothing, and so clears nothing.
	h, _ := talking(t)
	if cmd := h.command("/theme dark"); cmd != nil {
		t.Errorf("/theme dark on a dark theme gives %v", inOrder(cmd))
	}
	if cmd := h.command("/color auto"); cmd != nil {
		t.Errorf("/color auto on a detected colour gives %v", inOrder(cmd))
	}
	h.wantOnScreen(`color: auto (truecolor now) for this session; color = "auto" in chat.toml keeps it`)
	// A choice outlasts a late answer from the terminal.
	h.command("/theme light")
	h.send(background(nearBlack()))
	if h.m.caps.Background != termcaps.BgLight {
		t.Errorf("a late answer undid /theme light: %v", h.m.caps.Background)
	}

	// A word the setting does not take changes nothing and says what it takes.
	for line, want := range map[string]string{
		"/theme blue":       "usage: /theme auto | dark | light",
		"/theme":            "usage: /theme auto | dark | light",
		"/theme dark light": "usage: /theme auto | dark | light",
		`/glyphs ""`:        "usage: /glyphs auto | unicode | ascii",
		"/glyphs emoji":     "usage: /glyphs auto | unicode | ascii",
		"/color 8":          "usage: /color auto | truecolor | 256 | 16 | none",
	} {
		h, _ := talking(t)
		over, caps := h.m.over, h.m.caps
		cmd := h.command(line)
		if cmd != nil || h.m.over != over || h.m.caps != caps || h.note() != want || h.draft() != line {
			t.Errorf("%s: the settings are %+v, the note %q, the draft %q", line, h.m.over, h.note(), h.draft())
		}
	}
}

func TestTopAndBottomCommands(t *testing.T) {
	t.Parallel()
	h := newHarness(t, trueDark())
	sess := h.openChat()
	h.from(sess, live(), messages(t, 101, 300, fixedNow()))
	c := h.m.chat
	first := func() int {
		h.screen()
		i, _, ok := c.win.Visible()
		if !ok {
			t.Fatal("no block is in view")
		}
		return i
	}
	h.wantOnScreen("message 300")
	h.command("/top")
	if first() != 0 || c.win.Following() || sess.olders != 1 {
		t.Errorf("after /top the first block in view is %d, following %v, and earlier pages were asked for %d times", first(), c.win.Following(), sess.olders)
	}
	h.wantOnScreen("message 101", "loading earlier", "↓ newer")
	h.wantNotOnScreen("message 300")
	// Asked again while the page is on its way, it does not ask twice.
	h.command("/top")
	if sess.olders != 1 {
		t.Errorf("earlier pages were asked for %d times", sess.olders)
	}
	// The page arrives above; /top goes to its first message.
	older := messages(t, 1, 100, fixedNow())
	older.Kind, older.Start = client.UpdateOlder, true
	h.from(sess, older)
	h.wantOnScreen("message 101")
	h.command("/top")
	h.wantOnScreen("the beginning", "message 1")
	if first() != 0 || sess.olders != 1 {
		t.Errorf("at the beginning the first block in view is %d and earlier pages were asked for %d times", first(), sess.olders)
	}

	h.command("/bottom")
	h.wantOnScreen("message 300")
	h.wantNotOnScreen("↓ newer")
	if !c.win.Following() {
		t.Error("after /bottom the view does not follow")
	}
}

func TestRedrawAndQuitCommands(t *testing.T) {
	t.Parallel()
	h, sess := talking(t)
	if cmd := h.command("/redraw"); !clears(cmd) || h.draft() != "" {
		t.Errorf("/redraw gives %v and leaves %q", produced(cmd), h.draft())
	}
	cmd := h.command("/quit")
	if cmd == nil {
		t.Fatal("/quit gives nothing")
	}
	if _, quit := cmd().(tea.QuitMsg); !quit || len(sess.said) != 0 {
		t.Errorf("/quit gives %v", cmd())
	}
}

// /reload reads the [glyph] table of chat.toml again, and says what the
// file has that cannot be used.
func TestReloadCommand(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write := func(s string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "chat.toml"), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	env, over := terminal(t, trueDark())
	h := newHarnessWith(t, Options{Env: env, Overrides: over, Dirs: conf.Dirs{Config: dir}})
	h.size(120, 30)
	sess := h.openChat()
	h.from(sess, live(), history(t))
	h.wantOnScreen("⏺ python")

	// No file is the same as an empty one.
	cmd := h.command("/reload")
	if cmd == nil || h.draft() != "" {
		t.Fatalf("/reload gives %v and leaves %q", cmd, h.draft())
	}
	if again := h.send(cmd()); again != nil {
		t.Errorf("a table that did not change gives %v", produced(again))
	}
	h.wantOnScreen("read chat.toml again; glyph overrides: 0", "⏺ python")

	// A new glyph is drawn at once, in every block already on screen. The
	// file's other settings are for the start of a session.
	write("theme = \"light\"\n[glyph]\ntool = \"●\"\nelbow = \"└\"\n")
	if again := h.send(h.command("/reload")()); !clears(again) {
		t.Errorf("a new table gives %v", produced(again))
	}
	h.wantOnScreen("read chat.toml again; glyph overrides: 2", "● python", "└ row 1")
	h.wantNotOnScreen("⏺", "⎿")
	if h.m.caps.Background != termcaps.BgDark {
		t.Errorf("/reload changed the theme to %v", h.m.caps.Background)
	}
	// It is kept through a change of what the terminal can do.
	h.send(h.command("/color 256")())
	h.wantOnScreen("● python")

	// What the table asks for that cannot be drawn is named; the rest counts.
	write("nosuch = 1\n[glyph]\ntool = \"●\"\nturn = \"8\"\n")
	h.then(h.command("/reload"))
	h.wantOnScreen(`chat.toml: glyph "turn": there is no glyph of that name; line 1: unknown key "nosuch"`, "● python", "⎿ row 1")

	// A file that cannot be read changes nothing, and says where it broke.
	write("[glyph\n")
	if again := h.send(h.command("/reload")()); again != nil {
		t.Errorf("a broken file gives %v", produced(again))
	}
	h.wantOnScreen("chat.toml", "line 1", "● python")

	// On the list of contacts the answer is the list's to show.
	write("[glyph]\ntool = \"●\"\npointer = \"four\"\n")
	reloaded := h.command("/reload")()
	h.then(h.key(ctrl('p')))
	h.send(reloaded)
	h.wantOnScreen("Contacts", `chat.toml: glyph "pointer"`)

	// With no config directory there is no file to read.
	h = newHarness(t, trueDark())
	h.openChat()
	if cmd := h.command("/reload"); cmd != nil || h.note() != "nothing to read again: no config directory is known" {
		t.Errorf("with no config directory /reload gives %v and says %q", cmd, h.note())
	}
}

// A command of a mod that nothing runs says so, and does nothing else.
func TestACommandOfAModThatIsNotRunning(t *testing.T) {
	t.Parallel()
	const line, note = "/standup", "/standup is a mod's command, and that mod is not running"
	h, sess := talking(t)
	mockMods(h.m.cmds)
	bar, caps := h.m.bar, h.m.caps
	cmd := h.command(line)
	if cmd != nil || h.note() != note || len(sess.said) != 0 {
		t.Errorf("%s: gives %v, says %q, sent %q", line, cmd, h.note(), sess.said)
	}
	if !reflect.DeepEqual(h.m.bar, bar) || h.m.caps != caps {
		t.Errorf("%s changed the bar or the caps", line)
	}
	h.wantOnScreen(note)
}

// What a command printed stays after the item that was the newest when it
// was made: under it whatever arrives later, and above it whatever was
// there, also when earlier history is loaded and when a day begins.
func TestALocalBlockStaysWhereItWasMade(t *testing.T) {
	t.Parallel()
	h := newHarness(t, trueDark())
	h.size(100, 60)
	sess := h.openChat()
	h.from(sess, live(), messages(t, 11, 13, fixedNow()))
	c := h.m.chat
	item := func(seq uint64) scrollback.Key { return scrollback.Key{Kind: blockItem, ID: seq} }
	local := func(id uint64) scrollback.Key { return scrollback.Key{Kind: blockLocal, ID: id} }
	// order is the keys of the blocks, without the rules.
	order := func() []scrollback.Key {
		h.screen()
		var keys []scrollback.Key
		for i := range c.list.Len() {
			if k := c.list.Key(i); k.Kind != blockDay && k.Kind != blockBeginning {
				keys = append(keys, k)
			}
		}
		return keys
	}
	want := func(what string, keys ...scrollback.Key) {
		t.Helper()
		if got := order(); !slices.Equal(got, keys) {
			t.Errorf("%s: the blocks are\n%v, want\n%v", what, got, keys)
		}
	}
	// below is whether a comes before b on the screen.
	before := func(a, b string) bool {
		screen := h.screen()
		i, j := strings.Index(screen, a), strings.Index(screen, b)
		return i >= 0 && j > i
	}

	h.command("/status")
	want("made", item(11), item(12), item(13), local(1))
	if got := c.list.Rev(c.list.Len() - 1); got != 0 {
		t.Errorf("the block's revision is %d", got)
	}

	// A message sent after it is under it, on its way and once it arrived.
	h.typeText("and then?")
	h.key(enter())
	pending := scrollback.Key{Kind: blockPending, ID: 1}
	want("a message on its way", item(11), item(12), item(13), local(1), pending)
	h.from(sess, client.Update{Kind: client.UpdateAck, Text: "and then?", Seq: 14})
	want("the message arrived", item(11), item(12), item(13), local(1), item(14))
	h.from(sess,
		delta(core.Delta{Type: core.DeltaTyping, Typing: true}),
		delta(core.Delta{Type: core.DeltaText, Text: "Then this."}),
	)
	if !before("  probe        off", "❯ and then?") || !before("❯ and then?", "Then this.") || !before("message 13", "\nstatus") {
		t.Errorf("the status block is not between message 13 and what came after:\n%s", h.screen())
	}
	h.from(sess, delta(core.Delta{Type: core.DeltaMessage, Text: "Then this.", Seq: 15}), delta(core.Delta{Type: core.DeltaTyping}))
	want("a reply", item(11), item(12), item(13), local(1), item(14), item(15))

	// A second one is after the newest item now, and the first has not moved.
	h.command("/help stop")
	want("a second", item(11), item(12), item(13), local(1), item(14), item(15), local(2))
	// Two made at the same moment keep the order they were made in.
	h.command("/help quit")
	want("a third", item(11), item(12), item(13), local(1), item(14), item(15), local(2), local(3))

	// Earlier history goes above all of it.
	older := messages(t, 1, 10, fixedNow())
	older.Kind, older.Start = client.UpdateOlder, true
	h.from(sess, older)
	keys := order()
	if len(keys) != 18 || keys[0] != item(1) || !slices.Equal(keys[10:], []scrollback.Key{
		item(11), item(12), item(13), local(1), item(14), item(15), local(2), local(3),
	}) {
		t.Errorf("after an earlier page the blocks are\n%v", keys)
	}

	// The next day's first message has its rule under the blocks, not over.
	h.now = h.now.Add(24 * time.Hour)
	h.from(sess, client.Update{Kind: client.UpdateEvents, Events: []core.Event{
		logged(t, 16, h.now, core.KindUserMsg, core.MsgBody{Text: "good morning"}),
	}})
	h.size(100, 90)
	if !before("leave (ctrl+c)", "Tue 13 Oct 2026") || !before("Tue 13 Oct 2026", "good morning") {
		t.Errorf("the day's rule is not between the blocks and the day's first message:\n%s", h.screen())
	}

	// A failure has no number of its own: it stands with the event it
	// follows, and a block made after that event stays under both.
	h.from(sess, delta(core.Delta{Type: core.DeltaError, Text: "step_limit"}))
	h.command("/help top")
	keys = order()
	if n := len(keys); n < 3 || keys[n-3] != item(16) || keys[n-2].Kind != blockFailure || keys[n-1] != local(4) {
		t.Errorf("after a failure the last blocks are %v", keys[max(len(keys)-3, 0):])
	}

	// None of it is in the log's account, and none of it was said.
	for _, it := range c.tr.Items() {
		if strings.Contains(it.Text, "connection") || strings.Contains(it.Text, "/stop") {
			t.Errorf("a local block is among the items: %+v", it)
		}
	}
	if !slices.Equal(sess.said, []string{"and then?"}) {
		t.Errorf("the contact was sent %q", sess.said)
	}

	// A block made before anything was read is above what is read later.
	h = newHarness(t, trueDark())
	h.size(100, 60)
	sess = h.openChat()
	h.command("/help stop")
	h.wantOnScreen("\nhelp", "  /stop ")
	h.from(sess, live(), messages(t, 1, 3, fixedNow()))
	c = h.m.chat
	want("made first", local(1), item(1), item(2), item(3))
}

// Opening the palette takes rows from the transcript and draws none of its
// blocks again; neither does choosing in it, or closing it.
func TestThePaletteDrawsNoBlocks(t *testing.T) {
	t.Parallel()
	h := newHarness(t, trueDark())
	h.size(100, 30)
	sess := h.openChat()
	h.from(sess, live(), messages(t, 1, 10000, fixedNow()))
	h.wantOnScreen("message 10000")
	c := h.m.chat
	drawn, built := c.win.Stats().Rendered, c.rebuilds
	same := func(what string) {
		t.Helper()
		h.screen()
		if got := c.win.Stats().Rendered; got != drawn || c.rebuilds != built {
			t.Errorf("%s drew %d blocks and rebuilt the list %d times", what, got-drawn, c.rebuilds-built)
		}
	}
	h.typeText("/")
	h.wantOnScreen("│ commands ", "message 10000")
	same("opening the palette")
	h.typeText("st")
	same("narrowing it")
	h.key(special(tea.KeyDown))
	same("choosing in it")
	h.key(special(tea.KeyTab))
	same("completing")
	h.key(special(tea.KeyBackspace))
	h.key(esc())
	h.wantOnScreen("message 10000")
	h.wantNotOnScreen("commands matching")
	same("closing it")

	// A command that prints draws its own block and no other, and the list
	// is built once for it.
	h.key(ctrl('c'))
	h.command("/help stop")
	h.screen()
	if got := c.win.Stats().Rendered - drawn; got < 1 || got > 2 || c.rebuilds != built+1 {
		t.Errorf("printing drew %d blocks and rebuilt the list %d times", got, c.rebuilds-built)
	}
}

// The palette on small terminals: it never takes the transcript's last
// three rows, and what is drawn of it fits.
func TestThePaletteFitsTheTerminal(t *testing.T) {
	t.Parallel()
	for name, caps := range map[string]termcaps.Caps{"truecolor": trueDark(), "no colour, ASCII": noneASCII()} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			for _, width := range []int{100, 60, 59, 40, 39, 30, 20, 10} {
				for _, height := range []int{30, 16, 12, 8, 5, 3} {
					h := newHarness(t, caps)
					h.size(width, height)
					sess := h.openChat()
					h.from(sess, live(), everything(t))
					c := h.m.chat
					for _, draft := range []string{"/", "st", "atusline", " preset", "\b\b\b\b\b\b\b\b\b\b\b\b\b\b\b\b\bzzz"} {
						for _, r := range draft {
							if r == '\b' {
								h.key(special(tea.KeyBackspace))
								continue
							}
							h.send(tea.KeyPressMsg{Code: r, Text: string(r)})
						}
						what := fmt.Sprintf("the draft %q", h.draft())
						h.wantFits(what, width, height)
						if rows := h.rows(); len(rows) != height {
							t.Errorf("%s at %dx%d: %d rows", what, width, height, len(rows))
						}
						if c.rows.Palette > 0 && c.rows.Body < 3 || c.rows.Palette > c.pal.Want(width) || c.rows.Body < 1 {
							t.Errorf("%s at %dx%d: the rows are %+v", what, width, height, c.rows)
						}
						if v := h.m.View(); v.Cursor == nil || v.Cursor.Y < c.rows.InputTop() || v.Cursor.X >= width {
							t.Errorf("%s at %dx%d: the cursor is at %+v", what, width, height, v.Cursor)
						}
						if caps.Glyphs == termcaps.GlyphsASCII && width >= 40 {
							for _, row := range h.rows()[c.rows.InputTop()-c.rows.Bar-c.rows.BarRule-c.rows.Palette:] {
								for i := range len(row) {
									if row[i] >= 0x80 {
										t.Fatalf("%s at %dx%d: with ASCII glyphs a row holds the byte %#x: %q", what, width, height, row[i], row)
									}
								}
							}
						}
					}
				}
			}
		})
	}
}

// The colours on the wire follow /color, up as well as down: after it the
// program writes the colours of the new profile and no others. Going up is
// what the renderer has to be told for: left at the old profile it would
// bring the theme's new colours down again.
func TestTheColorCommandOnTheWire(t *testing.T) {
	t.Parallel()
	// A terminal with a dark background that shows no colour.
	noneDark := trueDark()
	noneDark.Color = termcaps.ColorNone
	for _, tc := range []struct {
		name string
		from termcaps.Caps
		// before are the kinds of colour written until the command, and
		// after the kinds written from the screen being cleared for it.
		before []string
		arg    string
		after  []string
	}{
		{"down to 256", trueDark(), []string{"bg 24-bit", "fg 24-bit"}, "256", []string{"bg 256", "fg 256"}},
		{"down to 16", trueDark(), []string{"bg 24-bit", "fg 24-bit"}, "16", []string{"fg 16"}},
		{"down to none", trueDark(), []string{"bg 24-bit", "fg 24-bit"}, "none", nil},
		{"up to 256", light16(), []string{"fg 16"}, "256", []string{"bg 256", "fg 256"}},
		{"up to truecolor", light16(), []string{"fg 16"}, "truecolor", []string{"bg 24-bit", "fg 24-bit"}},
		{"up from none", noneDark, nil, "16", []string{"fg 16"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env, over := terminal(t, tc.from)
			r := startProgram(t, Options{Env: env, Overrides: over, Open: &client.Contact{ID: 1, Name: "ada", Provider: "deepseek", Model: "deepseek-flash"}})
			untilShown(t, r.out, 0, "Message ada")
			mark := r.out.Mark()
			r.p.Send(updatesMsg{sess: r.b.sessions[0], batch: []client.Update{live(), history(t)}})
			untilShown(t, r.out, mark, "It is 42.")
			if got := colourKinds(t, string(r.out.Since(0))); !slices.Equal(got, tc.before) {
				t.Fatalf("before the command the program wrote the colours %q, want %q", got, tc.before)
			}

			mark = r.out.Mark()
			r.write("/color " + tc.arg + "\r")
			note := "color: " + tc.arg + " for this session"
			untilShown(t, r.out, mark, note)
			// The note is drawn with the command, and the screen is cleared
			// in a frame of its own: that frame is what is waited for.
			until(t, r.out, mark, "\x1b[2J")
			r.quit()
			wrote := string(r.out.Since(mark))
			// The line being typed was still drawn in the old colours; what
			// counts is everything from the screen being cleared.
			cleared := strings.LastIndex(wrote, "\x1b[2J")
			if cleared < 0 {
				t.Fatalf("the screen was not cleared:\n%q", wrote)
			}
			after := wrote[cleared:]
			if got := colourKinds(t, after); !slices.Equal(got, tc.after) {
				t.Errorf("after /color %s the program wrote the colours %q, want %q:\n%q", tc.arg, got, tc.after, after)
			}
			// The whole screen was drawn again for the new profile.
			for _, text := range []string{"ada", "what is 6*7?", "It is 42.", "live", note} {
				if !strings.Contains(ansi.Strip(after), text) {
					t.Errorf("after /color %s the screen was not drawn again: %q is missing:\n%q", tc.arg, text, after)
				}
			}
			if r.m.caps.Color.String() != tc.arg {
				t.Errorf("the caps say %v", r.m.caps.Color)
			}
		})
	}
}
