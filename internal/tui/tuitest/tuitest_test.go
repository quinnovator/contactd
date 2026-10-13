package tuitest

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMain(m *testing.M) { Main(m) }

func TestMainRegistersUpdate(t *testing.T) {
	t.Parallel()
	f := flag.Lookup("update")
	if f == nil {
		t.Fatal("Main did not register -update")
	}
	if f.DefValue != "false" {
		t.Errorf("-update defaults to %q, want false", f.DefValue)
	}
	if got, want := updating(), f.Value.String() == "true"; got != want {
		t.Errorf("updating() = %v with -update=%s", got, f.Value)
	}
	// A binary that already has the flag keeps it: defining it again would
	// panic before any test ran.
	register()
	if again := flag.Lookup("update"); again != f {
		t.Error("registering twice replaced the flag")
	}
}

// The exported path, against a file that is checked in. It holds an escape
// sequence and a trailing space, which a golden file must keep as they are.
func TestGolden(t *testing.T) {
	t.Parallel()
	Golden(t, "hello.golden", []byte("\x1b[1mhello\x1b[m \nworld\n"))
	Golden(t, "nested/empty.golden", nil)
}

func TestCompare(t *testing.T) {
	t.Parallel()

	t.Run("a missing file says how to create it", func(t *testing.T) {
		t.Parallel()
		got := compare(t.TempDir(), "absent.golden", []byte("x"), false)
		if !strings.Contains(got, "read golden") || !strings.Contains(got, "-update") {
			t.Errorf("compare = %q, want a read error that names -update", got)
		}
	})

	t.Run("update writes the file and its directories", func(t *testing.T) {
		t.Parallel()
		dir := filepath.Join(t.TempDir(), "testdata")
		if got := compare(dir, "frame/100x30.golden", []byte("one\ntwo\n"), true); got != "" {
			t.Fatalf("compare with update = %q", got)
		}
		data, err := os.ReadFile(filepath.Join(dir, "frame", "100x30.golden"))
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "one\ntwo\n" {
			t.Errorf("file holds %q", data)
		}
		if got := compare(dir, "frame/100x30.golden", []byte("one\ntwo\n"), false); got != "" {
			t.Errorf("compare with what was written = %q", got)
		}
	})

	t.Run("update replaces what was there", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		for _, text := range []string{"a much longer first version\n", "short\n"} {
			if got := compare(dir, "x.golden", []byte(text), true); got != "" {
				t.Fatalf("compare with update = %q", got)
			}
		}
		if got := compare(dir, "x.golden", []byte("short\n"), false); got != "" {
			t.Errorf("compare = %q", got)
		}
	})

	t.Run("without update nothing is written", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		if got := compare(dir, "x.golden", []byte("x"), false); got == "" {
			t.Fatal("compare with no file passed")
		}
		if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
			t.Errorf("directory holds %v (%v), want nothing", entries, err)
		}
	})

	t.Run("a difference names the file, the line and both sides", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		if got := compare(dir, "x.golden", []byte("same\n\x1b[1mold\x1b[m\nlast\n"), true); got != "" {
			t.Fatal(got)
		}
		got := compare(dir, "x.golden", []byte("same\n\x1b[1mnew\x1b[m \nlast\n"), false)
		for _, want := range []string{
			filepath.Join(dir, "x.golden") + " differs", "-update",
			"line 2 of 3 (want 3)", ` got: "\x1b[1mnew\x1b[m \n"`, `want: "\x1b[1mold\x1b[m\n"`,
		} {
			if !strings.Contains(got, want) {
				t.Errorf("compare = %q, want it to contain %q", got, want)
			}
		}
		// The file is left as it was.
		if data, err := os.ReadFile(filepath.Join(dir, "x.golden")); err != nil || !bytes.Contains(data, []byte("old")) {
			t.Errorf("file holds %q (%v)", data, err)
		}
	})

	t.Run("a difference of one byte in a text of the same length", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		if got := compare(dir, "x.golden", []byte("cache 82%\n"), true); got != "" {
			t.Fatal(got)
		}
		if got := compare(dir, "x.golden", []byte("cache 83%\n"), false); !strings.Contains(got, "line 1 of 1") {
			t.Errorf("compare = %q, want a difference on line 1", got)
		}
	})

	t.Run("a name that leaves the directory is refused", func(t *testing.T) {
		t.Parallel()
		for _, name := range []string{"../escaped.golden", "a/../../escaped.golden", "/escaped.golden", ""} {
			parent := t.TempDir()
			dir := filepath.Join(parent, "testdata")
			if got := compare(dir, name, []byte("x"), true); !strings.Contains(got, "does not stay inside") {
				t.Errorf("compare(%q) = %q, want a refusal", name, got)
			}
			if entries, err := os.ReadDir(parent); err != nil || len(entries) != 0 {
				t.Errorf("compare(%q) wrote %v (%v)", name, entries, err)
			}
		}
	})
}

