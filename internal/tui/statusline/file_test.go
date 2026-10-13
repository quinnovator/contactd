package statusline_test

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/quinnovator/contactd/internal/tui/statusline"
	"github.com/quinnovator/contactd/internal/tui/toml"
	"github.com/quinnovator/contactd/internal/tui/tuitest"
)

// specFile is the file section 4 of the spec shows.
const specFile = `[[segment]]
name = "cache"
from = "core"          # core | mod:<mod> | shell
on = true
side = "left"          # left | right
format = "cache $pct%"
short = "$pct%"
warn_below = 50
drop = 4               # 1 is dropped first when narrow; 0 is never dropped

[[segment]]
name = "branch"
from = "mod:git"
side = "right"

[[segment]]
name = "load"
from = "shell"
on = false
side = "right"
command = "uptime"
match = 'load averages?: ([0-9.]+)'   # optional RE2; $1..$9 in format
format = "load $1"                     # $out is the first line of output
interval = "10s"                       # at least 1s
timeout = "1s"                         # at most the interval
`

// unlisted returns the core segments a file did not list, as Parse adds
// them: at the end, in their usual order, switched off.
func unlisted(t *testing.T, listed ...string) []statusline.Segment {
	t.Helper()
	var out []statusline.Segment
	for _, s := range preset(t, "ops").Segments {
		if s.From != statusline.FromCore || slices.Contains(listed, s.Name) {
			continue
		}
		s.On = false
		out = append(out, s)
	}
	return out
}

