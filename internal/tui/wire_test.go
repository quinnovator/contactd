package tui

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/quinnovator/contactd/internal/client"
	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/tui/conf"
	"github.com/quinnovator/contactd/internal/tui/mods"
	"github.com/quinnovator/contactd/internal/tui/mods/wire"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
	"github.com/quinnovator/contactd/internal/tui/termimg"
	"github.com/quinnovator/contactd/internal/tui/tuitest"
)

// What the client counts on from the renderer under Bubble Tea, and what it
// writes to a terminal for the smallest changes there are. The renderer is a
// library that is still moving: each test here fails when an update of it
// changes something a picture, a colour or the safety of the screen rests on.

// pins is the smallest program there is: it shows the text it is told to.
// The tests of the renderer run it in place of the client, so that what
// reaches the terminal is the renderer's doing and nothing else's.
type pins struct {
	view string
	// then is what is shown once the bytes asked for with pinWrite are on
	// their way.
	then string
}

type (
	// pinShow has the text shown.
	pinShow string
	// pinWrite has raw written beside the screen, and then shown in the same
	// turn of the program's loop in which the write is made.
	pinWrite struct{ raw, then string }
)

func (p *pins) Init() tea.Cmd { return nil }

func (p *pins) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case pinShow:
		p.view = string(msg)
	case pinWrite:
		p.then = msg.then
		return p, tea.Raw(msg.raw)
	case tea.RawMsg:
		// The program has just taken the bytes to write. The screen changes
		// now, before anything is flushed.
		if p.then != "" {
			p.view, p.then = p.then, ""
		}
	}
	return p, nil
}

func (p *pins) View() tea.View {
	v := tea.NewView(p.view)
	v.AltScreen = true
	return v
}

// startPins runs pins under a real program, 40 columns by 16 rows, for a
// terminal of the given colours. It returns the program and what it writes.
// The program is stopped when the test ends.
func startPins(t *testing.T, profile colorprofile.Profile, view string) (*tea.Program, *tuitest.Recorder) {
	t.Helper()
	in, keys := io.Pipe()
	out := &tuitest.Recorder{}
	p := tea.NewProgram(&pins{view: view},
		tea.WithContext(t.Context()), tea.WithInput(in), tea.WithOutput(out),
		tea.WithWindowSize(40, 16), tea.WithEnvironment([]string{"TERM=xterm-256color"}),
		tea.WithColorProfile(profile),
	)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = p.Run() // a program that is told to quit ends with no error worth a test
	}()
	t.Cleanup(func() {
		p.Quit()
		<-done
		_ = keys.Close() // closing a pipe's writer never fails
	})
	return p, out
}

// A cell that shows a picture is U+10EEEE with two marks on it, which say
// which row and column of the picture it is. The renderer has to take the
// three characters for one cell, one column wide, however the terminal counts
// cells: a cell it took for two, or for three cells of none, would put every
// cell after it in the wrong column.
func TestAPictureCellIsOneCell(t *testing.T) {
	t.Parallel()
	const cols, rows = 7, 3
	lines := termimg.KittyPlaceholders(42, cols, rows)
	if len(lines) != rows {
		t.Fatalf("%d rows of cells, want %d", len(lines), rows)
	}
	for name, method := range map[string]ansi.Method{"by character": ansi.WcWidth, "by cluster": ansi.GraphemeWidth} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			buf := uv.NewScreenBuffer(cols+4, rows)
			buf.Method = method
			// Each row of cells has text straight after it, as a caption would
			// follow a picture were it on the same row.
			text := make([]string, rows)
			for i, line := range lines {
				text[i] = line + "ab"
			}
			uv.NewStyledString(strings.Join(text, "\n")).Draw(buf, buf.Bounds())
			for y := range rows {
				for x := range cols {
					c := buf.CellAt(x, y)
					if c == nil {
						t.Fatalf("no cell at %d,%d", x, y)
					}
					marks := []rune(c.Content)
					if c.Width != 1 || len(marks) != 3 || marks[0] != 0x10EEEE {
						t.Errorf("the cell at %d,%d is %q, %d wide; want the placeholder and its two marks in one cell", x, y, c.Content, c.Width)
					}
					// The picture's number is the cell's colour, one of the 256.
					if got := fmt.Sprint(c.Style.Fg); got != fmt.Sprint(ansi.IndexedColor(42)) {
						t.Errorf("the cell at %d,%d has the colour %v, want index 42", x, y, c.Style.Fg)
					}
				}
				// What follows the cells is in the column after them, and is
				// not drawn in the picture's colour.
				for i, want := range []string{"a", "b"} {
					c := buf.CellAt(cols+i, y)
					if c == nil || c.Content != want || c.Width != 1 || c.Style.Fg != nil {
						t.Errorf("the cell at %d,%d is %+v, want a plain %q", cols+i, y, c, want)
					}
				}
			}
			// Each row of cells says its own row of the picture, and each cell
			// its own column: no two cells of a picture are the same.
			seen := map[string]bool{}
			for y := range rows {
				for x := range cols {
					seen[buf.CellAt(x, y).Content] = true
				}
			}
			if len(seen) != cols*rows {
				t.Errorf("%d different cells, want %d", len(seen), cols*rows)
			}
		})
	}
}

