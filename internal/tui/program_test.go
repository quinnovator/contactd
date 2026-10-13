package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/quinnovator/contactd/internal/client"
	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
	"github.com/quinnovator/contactd/internal/tui/tuitest"
)

// until waits for the program to have written want since mark, and fails
// the test if it falls idle again and again without it.
func until(t *testing.T, out *tuitest.Recorder, mark int, want string) {
	t.Helper()
	for range 50 {
		out.Quiet(t)
		if bytes.Contains(out.Since(mark), []byte(want)) {
			return
		}
	}
	t.Fatalf("the program never wrote %q; since the mark it wrote:\n%q", want, out.Since(mark))
}

// untilShown is until for text on the screen: want is looked for in what the
// program wrote with the escape sequences taken out, so text in two colours
// is found as it reads.
func untilShown(t *testing.T, out *tuitest.Recorder, mark int, want string) {
	t.Helper()
	for range 50 {
		out.Quiet(t)
		if strings.Contains(ansi.Strip(string(out.Since(mark))), want) {
			return
		}
	}
	t.Fatalf("the program never showed %q; since the mark it wrote:\n%q", want, out.Since(mark))
}

// rig is the interface under a real program, with a pipe where the terminal
// would be read and a recorder where it would be written: keys and the
// terminal's answers go into the pipe, and what the program draws is read
// back from the recorder.
type rig struct {
	t    *testing.T
	m    *model
	p    *tea.Program
	b    *fakeBackend
	out  *tuitest.Recorder
	keys *io.PipeWriter
	done chan error
}

// startProgram runs the interface for the terminal and the settings in opt,
// 100 columns by 30 rows, over a server that has ada. The wait for the
// terminal's answers never ends by itself: a test that wants the deadline
// sends it. A fake server handed in as opt.Backend is the one used, with
// what it has, and so is a clock handed in as opt.Now; without one it is
// always noon.
func startProgram(t *testing.T, opt Options) *rig {
	t.Helper()
	r := &rig{
		t:    t,
		b:    &fakeBackend{contacts: []client.Contact{{ID: 1, Name: "ada", Provider: "deepseek", Model: "deepseek-flash"}}},
		out:  &tuitest.Recorder{},
		done: make(chan error, 1),
	}
	if b, ok := opt.Backend.(*fakeBackend); ok {
		r.b = b
	}
	in, keys := io.Pipe()
	r.keys = keys
	t.Cleanup(func() { _ = keys.Close() }) // closing a pipe's writer never fails
	opt.Backend, opt.Location = r.b, time.UTC
	if opt.Now == nil {
		opt.Now = fixedNow
	}
	opt.Program = []tea.ProgramOption{
		tea.WithInput(in), tea.WithOutput(r.out),
		tea.WithWindowSize(100, 30), tea.WithEnvironment([]string{"TERM=xterm-256color"}),
	}
	r.m, r.p = newProgram(t.Context(), opt)
	r.m.probeWait = 24 * time.Hour
	go func() {
		_, err := r.p.Run()
		r.done <- err
	}()
	return r
}

// write sends bytes as the terminal would: keys pressed, or its answers.
func (r *rig) write(s string) {
	r.t.Helper()
	if _, err := r.keys.Write([]byte(s)); err != nil {
		r.t.Fatal(err)
	}
}

// quit presses ctrl+c and waits for the program to end. Everything written
// before has been read by then, and the model may be looked at.
func (r *rig) quit() {
	r.t.Helper()
	r.write("\x03")
	if err := <-r.done; err != nil {
		r.t.Fatalf("the program ended with %v", err)
	}
}