func TestFirstDifference(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, got, want, report string
	}{
		{
			name: "first line", got: "a\n", want: "b\n",
			report: "line 1 of 1 (want 1):\n got: \"a\\n\"\nwant: \"b\\n\"",
		},
		{
			name: "a later line", got: "a\nb\nc\n", want: "a\nB\nc\n",
			report: "line 2 of 3 (want 3):\n got: \"b\\n\"\nwant: \"B\\n\"",
		},
		{
			name: "got is shorter", got: "a\n", want: "a\nb\n",
			report: "line 2 of 1 (want 2):\n got: (no such line)\nwant: \"b\\n\"",
		},
		{
			name: "got is longer", got: "a\nb\n", want: "a\n",
			report: "line 2 of 2 (want 1):\n got: \"b\\n\"\nwant: (no such line)",
		},
		{
			name: "only the last newline differs", got: "a", want: "a\n",
			report: "line 1 of 1 (want 1):\n got: \"a\"\nwant: \"a\\n\"",
		},
		{
			name: "got is empty", got: "", want: "a\n",
			report: "line 1 of 0 (want 1):\n got: (no such line)\nwant: \"a\\n\"",
		},
		{
			name: "a trailing space shows", got: "a \n", want: "a\n",
			report: "line 1 of 1 (want 1):\n got: \"a \\n\"\nwant: \"a\\n\"",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := firstDifference([]byte(tc.got), []byte(tc.want)); got != tc.report {
				t.Errorf("firstDifference(%q, %q) =\n%s\nwant:\n%s", tc.got, tc.want, got, tc.report)
			}
		})
	}
}