// The terminal learns which picture a cell shows from the cell's colour: one
// of the 256, by its number. So the renderer must write that colour as the
// number it was given, where the terminal has 256 colours and where it has
// all of them: turned into a 24-bit colour, or into the nearest of 16, it
// would name no picture, or another one.
func TestAnIndexedColourStaysIndexed(t *testing.T) {
	t.Parallel()
	// The first 16 are the ones a renderer could write the short way.
	ids := []int{1, 7, 8, 15, 16, 42, 196, 231, 232, 255}
	var view strings.Builder
	for _, id := range ids {
		view.WriteString(termimg.KittyPlaceholders(id, 2, 1)[0] + "\n")
	}
	for name, profile := range map[string]colorprofile.Profile{"256 colours": colorprofile.ANSI256, "truecolor": colorprofile.TrueColor} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p, out := startPins(t, profile, "0")
			until(t, out, 0, "0")
			mark := out.Mark()
			p.Send(pinShow(view.String() + "done"))
			until(t, out, mark, "done")
			out.Quiet(t)
			wrote := string(out.Since(mark))
			for _, id := range ids {
				// The colour may share its sequence with other attributes.
				cell := regexp.MustCompile(fmt.Sprintf(`\x1b\[(?:[0-9;]*;)?38;5;%d(?:;[0-9;]*)?m%s\p{Mn}\p{Mn}%s\p{Mn}\p{Mn}`, id, placeholder, placeholder))
				if !cell.MatchString(wrote) {
					t.Errorf("the cells of picture %d were not written in colour %d of 256:\n%q", id, id, wrote)
				}
			}
			if strings.Contains(wrote, "38;2;") {
				t.Errorf("a colour of the 256 was written as a 24-bit one:\n%q", wrote)
			}
		})
	}
}

// What is written beside the screen in one turn of the program's loop reaches
// the terminal before the screen drawn in that turn. A picture is sent that
// way, and the cells that show it are drawn after: the other way round the
// terminal would be shown cells of a picture it does not have yet.
func TestWhatIsWrittenBesideTheScreenComesFirst(t *testing.T) {
	t.Parallel()
	const beside = "\x1b_Ga=d,d=I,i=77,q=2\x1b\\"
	p, out := startPins(t, colorprofile.TrueColor, "11111")
	until(t, out, 0, "11111")
	for _, screen := range []string{"22222", "33333", "44444"} {
		mark := out.Mark()
		p.Send(pinWrite{raw: beside, then: screen})
		until(t, out, mark, screen)
		wrote := out.Since(mark)
		raw, frame := bytes.Index(wrote, []byte(beside)), bytes.Index(wrote, []byte(screen))
		if raw < 0 || raw > frame {
			t.Errorf("the bytes beside the screen are at %d and the screen at %d:\n%q", raw, frame, wrote)
		}
		if n := bytes.Count(wrote, []byte(beside)); n != 1 {
			t.Errorf("the bytes beside the screen were written %d times", n)
		}
	}
}

