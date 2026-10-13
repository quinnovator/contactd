package statusline_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/quinnovator/contactd/internal/tui/statusline"
)

// load returns the shell segment every preset lists.
func load(t *testing.T) statusline.Segment {
	t.Helper()
	c := preset(t, "default")
	return c.Segments[index(t, c, "shell:load")]
}

func TestShellValue(t *testing.T) {
	t.Parallel()
	plain := statusline.Segment{Name: "branch", From: statusline.FromShell}
	matched := func(match, format string) statusline.Segment {
		return statusline.Segment{Name: "m", From: statusline.FromShell, Match: match, Format: format}
	}
	failed := errors.New("exit status 1")
	tests := []struct {
		name    string
		segment statusline.Segment
		out     string
		err     error
		ascii   bool
		text    string // what the segment shows; "" for nothing
		tone    string
		num     float64
		hasNum  bool
	}{
		{name: "the output, without its newline", segment: plain, out: "main\n", text: "main"},
		{name: "the first line that is not blank", segment: plain, out: "\n  \r\n  up 3 days\nsecond line\n", text: "up 3 days"},
		{name: "no output shows nothing", segment: plain, out: ""},
		{name: "blank output shows nothing", segment: plain, out: " \n\n", text: ""},
		{name: "output that is a number has one", segment: plain, out: " 0.42\n", text: "0.42", num: 0.42, hasNum: true},
		{name: "a negative number", segment: plain, out: "-3", text: "-3", num: -3, hasNum: true},
		{name: "not every word that parses is a number", segment: plain, out: "NaN", text: "NaN"},
		{name: "infinity is no number to warn about", segment: plain, out: "inf", text: "inf"},

		{
			name: "the mock's load on macOS", segment: load(t),
			out:  "12:04  up 3 days,  2:11, 3 users, load averages: 0.42 1.50 1.60\n",
			text: "load 0.42", num: 0.42, hasNum: true,
		},
		{
			name: "and on Linux", segment: load(t),
			out:  " 12:04:01 up 3 days,  2:11,  3 users,  load average: 1.20, 1.50, 1.60\n",
			text: "load 1.20", num: 1.2, hasNum: true,
		},
		{
			name: "a match may be on a later line", segment: matched(`(?m)^rate: (\d+)%$`, "$1 percent"),
			out: "host: a\nrate: 97%\n", text: "97 percent", num: 97, hasNum: true,
		},
		{
			name: "several groups", segment: matched(`(\w+)@(\w+):(\d+)`, "$2/$1 on $3"),
			out: "ada@box:22", text: "box/ada on 22",
		},
		{
			name: "a group that took no part is empty", segment: matched(`a(x)?(b)`, "[$1][$2]"),
			out: "ab", text: "[][b]",
		},
		{
			name: "a group the expression does not have is left as written", segment: matched(`(a)`, "$1 $2"),
			out: "a", text: "a $2",
		},
		{
			name: "only the first nine groups are variables", segment: matched(`(a)(b)(c)(d)(e)(f)(g)(h)(i)(j)`, "$9${1}0"),
			out: "abcdefghij", text: "ia0",
		},
		{
			name: "with a match and no groups, $out is still the first line", segment: matched(`up`, "$out"),
			out: "up 3 days\nmore", text: "up 3 days",
		},
		{
			name: "the default format is $out", segment: matched(`(\d+)`, ""),
			out: "up 3 days", text: "up 3 days", num: 3, hasNum: true,
		},
		{
			name: "the number is the first group, not the line", segment: matched(`load (\S+)`, "$out"),
			out: "7 load 0.5", text: "7 load 0.5", num: 0.5, hasNum: true,
		},
		{
			name: "a first group that is no number", segment: matched(`on (\w+)`, "$1"),
			out: "on main", text: "main",
		},

		{name: "a failed run shows the name and a question mark", segment: plain, err: failed, text: "branch ?", tone: "dim"},
		{name: "and nothing of what it printed", segment: plain, out: "fatal: not a git repository\n", err: failed, text: "branch ?", tone: "dim"},
		{name: "a timeout is a failed run", segment: load(t), err: context.DeadlineExceeded, text: "load ?", tone: "dim"},
		{name: "a match that finds nothing", segment: load(t), out: "uptime: not found\n", text: "load ?", tone: "dim"},
		{name: "a match that is not an expression", segment: matched(`(`, "$1"), out: "x", text: "m ?", tone: "dim"},

		{
			name: "escape sequences are not kept", segment: plain,
			out:  "\x1b]0;owned\x07\x1b[31mred\x1b[0m\x9b2J\u202e\u2066\U0010EEEE\x00\n",
			text: "]0;owned[31mred[0m\ufffd2J",
		},
		{
			name: "nor in a group", segment: matched(`<(.*)>`, "$1"),
			out: "<a\x1b[2Jb\tc\u200d\ufe0f>", text: "a[2Jb    c",
		},
		{
			name: "the match sees the output as it was printed", segment: matched(`^a\tb\x1b\[(\d+)m`, "$1"),
			out: "a\tb\x1b[31m", text: "31", num: 31, hasNum: true,
		},
		{
			name: "Windows line ends", segment: plain,
			out: "\r\nmain\r\nsecond\r\n", text: "main",
		},
		{
			name: "a carriage return does not hide what came before", segment: plain,
			out: "real\rfake\n", text: "realfake",
		},
		{
			name: "ASCII glyphs get ASCII output", segment: plain, ascii: true,
			out: "⎇ maïn\n", text: "? ma?n",
		},
		{
			name: "and ASCII groups", segment: matched(`on (\S+)`, "$1!"), ascii: true,
			out: "on brånch", text: "br?nch!",
		},
		{
			name: "the output is cut at 64 runes", segment: plain,
			out: strings.Repeat("é", 200), text: strings.Repeat("é", 64),
		},
		{
			name: "and so is a group", segment: matched(`(x+)`, "$1"),
			out: strings.Repeat("x", 200), text: strings.Repeat("x", 64),
		},
		{
			name: "what comes after the first 4 KiB is not looked at", segment: load(t),
			out: strings.Repeat("x", 4096) + "\nload average: 9.99\n", text: "load ?", tone: "dim",
		},
		{
			name: "and what comes just before is", segment: matched(`(\d+)$`, "$1"),
			out: strings.Repeat("x", 4095) + "789", text: "7", num: 7, hasNum: true,
		},
		{
			name: "a hostile name is made safe too", segment: statusline.Segment{Name: "lo\x1b[2Jad", From: statusline.FromShell},
			err: failed, text: "lo[2Jad ?", tone: "dim",
		},
		{
			name: "and ASCII when asked", segment: statusline.Segment{Name: "läst", From: statusline.FromShell},
			err: failed, ascii: true, text: "l?st ?", tone: "dim",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			v := statusline.ShellValue(tt.segment, tt.out, tt.err, tt.ascii)
			full, short := tt.segment.Text(v)
			if full != tt.text || short != "" {
				t.Errorf("shows %q, short %q; want %q and no short form", full, short, tt.text)
			}
			if v.Tone != tt.tone {
				t.Errorf("the tone is %q, want %q", v.Tone, tt.tone)
			}
			if v.HasNum != tt.hasNum || v.Num != tt.num {
				t.Errorf("the number is %v (%v), want %v (%v)", v.Num, v.HasNum, tt.num, tt.hasNum)
			}
			if _, has := v.Vars["10"]; has {
				t.Error("there is a variable for a tenth group")
			}
			for name, val := range v.Vars {
				if n := utf8.RuneCountInString(val); n > 64 {
					t.Errorf("$%s is %d runes long", name, n)
				}
				for _, r := range val + v.Text {
					if r < 0x20 || (r >= 0x7f && r <= 0x9f) || (tt.ascii && r >= utf8.RuneSelf) {
						t.Errorf("$%s holds %U: %q", name, r, val)
					}
				}
			}
		})
	}
}

