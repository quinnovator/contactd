//go:build unix

package tui

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/quinnovator/contactd/internal/client"
	"github.com/quinnovator/contactd/internal/tui/conf"
	"github.com/quinnovator/contactd/internal/tui/statusline"
	"github.com/quinnovator/contactd/internal/tui/tuitest"
)

// Shell segments run commands, and whether a file's commands may be run
// depends on who may write to the file: both are Unix matters, and off Unix
// no shell segment runs at all.

// loadBar is a status line with one shell segment, load, that runs uptime
// every ten seconds.
const loadBar = `
[[segment]]
name = "state"

[[segment]]
name = "load"
from = "shell"
command = "uptime"
match = 'load averages?: ([0-9.]+)'
format = "load $1"
interval = "10s"
timeout = "2s"

[[segment]]
name = "clock"
`

// uptime is what the command prints.
func uptime(load string) string {
	return "12:00  up 3 days,  2:14, 2 users, load averages: " + load + " 0.40 0.38\n"
}

// tick is the message of the timer the model set last.
func (r *barRig) tick() tea.Msg {
	r.t.Helper()
	if len(r.timers) == 0 {
		r.t.Fatal("no timer was set")
	}
	return r.timers[len(r.timers)-1].msg
}

// A shell segment's command is run when a conversation opens. What it
// printed is drawn when the run is done, and the next run starts an interval
// after that: never sooner, and never while one is still going.
func TestAShellSegmentRunsOnItsInterval(t *testing.T) {
	t.Parallel()
	r := newBarRig(t, trueDark(), barDir(t, loadBar, 0o600), Options{})
	r.shell.answer("uptime", uptime("0.42"), nil)
	if r.m.note != "" {
		t.Fatalf("a file of the person's own left the note %q", r.m.note)
	}
	// On the list there is no bar, and nothing runs.
	r.send(r.m.showContacts()())
	if got := r.shell.commands(); len(got) != 0 || len(r.timers) != 0 {
		t.Fatalf("on the list %q was run and %d timers were set", got, len(r.timers))
	}

	_, started := r.talk()
	if got := r.shell.commands(); !slices.Equal(got, []string{"uptime"}) || !slices.Equal(r.shell.limits, []time.Duration{2 * time.Second}) {
		t.Fatalf("opening the conversation ran %q with %v to finish, want uptime once with 2s", got, r.shell.limits)
	}
	// Until the run is done the segment has nothing to show, and no timer
	// is set: the interval starts when the run ends.
	r.wantBar("while the first run goes", " ● live", "12:00 ")
	if len(started) != 1 || len(r.timers) != 0 {
		t.Fatalf("the conversation started %d commands and %d timers", len(started), len(r.timers))
	}

	// The run takes three seconds. Its value is drawn, and the timer is set
	// for ten seconds after its end. Nothing of the transcript is drawn or
	// listed again for it.
	r.now = r.now.Add(3 * time.Second)
	drawn, built := r.m.chat.win.Stats().Rendered, r.m.chat.rebuilds
	if again := r.deliver(started...); len(again) != 0 {
		t.Fatalf("a finished run started %d commands", len(again))
	}
	r.wantBar("after the first run", " ● live", "load 0.42 │ 12:00 ")
	if c := r.m.chat; c.win.Stats().Rendered != drawn || c.rebuilds != built {
		t.Errorf("a shell segment's value drew %d blocks and built the list %d times", c.win.Stats().Rendered-drawn, c.rebuilds-built)
	}
	if len(r.timers) != 1 || r.timers[0].wait != 10*time.Second {
		t.Fatalf("after the run the timers are %+v, want one of 10s", r.timers)
	}

	// A timer that goes off early starts nothing: the interval has not
	// passed since the run ended.
	first := r.tick()
	r.now = r.now.Add(9 * time.Second)
	if again := r.deliver(first); len(again) != 0 || len(r.shell.commands()) != 1 {
		t.Fatalf("a tick after 9s ran %q", r.shell.commands())
	}
	// The wait that is left gets a timer of its own.
	if len(r.timers) != 2 || r.timers[1].wait != time.Second {
		t.Fatalf("after an early tick the timers are %+v, want a second one of 1s", r.timers)
	}

	// On time, the command runs again. A second tick while it runs starts
	// nothing, and neither does the tick of a timer that was replaced.
	r.shell.answer("uptime", uptime("1.50"), nil)
	r.now = r.now.Add(time.Second)
	run := r.send(r.tick())
	// The update loop only hands the run back as a command of the program's.
	if len(r.shell.commands()) != 1 {
		t.Fatal("the command was run inside Update")
	}
	second := produced(run)
	if len(second) != 1 || len(r.shell.commands()) != 2 {
		t.Fatalf("a tick on time started %d commands; run so far: %q", len(second), r.shell.commands())
	}
	for _, tick := range []tea.Msg{r.tick(), first, r.tick()} {
		r.now = r.now.Add(time.Minute)
		if again := r.deliver(tick); len(again) != 0 {
			t.Fatalf("a tick while the command runs started it again")
		}
	}
	if got := r.shell.commands(); len(got) != 2 || len(r.timers) != 2 {
		t.Fatalf("two ticks ran %q and set %d timers, want two runs and two timers", got, len(r.timers))
	}
	// Nothing was drawn again, the clock included: nothing changed.
	r.wantBar("while the second run goes", " ● live", "load 0.42 │ 12:00 ")
	r.deliver(second...)
	r.wantBar("after the second run", " ● live", "load 1.50 │ 12:03 ")
	if len(r.timers) != 3 || r.timers[2].wait != 10*time.Second {
		t.Fatalf("after the second run the timers are %+v, want a third of 10s", r.timers)
	}
	// The tick of a timer that was replaced since is dropped: the one that
	// is set stands, and no other is set beside it.
	r.now = r.now.Add(5 * time.Second)
	if again := r.deliver(first); len(again) != 0 || len(r.timers) != 3 || len(r.shell.commands()) != 2 {
		t.Fatalf("the tick of an old timer ran %q and left %d timers", r.shell.commands(), len(r.timers))
	}

	// A run that fails shows the segment's name and a question mark.
	r.shell.answer("uptime", "", errors.New("exit status 1"))
	r.now = r.now.Add(5 * time.Second)
	r.deliver(r.deliver(r.tick())...)
	r.wantBar("after a failed run", " ● live", "load ? │ 12:03 ")

	// The conversation left, the timer goes off and nothing runs; no timer
	// is set again until a conversation is open.
	r.send(r.key(ctrl('p'))())
	timers, ran := len(r.timers), len(r.shell.commands())
	r.now = r.now.Add(time.Hour)
	if again := r.deliver(r.tick()); len(again) != 0 || len(r.timers) != timers || len(r.shell.commands()) != ran {
		t.Fatalf("with no conversation open a tick ran %q and set %d timers", r.shell.commands()[ran:], len(r.timers)-timers)
	}
	// Opened again, the segment is due at once, and shows what it last had
	// until the new run is done.
	r.shell.answer("uptime", uptime("0.07"), nil)
	_, started = r.talk()
	if len(started) != 1 || len(r.shell.commands()) != ran+1 {
		t.Fatalf("opening a conversation again started %d commands", len(started))
	}
	r.wantBar("opened again", " ● live", "load ? │ 13:03 ")
	r.deliver(started...)
	r.wantBar("opened again, after its run", " ● live", "load 0.07 │ 13:03 ")
}