// The interface under a real program, with a pipe for a keyboard and a
// recorder for a terminal: it draws the list, opens a conversation, takes a
// key, stays silent when nothing changes, and leaves when told to.
func TestTheProgramRunsOnAPipe(t *testing.T) {
	t.Parallel()
	env, over := terminal(t, trueDark())
	r := startProgram(t, Options{Env: env, Overrides: over})
	out, p, press := r.out, r.p, r.write

	untilShown(t, out, 0, "▸ ada  deepseek/deepseek-flash")
	mark := out.Mark()
	press("\r")
	untilShown(t, out, mark, "❯ Message ada")
	untilShown(t, out, mark, "● connecting │ deepseek-flash")
	until(t, out, mark, "\x1b]2;contactd: ada\a")

	mark = out.Mark()
	press("x")
	until(t, out, mark, "x")
	out.Quiet(t)
	if typed := out.Since(mark); len(typed) > 40 {
		t.Errorf("one typed character wrote %d bytes: %q", len(typed), typed)
	}

	// A message that changes nothing writes nothing.
	mark = out.Mark()
	p.Send(tea.FocusMsg{})
	p.Send(updatesMsg{})
	out.Quiet(t)
	if idle := out.Since(mark); len(idle) != 0 {
		t.Errorf("a message that changes nothing wrote %d bytes: %q", len(idle), idle)
	}

	// ctrl+c empties the box, and then leaves.
	mark = out.Mark()
	press("\x03")
	untilShown(t, out, mark, "Message ada")
	r.quit()
	// The mouse is never asked for: selecting text stays the terminal's.
	for _, mode := range []string{"?1000h", "?1002h", "?1003h", "?1006h"} {
		if bytes.Contains(out.Since(0), []byte(mode)) {
			t.Errorf("the program switched mouse mode %s on", mode)
		}
	}
	if m, b := r.m, r.b; m.chat == nil || m.chat.input.Value() != "" || len(b.sessions) != 1 {
		t.Errorf("the program left with %d sessions and the chat %+v", len(b.sessions), m.chat)
	}
}

// The questions go out once, when the client starts, and they are the ones
// the plan for this terminal names: none at all for one that is asked
// nothing, the background alone for Apple's, and for the rest the background
// and then one write that ends with the device attributes.
func TestTheTerminalIsAskedOnce(t *testing.T) {
	t.Parallel()
	inTmux := asked()
	inTmux.Tmux = "/tmp/tmux-501/default,1,0"
	for _, tc := range []struct {
		name string
		env  termcaps.Env
		over termcaps.Overrides
		// background says the background colour is asked for, and questions
		// are the bytes of the write that follows it.
		background bool
		questions  string
	}{
		{name: "probing switched off", env: asked(), over: termcaps.Overrides{NoProbe: true}},
		{name: "a terminal too plain to ask", env: termcaps.Env{Term: "vt100", Detected: termcaps.ColorNone}},
		{
			name: "Apple Terminal", background: true,
			env: termcaps.Env{Term: "xterm-256color", TermProgram: "Apple_Terminal", Detected: termcaps.Color256},
		},
		{name: "a terminal", env: asked(), background: true, questions: termcaps.ProbeBytes(termcaps.ProbeFull)},
		{name: "tmux", env: inTmux, background: true, questions: termcaps.ProbeBytes(termcaps.ProbeTmux)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := startProgram(t, Options{Env: tc.env, Overrides: tc.over})
			untilShown(t, r.out, 0, "Contacts")
			// The list is there once the server has answered: enter opens
			// nothing before.
			untilShown(t, r.out, 0, "ada  deepseek/deepseek-flash")
			// Opening a conversation, typing and leaving ask nothing more.
			mark := r.out.Mark()
			r.write("\r")
			untilShown(t, r.out, mark, "Message ada")
			r.write("x\x03")
			r.quit()

			wrote := string(r.out.Since(0))
			if got, want := strings.Count(wrote, ansi.RequestBackgroundColor), map[bool]int{true: 1}[tc.background]; got != want {
				t.Errorf("the background was asked for %d times, want %d:\n%q", got, want, wrote)
			}
			if tc.questions != "" {
				at := strings.Index(wrote, tc.questions)
				if at < strings.Index(wrote, ansi.RequestBackgroundColor) || strings.Count(wrote, tc.questions) != 1 {
					t.Errorf("the questions %q were not written once, after the background's:\n%q", tc.questions, wrote)
				}
				wrote = wrote[:at] + wrote[at+len(tc.questions):]
			}
			// Nothing else of the kind is written by the client. (Bubble Tea
			// asks about two modes and the keyboard by itself.)
			for name, question := range map[string]string{
				"kitty graphics":        "\x1b_G",
				"the name":              "\x1b[>q",
				"the cell size":         "\x1b[16t",
				"the device attributes": "\x1b[c",
				"anything through tmux": "\x1bPtmux;",
			} {
				if strings.Contains(wrote, question) {
					t.Errorf("%s was asked for besides the plan's questions:\n%q", name, wrote)
				}
			}
		})
	}
}