// A shell segment with a short format has a short form, also when its run
// failed; and its number is held against warn_below.
func TestShellValueOnTheBar(t *testing.T) {
	t.Parallel()
	seg := load(t)
	seg.On, seg.Short, seg.WarnBelow, seg.HasWarn = true, "$1", 1, true
	c := statusline.Config{Segments: []statusline.Segment{seg}}
	tests := []struct {
		name  string
		out   string
		err   error
		width int
		want  statusline.Cell
	}{
		{"a number over warn_below", "load average: 1.20", nil, 50, statusline.Cell{ID: "shell:load", Text: "load 1.20"}},
		{"a number under it", "load average: 0.42", nil, 50, statusline.Cell{ID: "shell:load", Text: "load 0.42", Tone: "warn"}},
		{"its short form", "load average: 0.42", nil, 39, statusline.Cell{ID: "shell:load", Text: "0.42", Tone: "warn"}},
		{"a failed run", "", errors.New("no"), 50, statusline.Cell{ID: "shell:load", Text: "load ?", Tone: "dim"}},
		{"a failed run, narrow", "", errors.New("no"), 39, statusline.Cell{ID: "shell:load", Text: "load ?", Tone: "dim"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			vals := map[string]statusline.Value{seg.ID(): statusline.ShellValue(seg, tt.out, tt.err, false)}
			left, right := statusline.Layout(c, vals, tt.width, client())
			if left != nil || !slices.Equal(right, []statusline.Cell{tt.want}) {
				t.Errorf("got %+v and %+v, want nothing and %+v", left, right, tt.want)
			}
		})
	}
}

