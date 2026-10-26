package tui

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/quinnovator/contactd/internal/client"
	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/tui/conf"
	"github.com/quinnovator/contactd/internal/tui/statusline"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
)

// fakeShell stands where /bin/sh would: it runs nothing, remembers the
// commands it was asked to run, and answers each with what the test gave it.
type fakeShell struct {
	mu     sync.Mutex
	ran    []string
	limits []time.Duration
	out    map[string]string
	fail   map[string]error
}

func (s *fakeShell) Run(_ context.Context, command string, timeout time.Duration) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ran = append(s.ran, command)
	s.limits = append(s.limits, timeout)
	return s.out[command], s.fail[command]
}

// commands is every command run so far, in order.
func (s *fakeShell) commands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.ran)
}

// answer sets what a command prints from now on, and whether it fails.
func (s *fakeShell) answer(command, out string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.out[command], s.fail[command] = out, err
}

// timer is a wait the model asked for, and the message it wanted after it.
type timer struct {
	wait time.Duration
	msg  tea.Msg
}

// barRig is a harness with a config directory, a shell that runs nothing,
// and timers that are written down and never go off by themselves.
type barRig struct {
	*harness

	dir    string
	shell  *fakeShell
	timers []timer
}

// barDir makes a config directory. With a file, it holds that as
// statusline.toml, with the mode given.
func barDir(t *testing.T, file string, mode os.FileMode) string {
	t.Helper()
	dir := t.TempDir()
	if file != "" {
		writeBar(t, dir, file, mode)
	}
	return dir
}