// The terminal's answers, written to the pipe the way a terminal writes
// them, change what the client knows it can do. The ones that were asked
// for count only until the deadline.
func TestTheTerminalAnswersOnThePipe(t *testing.T) {
	t.Parallel()
	const (
		kittyOK = "\x1b_Gi=31;OK\x1b\\"
		ghostty = "\x1bP>|ghostty 1.2.0\x1b\\"
		// A cell 16 pixels wide and 32 high: the height comes first.
		cell = "\x1b[6;32;16t"
		da1  = "\x1b[?62;4c"
	)
	start := termcaps.Caps{
		Color: termcaps.ColorTrue, Background: termcaps.BgUnknown, Glyphs: termcaps.GlyphsUnicode,
		Images: termcaps.ImagesBlocks, Width: termcaps.WidthWc, CellW: 8, CellH: 16,
	}
	with := func(change func(*termcaps.Caps)) termcaps.Caps {
		c := start
		change(&c)
		return c
	}
	for _, tc := range []struct {
		name string
		// late says the deadline passes before the answers come.
		late    bool
		answers string
		want    termcaps.Caps
		// shown and wrote are what the screen then shows and a sequence it
		// is drawn with.
		shown, wrote string
	}{
		{
			name: "kitty graphics and ghostty give pixels", answers: kittyOK + ghostty + cell + da1,
			want: with(func(c *termcaps.Caps) {
				c.Images, c.Terminal, c.CellW, c.CellH, c.Settled = termcaps.ImagesPixels, "ghostty 1.2.0", 16, 32, true
			}),
		},
		{
			name: "kitty graphics alone do not", answers: kittyOK + da1,
			want: with(func(c *termcaps.Caps) { c.Settled = true }),
		},
		{
			name: "a dark background", answers: "\x1b]11;rgb:1111/1414/1717\x1b\\",
			want:  with(func(c *termcaps.Caps) { c.Background = termcaps.BgDark }),
			wrote: "38;2;94;196;200",
		},
		{
			name: "a light background", answers: "\x1b]11;rgb:f7f7/f5f5/eeee\x07",
			want:  with(func(c *termcaps.Caps) { c.Background = termcaps.BgLight }),
			wrote: "38;2;15;111;119",
		},
		{
			name: "key enhancements", answers: "\x1b[?1u",
			want:  with(func(c *termcaps.Caps) { c.ShiftEnter = true }),
			shown: "shift+enter new line",
		},
		{
			name: "clusters", answers: "\x1b[?2027;2$y",
			want: with(func(c *termcaps.Caps) { c.Width = termcaps.WidthGrapheme }),
		},
		{
			name: "after the deadline", late: true, answers: kittyOK + ghostty + cell + da1,
			want: with(func(c *termcaps.Caps) { c.Settled = true }),
		},
		{
			name: "the background after the deadline", late: true, answers: "\x1b]11;rgb:1111/1414/1717\x1b\\" + kittyOK + ghostty,
			want:  with(func(c *termcaps.Caps) { c.Background, c.Settled = termcaps.BgDark, true }),
			wrote: "38;2;94;196;200",
		},
		{
			// Answers to questions nobody asked, and sequences no one knows.
			name:    "noise",
			answers: "\x1bP1$r0m\x1b\\" + "\x1b_Zhello\x1b\\" + "\x1b[>1;10;0c" + "\x1b]777;notify\x07" + "\x1b[4;960;1600t" + "\x1b_Gi=7;OK\x1b\\",
			want:    start,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := startProgram(t, Options{Env: asked(), Open: &client.Contact{ID: 1, Name: "ada", Provider: "deepseek", Model: "deepseek-flash"}})
			untilShown(t, r.out, 0, "ctrl+j new line")
			if tc.late {
				r.p.Send(probeDeadlineMsg{})
			}
			mark := r.out.Mark()
			r.write(tc.answers)
			if tc.shown != "" {
				untilShown(t, r.out, mark, tc.shown)
			}
			if tc.wrote != "" {
				until(t, r.out, mark, tc.wrote)
			}
			r.quit()
			if r.m.caps != tc.want {
				t.Errorf("the terminal can do\n%+v, want\n%+v", r.m.caps, tc.want)
			}
			// None of it was typed.
			if got := r.m.chat.input.Value(); got != "" {
				t.Errorf("the answers left %q in the box", got)
			}
		})
	}
}