func TestParse(t *testing.T) {
	t.Parallel()
	const l, r = statusline.Left, statusline.Right
	tests := []struct {
		name     string
		src      string
		want     []statusline.Segment // the segments the file lists
		listed   []string             // the core segments among them
		warnings []string
	}{
		{
			name: "the spec's file",
			src:  specFile,
			want: []statusline.Segment{
				{
					Name: "cache", From: "core", On: true, Side: l,
					Format: "cache $pct%", Short: "$pct%", WarnBelow: 50, HasWarn: true, Drop: 4,
				},
				{Name: "branch", From: "mod:git", On: true, Side: r, Drop: 1},
				{
					Name: "load", From: "shell", Side: r, Drop: 1, Format: "load $1",
					Command: "uptime", Match: `load averages?: ([0-9.]+)`,
					Interval: 10 * time.Second, Timeout: time.Second,
				},
			},
			listed: []string{"cache"},
		},
		{
			name: "an empty file lists nothing, and every core segment is offered",
			src:  "",
		},
		{
			name: "a comment alone",
			src:  "# nothing here\n",
		},
		{
			name: "absent keys take the segment's defaults",
			src: `[[segment]]
name = "tokens"

[[segment]]
name = "clock"
from = "core"

[[segment]]
name = "cache"

[[segment]]
name = "stats"
from = "mod:cache"

[[segment]]
name = "host"
from = "shell"
command = "hostname"
`,
			want: []statusline.Segment{
				{Name: "tokens", From: "core", On: true, Side: l, Drop: 1},
				{Name: "clock", From: "core", On: true, Side: r, Drop: 7},
				// No warn_below in the file means no warning.
				{Name: "cache", From: "core", On: true, Side: l, Drop: 4},
				{Name: "stats", From: "mod:cache", On: true, Side: r, Drop: 1},
				{
					Name: "host", From: "shell", On: true, Side: r, Drop: 1, Command: "hostname",
					Interval: 10 * time.Second, Timeout: time.Second,
				},
			},
			listed: []string{"tokens", "clock", "cache"},
		},
		{
			name: "every key, with values that are not the defaults",
			src: `[[segment]]
name = "clock"
from = "core"
on = false
side = "left"
format = "at $time"
short = ""
warn_below = -3
drop = 0

[[segment]]
name = "up"
from = "shell"
side = "left"
drop = 12
command = "uptime | cut -c1-20"
match = "up +(\\d+)"
interval = "1m30s"
timeout = "1m30s"
`,
			want: []statusline.Segment{
				{Name: "clock", From: "core", Side: l, Format: "at $time", WarnBelow: -3, HasWarn: true},
				{
					Name: "up", From: "shell", On: true, Side: l, Drop: 12,
					Command: "uptime | cut -c1-20", Match: `up +(\d+)`,
					Interval: 90 * time.Second, Timeout: 90 * time.Second,
				},
			},
			listed: []string{"clock"},
		},
		{
			name: "an empty from is core, and is kept as the word",
			src:  "[[segment]]\nname = \"contact\"\nfrom = \"\"\n",
			want: []statusline.Segment{
				{Name: "contact", From: "core", On: true, Side: l, Drop: 5},
			},
			listed: []string{"contact"},
		},
		{
			name: "the timeout may come before the interval",
			src:  "[[segment]]\nname = \"x\"\nfrom = \"shell\"\ncommand = \"true\"\ntimeout = \"30s\"\ninterval = \"1m\"\n",
			want: []statusline.Segment{{
				Name: "x", From: "shell", On: true, Side: r, Drop: 1, Command: "true",
				Interval: time.Minute, Timeout: 30 * time.Second,
			}},
		},
		{
			name: "unknown keys and tables are named and skipped",
			src: `theme = "dark"
turn = 8

[[segment]]
name = "state"
colour = "green"

[panel]
side = "right"

[[dock]]
name = "a"

[[dock]]
name = "b"

[[segment]]
name = "clock"
refresh = "1m"
`,
			want: []statusline.Segment{
				{Name: "state", From: "core", On: true, Side: l},
				{Name: "clock", From: "core", On: true, Side: r, Drop: 7},
			},
			listed: []string{"state", "clock"},
			warnings: []string{
				`line 1: unknown key "theme"`,
				`line 2: unknown key "turn"`,
				`line 6: unknown key "colour"`,
				`line 8: unknown table [panel]`,
				`line 11: unknown table [[dock]]`,
				`line 19: unknown key "refresh"`,
			},
		},
		{
			name: "a core segment this client does not have is left out",
			src:  "[[segment]]\nname = \"turn\"\n\n[[segment]]\nname = \"clock\"\n",
			want: []statusline.Segment{
				{Name: "clock", From: "core", On: true, Side: r, Drop: 7},
			},
			listed:   []string{"clock"},
			warnings: []string{`line 1: no core segment is named "turn"; left out`},
		},
		{
			name: "a segment listed twice counts once",
			src: `[[segment]]
name = "clock"
side = "left"

[[segment]]
name = "clock"
from = "core"

[[segment]]
name = "branch"
from = "mod:git"

[[segment]]
name = "branch"
from = "mod:git"
on = false

[[segment]]
name = "branch"
from = "mod:hg"
`,
			want: []statusline.Segment{
				{Name: "clock", From: "core", On: true, Side: l, Drop: 7},
				{Name: "branch", From: "mod:git", On: true, Side: r, Drop: 1},
				{Name: "branch", From: "mod:hg", On: true, Side: r, Drop: 1},
			},
			listed: []string{"clock"},
			warnings: []string{
				`line 5: the segment "core:clock" is listed twice; this one is left out`,
				`line 13: the segment "mod:git/branch" is listed twice; this one is left out`,
			},
		},
		{
			name: "a shell key on another kind of segment is ignored",
			src:  "[[segment]]\nname = \"clock\"\ncommand = \"date\"\ninterval = \"never\"\n",
			want: []statusline.Segment{
				{Name: "clock", From: "core", On: true, Side: r, Drop: 7},
			},
			listed: []string{"clock"},
			warnings: []string{
				"line 3: command is for shell segments; ignored",
				"line 4: interval is for shell segments; ignored",
			},
		},
		{
			name: "a shell segment with no command is kept, with a note",
			src:  "[[segment]]\nname = \"x\"\nfrom = \"shell\"\n",
			want: []statusline.Segment{{
				Name: "x", From: "shell", On: true, Side: r, Drop: 1,
				Interval: 10 * time.Second, Timeout: time.Second,
			}},
			warnings: []string{`line 1: the shell segment "x" has no command; it shows nothing`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, warnings, err := statusline.Parse([]byte(tt.src))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			want := slices.Concat(tt.want, unlisted(t, tt.listed...))
			if !reflect.DeepEqual(got.Segments, want) {
				t.Errorf("the segments are\n%+v\nwant\n%+v", got.Segments, want)
			}
			if !slices.Equal(warnings, tt.warnings) {
				t.Errorf("the warnings are %q, want %q", warnings, tt.warnings)
			}
			for _, s := range got.Segments {
				if err := s.Validate(); err != nil {
					t.Errorf("Parse accepted what Validate refuses: %s: %v", s.ID(), err)
				}
			}
		})
	}
}