// Two shell segments keep their own intervals with one timer between them:
// it is set for whichever is due first, and each run starts when its own
// interval has passed.
func TestTwoShellSegmentsShareOneTimer(t *testing.T) {
	t.Parallel()
	file := `
[[segment]]
name = "a"
from = "shell"
command = "fast"
interval = "10s"

[[segment]]
name = "b"
from = "shell"
command = "slow"
interval = "30s"
`
	r := newBarRig(t, trueDark(), barDir(t, file, 0o600), Options{})
	r.shell.answer("fast", "f1\n", nil)
	r.shell.answer("slow", "s1\n", nil)
	_, started := r.talk()
	if got := r.shell.commands(); !slices.Equal(got, []string{"fast", "slow"}) || len(started) != 2 {
		t.Fatalf("the conversation ran %q", got)
	}
	// fast ends at once and slow two seconds later: the timer is for fast,
	// and slow's end sets no other.
	r.deliver(started[0])
	r.wantBar("fast alone", "", "f1 ")
	r.now = r.now.Add(2 * time.Second)
	r.deliver(started[1])
	r.wantBar("both", "", "f1 │ s1 ")
	if len(r.timers) != 1 || r.timers[0].wait != 10*time.Second {
		t.Fatalf("the timers are %+v, want one of 10s", r.timers)
	}

	// Ten seconds after fast ended only fast runs. While it does, the timer
	// is for slow; when it has ended, for fast again, which is due sooner.
	r.shell.answer("fast", "f2\n", nil)
	r.now = r.now.Add(8 * time.Second)
	again := r.deliver(r.tick())
	if got := r.shell.commands(); !slices.Equal(got, []string{"fast", "slow", "fast"}) || len(again) != 1 {
		t.Fatalf("the first tick ran %q", got)
	}
	if len(r.timers) != 2 || r.timers[1].wait != 22*time.Second {
		t.Fatalf("while fast runs the timers are %+v, want a second of 22s", r.timers)
	}
	stale := r.tick()
	r.deliver(again...)
	r.wantBar("fast again", "", "f2 │ s1 ")
	if len(r.timers) != 3 || r.timers[2].wait != 10*time.Second {
		t.Fatalf("after fast ended the timers are %+v, want a third of 10s", r.timers)
	}

	// At twenty and at thirty seconds fast runs again. The tick of the timer
	// that was replaced starts nothing.
	r.shell.answer("slow", "s2\n", nil)
	for _, at := range []string{"twenty", "thirty"} {
		r.now = r.now.Add(10 * time.Second)
		if early := r.deliver(stale); len(early) != 0 {
			t.Fatal("the tick of a replaced timer started a run")
		}
		r.deliver(r.deliver(r.tick())...)
		if got := r.shell.commands(); got[len(got)-1] != "fast" || slices.Index(got[2:], "slow") >= 0 {
			t.Fatalf("at %s seconds %q had run", at, got)
		}
	}
	// slow ended at two seconds, so it is due at thirty-two: the timer set
	// while fast ran its last run is for the two seconds left, and fast's
	// end, which is due later, set no other.
	if got := r.shell.commands(); len(got) != 5 || len(r.timers) != 6 || r.timers[5].wait != 2*time.Second {
		t.Fatalf("at thirty seconds %q had run and the timers are %+v", got, r.timers)
	}
	r.now = r.now.Add(2 * time.Second)
	r.deliver(r.deliver(r.tick())...)
	if got := r.shell.commands(); len(got) != 6 || got[5] != "slow" {
		t.Fatalf("at thirty-two seconds %q had run", got)
	}
	r.wantBar("slow again", "", "f2 │ s2 ")
}