// A tmux the environment did not name is asked about the terminal behind it
// when its own answer gives it away, in one more write and no more.
func TestTheRoundThroughTmuxIsWrittenOnce(t *testing.T) {
	t.Parallel()
	r := startProgram(t, Options{Env: asked()})
	untilShown(t, r.out, 0, "Contacts")
	mark := r.out.Mark()
	r.write("\x1bP>|tmux 3.5a\x1b\\" + "\x1b[?1;2c")
	until(t, r.out, mark, termcaps.TmuxRoundBytes())
	// The terminal behind answers through tmux, and tmux names itself again.
	r.write("\x1b_Gi=32;OK\x1b\\" + "\x1bP>|ghostty 1.2.0\x1b\\" + "\x1bP>|tmux 3.5a\x1b\\" + "\x1b[?62;4c")
	r.quit()
	if got := strings.Count(string(r.out.Since(0)), termcaps.TmuxRoundBytes()); got != 1 {
		t.Errorf("the round through tmux was written %d times", got)
	}
	want := termcaps.Caps{
		Color: termcaps.ColorTrue, Background: termcaps.BgUnknown, Glyphs: termcaps.GlyphsUnicode,
		Images: termcaps.ImagesPixels, Width: termcaps.WidthWc, CellW: 8, CellH: 16,
		Tmux: true, Terminal: "ghostty 1.2.0", Settled: true,
	}
	if r.m.caps != want {
		t.Errorf("the terminal can do\n%+v, want\n%+v", r.m.caps, want)
	}
}

// The renderer is told how many colours the terminal shows and never left
// to work it out, so what reaches the terminal is what the theme picked for
// it: 24-bit colours where it shows them, indexes or the 16 where it does
// not, and none at all where it shows none.
func TestTheProgramWritesThePickedColours(t *testing.T) {
	t.Parallel()
	for _, l := range looks() {
		t.Run(l.name, func(t *testing.T) {
			t.Parallel()
			env, over := terminal(t, l.caps)
			r := startProgram(t, Options{Env: env, Overrides: over})
			untilShown(t, r.out, 0, "Contacts")
			// The list is there once the server has answered: enter opens
			// nothing before.
			untilShown(t, r.out, 0, "ada  deepseek/deepseek-flash")
			mark := r.out.Mark()
			r.write("\r")
			untilShown(t, r.out, mark, "Message ada")
			mark = r.out.Mark()
			r.p.Send(updatesMsg{sess: r.b.sessions[0], batch: []client.Update{live(), {Kind: client.UpdateEvents, Events: []core.Event{
				logged(t, 1, fixedNow(), core.KindUserMsg, core.MsgBody{Text: "plot last week's latency"}),
				logged(t, 2, fixedNow(), core.KindToolCall, core.ToolCall{ID: "c", Name: "python", Code: "df.plot.bar()"}),
				logged(t, 3, fixedNow(), core.KindToolResult, core.ToolResult{ID: "c", Output: "no such column\n", IsError: true}),
				logged(t, 4, fixedNow(), core.KindAgentMsg, core.MsgBody{Text: "p99 **peaked** on `Thursday`."}),
			}}}})
			untilShown(t, r.out, mark, "plot last week's latency")
			r.quit()

			wrote := string(r.out.Since(0))
			got := colourKinds(t, wrote)
			if !slices.Equal(got, slices.Sorted(slices.Values(l.colours))) {
				t.Errorf("the program wrote the colours %q, want %q:\n%q", got, l.colours, wrote)
			}
			// Bold is no colour, and every terminal is sent it.
			if !strings.Contains(wrote, "\x1b[1mContacts") {
				t.Errorf("the list's title was not written bold:\n%q", wrote)
			}
			if l.caps.Glyphs == termcaps.GlyphsASCII {
				for i := range len(wrote) {
					if wrote[i] >= 0x80 {
						t.Fatalf("with ASCII glyphs the program wrote the byte %#x:\n%q", wrote[i], wrote)
					}
				}
			}
		})
	}
}