// A file with more mod and shell segments than a bar could hold keeps the
// first of them, and all its core segments.
func TestParseManySegments(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	for i := range statusline.MaxSegments + 3 {
		fmt.Fprintf(&b, "[[segment]]\nname = \"s%d\"\nfrom = \"shell\"\ncommand = \"true\"\n", i)
	}
	b.WriteString("[[segment]]\nname = \"clock\"\n")
	got, warnings, err := statusline.Parse([]byte(b.String()))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if n := len(got.Segments); n != statusline.MaxSegments+11 {
		t.Errorf("%d segments, want %d", n, statusline.MaxSegments+11)
	}
	if s := got.Segments[statusline.MaxSegments]; s.ID() != "core:clock" || !s.On {
		t.Errorf("after the shell segments comes %+v, want the clock the file lists", s)
	}
	line := 4*statusline.MaxSegments + 1
	want := []string{fmt.Sprintf("line %d: more than 32 mod and shell segments; the rest are left out", line)}
	if !slices.Equal(warnings, want) {
		t.Errorf("the warnings are %q, want %q", warnings, want)
	}
	back, _, err := statusline.Parse(got.Encode())
	if err != nil || !reflect.DeepEqual(back, got) {
		t.Errorf("the full config does not read back: %v", err)
	}
}