// The timer exists only while a shell segment is switched on.
func TestTheTimerIsSetOnlyForAShellSegment(t *testing.T) {
	t.Parallel()
	r := newBarRig(t, trueDark(), barDir(t, loadBar, 0o600), Options{})
	r.shell.answer("uptime", uptime("0.42"), nil)
	_, started := r.talk()
	r.deliver(started...)
	if len(r.timers) != 1 {
		t.Fatalf("%d timers after the first run", len(r.timers))
	}
	// Every preset has load switched off: the timer that is set goes off
	// once more, and no other follows.
	r.command("/statusline preset default")
	r.wantBar("the preset", defaultLeft, defaultRight)
	r.now = r.now.Add(10 * time.Second)
	if again := r.deliver(r.tick()); len(again) != 0 || len(r.timers) != 1 || len(r.shell.commands()) != 1 {
		t.Fatalf("with no shell segment on, a tick ran %q and left %d timers", r.shell.commands(), len(r.timers))
	}
	// /reload puts the file's segment back. Its interval has passed.
	if again := r.deliver(r.command("/reload")()); len(again) != 1 || len(r.shell.commands()) != 2 {
		t.Fatalf("/reload with a shell segment on started %d commands", len(again))
	}
}

// What a command printed is shown as the terminal can draw it, also when the
// terminal changes after the run.
func TestShellOutputFollowsTheGlyphSet(t *testing.T) {
	t.Parallel()
	file := "[[segment]]\nname = \"who\"\nfrom = \"shell\"\ncommand = \"whoami\"\n"
	r := newBarRig(t, trueDark(), barDir(t, file, 0o600), Options{})
	r.shell.answer("whoami", "\n  zoë ✓\nand a second line\n", nil)
	_, started := r.talk()
	r.deliver(started...)
	r.wantBar("unicode", "", "zoë ✓ ")
	ran := len(r.shell.commands())
	r.m.setCaps(noneASCII())
	r.wantOnScreen("zo? ?")
	r.wantNotOnScreen("ë", "✓", "second line")
	if len(r.shell.commands()) != ran {
		t.Error("a change of glyphs ran the command again")
	}

	// A command can print anything. None of it reaches the terminal as more
	// than text, and the bar keeps its one row.
	r.m.setCaps(trueDark())
	r.shell.answer("whoami", "\x1b]0;owned\x07zoë\x1b[2J\x1b[31m \u202ered\r\n\x00", nil)
	r.now = r.now.Add(time.Minute)
	r.deliver(r.deliver(r.tick())...)
	raw := r.m.View().Content
	for _, bad := range []string{"\x1b]0", "\x07", "\x1b[2J", "\x1b[31m", "\u202e", "\x00", "\r"} {
		if strings.Contains(raw, bad) {
			t.Errorf("the screen holds %q from a command's output", bad)
		}
	}
	r.wantOnScreen("zoë")
	r.wantFits("hostile output", 100, 30)
	if rows := r.rows(); len(rows) != 30 {
		t.Errorf("hostile output made the screen %d rows", len(rows))
	}

	// Wide output never makes the bar wider than the terminal.
	r.shell.answer("whoami", strings.Repeat("日本語👩‍👩‍👧", 40)+"\n", nil)
	r.now = r.now.Add(time.Minute)
	r.deliver(r.deliver(r.tick())...)
	for _, size := range [][2]int{{100, 30}, {60, 16}, {46, 12}, {30, 12}, {10, 5}} {
		r.size(size[0], size[1])
		r.wantFits("wide output", size[0], size[1])
		if rows := r.rows(); len(rows) != size[1] {
			t.Errorf("wide output at %v made the screen %d rows", size, len(rows))
		}
	}
}