// writeBar writes statusline.toml in a config directory.
func writeBar(t *testing.T, dir, file string, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(dir, "statusline.toml")
	if err := os.WriteFile(path, []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	// The mode asked for, whatever the umask made of it.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// newBarRig makes the interface over a config directory, 100 columns by 30
// rows. The home directory is the one above it, so that the editor's header
// reads ~/<directory>/statusline.toml.
func newBarRig(t *testing.T, caps termcaps.Caps, dir string, opt Options) *barRig {
	t.Helper()
	r := &barRig{dir: dir, shell: &fakeShell{out: map[string]string{}, fail: map[string]error{}}}
	opt.Env, opt.Overrides = terminal(t, caps)
	opt.Dirs, opt.Home, opt.Shell = conf.Dirs{Config: dir}, filepath.Dir(dir), r.shell
	r.harness = newHarnessWith(t, opt, client.Contact{ID: 1, Name: "ada", Provider: "fake", Model: "fake-1"})
	r.m.after = func(wait time.Duration, msg tea.Msg) tea.Cmd {
		r.timers = append(r.timers, timer{wait: wait, msg: msg})
		return nil
	}
	r.size(100, 30)
	return r
}

// talk opens a conversation with ada the way the program does, connected and
// with a little history. It returns the session, and what the commands that
// were started with the conversation gave: the runs of the shell segments.
func (r *barRig) talk() (*fakeSession, []tea.Msg) {
	r.t.Helper()
	cmd := r.m.open(client.Contact{ID: 1, Name: "ada", Provider: "fake", Model: "fake-1"})
	sess := r.b.sessions[len(r.b.sessions)-1]
	// The command that listens to the session ends when the session speaks.
	go func() { sess.updates <- live() }()
	var started []tea.Msg
	for _, msg := range produced(cmd) {
		if _, heard := msg.(updatesMsg); heard {
			r.send(msg) // the answer is to listen again, which is not run here
			continue
		}
		started = append(started, msg)
	}
	r.from(sess, history(r.t))
	return sess, started
}

// deliver sends messages, and returns what the commands they were answered
// with gave. Timers are not among those: they are written down.
func (r *barRig) deliver(msgs ...tea.Msg) []tea.Msg {
	r.t.Helper()
	out := make([]tea.Msg, 0, len(msgs))
	for _, msg := range msgs {
		out = append(out, produced(r.send(msg))...)
	}
	return out
}

// bar is the status bar's row of the conversation.
func (h *harness) bar() string {
	h.t.Helper()
	rows, plan := h.rows(), h.m.chat.rows
	return rows[plan.Header+plan.HeaderRule+plan.Body+plan.Palette+plan.BarRule]
}

// spread is a row of the screen with text at both ends.
func spread(width int, left, right string) string {
	return left + strings.Repeat(" ", width-ansi.StringWidth(left)-ansi.StringWidth(right)) + right
}

// wantBar fails the test unless the bar reads left at its left end and right
// at its right end.
func (h *harness) wantBar(what, left, right string) {
	h.t.Helper()
	if got, want := h.bar(), spread(h.m.width, left, right); got != want {
		h.t.Errorf("%s: the bar is\n%q, want\n%q", what, got, want)
	}
}

// defaultBar is the bar of the default preset in the conversation talk
// opens, at noon.
const (
	defaultLeft  = " ● live │ fake-1 │ cache 75% │ 2.0k tok │ 1.2s"
	defaultRight = "12:00 "
)

// With no file the status line is the default preset, the same bar as with
// no config directory at all, and nothing is said or run.
func TestWithNoFileTheBarIsTheDefault(t *testing.T) {
	t.Parallel()
	r := newBarRig(t, trueDark(), barDir(t, "", 0), Options{})
	_, started := r.talk()
	r.wantBar("no file", defaultLeft, defaultRight)
	if r.m.note != "" || r.note() != "" || len(started) != 0 || len(r.shell.commands()) != 0 || len(r.timers) != 0 {
		t.Errorf("with no file: the notes %q and %q, %d commands started, %q run, %d timers",
			r.m.note, r.note(), len(started), r.shell.commands(), len(r.timers))
	}
	preset, _ := statusline.Preset(statusline.DefaultPreset)
	if !reflect.DeepEqual(r.m.bar, preset) {
		t.Errorf("the status line is\n%+v, want the default preset", r.m.bar)
	}

	// The same conversation with no config directory.
	h, _ := talking(t)
	h.size(100, 30)
	if h.bar() != r.bar() {
		t.Errorf("the bar with no config directory is\n%q, and with an empty one\n%q", h.bar(), r.bar())
	}

	// The model's own choice of shell is the real one.
	if _, fake := h.m.sh.run.(*fakeShell); fake || h.m.sh.run == nil {
		t.Errorf("with no Shell in the options the shell is %T", h.m.sh.run)
	}
}

// The file decides which segments there are, where they stand and what they
// read. What it has that the client does not know is said once.
func TestAFileChangesTheBar(t *testing.T) {
	t.Parallel()
	file := `
[[segment]]
name = "clock"
side = "left"
format = "at $time"

[[segment]]
name = "state"
colour = "green"

[[segment]]
name = "contact"
side = "right"

[[segment]]
name = "cache"
side = "right"
format = "$cached of $input"

[[segment]]
name = "tokens"
on = false
`
	dir := barDir(t, file, 0o600)
	r := newBarRig(t, trueDark(), dir, Options{})
	note := `statusline.toml: line 9: unknown key "colour"`
	if r.m.note != note {
		t.Fatalf("at the start the note is %q, want %q", r.m.note, note)
	}
	r.talk()
	r.wantBar("the file's bar", " at 12:00 │ ● live", "ada │ 1.5k of 2.0k ")
	if len(r.shell.commands()) != 0 || len(r.timers) != 0 {
		t.Errorf("a bar with no shell segment ran %q and set %d timers", r.shell.commands(), len(r.timers))
	}

	// Started in a conversation, the conversation says what is wrong with the
	// file, until the next key. It is not said again.
	r = newBarRig(t, trueDark(), dir, Options{Open: &client.Contact{ID: 1, Name: "ada"}})
	r.m.Init()
	r.wantOnScreen(note)
	r.typeText("x")
	r.wantNotOnScreen("statusline.toml")
	r.send(clockMsg{})
	r.send(r.key(ctrl('p'))())
	r.wantOnScreen("Contacts")
	r.wantNotOnScreen("statusline.toml")
}

// A file that cannot be used leaves the default status line, and the note
// names the line it broke at.
func TestABrokenFileLeavesTheDefault(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		file string
		note string
	}{
		"not TOML": {
			file: "[[segment]]\nname = \"clock\"\n[[segment\n",
			note: "statusline.toml: line 3: ",
		},
		"a value the key cannot take": {
			file: "[[segment]]\nname = \"clock\"\nside = \"middle\"\n",
			note: "statusline.toml: line 3: ",
		},
		"a timeout longer than the interval": {
			file: "[[segment]]\nname = \"up\"\nfrom = \"shell\"\ncommand = \"uptime\"\ninterval = \"2s\"\ntimeout = \"5s\"\n",
			note: "statusline.toml: line ",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newBarRig(t, trueDark(), barDir(t, tc.file, 0o600), Options{})
			if !strings.HasPrefix(r.m.note, tc.note) || !strings.HasSuffix(r.m.note, "; the default status line is used") {
				t.Errorf("the note is %q, want %q, what is wrong, and that the default is used", r.m.note, tc.note)
			}
			if strings.Contains(r.m.note, r.dir) {
				t.Errorf("the note has the whole path: %q", r.m.note)
			}
			// The list shows it.
			r.size(200, 24)
			r.send(r.m.showContacts()())
			r.wantOnScreen("Contacts", tc.note, "the default status line is used")
			_, started := r.talk()
			r.size(100, 30)
			r.wantBar(name, defaultLeft, defaultRight)
			if len(started) != 0 || len(r.shell.commands()) != 0 {
				t.Errorf("a broken file started %d commands and ran %q", len(started), r.shell.commands())
			}
		})
	}

	// A file that cannot be read at all: here, a directory of that name.
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "statusline.toml"), 0o700); err != nil {
		t.Fatal(err)
	}
	r := newBarRig(t, trueDark(), dir, Options{})
	if !strings.HasPrefix(r.m.note, "status line: ") || !strings.HasSuffix(r.m.note, "; the default status line is used") {
		t.Errorf("a file that cannot be read left the note %q", r.m.note)
	}
	r.talk()
	r.wantBar("a file that cannot be read", defaultLeft, defaultRight)
}