func TestRecorder(t *testing.T) {
	t.Parallel()
	var r Recorder
	if got := r.Mark(); got != 0 {
		t.Errorf("Mark of a new recorder = %d", got)
	}
	if got := r.Since(0); len(got) != 0 {
		t.Errorf("Since(0) of a new recorder = %q", got)
	}
	if n, err := r.Write([]byte("first")); n != 5 || err != nil {
		t.Errorf("Write = %d, %v", n, err)
	}
	mark := r.Mark()
	if mark != 5 {
		t.Errorf("Mark = %d, want 5", mark)
	}
	if n, err := r.Write(nil); n != 0 || err != nil {
		t.Errorf("Write(nil) = %d, %v", n, err)
	}
	if got := r.Since(mark); len(got) != 0 {
		t.Errorf("Since(mark) with nothing new = %q", got)
	}
	for _, p := range []string{"\x1b[?25l", "h", "\x1b[?25h"} {
		if _, err := r.Write([]byte(p)); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := string(r.Since(mark)), "\x1b[?25lh\x1b[?25h"; got != want {
		t.Errorf("Since(mark) = %q, want %q", got, want)
	}
	if got, want := string(r.Since(0)), "first\x1b[?25lh\x1b[?25h"; got != want {
		t.Errorf("Since(0) = %q, want %q", got, want)
	}
	// Out-of-range marks are clamped, not a panic.
	if got, want := string(r.Since(-3)), "first\x1b[?25lh\x1b[?25h"; got != want {
		t.Errorf("Since(-3) = %q, want %q", got, want)
	}
	if got := r.Since(r.Mark() + 10); len(got) != 0 {
		t.Errorf("Since past the end = %q", got)
	}
	// What Since returns is the caller's: changing it changes nothing here.
	got := r.Since(0)
	for i := range got {
		got[i] = '!'
	}
	if got, want := string(r.Since(0)), "first\x1b[?25lh\x1b[?25h"; got != want {
		t.Errorf("the recorder shared its buffer: Since(0) = %q", got)
	}
}

// The writer must not keep the caller's slice: a program reuses its buffer.
func TestRecorderCopiesWhatItIsGiven(t *testing.T) {
	t.Parallel()
	var r Recorder
	p := []byte("abc")
	if _, err := r.Write(p); err != nil {
		t.Fatal(err)
	}
	copy(p, "xyz")
	if got := string(r.Since(0)); got != "abc" {
		t.Errorf("Since(0) = %q, want abc", got)
	}
}

func TestRecorderConcurrentUse(t *testing.T) {
	t.Parallel()
	const writers, each = 8, 200
	var r Recorder
	var wg sync.WaitGroup
	for w := range writers {
		wg.Go(func() {
			for range each {
				if _, err := r.Write([]byte{byte('a' + w)}); err != nil {
					t.Error(err)
				}
				_ = r.Since(r.Mark() / 2)
			}
		})
	}
	wg.Wait()
	got := r.Since(0)
	if len(got) != writers*each || r.Mark() != writers*each {
		t.Fatalf("recorded %d bytes, Mark %d, want %d", len(got), r.Mark(), writers*each)
	}
	for w := range writers {
		if n := bytes.Count(got, []byte{byte('a' + w)}); n != each {
			t.Errorf("writer %d: %d bytes recorded, want %d", w, n, each)
		}
	}
}

// fakeClock stands in for the clock and for sleeping. Each sleep moves the
// time on and may write to the recorder, the way a program would meanwhile.
type fakeClock struct {
	t     time.Time
	steps int
	each  func(step int)
}

func (c *fakeClock) now() time.Time { return c.t }

func (c *fakeClock) sleep(d time.Duration) {
	c.steps++
	c.t = c.t.Add(d)
	if c.each != nil {
		c.each(c.steps)
	}
}

func TestQuietWaits(t *testing.T) {
	t.Parallel()
	const perWindow = int(quietWindow / quietStep)
	const perLimit = int(quietLimit / quietStep)
	cases := []struct {
		name string
		// writes says whether the program writes during a step.
		writes    func(step int) bool
		before    string
		wantQuiet bool
		wantSteps int
	}{
		{name: "an idle program", writes: func(int) bool { return false }, wantQuiet: true, wantSteps: perWindow},
		{
			name: "what was written before the call does not count", before: "earlier",
			writes: func(int) bool { return false }, wantQuiet: true, wantSteps: perWindow,
		},
		{
			name:   "the window starts at the last write",
			writes: func(step int) bool { return step <= 10 }, wantQuiet: true, wantSteps: 10 + perWindow,
		},
		{
			name:   "a late write starts the window again",
			writes: func(step int) bool { return step == perWindow-1 }, wantQuiet: true, wantSteps: 2*perWindow - 1,
		},
		{
			name:   "a gap just under the window is not quiet",
			writes: func(step int) bool { return step%(perWindow-1) == 0 }, wantQuiet: false, wantSteps: perLimit,
		},
		{
			name:   "a gap of the window is quiet",
			writes: func(step int) bool { return step%(perWindow+1) == 0 }, wantQuiet: true, wantSteps: perWindow,
		},
		{name: "a program that never stops", writes: func(int) bool { return true }, wantQuiet: false, wantSteps: perLimit},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var r Recorder
			if _, err := r.Write([]byte(tc.before)); err != nil {
				t.Fatal(err)
			}
			clock := &fakeClock{t: time.Unix(1_760_000_000, 0)}
			clock.each = func(step int) {
				if tc.writes(step) {
					if _, err := r.Write([]byte("x")); err != nil {
						t.Error(err)
					}
				}
			}
			got := r.quiet(quietWindow, quietLimit, clock.now, clock.sleep)
			if got != tc.wantQuiet || clock.steps != tc.wantSteps {
				t.Errorf("quiet = %v after %d steps, want %v after %d", got, clock.steps, tc.wantQuiet, tc.wantSteps)
			}
		})
	}
}

// stopped records what Quiet tells the test.
type stopped struct {
	helper int
	said   []string
}

func (s *stopped) Helper() { s.helper++ }

func (s *stopped) Fatalf(format string, args ...any) {
	s.said = append(s.said, fmt.Sprintf(format, args...))
}