// A file that is still not safe to run commands from after the editor wrote
// it, as off Unix, runs nothing either. The editor has the screen, so the
// note waits until it is left.
func TestAFileNotSafeAfterASave(t *testing.T) {
	t.Parallel()
	dir := barDir(t, "", 0)
	r := newBarRig(t, trueDark(), dir, Options{})
	r.shell.answer("uptime", uptime("0.42"), nil)
	path := filepath.Join(dir, "statusline.toml")
	r.m.sh.safe = func(asked string) error {
		if asked != path {
			t.Errorf("asked about %q, want %q", asked, path)
		}
		return errors.New(asked + " is a link owned by another user")
	}
	r.talk()
	r.command("/statusline")
	// load is the eleventh segment of the default preset.
	for range 10 {
		r.key(special(tea.KeyDown))
	}
	r.key(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	r.wantOnScreen(" [x] load ")
	if cmd := r.key(tea.KeyPressMsg{Code: 's', Text: "s"}); cmd != nil {
		t.Errorf("saving to a file that is not safe gives %v", produced(cmd))
	}
	// The file has what was saved; the bar in use has the segment off.
	cfg, _, err := statusline.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(statusline.Due(cfg, nil, time.Time{})) != 1 || len(statusline.Due(r.m.bar, nil, time.Time{})) != 0 {
		t.Errorf("the file has %d shell segments to run and the bar in use %d, want 1 and 0",
			len(statusline.Due(cfg, nil, time.Time{})), len(statusline.Due(r.m.bar, nil, time.Time{})))
	}
	r.key(esc())
	why := "shell segments are off: statusline.toml is a link owned by another user"
	if r.m.screen != screenChat || r.note() != why {
		t.Errorf("after the editor the screen is %v and the note %q, want %q", r.m.screen, r.note(), why)
	}
	r.wantOnScreen(why)
	r.wantBar("not safe", defaultLeft, defaultRight)
	if len(r.shell.commands()) != 0 || len(r.timers) != 0 {
		t.Errorf("a file that is not safe ran %q and set %d timers", r.shell.commands(), len(r.timers))
	}
}

// A file that someone else could have written is not trusted with commands:
// its shell segments are switched off, nothing runs, and the note says why.
func TestAFileOthersCanWriteRunsNothing(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		mode os.FileMode
		why  string
	}{
		"its group can write":  {0o660, "shell segments are off: statusline.toml can be written by its group (mode 0660); chmod go-w fixes that"},
		"everyone can write":   {0o606, "shell segments are off: statusline.toml can be written by everyone (mode 0606); chmod go-w fixes that"},
		"both, and it is read": {0o666, "shell segments are off: statusline.toml can be written by its group (mode 0666); chmod go-w fixes that"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := barDir(t, loadBar, tc.mode)
			r := newBarRig(t, trueDark(), dir, Options{})
			r.shell.answer("uptime", uptime("0.42"), nil)
			if r.m.note != tc.why {
				t.Fatalf("the note is\n%q, want\n%q", r.m.note, tc.why)
			}
			r.size(140, 30)
			r.send(r.m.showContacts()())
			r.wantOnScreen(tc.why)
			_, started := r.talk()
			// The rest of the file counts.
			r.wantBar(name, " ● live", "12:00 ")
			for range 3 {
				r.now = r.now.Add(time.Minute)
				r.deliver(shellTickMsg{gen: r.m.sh.gen})
				r.send(clockMsg{})
			}
			if got := r.shell.commands(); len(got) != 0 || len(started) != 0 || len(r.timers) != 0 {
				t.Fatalf("a file others can write ran %q, started %d commands and set %d timers", got, len(started), len(r.timers))
			}
			// The editor shows the segment switched off.
			r.command("/statusline")
			r.wantOnScreen(" [ ] load ")
			r.key(esc())

			// Read again as it is, it is refused again.
			r.then(r.command("/reload"))
			if r.note() != tc.why || len(r.shell.commands()) != 0 {
				t.Errorf("after /reload the note is %q and %q was run", r.note(), r.shell.commands())
			}
			// Made the person's own, it is read and run.
			if err := os.Chmod(filepath.Join(dir, "statusline.toml"), 0o644); err != nil {
				t.Fatal(err)
			}
			again := r.deliver(r.command("/reload")())
			if len(again) != 1 || !slices.Equal(r.shell.commands(), []string{"uptime"}) {
				t.Fatalf("after chmod, /reload started %d commands and ran %q", len(again), r.shell.commands())
			}
			r.deliver(again...)
			r.wantBar("after chmod", " ● live", "load 0.42 │ 12:03 ")
		})
	}

	// A file others can write that runs nothing is no matter: nothing is
	// said about it.
	r := newBarRig(t, trueDark(), barDir(t, "[[segment]]\nname = \"clock\"\n", 0o666), Options{})
	if r.m.note != "" {
		t.Errorf("a file with no command to run left the note %q", r.m.note)
	}
	// So is one whose shell segment is switched off, or has no command.
	off := strings.Replace(loadBar, `from = "shell"`, "from = \"shell\"\non = false", 1)
	r = newBarRig(t, trueDark(), barDir(t, off, 0o666), Options{})
	if r.m.note != "" {
		t.Errorf("a file with its shell segment off left the note %q", r.m.note)
	}
}