// /statusline preset <name> puts a preset in the bar for this session. It
// writes nothing, and keeps the segments a preset does not list.
func TestStatusLinePreset(t *testing.T) {
	t.Parallel()
	r := newBarRig(t, trueDark(), barDir(t, "", 0), Options{})
	sess, _ := r.talk()

	if cmd := r.command("/statusline preset minimal"); cmd != nil {
		t.Errorf("a preset with no shell segment gives %v", produced(cmd))
	}
	r.wantBar("minimal", " ● live │ cache 75%", "12:00 ")
	done := "status line: preset minimal for this session; /statusline edit, then s, keeps it"
	if r.note() != done || r.m.chat.noteTone != toneDone || r.draft() != "" {
		t.Errorf("after the preset the note is %q in the tone %q, and the draft %q", r.note(), r.m.chat.noteTone, r.draft())
	}
	r.wantOnScreen(done)
	if _, err := os.Stat(r.m.opt.Dirs.StatusLineFile()); !os.IsNotExist(err) {
		t.Errorf("a preset for the session wrote the file: %v", err)
	}

	// The name in any case; the widest preset names the contact.
	r.command("/statusline PRESET Ops")
	r.wantBar("ops", " ● live │ ada │ fake-1 │ cache 75% │ 2.0k tok │ 1.2s", "12:00 ")
	r.command("/statusline preset default")
	r.wantBar("default", defaultLeft, defaultRight)

	// What is not a preset, and what is not an argument, changes nothing and
	// stays in the box to be put right.
	bar := r.m.bar
	for line, note := range map[string]string{
		"/statusline preset":      "usage: /statusline preset <name> | edit",
		"/statusline preset a b":  "usage: /statusline preset <name> | edit",
		"/statusline minimal":     "usage: /statusline preset <name> | edit",
		"/statusline edit now":    "usage: /statusline preset <name> | edit",
		"/statusline preset tiny": `no preset "tiny"; there are minimal, default, ops`,
		// What was typed is repeated without anything a terminal acts on.
		"/statusline preset \"a\tb\"":  `no preset "a    b"; there are minimal, default, ops`,
		"/statusline preset \x1b[31mx": `no preset "[31mx"; there are minimal, default, ops`,
	} {
		if strings.ContainsAny(line, "\t\x1b") {
			r.send(tea.PasteMsg{Content: line})
			r.key(enter())
		} else {
			r.command(line)
		}
		if r.note() != note || r.m.screen != screenChat || !reflect.DeepEqual(r.m.bar, bar) {
			t.Errorf("%q: the note is %q, want %q; the screen is %v", line, r.note(), note, r.m.screen)
		}
		if r.draft() == "" {
			t.Errorf("%q did nothing and was taken out of the box", line)
		}
		r.key(ctrl('c'))
	}
	if len(sess.said) != 0 || sess.typings != 0 {
		t.Errorf("the contact was sent %q and %d typing signals", sess.said, sess.typings)
	}
}