func TestParseErrors(t *testing.T) {
	t.Parallel()
	shell := func(lines ...string) string {
		return "[[segment]]\nname = \"x\"\nfrom = \"shell\"\n" + strings.Join(lines, "\n") + "\n"
	}
	core := func(lines ...string) string {
		return "[[segment]]\nname = \"cache\"\n" + strings.Join(lines, "\n") + "\n"
	}
	tests := []struct {
		name string
		src  string
		line int
		want string // a part of the message
	}{
		{"not TOML", "[[segment]]\nname = cache\n", 2, ""},
		{"a construct the parser refuses", "[[segment]]\nname = \"cache\"\ndrop = 1.5\n", 3, ""},
		{"one table where a list is meant", "\n[segment]\nname = \"cache\"\n", 2, "written [[segment]]"},
		{"no name", "# a comment\n[[segment]]\nfrom = \"core\"\n", 2, "a segment needs a name"},
		{"an empty name", core()[:12] + "name = \"\"\n", 2, "a segment needs a name"},
		{"a name that is not a string", "[[segment]]\nname = 3\n", 2, "name"},
		{"a name with a space", "[[segment]]\nfrom = \"shell\"\nname = \"my load\"\n", 3, "space"},
		{"a name too long", "[[segment]]\nname = \"" + strings.Repeat("n", 65) + "\"\n", 2, "longer than 64 bytes"},
		{"an unknown from", "[[segment]]\nname = \"x\"\nfrom = \"panel\"\n", 3, `from is "panel"; it can be core, mod:<name> or shell`},
		{"a mod with no name", "[[segment]]\nname = \"x\"\nfrom = \"mod:\"\n", 3, "it can be core"},
		{"a mod with a slash", "[[segment]]\nname = \"x\"\nfrom = \"mod:a/b\"\n", 3, "it can be core"},
		{"a from that is not a string", "[[segment]]\nname = \"x\"\nfrom = true\n", 3, "from"},
		{"on is not a boolean", core(`on = "yes"`), 3, "on"},
		{"a side that is neither", core(`side = "middle"`), 3, `side is "middle"; it can be left or right`},
		{"a side that is not a string", core(`side = 1`), 3, "side"},
		{"a format that is not a string", core(`format = 5`), 3, "format"},
		{"a format too long", core(`format = "` + strings.Repeat("x", 257) + `"`), 3, "format is longer than 256 bytes"},
		{"a short too long", core(`on = true`, `short = "`+strings.Repeat("x", 257)+`"`), 4, "short is longer than 256 bytes"},
		{"warn_below is not a number", core(`warn_below = "50"`), 3, "warn_below"},
		{"warn_below out of range", core(`warn_below = 9000000000`), 3, "warn_below is 9000000000"},
		{"a negative drop", core(`drop = -1`), 3, "drop is -1; it can be 0 (never) or more"},
		{"a drop out of range", core(`drop = 9000000000`), 3, "drop is 9000000000"},
		{"a drop that is not a number", core(`drop = "first"`), 3, "drop"},
		{"a command that is not a string", shell(`command = ["uptime"]`), 4, "command"},
		{"a command too long", shell(`command = "` + strings.Repeat("x", 2049) + `"`), 4, "command is longer than 2048 bytes"},
		{"a match that is not an expression", shell(`command = "uptime"`, `match = '(['`), 5, "match is not an expression"},
		{"a match too long", shell(`match = "` + strings.Repeat("x", 1025) + `"`), 4, "match is longer than 1024 bytes"},
		{"an interval that is not a time", shell(`interval = "soon"`), 4, `interval is "soon"; write a time such as "10s"`},
		{"an interval with no unit", shell(`interval = "10"`), 4, `interval is "10"`},
		{"an interval that is a number", shell(`interval = 10`), 4, "interval"},
		{"an interval under a second", shell(`interval = "500ms"`), 4, "interval is 500ms; the least is 1s"},
		{"a negative interval", shell(`interval = "-10s"`), 4, "the least is 1s"},
		{"a timeout that is not a time", shell(`timeout = "1 second"`), 4, `timeout is "1 second"`},
		{"a timeout of nothing", shell(`timeout = "0s"`), 4, "timeout is 0s; it must be more than 0"},
		{"a timeout over the interval", shell(`interval = "5s"`, `timeout = "6s"`), 5, "timeout is 6s; the most is the interval, 5s"},
		{"a timeout over the interval that follows it", shell(`timeout = "6s"`, `interval = "5s"`), 4, "the most is the interval, 5s"},
		{"a timeout over the default interval", shell(`timeout = "11s"`), 4, "the most is the interval, 10s"},
		{"the first mistake is the one reported", core(`side = "up"`, `drop = -1`), 3, "side"},
		{
			"a mistake in a later segment",
			"[[segment]]\nname = \"clock\"\n\n[[segment]]\nname = \"cache\"\nside = \"top\"\n", 6, `side is "top"`,
		},
		{"too large a file", strings.Repeat("# padding\n", 110_000), 0, ""},
	}
	def := preset(t, "default")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, warnings, err := statusline.Parse([]byte(tt.src))
			if err == nil {
				t.Fatalf("accepted: %+v", got.Segments)
			}
			var refusal *toml.Error
			if !errors.As(err, &refusal) {
				t.Fatalf("the error is a %T, want it to wrap a *toml.Error: %v", err, err)
			}
			if refusal.Line != tt.line {
				t.Errorf("the error blames line %d, want %d: %v", refusal.Line, tt.line, err)
			}
			if !strings.Contains(refusal.Msg, tt.want) {
				t.Errorf("the message is %q, want it to hold %q", refusal.Msg, tt.want)
			}
			if tt.line > 0 && !strings.HasPrefix(err.Error(), fmt.Sprintf("status line: line %d: ", tt.line)) {
				t.Errorf("the error reads %q", err)
			}
			if strings.ContainsAny(err.Error(), "\x1b\n") || len(err.Error()) > 400 {
				t.Errorf("the error is not one short plain line: %q", err)
			}
			if !reflect.DeepEqual(got, def) {
				t.Errorf("with an error the config is %+v, want the default preset", got.Segments)
			}
			if warnings != nil {
				t.Errorf("with an error there are warnings: %q", warnings)
			}
		})
	}
}