func TestDue(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 12, 12, 4, 0, 0, time.UTC)
	shell := func(name string, interval time.Duration) statusline.Segment {
		return statusline.Segment{
			Name: name, From: statusline.FromShell, On: true, Side: statusline.Right,
			Command: "true", Interval: interval, Timeout: time.Second,
		}
	}
	off := shell("off", 10*time.Second)
	off.On = false
	empty := shell("empty", 10*time.Second)
	empty.Command = " \n"
	c := statusline.Config{Segments: []statusline.Segment{
		{Name: "clock", On: true},
		shell("ten", 10*time.Second),
		{Name: "branch", From: "mod:git", On: true, Command: "true", Interval: time.Second},
		off,
		shell("minute", time.Minute),
		empty,
		// Built by hand with no interval: it runs at the shortest one.
		shell("none", 0),
		{Name: "clock", On: true, Command: "date", Interval: time.Second},
	}}
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	tests := []struct {
		name string
		last map[string]time.Time
		want []string
	}{
		{"never run: all are due, in file order", nil, []string{"ten", "minute", "none"}},
		{
			"an empty map is the same",
			map[string]time.Time{},
			[]string{"ten", "minute", "none"},
		},
		{
			"one that finished less than its interval ago waits",
			map[string]time.Time{"shell:ten": ago(9 * time.Second), "shell:minute": ago(59 * time.Second), "shell:none": ago(999 * time.Millisecond)},
			nil,
		},
		{
			"at the interval it is due",
			map[string]time.Time{"shell:ten": ago(10 * time.Second), "shell:minute": ago(59 * time.Second), "shell:none": ago(time.Second)},
			[]string{"ten", "none"},
		},
		{
			"long after it is due",
			map[string]time.Time{"shell:ten": ago(time.Hour), "shell:minute": ago(time.Minute), "shell:none": ago(time.Hour)},
			[]string{"ten", "minute", "none"},
		},
		{
			"a finish time in the future keeps a segment waiting",
			map[string]time.Time{"shell:ten": now.Add(time.Hour), "shell:minute": now, "shell:none": now},
			nil,
		},
		{
			"other segments' times are not looked at",
			map[string]time.Time{"shell:off": ago(time.Hour), "shell:empty": ago(time.Hour), "core:clock": ago(time.Hour), "mod:git/branch": ago(time.Hour), "ten": now},
			[]string{"ten", "minute", "none"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var got []string
			for _, s := range statusline.Due(c, tt.last, now) {
				got = append(got, s.Name)
				if s.From != statusline.FromShell || !s.On {
					t.Errorf("%+v is due", s)
				}
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("due are %q, want %q", got, tt.want)
			}
		})
	}

	// A segment listed twice is due once: never two runs at once for one.
	twice := statusline.Config{Segments: []statusline.Segment{shell("a", time.Second), shell("a", time.Second)}}
	if got := statusline.Due(twice, nil, now); len(got) != 1 {
		t.Errorf("a segment listed twice is due %d times", len(got))
	}
	// No preset runs anything.
	for _, name := range statusline.Presets() {
		if got := statusline.Due(preset(t, name), nil, now); got != nil {
			t.Errorf("the preset %s has %+v due", name, got)
		}
	}
}