// running is a reply being written in which a tool runs: what the editor's
// live values follow.
func running() []client.Update {
	return []client.Update{
		delta(core.Delta{Type: core.DeltaTyping, Typing: true}),
		delta(core.Delta{Type: core.DeltaToolCall, Tool: "python", Text: "import time", Seq: 6}),
	}
}

// /statusline opens the editor over the conversation. A segment switched on
// there and saved is in the file, which reads back as the status line in
// use, and in the bar. Esc goes back to the conversation.
func TestTheStatusLineEditor(t *testing.T) {
	t.Parallel()
	for _, line := range []string{"/statusline", "/statusline edit", "/statusline EDIT"} {
		r := newBarRig(t, trueDark(), barDir(t, "", 0), Options{})
		sess, _ := r.talk()
		path := r.m.opt.Dirs.StatusLineFile()

		if cmd := r.command(line); cmd != nil || r.m.screen != screenStatusLine {
			t.Fatalf("%s gives %v and the screen %v", line, cmd, r.m.screen)
		}
		rows := r.rows()
		if len(rows) != 30 {
			t.Fatalf("%s: the editor has %d rows:\n%s", line, len(rows), r.screen())
		}
		// The header names the file, with the home directory as ~.
		if want := spread(100, "Status line", "~/"+filepath.Base(r.dir)+"/statusline.toml"); rows[0] != want {
			t.Errorf("the editor's first row is\n%q, want\n%q", rows[0], want)
		}
		// The table shows what the conversation's bar shows right now.
		r.wantOnScreen(
			" [x] state      ● live ",
			" [ ] contact    ada ",
			" [x] route      fake-1 ",
			" [x] cache      cache 75% ",
			" [x] tokens     2.0k tok ",
			" [x] latency    1.2s ",
			" [x] clock      12:00 ",
			"100 columns  ● live │ fake-1 │ cache 75% │ 2.0k tok │ 1.2s",
			"presets  minimal  default  ops",
			"s save · esc back",
		)
		if v := r.m.View(); v.Cursor != nil || !v.AltScreen || v.WindowTitle != "contactd: status line" {
			t.Errorf("the editor's view has the cursor %+v, the title %q, alt screen %v", v.Cursor, v.WindowTitle, v.AltScreen)
		}
		if r.draft() != "" || len(sess.said) != 0 {
			t.Errorf("the command stayed in the box as %q, or was sent: %q", r.draft(), sess.said)
		}

		// Down to contact, space switches it on, s saves.
		r.key(special(tea.KeyDown))
		r.key(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
		r.wantOnScreen(" [x] contact    ada ", "Status line  not saved")
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("the file was written before s: %v", err)
		}
		// The previews are the editor's own; the bar in use has not changed.
		r.wantOnScreen("100 columns  ● live │ ada │ fake-1 │")
		if slices.ContainsFunc(r.m.bar.Segments, func(seg statusline.Segment) bool { return seg.Name == "contact" && seg.On }) {
			t.Fatal("a change in the editor reached the bar before it was saved")
		}
		if cmd := r.key(tea.KeyPressMsg{Code: 's', Text: "s"}); cmd != nil {
			t.Errorf("saving a bar with no shell segment gives %v", produced(cmd))
		}
		r.wantOnScreen("saved")
		r.wantNotOnScreen("not saved")
		if r.m.screen != screenStatusLine {
			t.Fatal("s closed the editor")
		}

		// The file is the person's own, and reads back as what is in use.
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("the file's mode is %v, want 0600", info.Mode().Perm())
		}
		cfg, warnings, err := statusline.Load(path)
		if err != nil || len(warnings) != 0 {
			t.Fatalf("the saved file reads back with %v and %q", err, warnings)
		}
		if !reflect.DeepEqual(cfg, r.m.bar) {
			t.Errorf("the file reads back as\n%+v, and the status line in use is\n%+v", cfg, r.m.bar)
		}
		on := func(c statusline.Config) []string {
			var out []string
			for _, seg := range c.Segments {
				if seg.On {
					out = append(out, seg.Name)
				}
			}
			return out
		}
		want := []string{"state", "contact", "route", "cache", "tokens", "latency", "activity", "stop", "scroll", "clock", "helpers"}
		if !slices.Equal(on(cfg), want) {
			t.Errorf("the file has %q switched on, want %q", on(cfg), want)
		}

		// Esc goes back, and the bar has the new segment.
		r.key(esc())
		if r.m.screen != screenChat || r.m.edit != nil || r.note() != "" {
			t.Fatalf("after esc the screen is %v, the editor %v, the note %q", r.m.screen, r.m.edit, r.note())
		}
		r.wantBar("after the save", " ● live │ ada │ fake-1 │ cache 75% │ 2.0k tok │ 1.2s", defaultRight)
		r.wantOnScreen("❯ Message ada", "It is 42.")

		// A client started afterwards reads the same bar from the file.
		again := newBarRig(t, trueDark(), r.dir, Options{})
		again.talk()
		if again.bar() != r.bar() || again.m.note != "" {
			t.Errorf("a client started on the saved file has the bar\n%q, want\n%q; its note is %q", again.bar(), r.bar(), again.m.note)
		}
	}
}