// A sequence that names a string for the terminal, and never ends, swallows
// whatever follows it: the terminal goes on reading the screen as that
// string. The client takes the escape character out of everything it is
// handed, so none should ever be in a screen. This is the second lock: the
// renderer itself drops such a sequence, and one that would set the window's
// title or write to the clipboard, when one is in a screen all the same.
func TestTheRendererDropsStringsThatNeverEnd(t *testing.T) {
	t.Parallel()
	for name, hostile := range map[string]string{
		"a title that never ends":           "\x1b]0;owned",
		"a title":                           "\x1b]0;owned\x07",
		"a clipboard write":                 "\x1b]52;c;b3duZWQ=\x1b\\",
		"a picture command that never ends": "\x1b_Gowned",
		"a device string that never ends":   "\x1bPowned",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p, out := startPins(t, colorprofile.TrueColor, "11111")
			until(t, out, 0, "11111")
			mark := out.Mark()
			p.Send(pinShow("22222" + hostile + "\nstill here"))
			until(t, out, mark, "22222")
			// The screen after it is drawn as any other.
			p.Send(pinShow("33333\nand after"))
			until(t, out, mark, "33333")
			out.Quiet(t)
			wrote := string(out.Since(mark))
			for _, bad := range []string{"\x1b]0", "\x1b]52", "\x1b_G", "\x1bP", "owned", "b3duZWQ", "\x07"} {
				if strings.Contains(wrote, bad) {
					t.Errorf("the terminal was written %q:\n%q", bad, wrote)
				}
			}
		})
	}
}

// A mod answers with text, and its text is all a terminal is ever written:
// under a real program, an answer that holds the start of a window title
// that never ends, a bell and a screen clear reaches the terminal as the
// characters that are left when those are taken out. The fake host hands the
// answer over as the mod gave it, which the real host never does.
func TestAModsAnswerOnTheWire(t *testing.T) {
	t.Parallel()
	const hostile = "\x1b]0;owned\x07\x1b[3Jloud\x1b]52;c;b3duZWQ="
	loud := mods.Manifest{
		ABI: wire.ABI, Name: "loud", Version: "1.0.0", Module: "loud.wasm",
		Segments: []mods.SegmentDecl{{Name: "say", Refresh: mods.RefreshState}},
		Commands: []mods.CommandDecl{{Name: "shout", Summary: hostile}},
	}
	host := newFakeMods(loud)
	host.says("loud", mods.OpSegment, "say", wire.Reply{ABI: wire.ABI, Text: hostile, Short: hostile, Tone: hostile})
	host.says("loud", mods.OpCommand, "shout", wire.Reply{
		ABI: wire.ABI, Note: hostile, Insert: hostile,
		Lines: []wire.Line{{{T: hostile, Tone: "warn"}}, {{T: "printed"}}},
	})
	env, over := terminal(t, trueDark())
	ada := client.Contact{ID: 1, Name: "ada", Provider: "deepseek", Model: "deepseek-flash"}
	r := startProgram(t, Options{Env: env, Overrides: over, Open: &ada, Mods: host})
	untilShown(t, r.out, 0, "Message ada")
	// The segment is in the bar: its text, without what made it dangerous.
	untilShown(t, r.out, 0, "]0;owned[3Jloud]52;c;b3duZWQ=")
	// The mod's command is in the list of commands, with a summary that
	// holds the same, and its answer is a block, a note and a draft.
	mark := r.out.Mark()
	r.write("/sh\r")
	untilShown(t, r.out, mark, "printed")
	r.out.Quiet(t)
	r.p.Quit()
	if err := <-r.done; err != nil {
		t.Fatalf("the program ended with %v", err)
	}
	wrote := string(r.out.Since(0))
	// The program names the window itself, with the one title it has, and
	// takes the name back when it leaves.
	for _, title := range []string{"\x1b]2;contactd: ada\a", "\x1b]2;\a"} {
		if !strings.Contains(wrote, title) {
			t.Errorf("the program did not write the title %q", title)
		}
		wrote = strings.ReplaceAll(wrote, title, "")
	}
	for _, bad := range []string{"\x1b]0", "\x1b]52", "\x1b[3J", "\x07", "\x1b]"} {
		if strings.Contains(wrote, bad) {
			at := strings.Index(wrote, bad)
			t.Errorf("a mod's answer put %q on the wire: %q", bad, wrote[max(at-40, 0):min(at+60, len(wrote))])
		}
	}
	if len(host.sent("loud", mods.OpCommand, "shout")) != 1 {
		t.Errorf("the mod's command was not run once")
	}
}