// fakeRunner is a Runner that runs nothing: it hands out the results it was
// given, in order, and keeps what it was asked.
type fakeRunner struct {
	results []result
	calls   []call
}

type result struct {
	out string
	err error
}

type call struct {
	command string
	timeout time.Duration
}

func (f *fakeRunner) Run(_ context.Context, command string, timeout time.Duration) (string, error) {
	f.calls = append(f.calls, call{command, timeout})
	r := f.results[0]
	f.results = f.results[1:]
	return r.out, r.err
}

// The client's loop for shell segments, on a clock the test moves: what is
// due is run, its value goes on the bar, and it is not run again until its
// interval has passed since the run finished.
func TestShellSegmentCycle(t *testing.T) {
	t.Parallel()
	c := preset(t, "default")
	c.Segments = slices.Insert(c.Segments, index(t, c, "core:clock"), statusline.Segment{
		Name: "branch", From: statusline.FromShell, On: true, Side: statusline.Right, Drop: 1,
		Command: "git branch --show-current", Interval: 5 * time.Second, Timeout: 2 * time.Second,
	})
	fake := &fakeRunner{results: []result{
		{out: "main\n"},
		{err: errors.New("exit status 128")},
		{out: "\x1b[31mfeature/x\x1b[0m\n"},
	}}
	var runner statusline.Runner = fake
	now := time.Date(2026, 10, 12, 12, 4, 0, 0, time.UTC)
	vals := statusline.CoreValues(idle(), unicodeMarks())
	last := map[string]time.Time{}
	tick := func(after time.Duration) []string {
		now = now.Add(after)
		for _, s := range statusline.Due(c, last, now) {
			out, err := runner.Run(t.Context(), s.Command, s.Timeout)
			vals[s.ID()] = statusline.ShellValue(s, out, err, false)
			// The run took a second; the interval counts from its end.
			now = now.Add(time.Second)
			last[s.ID()] = now
		}
		_, right := statusline.Layout(c, vals, 100, client())
		return texts(right)
	}
	steps := []struct {
		after time.Duration
		right []string
		calls int
	}{
		{0, []string{"main", "12:04"}, 1},
		{0, []string{"main", "12:04"}, 1},
		{4 * time.Second, []string{"main", "12:04"}, 1},
		{time.Second, []string{"branch ?", "12:04"}, 2},
		{4999 * time.Millisecond, []string{"branch ?", "12:04"}, 2},
		{time.Millisecond, []string{"[31mfeature/x[0m", "12:04"}, 3},
	}
	for i, step := range steps {
		if got := tick(step.after); !slices.Equal(got, step.right) {
			t.Errorf("step %d: the right side is %q, want %q", i, got, step.right)
		}
		if len(fake.calls) != step.calls {
			t.Fatalf("step %d: %d runs so far, want %d", i, len(fake.calls), step.calls)
		}
	}
	for _, got := range fake.calls {
		if want := (call{"git branch --show-current", 2 * time.Second}); got != want {
			t.Errorf("ran %+v, want %+v", got, want)
		}
	}
}