// Quiet stops the test when the program never falls idle, and says nothing
// when it does.
func TestQuietStopsTheTest(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		writes bool
		want   []string
	}{
		{name: "a program that never stops", writes: true, want: []string{"the program was still writing after 5s"}},
		{name: "an idle program", writes: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var r Recorder
			clock := &fakeClock{t: time.Unix(1_760_000_000, 0)}
			clock.each = func(int) {
				if tc.writes {
					if _, err := r.Write([]byte("x")); err != nil {
						t.Error(err)
					}
				}
			}
			var told stopped
			r.settle(&told, clock.now, clock.sleep)
			if !slices.Equal(told.said, tc.want) {
				t.Errorf("Quiet said %q, want %q", told.said, tc.want)
			}
			if told.helper == 0 {
				t.Error("Quiet did not mark itself as a helper")
			}
		})
	}
}

// childEnv marks a run of this test binary that was started by one of the
// tests below, to do what a package's own test would do.
const childEnv = "TUITEST_CHILD"

// child runs this test binary again for one test, in dir, and returns what
// it printed and whether it passed.
func child(t *testing.T, dir, test string, flags ...string) (string, bool) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	args := append([]string{"-test.run=^" + test + "$"}, flags...)
	cmd := exec.CommandContext(t.Context(), self, args...)
	cmd.Dir = dir
	// A binary built with -race sleeps for a second before a clean exit, to
	// let reports through; this one has nothing still running by then.
	cmd.Env = append(os.Environ(), childEnv+"=1", "GORACE=atexit_sleep_ms=0")
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		t.Fatalf("run %s again: %v", test, err)
	}
	return string(out), err == nil
}

// Golden through the exported path, in a test of its own: a difference and a
// missing file each fail the test, and neither stops it.
func TestGoldenFailsTheTest(t *testing.T) {
	if os.Getenv(childEnv) != "" {
		Golden(t, "hello.golden", []byte("\x1b[1mhello\x1b[m\nworld\n"))
		Golden(t, "absent.golden", []byte("x"))
		Golden(t, "../escaped.golden", []byte("x"))
		return
	}
	t.Parallel()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	out, passed := child(t, dir, "TestGoldenFailsTheTest")
	if passed {
		t.Fatalf("a test whose goldens differ passed:\n%s", out)
	}
	for _, want := range []string{
		filepath.Join("testdata", "hello.golden") + " differs", "line 1 of 2 (want 2)",
		`want: "\x1b[1mhello\x1b[m \n"`, "read golden", "absent.golden", "-update",
		`golden name "../escaped.golden" does not stay inside testdata`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the failing test printed\n%s\nwant it to contain %q", out, want)
		}
	}
	if _, err := os.Stat(filepath.Join("testdata", "absent.golden")); err == nil {
		t.Error("a run without -update wrote a golden file")
	}
}

// The -update flag through Main and Golden: the files are written under
// testdata in the package's directory, and the test passes.
func TestUpdateWritesGoldens(t *testing.T) {
	if os.Getenv(childEnv) != "" {
		Golden(t, "new.golden", []byte("first\n"))
		Golden(t, "frame/100x30.golden", []byte("\x1b[1msecond\x1b[m \n"))
		return
	}
	t.Parallel()
	dir := t.TempDir()
	if out, passed := child(t, dir, "TestUpdateWritesGoldens", "-update"); !passed {
		t.Fatalf("with -update the test failed:\n%s", out)
	}
	for name, want := range map[string]string{
		"new.golden": "first\n", "frame/100x30.golden": "\x1b[1msecond\x1b[m \n",
	} {
		data, err := os.ReadFile(filepath.Join(dir, "testdata", filepath.FromSlash(name)))
		if err != nil || string(data) != want {
			t.Errorf("%s holds %q (%v), want %q", name, data, err, want)
		}
	}

	// What -update wrote is what the next run without it accepts.
	if out, passed := child(t, dir, "TestUpdateWritesGoldens", "-update=false"); !passed {
		t.Errorf("the goldens -update wrote were not accepted:\n%s", out)
	}
}

// Quiet on the real clock: it returns, and everything written by then is
// there to read.
func TestQuietReturns(t *testing.T) {
	t.Parallel()
	var r Recorder
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 20 {
			if _, err := r.Write([]byte("frame")); err != nil {
				t.Error(err)
			}
		}
	}()
	<-done
	r.Quiet(t)
	if got := r.Mark(); got != 100 {
		t.Errorf("Mark = %d, want 100", got)
	}
}