// While a tool runs, the time it has taken is drawn again five times a
// second. Each of those costs the terminal a few bytes: the digits that
// changed and the way to them and back, never the row, and never the screen.
// The most is when the time gains a digit or goes from seconds to minutes.
func TestTheToolTickOnTheWire(t *testing.T) {
	t.Parallel()
	// most is the bound of the spec, in bytes for one tick.
	const most = 120
	var passed atomic.Int64
	now := func() time.Time { return fixedNow().Add(time.Duration(passed.Load())) }
	env, over := terminal(t, trueDark())
	ada := client.Contact{ID: 1, Name: "ada", Provider: "deepseek", Model: "deepseek-flash"}
	r := startProgram(t, Options{Env: env, Overrides: over, Open: &ada, Now: now})
	untilShown(t, r.out, 0, "Message ada")
	sess := r.b.sessions[0]
	r.p.Send(updatesMsg{sess: sess, batch: []client.Update{
		live(), messages(t, 1, 40, fixedNow()),
		delta(core.Delta{Type: core.DeltaTyping, Typing: true}),
		delta(core.Delta{Type: core.DeltaToolCall, Tool: "python", Text: "import time\ntime.sleep(90)", Seq: 41}),
	}})
	untilShown(t, r.out, 0, "running python")
	r.out.Quiet(t)

	worst := 0
	for _, at := range []time.Duration{
		200 * time.Millisecond, 400 * time.Millisecond, 9900 * time.Millisecond, 10 * time.Second,
		59900 * time.Millisecond, time.Minute, 599 * time.Second, 10 * time.Minute,
	} {
		mark := r.out.Mark()
		passed.Store(int64(at))
		// The tick the program set itself comes within a fifth of a second;
		// this one is not waited for. Whichever is first draws the new time.
		r.p.Send(toolTickMsg{sess: sess})
		for range 50 {
			r.out.Quiet(t)
			if len(r.out.Since(mark)) > 0 {
				break
			}
		}
		r.out.Quiet(t)
		wrote := r.out.Since(mark)
		if len(wrote) == 0 {
			t.Fatalf("after %v the time was not drawn again", at)
		}
		if len(wrote) > most {
			t.Errorf("after %v a tick wrote %d bytes, want at most %d: %q", at, len(wrote), most, wrote)
		}
		worst = max(worst, len(wrote))
		// A tick is digits and cursor movement: it clears nothing, and it
		// never draws a row over.
		if bytes.Contains(wrote, []byte("\x1b[2J")) || bytes.Contains(wrote, []byte("running")) {
			t.Errorf("after %v a tick drew more than the time: %q", at, wrote)
		}
	}
	t.Logf("the largest tick wrote %d bytes", worst)

	// With the clock standing still a tick writes nothing at all.
	mark := r.out.Mark()
	r.p.Send(toolTickMsg{sess: sess})
	r.out.Quiet(t)
	if idle := r.out.Since(mark); len(idle) != 0 {
		t.Errorf("a tick that changes nothing wrote %d bytes: %q", len(idle), idle)
	}
	r.p.Quit()
	if err := <-r.done; err != nil {
		t.Fatalf("the program ended with %v", err)
	}
}

// onlyColours fails the test when a screen holds an escape sequence that is
// not a colour or a link, or a control character other than the line break.
// Those are the two kinds of sequence the client writes into a screen itself.
func onlyColours(t *testing.T, what string, v tea.View) {
	t.Helper()
	rest := regexp.MustCompile(`\x1b\[[0-9;:]*m`).ReplaceAllString(v.Content, "")
	rest = regexp.MustCompile(`\x1b\]8;[^\x07\x1b]*(?:\x07|\x1b\\)`).ReplaceAllString(rest, "")
	for i, c := range rest + v.WindowTitle {
		if c == '\n' && i < len(rest) {
			continue
		}
		if c < 0x20 || c == 0x7f || c >= 0x80 && c <= 0x9f {
			from := max(min(i, len(rest))-30, 0)
			t.Fatalf("%s: the screen holds the control character %#x: %q", what, c, (rest + v.WindowTitle)[from:min(i+30, len(rest)+len(v.WindowTitle))])
		}
	}
}