// Run is all of the above in one call: it works out what the terminal can
// do, tells the renderer how many colours that is, shows the interface until
// the person leaves or the context ends, and then ends the conversation's
// session.
func TestRun(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		// leave ends the run: a key, or the context.
		leave func(keys io.Writer, cancel func()) error
	}{
		{name: "the person leaves", leave: func(keys io.Writer, _ func()) error {
			_, err := keys.Write([]byte("\x03"))
			return err
		}},
		{name: "the context ends", leave: func(_ io.Writer, cancel func()) error {
			cancel()
			return nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ada := client.Contact{ID: 1, Name: "ada", Provider: "deepseek", Model: "deepseek-flash"}
			b := &fakeBackend{contacts: []client.Contact{ada}}
			in, keys := io.Pipe()
			t.Cleanup(func() { _ = keys.Close() }) // closing a pipe's writer never fails
			out := &tuitest.Recorder{}
			env, over := terminal(t, light16())
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				done <- Run(ctx, Options{
					Backend: b, Open: &ada, Now: fixedNow, Location: time.UTC, Env: env, Overrides: over,
					Program: []tea.ProgramOption{
						tea.WithInput(in), tea.WithOutput(out), tea.WithWindowSize(100, 30),
						tea.WithEnvironment([]string{"TERM=xterm-256color"}),
					},
				})
			}()
			untilShown(t, out, 0, "❯ Message ada")
			if err := tc.leave(keys, cancel); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatalf("Run returned %v", err)
			}
			if len(b.sessions) != 1 || !b.sessions[0].closed {
				t.Errorf("Run left the conversation's session open: %+v", b.sessions)
			}
			// Either way the terminal's own screen is back by the time Run returns.
			wrote := string(out.Since(0))
			if on, off := strings.LastIndex(wrote, "\x1b[?1049h"), strings.LastIndex(wrote, "\x1b[?1049l"); on < 0 || off < on {
				t.Errorf("Run did not leave the alternate screen it took:\n%q", wrote)
			}
			// A terminal of 16 colours on a light background was sent those.
			if got := colourKinds(t, wrote); !slices.Equal(got, []string{"fg 16"}) || !strings.Contains(wrote, "\x1b[34;1m❯") {
				t.Errorf("Run wrote the colours %q:\n%q", got, wrote)
			}
		})
	}
}

// The terminal contract: the client takes the alternate screen, shows the
// terminal's own cursor in the box, names the window, and never asks for the
// mouse. Leaving gives all of it back, in whichever way the client is left.
func TestLeavingPutsTheTerminalBack(t *testing.T) {
	t.Parallel()
	const (
		altOn, altOff       = "\x1b[?1049h", "\x1b[?1049l"
		pasteOn, pasteOff   = "\x1b[?2004h", "\x1b[?2004l"
		cursorOn, cursorOff = "\x1b[?25h", "\x1b[?25l"
	)
	for _, tc := range []struct {
		name string
		// leave is the keys that end the client from a conversation.
		leave string
	}{
		{name: "ctrl+c on an empty box", leave: "\x03"},
		{name: "ctrl+d on an empty box", leave: "\x04"},
		{name: "ctrl+c twice with a draft", leave: "half a thought\x03\x03"},
		{name: "from the list", leave: "\x10q"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env, over := terminal(t, trueDark())
			ada := client.Contact{ID: 1, Name: "ada", Provider: "deepseek", Model: "deepseek-flash"}
			r := startProgram(t, Options{Env: env, Overrides: over, Open: &ada})
			untilShown(t, r.out, 0, "❯ Message ada")
			until(t, r.out, 0, cursorOn)
			// The cursor is put after the prompt, in the box: row 28, column 5.
			// A first frame can be drawn before the program has heard the
			// terminal's size, for 24 rows; the one for 30 rows is waited for.
			until(t, r.out, 0, "\x1b[28;5H")
			started := string(r.out.Since(0))
			for name, seq := range map[string]string{
				"the alternate screen": altOn, "bracketed paste": pasteOn,
				"the window's name": "\x1b]2;contactd: ada\a", "the cursor": cursorOn,
			} {
				if !strings.Contains(started, seq) {
					t.Errorf("starting did not write %s (%q):\n%q", name, seq, started)
				}
			}

			mark := r.out.Mark()
			r.write(tc.leave)
			if err := <-r.done; err != nil {
				t.Fatalf("the program ended with %v", err)
			}
			left := string(r.out.Since(mark))
			for name, seq := range map[string]string{"the alternate screen": altOff, "bracketed paste": pasteOff} {
				if !strings.Contains(left, seq) {
					t.Errorf("leaving did not give back %s (%q):\n%q", name, seq, left)
				}
			}
			all := string(r.out.Since(0))
			if strings.LastIndex(all, altOff) < strings.LastIndex(all, altOn) {
				t.Errorf("the client left on the alternate screen:\n%q", all)
			}
			// Nothing is drawn on the terminal's own screen afterwards.
			after := all[strings.LastIndex(all, altOff)+len(altOff):]
			if text := ansi.Strip(after); strings.TrimSpace(text) != "" {
				t.Errorf("after the alternate screen the client wrote %q", text)
			}
			// The cursor is left showing, also when the last screen had none.
			if strings.LastIndex(all, cursorOn) < strings.LastIndex(all, cursorOff) {
				t.Errorf("the cursor was left hidden:\n%q", all[max(strings.LastIndex(all, cursorOff)-40, 0):])
			}
			for _, mode := range []string{"?1000h", "?1002h", "?1003h", "?1006h"} {
				if strings.Contains(all, mode) {
					t.Errorf("the program switched mouse mode %s on", mode)
				}
			}
			if len(r.b.sessions) != 1 {
				t.Errorf("the client followed %d sessions", len(r.b.sessions))
			}
		})
	}
}