// A shell segment added in the editor and saved runs at once: the file is
// the client's own writing then, whoever could write to it before.
func TestAShellSegmentAddedInTheEditorRuns(t *testing.T) {
	t.Parallel()
	dir := barDir(t, loadBar, 0o666)
	r := newBarRig(t, trueDark(), dir, Options{})
	r.shell.answer("uptime", uptime("0.42"), nil)
	r.shell.answer("df", "71% used\n", nil)
	r.talk()
	char := func(c rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: c, Text: string(c)} }

	// a adds a segment and asks for its name, then its command.
	r.command("/statusline")
	r.key(char('a'))
	r.typeText("disk")
	r.key(enter())
	r.typeText("df")
	r.key(enter())
	r.key(esc())
	if len(r.shell.commands()) != 0 {
		t.Fatalf("%q was run before anything was saved", r.shell.commands())
	}
	started := produced(r.key(char('s')))
	if got := r.shell.commands(); !slices.Equal(got, []string{"df"}) || len(started) != 1 {
		t.Fatalf("saving ran %q and started %d commands, want df alone: load is still switched off", got, len(started))
	}
	r.wantOnScreen("saved")
	path := filepath.Join(dir, "statusline.toml")
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("the saved file: %v, %v", info.Mode().Perm(), err)
	}
	if err := conf.SafeToExecuteFrom(path); err != nil {
		t.Errorf("the saved file is not the person's own: %v", err)
	}
	// The value arrives under the editor, which shows it.
	r.deliver(started...)
	r.wantOnScreen(" [x] disk       71% used ", " [ ] load ")
	r.key(esc())
	r.wantBar("after the save", " ● live", "71% used │ 12:00 ")
	if r.note() != "" {
		t.Errorf("leaving after a save says %q", r.note())
	}

	// Its command changed while a run of the old one is on its way: what
	// that run prints is not the segment's any more, and the new command
	// runs as soon as the old one has ended.
	r.now = r.now.Add(10 * time.Second)
	old := r.deliver(r.tick())
	if len(old) != 1 {
		t.Fatalf("the tick started %d commands", len(old))
	}
	cfg, _, err := statusline.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := range cfg.Segments {
		if cfg.Segments[i].Name == "disk" {
			cfg.Segments[i].Command = "df -h"
		}
	}
	writeBar(t, dir, string(cfg.Encode()), 0o600)
	r.shell.answer("df -h", "9% used\n", nil)
	if again := r.deliver(r.command("/reload")()); len(again) != 0 {
		t.Fatalf("/reload started %d commands while the segment's run was going", len(again))
	}
	// The old value went with the old command.
	r.wantBar("a new command", " ● live", "12:00 ")
	now := r.deliver(old...)
	if got := r.shell.commands(); len(now) != 1 || got[len(got)-1] != "df -h" {
		t.Fatalf("after the old run ended, %d commands were started; run so far: %q", len(now), got)
	}
	r.wantBar("the old run ended", " ● live", "12:00 ")
	r.deliver(now...)
	r.wantBar("the new run ended", " ● live", "9% used │ 12:00 ")
}