// The renderer writes some sequences back to the terminal when one is in a
// screen: a string for the terminal that ends as it should, which is how a
// program sends a picture, and others besides. So the client can count on
// nobody but itself: whatever it is handed, by the server, a mod, a command
// of the status line, a file or the keyboard, no screen of it ever holds a
// sequence other than its own colours and links.
func TestNoScreenHoldsASequenceItWasHanded(t *testing.T) {
	t.Parallel()
	// One of every kind of sequence a terminal acts on, each ended as it
	// should be, and a letter after each.
	const hostile = "\x1b_Gi=1,a=d\x1b\\A\x1bPq\x1b\\B\x1bXs\x1b\\C\x1b^p\x1b\\D\x1b]52;c;eA==\x07E\x1b[2JF\u009b2JG\u009d0;t\u009cH\x1bcI\u0090q\u009cJ\x7fK"
	// What is left of it is text.
	const left = "Gi=1,a=d"

	dir := t.TempDir()
	writeBar(t, dir, "[[segment]]\nname = \"state\"\n\n[[segment]]\nname = \"noise\"\nfrom = \"shell\"\ncommand = \"noise\"\nside = \"left\"\n", 0o600)
	shell := &fakeShell{out: map[string]string{"noise": hostile}, fail: map[string]error{}}
	loud := mods.Manifest{
		ABI: wire.ABI, Name: "loud", Version: "1" + hostile, Module: "loud.wasm", Description: hostile,
		Read:      []mods.Grant{mods.GrantMessages},
		Segments:  []mods.SegmentDecl{{Name: "say", Refresh: mods.RefreshModelCall}},
		Commands:  []mods.CommandDecl{{Name: "shout", Usage: hostile, Summary: hostile}},
		Renderers: []mods.RendererDecl{{Kind: "tool:python"}},
	}
	host := newFakeMods(loud)
	host.says("loud", mods.OpSegment, "say", wire.Reply{ABI: wire.ABI, Text: hostile, Short: hostile, Tone: hostile})
	host.says("loud", mods.OpCommand, "shout", wire.Reply{
		ABI: wire.ABI, Note: hostile, Insert: hostile, Tone: hostile,
		Lines: []wire.Line{{{T: hostile, Tone: hostile}, {T: hostile, Bold: true}}},
	})
	host.says("loud", mods.OpRender, "tool:python", wire.Reply{ABI: wire.ABI, Lines: []wire.Line{{{T: hostile}}}})
	host.set("loud", mods.StatusLoading, hostile)
	r := newModRig(t, trueDark(), host, Options{Dirs: conf.Dirs{Config: dir}, Home: filepath.Dir(dir), Shell: shell, ModsOff: hostile})
	loudly := client.Contact{ID: 1, Name: "ad" + hostile, Provider: hostile, Model: hostile}
	r.b.contacts = []client.Contact{loudly}
	r.load()
	paste := tea.PasteMsg{Content: hostile}
	check := func(what string, shows bool) {
		t.Helper()
		r.settle()
		onlyColours(t, what, r.m.View())
		if shows && !strings.Contains(r.screen(), left) {
			t.Errorf("%s: the text that was handed over is not shown at all:\n%s", what, r.screen())
		}
	}

	// The list and its forms.
	r.send(r.m.showContacts()())
	check("the list", true)
	r.typeText("n")
	r.send(paste)
	r.send(special(tea.KeyTab))
	r.send(paste)
	check("the new-contact form", true)
	r.send(esc())
	r.typeText("r")
	r.send(paste)
	check("the rename prompt", true)
	r.send(esc())

	// A conversation in which everything said holds the sequences.
	r.kept = append(r.kept, r.m.open(loudly))
	sess := r.b.sessions[len(r.b.sessions)-1]
	close(sess.updates)
	at := fixedNow()
	r.from(sess, live(), client.Update{Kind: client.UpdateEvents, Start: true, Events: []core.Event{
		logged(t, 1, at, core.KindUserMsg, core.MsgBody{Text: hostile}),
		logged(t, 2, at, core.KindToolCall, core.ToolCall{ID: "c", Name: "python", Code: hostile, Text: hostile}),
		logged(t, 3, at, core.KindToolResult, core.ToolResult{ID: "c", Output: hostile}),
		logged(t, 4, at, core.KindAttempt, core.AttemptBody{Route: hostile, Error: hostile}),
		logged(t, 5, at, core.KindAgentMsg, core.MsgBody{Text: hostile + "\n\n[docs](https://example.com/" + hostile + ")\n\n```" + hostile + "\n" + hostile + "\n```\n\n![" + hostile + "](a" + hostile + ".png)\n"}),
		logged(t, 6, at, core.KindNotice, core.NoticeBody{Text: hostile}),
		logged(t, 7, at, core.KindCheckpoint, core.CheckpointBody{Text: hostile}),
		logged(t, 8, at, core.KindToolCall, core.ToolCall{ID: "d", Name: "py" + hostile, Code: hostile}),
		logged(t, 9, at, core.KindAgentMsg, core.MsgBody{Text: "see [the docs](https://example.com/a)", Interrupted: true}),
	}},
		delta(core.Delta{Type: core.DeltaTyping, Typing: true}),
		delta(core.Delta{Type: core.DeltaText, Text: hostile}),
		delta(core.Delta{Type: core.DeltaToolStart, Tool: hostile}),
		delta(core.Delta{Type: core.DeltaError, Text: hostile}),
	)
	check("a conversation", true)
	r.send(ctrl('o'))
	check("a conversation unfolded", true)
	if !strings.Contains(r.m.View().Content, "\x1b]8;") {
		t.Errorf("the reply's link is not drawn as one, so the screen was not held to its links:\n%q", r.m.View().Content)
	}
	if got := shell.commands(); len(got) != 1 {
		t.Errorf("the shell segment ran %d times", len(got))
	}

	// What is typed, what is sent and not yet there, and what comes back.
	r.send(paste)
	check("a draft", true)
	r.send(enter())
	check("a message on its way", true)
	r.from(sess, client.Update{Kind: client.UpdateRefused, Text: hostile, Reason: hostile})
	check("a message given back", true)
	r.from(sess, client.Update{Kind: client.UpdateState, State: client.StateDown, Err: errors.New(hostile)})
	check("a lost connection", true)
	r.from(sess, live())
	r.send(ctrl('c'))

	// The commands: the list of them, a mod's answer, and the client's own.
	r.typeText("/sh")
	check("the list of commands", true)
	r.send(enter())
	check("a mod's answer", true)
	r.send(ctrl('c'))
	for _, line := range []string{"/help", "/help shout", "/status", "/open " + hostile, "/rename " + hostile, "/image save 5 " + hostile, "/" + hostile} {
		r.command(line)
		check("after "+strings.SplitN(line, " ", 2)[0], true)
		r.send(ctrl('c'))
	}

	// The two screens over the conversation, and what is typed into them.
	r.command("/statusline")
	check("the status line's editor", true)
	for range 12 {
		r.send(special(tea.KeyDown))
	}
	r.typeText("e")
	r.send(paste)
	check("a field of the editor", true)
	r.send(esc())
	r.send(esc())
	r.send(esc())
	if r.m.screen != screenChat {
		t.Fatalf("the editor is still open")
	}
	r.command("/mods")
	check("the list of mods", true)
	r.send(enter())
	check("a mod's details", true)
	r.typeText("i")
	r.send(paste)
	check("a path typed for a mod", true)
	r.send(enter())
	check("a mod that is not there", true)
}