// Leaving the editor without saving changes nothing, and the conversation
// says so.
func TestLeavingTheEditorWithoutSaving(t *testing.T) {
	t.Parallel()
	r := newBarRig(t, trueDark(), barDir(t, "", 0), Options{})
	r.talk()
	r.command("/statusline")
	r.key(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}) // state off
	r.key(esc())
	if r.m.screen != screenStatusLine {
		t.Fatal("the first esc left the editor with changes not saved")
	}
	r.wantOnScreen("not saved: s saves, esc again leaves without saving")
	r.key(esc())
	note := "the status line changes were not saved"
	if r.m.screen != screenChat || r.note() != note {
		t.Fatalf("after the second esc the screen is %v and the note %q", r.m.screen, r.note())
	}
	r.wantOnScreen(note)
	r.wantBar("not saved", defaultLeft, defaultRight)
	if _, err := os.Stat(r.m.opt.Dirs.StatusLineFile()); !os.IsNotExist(err) {
		t.Errorf("leaving without saving wrote the file: %v", err)
	}
	// The next key clears the note, as any.
	r.typeText("x")
	r.wantNotOnScreen(note)

	// Opened again, the editor starts from the bar in use, not from where the
	// last one was left.
	r.key(ctrl('c'))
	r.command("/statusline")
	r.wantOnScreen(" [x] state ")
	r.wantNotOnScreen("not saved")
}