// What a file can put into an error or a warning is quoted, so that neither
// can draw an escape sequence.
func TestParseNotesAreSafe(t *testing.T) {
	t.Parallel()
	srcs := []string{
		"[[segment]]\nname = \"a\\u001b[2Jb\"\n",
		"[[segment]]\nname = \"x\"\nfrom = \"\\u001b]0;t\\u0007\"\n",
		"[[segment]]\nname = \"x\"\nside = \"\\u001b[31m\"\n",
		"[[segment]]\nname = \"x\"\nfrom = \"shell\"\nmatch = \"(\\u001b[31m\"\n",
		"[[segment]]\nname = \"x\"\nfrom = \"shell\"\ninterval = \"\\u001b[31m\"\n",
		"[[segment]]\nname = \"x\\u009b\"\nfrom = \"shell\"\n",
		"[[segment]]\nname = \"x\\u2028y\"\nfrom = \"shell\"\n[[segment]]\nname = \"x\\u2028y\"\nfrom = \"shell\"\n",
	}
	for _, src := range srcs {
		_, warnings, err := statusline.Parse([]byte(src))
		notes := slices.Clone(warnings)
		if err != nil {
			notes = append(notes, err.Error())
		}
		if len(notes) == 0 {
			t.Errorf("%q gave no note", src)
		}
		for _, note := range notes {
			for _, r := range note {
				if r < 0x20 || (r >= 0x7f && r <= 0x9f) || r == 0x2028 {
					t.Errorf("a note for %q holds %U: %q", src, r, note)
				}
			}
		}
	}
}

func TestEncode(t *testing.T) {
	t.Parallel()
	tuitest.Golden(t, "default.toml.golden", preset(t, "default").Encode())
	tuitest.Golden(t, "drawing.toml.golden", drawing(t).Encode())

	got := preset(t, "default").Encode()
	first, _, _ := bytes.Cut(got, []byte("\n"))
	if !bytes.HasPrefix(first, []byte("# ")) || !bytes.Contains(first, []byte("/statusline edit")) {
		t.Errorf("the first line is %q, want a comment that names the editor", first)
	}
	if !bytes.Equal(got, preset(t, "default").Encode()) {
		t.Error("the same config encoded twice gives different bytes")
	}
	// The file is TOML to the client's own parser, with one table a segment.
	doc, err := toml.Parse(got)
	if err != nil {
		t.Fatalf("the encoded file does not parse: %v", err)
	}
	if n := len(doc.Named("segment")); n != 12 {
		t.Errorf("%d [[segment]] tables, want 12", n)
	}
	if got := (statusline.Config{}).Encode(); !bytes.HasPrefix(got, []byte("# ")) {
		t.Errorf("an empty config encodes to %q", got)
	}
}

func TestRoundTrip(t *testing.T) {
	t.Parallel()
	parsed := func(src string) statusline.Config {
		c, _, err := statusline.Parse([]byte(src))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		return c
	}
	edited := drawing(t)
	edited.Segments = append(edited.Segments, statusline.Segment{
		Name: "héllo-wörld", From: statusline.FromShell, On: true, Side: statusline.Left, Drop: 3,
		Format: `"$1" \ '$out'`, Short: "$1", WarnBelow: 2, HasWarn: true,
		Command: "printf 'a\\tb\\n'; echo \"it's\"\n", Match: `^(\S+)\s+"?(\w*)`,
		Interval: 36 * time.Hour, Timeout: 1500 * time.Millisecond,
	})
	tests := []struct {
		name string
		c    statusline.Config
	}{
		{"minimal", preset(t, "minimal")},
		{"default", preset(t, "default")},
		{"ops", preset(t, "ops")},
		{"the drawing", drawing(t)},
		{"the spec's file", parsed(specFile)},
		{"an empty file", parsed("")},
		{"a shell segment with awkward strings", edited},
		{"no warning and no formats", parsed("[[segment]]\nname = \"cache\"\n[[segment]]\nname = \"x\"\nfrom = \"shell\"\n")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			encoded := tt.c.Encode()
			got, _, err := statusline.Parse(encoded)
			if err != nil {
				t.Fatalf("the encoded config does not parse: %v\n%s", err, encoded)
			}
			// Parse adds the core segments a config does not list.
			want := tt.c
			if !reflect.DeepEqual(got, want) {
				t.Errorf("read back as\n%+v\nwant\n%+v", got.Segments, want.Segments)
			}
			if again := got.Encode(); !bytes.Equal(again, encoded) {
				t.Errorf("encoded again it differs:\n%s\nwas\n%s", again, encoded)
			}
		})
	}

	// A preset read back gives no warnings: it holds nothing unknown.
	for _, name := range statusline.Presets() {
		if _, warnings, _ := statusline.Parse(preset(t, name).Encode()); warnings != nil {
			t.Errorf("the preset %s reads back with warnings: %q", name, warnings)
		}
	}
}