// The one thing the client learns late is that the terminal has had its time
// to answer: at once from the last answer, or two seconds after the start
// from a terminal that left a question open. Nothing on the screen follows
// from it. So the screen is not cleared, no picture is sent again, and what
// is drawn stays as it is, while the theme is still the one made for what
// the terminal can do.
func TestTheEndOfTheWaitDrawsNothingAgain(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		// last is what ends the wait.
		last tea.Msg
	}{
		{name: "the last answer", last: attributes()},
		{name: "the deadline", last: probeDeadlineMsg{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newImageRig(t, termcaps.Caps{}, Options{Env: asked()})
			r.put("latency.png", chartPNG(t, 640, 360), fixedNow().Add(-2*time.Hour))
			r.talk(chartReply)
			r.send(kittyAnswer(31, "OK"))
			r.send(version("ghostty 1.2.0"))
			r.settle()
			if cols, rows := r.picture(placeholder); cols != 35 || rows != 10 || r.m.caps.Settled {
				t.Fatalf("before the wait ends: %d by %d cells, settled %v:\n%s", cols, rows, r.m.caps.Settled, r.screen())
			}
			was, shown, drawn := r.counts(), r.m.View().Content, r.m.th

			cmd := r.send(tc.last)
			if !r.m.caps.Settled {
				t.Fatal("the wait did not end")
			}
			if clears(cmd) {
				t.Error("the end of the wait cleared the screen")
			}
			r.settle()
			if got := r.counts(); got != was {
				t.Errorf("the end of the wait cost %+v, was %+v", got, was)
			}
			if got := r.m.View().Content; got != shown {
				t.Errorf("the end of the wait changed the screen:\n%s", ansi.Strip(got))
			}
			if r.m.th == drawn || r.m.th.Caps != r.m.caps || r.m.chat.th != r.m.th {
				t.Errorf("the theme is made for %+v, and the terminal can do %+v", r.m.th.Caps, r.m.caps)
			}

			// An answer that does change the look still clears the screen,
			// after the wait as before it.
			if cmd := r.send(background(paper())); !clears(cmd) || r.m.caps.Background != termcaps.BgLight {
				t.Errorf("a light background after the wait: cleared %v, the background is %v", clears(cmd), r.m.caps.Background)
			}
		})
	}
}