// A status line that cannot be written is not used. The editor says why and
// stays open, with its changes still to be saved.
func TestAStatusLineThatCannotBeSaved(t *testing.T) {
	t.Parallel()
	// The config directory is a file: nothing can be written in it.
	dir := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(dir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	r := newBarRig(t, trueDark(), dir, Options{})
	r.size(140, 30)
	r.talk()
	r.wantBar("before", defaultLeft, defaultRight)
	r.command("/statusline")
	r.key(special(tea.KeyDown))
	r.key(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	if cmd := r.key(tea.KeyPressMsg{Code: 's', Text: "s"}); cmd != nil {
		t.Errorf("a save that failed gives %v", produced(cmd))
	}
	r.wantOnScreen("not saved: write statusline.toml: ", "Status line  not saved")
	if r.m.screen != screenStatusLine {
		t.Fatal("a failed save closed the editor")
	}
	// The changes count as not saved again: esc asks first.
	r.key(esc())
	r.wantOnScreen("not saved: s saves, esc again leaves without saving")
	r.key(esc())
	r.wantBar("after a save that failed", defaultLeft, defaultRight)
	if r.note() != "the status line changes were not saved" {
		t.Errorf("the note is %q", r.note())
	}

	// With no config directory there is nowhere to write.
	h, _ := talking(t)
	h.command("/statusline")
	h.key(tea.KeyPressMsg{Code: 's', Text: "s"})
	h.wantOnScreen("Status line", "not saved: write file: there is no path to write to")
}

// The conversation goes on under the editor, and the values the editor
// shows are the conversation's: a tool that starts, the minute that passes.
func TestTheEditorFollowsTheConversation(t *testing.T) {
	t.Parallel()
	r := newBarRig(t, trueDark(), barDir(t, "", 0), Options{})
	sess, _ := r.talk()
	r.command("/statusline")
	r.wantOnScreen(" [x] clock      12:00 ")
	before := r.m.View()

	// A message that changes nothing the editor shows leaves its view as it
	// was: the very same one.
	r.send(tea.FocusMsg{})
	r.from(sess)
	if r.m.stale {
		t.Error("a message that changes nothing has the editor drawn again")
	}
	if after := r.m.View(); after.Content != before.Content {
		t.Error("a message that changes nothing changed the editor")
	}

	r.from(sess, running()...)
	r.wantOnScreen(
		" [x] activity   running python ",
		" [x] stop       esc stop ",
		"running python │ esc stop │ 12:00",
	)
	r.now = r.now.Add(time.Minute)
	r.send(clockMsg{})
	r.wantOnScreen(" [x] clock      12:01 ", "running python │ esc stop │ 12:01")
	r.wantNotOnScreen("12:00")

	// The connection is lost: the state says so, and the turn has ended.
	r.from(sess, dropped())
	r.wantOnScreen(" [x] state      ● offline, retrying ")
	r.wantNotOnScreen("running python │ esc stop")

	// A reply that settles and an answer to a rename both land in the
	// conversation, and the editor stays.
	r.from(sess, back(logged(t, 6, r.now, core.KindAgentMsg, core.MsgBody{Text: "Done while you were editing."}))...)
	r.send(openedMsg{contact: client.Contact{ID: 1, Name: "lovelace", Provider: "fake", Model: "fake-1"}, back: true, sess: sess})
	if r.m.screen != screenStatusLine {
		t.Fatalf("an answer for the conversation closed the editor: the screen is %v", r.m.screen)
	}
	r.wantOnScreen("Status line", " [x] state      ● live ")
	// A new look reaches the editor too.
	if cmd := r.m.setCaps(noneASCII()); !clears(cmd) {
		t.Error("a new look did not clear the screen")
	}
	r.wantOnScreen(" [x] state      * live ", "s save")
	for _, b := range []byte(r.m.View().Content) {
		if b >= 0x80 {
			t.Fatalf("the editor on an ASCII terminal holds the byte %#x:\n%s", b, r.screen())
		}
	}
	r.m.setCaps(trueDark())

	r.key(esc())
	r.wantOnScreen("lovelace  fake/fake-1", "Done while you were editing.", "renamed to lovelace")
	r.wantBar("back in the conversation", " ● live │ fake-1 │ cache 75% │ 2.0k tok │ 1.2s", "12:01 ")
}

// The keys of the editor are the editor's: text goes into the field that is
// open, and esc there closes the field, not the screen. Two keys are the
// client's on every screen.
func TestTheEditorsKeys(t *testing.T) {
	t.Parallel()
	r := newBarRig(t, trueDark(), barDir(t, "", 0), Options{})
	sess, _ := r.talk()
	r.command("/statusline")

	if cmd := r.key(ctrl('l')); !clears(cmd) {
		t.Error("ctrl+l did not clear the screen")
	}
	// e opens the first field of the selected segment, with the cursor in it.
	r.key(tea.KeyPressMsg{Code: 'e', Text: "e"})
	v := r.m.View()
	if v.Cursor == nil || v.Cursor.Y >= 30 || v.Cursor.X >= 100 || v.Cursor.Blink {
		t.Fatalf("with a field open the cursor is %+v", v.Cursor)
	}
	// Letters that are keys of the table, a paste, and text that reads like a
	// key are all text here.
	r.typeText(" sq")
	r.send(tea.PasteMsg{Content: " pasted"})
	r.key(tea.KeyPressMsg{Text: "esc"})
	r.wantOnScreen(`$dot $state sq pastedesc`)
	if r.m.screen != screenStatusLine || len(sess.said) != 0 || sess.typings != 0 || r.draft() != "" {
		t.Fatalf("typing in a field: the screen %v, said %q, %d typing signals, the draft %q", r.m.screen, sess.said, sess.typings, r.draft())
	}
	// Esc closes the field and leaves the screen open; the next one leaves.
	r.key(esc())
	if r.m.screen != screenStatusLine || r.m.View().Cursor != nil {
		t.Fatal("esc in a field left the editor, or left the cursor")
	}
	r.key(esc())
	if r.m.screen != screenChat {
		t.Fatal("esc in the table did not leave the editor")
	}

	// ctrl+c leaves the client from the editor, as from the list. Text that
	// only reads like it does not.
	r.command("/statusline")
	if cmd := r.key(tea.KeyPressMsg{Text: "ctrl+c"}); cmd != nil {
		t.Errorf("the text ctrl+c gives %v", produced(cmd))
	}
	cmd := r.key(ctrl('c'))
	if cmd == nil {
		t.Fatal("ctrl+c in the editor gives nothing")
	}
	if _, quit := cmd().(tea.QuitMsg); !quit {
		t.Errorf("ctrl+c in the editor gives %v", cmd())
	}
}

// The editor is never wider or taller than the terminal, whatever is open in
// it, and with ASCII glyphs it holds ASCII only.
func TestTheEditorFitsTheTerminal(t *testing.T) {
	t.Parallel()
	looks := map[string]termcaps.Caps{
		"truecolor": trueDark(), "16 colours": light16(), "256 colours": unknown256(), "no colour, ASCII": noneASCII(),
	}
	file := `
[[segment]]
name = "state"

[[segment]]
name = "disk"
from = "shell"
command = "df -h / | tail -1"
match = '([0-9]+)%'
format = "disk $1%"

[[segment]]
name = "clock"
`
	char := func(c rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: c, Text: string(c)} }
	states := []struct {
		name string
		keys []tea.Msg
	}{
		{"the table", nil},
		{"the last segment", slices.Repeat([]tea.Msg{special(tea.KeyDown)}, 12)},
		{"a field open", []tea.Msg{char('e'), tea.PasteMsg{Content: "a format that goes on and on and on, well past the end of any row there is"}}},
		{"a new shell segment", []tea.Msg{char('a'), char('u'), char('p')}},
		{"asked about leaving", []tea.Msg{char(' '), esc()}},
		{"saved", []tea.Msg{char('p'), char('s')}},
	}
	for name, caps := range looks {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			for _, size := range [][2]int{{100, 30}, {60, 16}, {120, 40}, {80, 24}, {79, 30}, {40, 16}, {30, 12}, {20, 8}, {10, 5}, {100, 5}, {1, 1}} {
				width, height := size[0], size[1]
				for _, state := range states {
					what := name + ", " + state.name
					r := newBarRig(t, caps, barDir(t, file, 0o600), Options{})
					_, started := r.talk()
					r.shell.answer("df -h / | tail -1", "/dev/disk1 460Gi 327Gi 71% /\n", nil)
					r.deliver(started...)
					r.from(r.b.sessions[0], running()...)
					r.command("/statusline")
					r.size(width, height)
					for _, key := range state.keys {
						r.send(key)
					}
					if r.m.screen != screenStatusLine {
						t.Fatalf("%s: the editor is not open", what)
					}
					r.wantFits(what, width, height)
					if rows := r.rows(); len(rows) != height {
						t.Errorf("%s at %dx%d: %d rows", what, width, height, len(rows))
					}
					v := r.m.View()
					if v.Cursor != nil && (v.Cursor.Y < 0 || v.Cursor.Y >= height || v.Cursor.X < 0 || v.Cursor.X >= width) {
						t.Errorf("%s at %dx%d: the cursor is at %+v", what, width, height, v.Cursor)
					}
					if v.MouseMode != tea.MouseModeNone {
						t.Errorf("%s: the editor asks for the mouse", what)
					}
					if caps.Glyphs != termcaps.GlyphsASCII {
						continue
					}
					for _, b := range []byte(v.Content) {
						if b >= 0x80 {
							t.Fatalf("%s at %dx%d holds the byte %#x:\n%s", what, width, height, b, r.screen())
						}
					}
				}
			}
		})
	}

	// At the two sizes asked for by name, the screen says what it is.
	for _, size := range [][2]int{{100, 30}, {60, 16}} {
		r := newBarRig(t, trueDark(), barDir(t, file, 0o600), Options{})
		r.talk()
		r.command("/statusline")
		r.size(size[0], size[1])
		r.wantOnScreen("Status line", "[x] state", "esc back")
	}
}