// A preset replaces the segments it lists. One a person added stays, as it
// does when the editor applies a preset.
func TestAPresetKeepsWhatAPersonAdded(t *testing.T) {
	t.Parallel()
	file := `
[[segment]]
name = "state"

[[segment]]
name = "tokens"

[[segment]]
name = "disk"
from = "shell"
command = "df"

[[segment]]
name = "clock"
`
	r := newBarRig(t, trueDark(), barDir(t, file, 0o600), Options{})
	r.shell.answer("df", "71% used\n", nil)
	_, started := r.talk()
	r.deliver(started...)
	r.wantBar("the file", " ● live │ 2.0k tok", "71% used │ 12:00 ")
	r.command("/statusline preset minimal")
	r.wantBar("minimal over the file", " ● live │ cache 75%", "71% used │ 12:00 ")
	if got := r.shell.commands(); len(got) != 1 {
		t.Errorf("the preset ran the command again: %q", got)
	}
	// /reload reads the file again, and the preset is gone.
	r.then(r.command("/reload"))
	r.wantBar("the file again", " ● live │ 2.0k tok", "71% used │ 12:00 ")
}

// slowShell is a shell whose command never ends by itself: it runs until its
// context is done, as a command does that is killed for that.
type slowShell struct {
	once    sync.Once
	started chan struct{}
	stopped atomic.Bool
}

func (s *slowShell) Run(ctx context.Context, _ string, _ time.Duration) (string, error) {
	s.once.Do(func() { close(s.started) })
	<-ctx.Done()
	s.stopped.Store(true)
	return "", ctx.Err()
}