// The same on the wire, for the terminals in which the wait ends late: the
// deadline passes two seconds after the start, in the middle of whatever the
// person is doing, and the program writes nothing for it. Apple's terminal is
// asked one question and never says it has finished; a tmux that hands
// nothing on to the terminal behind it answers once where twice is waited
// for; and some terminals do not answer at all.
func TestTheDeadlineWritesNothing(t *testing.T) {
	t.Parallel()
	inTmux := asked()
	inTmux.Tmux = "/tmp/tmux-501/default,1,0"
	for _, tc := range []struct {
		name string
		env  termcaps.Env
		// answers are what the terminal says before the deadline.
		answers string
	}{
		{name: "Apple Terminal", env: termcaps.Env{Term: "xterm-256color", TermProgram: "Apple_Terminal", Detected: termcaps.Color256}},
		{name: "tmux that passes nothing on", env: inTmux, answers: "\x1bP>|tmux 3.5a\x1b\\" + "\x1b[?1;2c"},
		{name: "a terminal that answers nothing", env: asked()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ada := client.Contact{ID: 1, Name: "ada", Provider: "deepseek", Model: "deepseek-flash"}
			r := startProgram(t, Options{Env: tc.env, Open: &ada})
			untilShown(t, r.out, 0, "Message ada")
			// The answers are read before the key that follows them, so once
			// the key is drawn they have been taken.
			mark := r.out.Mark()
			r.write(tc.answers + "Q")
			until(t, r.out, mark, "Q")
			r.out.Quiet(t)

			// The deadline is taken before Send returns, and the key after it
			// shows that the program has drawn since.
			mark = r.out.Mark()
			r.p.Send(probeDeadlineMsg{})
			r.write("!")
			until(t, r.out, mark, "!")
			r.out.Quiet(t)
			wrote := r.out.Since(mark)
			if len(wrote) > 40 || bytes.Contains(wrote, []byte("\x1b[2J")) {
				t.Errorf("the deadline and one key wrote %d bytes, want the key alone: %q", len(wrote), wrote)
			}
			r.p.Quit()
			if err := <-r.done; err != nil {
				t.Fatalf("the program ended with %v", err)
			}
			if !r.m.caps.Settled || r.m.chat.input.Value() != "Q!" {
				t.Errorf("settled %v, and the box holds %q", r.m.caps.Settled, r.m.chat.input.Value())
			}
		})
	}
}