// A terminal's answer can arrive in pieces, and a slow link can hold a piece
// back so long that the rest reads as keys. Whatever that leaves in the box,
// the client must still be there afterwards: it hears the terminal, draws,
// and takes keys.
func TestAnswersInPiecesDoNotStopTheProgram(t *testing.T) {
	t.Parallel()
	ada := client.Contact{ID: 1, Name: "ada", Provider: "deepseek", Model: "deepseek-flash"}
	r := startProgram(t, Options{Env: asked(), Open: &ada})
	untilShown(t, r.out, 0, "ctrl+j new line")
	for _, piece := range []string{
		"\x1b_Gi=31;O", "K\x1b\\", // kitty graphics
		"\x1b]11;rgb:11", "11/1414/1717\x1b\\", // the background
		"\x1bP>|ghos", "tty 1.2.0\x1b\\", // the name
		"\x1b[6;32", ";16t", // the cell size
		"\x1b[?62;", "4c", // the device attributes
		// Sequences that never end, bytes that begin nothing, and bytes that
		// are not text at all.
		"\x1b_G", "\x1bP", "\x1b]", "\x1b[", "\x9b", "\x90", "\x1b\x1b\x1b", "\xff\xfe", "\x00\x01\x02",
	} {
		r.write(piece)
		r.out.Quiet(t)
	}
	// It still hears the terminal: a whole answer changes the hint line.
	r.write("\x1b[?1u")
	untilShown(t, r.out, 0, "shift+enter new line")
	// And it still takes keys, each drawn as it is typed.
	for _, key := range []string{"Q", "W"} {
		mark := r.out.Mark()
		r.write(key)
		until(t, r.out, mark, key)
	}
	r.p.Quit()
	if err := <-r.done; err != nil {
		t.Fatalf("the program ended with %v", err)
	}
	if got := r.m.chat.input.Value(); !strings.Contains(got, "QW") {
		t.Errorf("the box holds %q, want the two keys typed last in it", got)
	}
	// What the pieces made of the terminal is what whole answers would have,
	// or less: never something the terminal did not say.
	if c := r.m.caps; c.Color != termcaps.ColorTrue || c.Glyphs != termcaps.GlyphsUnicode || c.Images == termcaps.ImagesText || !c.ShiftEnter {
		t.Errorf("the pieces left the caps %+v", c)
	}
}

// Leaving is no error, however it was done. A panic is one, though the
// program reports it as being killed.
func TestEnded(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		err  error
		// want is an error the result wraps; nil means Run returns nil.
		want error
	}{
		{name: "the person left"},
		{name: "the program was killed", err: tea.ErrProgramKilled},
		{name: "the context ended", err: fmt.Errorf("%w: %w", tea.ErrProgramKilled, context.Canceled)},
		{name: "a signal interrupted it", err: tea.ErrInterrupted},
		{name: "a panic", err: fmt.Errorf("%w: %w", tea.ErrProgramKilled, tea.ErrProgramPanic), want: tea.ErrProgramPanic},
		{name: "no terminal to open", err: io.ErrClosedPipe, want: io.ErrClosedPipe},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ended(tc.err)
			if tc.want == nil {
				if got != nil {
					t.Errorf("ended(%v) = %v, want nil", tc.err, got)
				}
				return
			}
			if !errors.Is(got, tc.want) {
				t.Errorf("ended(%v) = %v, want an error that wraps %v", tc.err, got, tc.want)
			}
		})
	}
}