// A config built by hand may hold what a file cannot. Encode still writes a
// file that parses, with a From of "" as core and no negative drop.
func TestEncodeHandBuilt(t *testing.T) {
	t.Parallel()
	c := statusline.Config{Segments: []statusline.Segment{
		{Name: "clock", On: true, Drop: -4},
		{Name: "x", From: statusline.FromShell, On: true, Command: "true", Interval: time.Minute, Timeout: time.Second},
	}}
	got, warnings, err := statusline.Parse(c.Encode())
	if err != nil || warnings != nil {
		t.Fatalf("Parse: %v, %q", err, warnings)
	}
	want := []statusline.Segment{
		{Name: "clock", From: "core", On: true, Side: statusline.Right},
		{
			Name: "x", From: "shell", On: true, Side: statusline.Right, Command: "true",
			Interval: time.Minute, Timeout: time.Second,
		},
	}
	if !reflect.DeepEqual(got.Segments[:2], want) {
		t.Errorf("read back as %+v, want %+v", got.Segments[:2], want)
	}
}

func TestLoad(t *testing.T) {
	t.Parallel()
	def := preset(t, "default")
	write := func(t *testing.T, data string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "statusline.toml")
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}

	t.Run("a missing file is the default preset", func(t *testing.T) {
		t.Parallel()
		got, warnings, err := statusline.Load(filepath.Join(t.TempDir(), "statusline.toml"))
		if err != nil || warnings != nil || !reflect.DeepEqual(got, def) {
			t.Errorf("got %+v, %q, %v; want the default preset", got.Segments, warnings, err)
		}
	})

	t.Run("a missing directory too", func(t *testing.T) {
		t.Parallel()
		got, warnings, err := statusline.Load(filepath.Join(t.TempDir(), "no", "such", "statusline.toml"))
		if err != nil || warnings != nil || !reflect.DeepEqual(got, def) {
			t.Errorf("got %+v, %q, %v; want the default preset", got.Segments, warnings, err)
		}
	})

	t.Run("no path is the default preset", func(t *testing.T) {
		t.Parallel()
		got, warnings, err := statusline.Load("")
		if err != nil || warnings != nil || !reflect.DeepEqual(got, def) {
			t.Errorf("got %+v, %q, %v; want the default preset", got.Segments, warnings, err)
		}
	})

	t.Run("a file is read", func(t *testing.T) {
		t.Parallel()
		got, warnings, err := statusline.Load(write(t, specFile+"\n[extra]\n"))
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		want, _, _ := statusline.Parse([]byte(specFile))
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v, want %+v", got.Segments, want.Segments)
		}
		if want := []string{"line 27: unknown table [extra]"}; !slices.Equal(warnings, want) {
			t.Errorf("the warnings are %q, want %q", warnings, want)
		}
	})

	t.Run("what the editor saved is read back", func(t *testing.T) {
		t.Parallel()
		saved := drawing(t)
		got, warnings, err := statusline.Load(write(t, string(saved.Encode())))
		if err != nil || warnings != nil || !reflect.DeepEqual(got, saved) {
			t.Errorf("got %+v, %q, %v; want what was saved", got.Segments, warnings, err)
		}
	})

	t.Run("a mistake names the file and the line, and leaves the default", func(t *testing.T) {
		t.Parallel()
		path := write(t, "[[segment]]\nname = \"cache\"\nside = \"middle\"\n")
		got, warnings, err := statusline.Load(path)
		if err == nil {
			t.Fatal("no error")
		}
		if want := path + `: line 3: side is "middle"; it can be left or right`; err.Error() != want {
			t.Errorf("the error is %q, want %q", err, want)
		}
		var refusal *toml.Error
		if !errors.As(err, &refusal) || refusal.Line != 3 {
			t.Errorf("the error does not unwrap to line 3: %v", err)
		}
		if warnings != nil || !reflect.DeepEqual(got, def) {
			t.Errorf("got %+v, %q; want the default preset", got.Segments, warnings)
		}
	})

	t.Run("a file that cannot be read is an error, with the default", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		got, warnings, err := statusline.Load(dir)
		if err == nil {
			t.Fatal("reading a directory gave no error")
		}
		if !strings.Contains(err.Error(), dir) {
			t.Errorf("the error does not name the path: %v", err)
		}
		if warnings != nil || !reflect.DeepEqual(got, def) {
			t.Errorf("got %+v, %q; want the default preset", got.Segments, warnings)
		}
	})

	t.Run("a huge file is refused", func(t *testing.T) {
		t.Parallel()
		got, _, err := statusline.Load(write(t, strings.Repeat("# padding\n", 110_000)))
		if err == nil || !reflect.DeepEqual(got, def) {
			t.Errorf("got %d segments and %v; want an error and the default preset", len(got.Segments), err)
		}
	})
}