// A command that is still running when the client is left is stopped, and
// has ended by the time Run returns: nothing the client started outlives it.
func TestLeavingStopsACommandThatRuns(t *testing.T) {
	t.Parallel()
	ada := client.Contact{ID: 1, Name: "ada", Provider: "deepseek", Model: "deepseek-flash"}
	b := &fakeBackend{contacts: []client.Contact{ada}}
	in, keys := io.Pipe()
	t.Cleanup(func() { _ = keys.Close() }) // closing a pipe's writer never fails
	out := &tuitest.Recorder{}
	env, over := terminal(t, trueDark())
	shell := &slowShell{started: make(chan struct{})}
	dir := barDir(t, loadBar, 0o600)
	done := make(chan error, 1)
	go func() {
		// The context is the test's, which does not end: leaving is what
		// stops the command.
		done <- Run(t.Context(), Options{
			Backend: b, Open: &ada, Now: fixedNow, Location: time.UTC, Env: env, Overrides: over,
			Dirs: conf.Dirs{Config: dir}, Shell: shell,
			Program: []tea.ProgramOption{
				tea.WithInput(in), tea.WithOutput(out), tea.WithWindowSize(100, 30),
				tea.WithEnvironment([]string{"TERM=xterm-256color"}),
			},
		})
	}()
	untilShown(t, out, 0, "❯ Message ada")
	// The command is started with the conversation. A client that never
	// starts it fails here, and does not hang.
	started := false
	for range 50 {
		select {
		case <-shell.started:
			started = true
		default:
			out.Quiet(t)
		}
		if started {
			break
		}
	}
	if !started {
		t.Fatal("the shell segment's command was never started")
	}
	if _, err := keys.Write([]byte("\x03")); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Run returned %v", err)
	}
	if !shell.stopped.Load() {
		t.Error("Run returned while a shell segment's command was still running")
	}
}

// Under a real program: the command runs when the client starts in a
// conversation, its value is drawn, and the editor opens and closes.
func TestTheStatusLineOnAPipe(t *testing.T) {
	t.Parallel()
	env, over := terminal(t, trueDark())
	shell := &fakeShell{out: map[string]string{"uptime": uptime("0.42")}, fail: map[string]error{}}
	dir := barDir(t, loadBar, 0o600)
	r := startProgram(t, Options{
		Env: env, Overrides: over, Dirs: conf.Dirs{Config: dir}, Home: filepath.Dir(dir), Shell: shell,
		Open: &client.Contact{ID: 1, Name: "ada", Provider: "deepseek", Model: "deepseek-flash"},
	})
	// Only the cells that changed are written, so each wait is for text
	// where there was none.
	untilShown(t, r.out, 0, "load 0.42")
	mark := r.out.Mark()
	r.write("/statusline\r")
	untilShown(t, r.out, mark, "SEGMENT")
	untilShown(t, r.out, mark, "~/"+filepath.Base(dir)+"/statusline.toml")
	untilShown(t, r.out, mark, "[x] load       load 0.42")
	mark = r.out.Mark()
	r.write("\x1b")
	untilShown(t, r.out, mark, "Message ada")
	r.quit()
	// The clock of this test stands still, so the interval never passes.
	if got := shell.commands(); !slices.Equal(got, []string{"uptime"}) {
		t.Errorf("the program ran %q, want uptime once", got)
	}
	if r.m.screen != screenChat || r.m.edit != nil {
		t.Errorf("the program left with the screen %v", r.m.screen)
	}
}

// With no Shell in the options the commands are run by /bin/sh, for real:
// here one that prints a line, whose value is drawn.
func TestTheRealShellOnAPipe(t *testing.T) {
	t.Parallel()
	env, over := terminal(t, trueDark())
	file := "[[segment]]\nname = \"sum\"\nfrom = \"shell\"\ncommand = \"echo six times seven is $((6*7))\"\n"
	r := startProgram(t, Options{
		Env: env, Overrides: over, Dirs: conf.Dirs{Config: barDir(t, file, 0o600)},
		Open: &client.Contact{ID: 1, Name: "ada", Provider: "deepseek", Model: "deepseek-flash"},
	})
	untilShown(t, r.out, 0, "six times seven is 42")
	r.quit()
}