// /reload reads the status line's file again. A file that broke since
// leaves the bar as it was and says where it broke.
func TestReloadReadsTheStatusLine(t *testing.T) {
	t.Parallel()
	dir := barDir(t, "", 0)
	r := newBarRig(t, trueDark(), dir, Options{})
	r.size(160, 30)
	r.talk()
	r.wantBar("no file", defaultLeft, defaultRight)

	// No file: the default, and nothing is wrong.
	r.then(r.command("/reload"))
	r.wantOnScreen("read chat.toml again; glyph overrides: 0; read statusline.toml again; segments on: 10")
	if r.m.chat.noteTone != toneDone {
		t.Errorf("the note is in the tone %q", r.m.chat.noteTone)
	}

	writeBar(t, dir, "[[segment]]\nname = \"state\"\n\n[[segment]]\nname = \"clock\"\nnosuch = 1\n", 0o600)
	r.then(r.command("/reload"))
	r.wantBar("a new file", " ● live", "12:00 ")
	if want := `statusline.toml: line 6: unknown key "nosuch"`; r.note() != want || r.m.chat.noteTone != "" {
		t.Errorf("the note is %q in the tone %q, want %q", r.note(), r.m.chat.noteTone, want)
	}

	// A file that broke changes nothing.
	writeBar(t, dir, "[[segment]]\nname = \"state\"\nside = 3\n", 0o600)
	if cmd := r.send(r.command("/reload")()); cmd != nil {
		t.Errorf("a broken file gives %v", produced(cmd))
	}
	r.wantBar("a broken file", " ● live", "12:00 ")
	if !strings.HasPrefix(r.note(), "statusline.toml: line 3: ") || !strings.HasSuffix(r.note(), "; the status line stays as it was") {
		t.Errorf("the note is %q", r.note())
	}

	// What is wrong with either file is said in one note, and a chat.toml
	// that cannot be read does not keep the status line from being read.
	writeBar(t, dir, "[[segment]]\nname = \"clock\"\nside = \"left\"\n", 0o600)
	if err := os.WriteFile(filepath.Join(dir, "chat.toml"), []byte("[glyph\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.then(r.command("/reload"))
	r.wantBar("beside a broken chat.toml", " 12:00", "")
	if !strings.Contains(r.note(), "chat.toml: line 1: ") || strings.Contains(r.note(), "statusline.toml") {
		t.Errorf("the note is %q", r.note())
	}
	if err := os.WriteFile(filepath.Join(dir, "chat.toml"), []byte("x = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeBar(t, dir, "[[segment]]\nname = \"clock\"\ny = 2\n", 0o600)
	r.then(r.command("/reload"))
	if want := `chat.toml: line 1: unknown key "x"; statusline.toml: line 3: unknown key "y"`; r.note() != want {
		t.Errorf("the note is %q, want %q", r.note(), want)
	}

	// The file gone again: the default again.
	if err := os.Remove(filepath.Join(dir, "statusline.toml")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "chat.toml")); err != nil {
		t.Fatal(err)
	}
	r.then(r.command("/reload"))
	r.wantBar("the file gone", defaultLeft, defaultRight)

	// An answer that arrives under the editor changes the bar in use, and
	// the conversation keeps the note for when the editor is left.
	writeBar(t, dir, "[[segment]]\nname = \"clock\"\ny = 2\n", 0o600)
	reloaded := r.command("/reload")()
	r.command("/statusline")
	r.send(reloaded)
	if r.m.screen != screenStatusLine {
		t.Fatal("the answer to /reload closed the editor")
	}
	r.wantOnScreen(" [x] state ") // the editor keeps the bar it was opened on
	r.key(esc())
	r.wantBar("after the editor", "", "12:00 ")
	r.wantOnScreen(`statusline.toml: line 3: unknown key "y"`)
}