func FuzzParse(f *testing.F) {
	seeds := []string{
		specFile, "", "[[segment]]\n", "[segment]\n", "x = 1\n",
		"[[segment]]\nname = \"x\"\nfrom = \"shell\"\ncommand = \"a\\u001bb\"\nmatch = \"(\"\n",
		"[[segment]]\nname = \"x\"\nfrom = \"shell\"\ninterval = \"1h\"\ntimeout = \"59m\"\nformat = \"$1 ${out}\"\n",
		"[[segment]]\nname = \"a\\u00e9\"\nfrom = \"mod:m\"\nwarn_below = -1\ndrop = 0\nside = \"left\"\non = false\n",
		"[[segment]]\nname = \"clock\"\n[[segment]]\nname = \"clock\"\n[[x]]\n[y]\n",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	for _, name := range statusline.Presets() {
		c, _ := statusline.Preset(name)
		f.Add(c.Encode())
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		c, warnings, err := statusline.Parse(data)
		if err != nil {
			if def, _ := statusline.Preset("default"); !reflect.DeepEqual(c, def) || warnings != nil {
				t.Fatalf("with an error the result is not the default preset")
			}
			return
		}
		for _, s := range c.Segments {
			if err := s.Validate(); err != nil {
				t.Fatalf("Parse accepted what Validate refuses: %q: %v", s.ID(), err)
			}
		}
		for _, w := range warnings {
			if strings.ContainsAny(w, "\x1b\n") || !strings.HasPrefix(w, "line ") {
				t.Fatalf("a warning is not one plain line with its line: %q", w)
			}
		}
		// What Parse gives, Encode writes and Parse reads back the same.
		encoded := c.Encode()
		back, _, err := statusline.Parse(encoded)
		if err != nil {
			t.Fatalf("the encoded config does not parse: %v\n%s", err, encoded)
		}
		if !reflect.DeepEqual(back, c) {
			t.Fatalf("read back as\n%+v\nwant\n%+v", back.Segments, c.Segments)
		}
		// And whatever it holds, the bar is never wider than its width.
		vals := statusline.CoreValues(toolRunning(), unicodeMarks())
		for _, s := range c.Segments {
			if s.From != statusline.FromCore {
				vals[s.ID()] = statusline.Value{Vars: map[string]string{"out": "out", "text": "text", "short": "s", "1": "one"}}
			}
		}
		for _, width := range []int{1, 20, 39, 40, 59, 60, 100} {
			left, right := statusline.Layout(c, vals, width, client())
			if got := need(left, right, width, client()); got > width {
				t.Fatalf("at %d columns the row needs %d", width, got)
			}
		}
	})
}
