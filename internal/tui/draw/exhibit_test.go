package draw_test

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/quinnovator/contactd/internal/transcript"
	"github.com/quinnovator/contactd/internal/tui/draw"
	"github.com/quinnovator/contactd/internal/tui/theme"
)

// What stands on a stage by itself is drawn as the reply it is part of is:
// the same code block, the same table, the same picture.
func TestAnExhibitIsDrawnAsItsReplyIs(t *testing.T) {
	t.Parallel()
	for _, l := range []look{trueDark(), light16(), unknown256(), noneASCII()} {
		t.Run(l.name, func(t *testing.T) {
			t.Parallel()
			images := &fakeImages{}
			rd := draw.New(draw.Options{Theme: theme.New(l.caps, theme.Options{}), Width: 64, Images: images})
			code := "x := 1\nfmt.Println(x) // a comment"
			reply := "Before.\n\n```go\n" + code + "\n```\n\n| day | ms |\n|-----|----|\n| Thu | 91 |\n\n![latency by day](latency.png)\n"
			whole := strings.Join(rd.Item(&transcript.Item{Seq: 31, Kind: transcript.Agent, Text: reply}), "\n")

			for what, lines := range map[string][]string{
				"the code":    rd.Code(31, 1, "go", code),
				"the table":   rd.Markdown("| day | ms |\n|-----|----|\n| Thu | 91 |"),
				"the picture": rd.Image(draw.ImageRef{Seq: 31, N: 1, Path: "latency.png", Alt: "latency by day"}),
			} {
				if len(lines) == 0 {
					t.Fatalf("%s draws nothing", what)
				}
				if !strings.Contains(whole, strings.Join(lines, "\n")) {
					t.Errorf("%s is drawn as\n%s\nwhich the reply does not hold:\n%s", what, strings.Join(lines, "\n"), whole)
				}
				for _, line := range lines {
					if w := max(ansi.StringWidth(line), ansi.StringWidthWc(line)); w > 64 {
						t.Errorf("%s has a line of %d cells: %q", what, w, ansi.Strip(line))
					}
				}
			}
			if len(images.asked) < 2 || images.asked[0] != images.asked[len(images.asked)-1] {
				t.Errorf("the picture was asked for as %+v", images.asked)
			}
		})
	}
}

// fakeImages answers every picture with a line, and writes down what it was
// asked.
type fakeImages struct{ asked []draw.ImageRef }

func (f *fakeImages) Image(ref draw.ImageRef, _, _ int) draw.Shown {
	f.asked = append(f.asked, ref)
	return draw.Shown{Line: "[image: " + ref.Path + ", 640×360]", Hint: "/image save 31"}
}

func TestExhibitsMakeTheirTextSafe(t *testing.T) {
	t.Parallel()
	rd := draw.New(draw.Options{Theme: theme.New(trueDark().caps, theme.Options{}), Width: 64})
	hostile := "a\x1b]0;owned\x07b \x1b[2J c\u202ed"
	for what, lines := range map[string][]string{
		"code":  rd.Code(0, 1, "go\x1b[31m", hostile),
		"table": rd.Markdown("| " + hostile + " |\n|---|\n| x |"),
	} {
		got := strings.Join(lines, "\n")
		if strings.ContainsAny(got, "\x07\u202e") || strings.Contains(got, "\x1b]") || strings.Contains(got, "\x1b[2J") {
			t.Errorf("%s lets a sequence through: %q", what, got)
		}
	}
	if got := rd.Markdown("  \n\n"); len(got) != 0 {
		t.Errorf("nothing draws %q", got)
	}
}

func TestClip(t *testing.T) {
	t.Parallel()
	rd := draw.New(draw.Options{Theme: theme.New(trueDark().caps, theme.Options{}), Width: 64})
	lines := []string{"one", "two", "three", "four", "five"}
	for _, tc := range []struct {
		rows int
		want []string
	}{
		{9, lines},
		{5, lines},
		{4, []string{"one", "two", "three", "  … 2 more rows (ctrl+o)"}},
		{2, []string{"one", "  … 4 more rows (ctrl+o)"}},
		{1, []string{"one"}},
		{0, []string{}},
		{-3, []string{}},
	} {
		got := stripped(rd.Clip(lines, tc.rows))
		if strings.Join(got, "\n") != strings.Join(tc.want, "\n") || len(got) != len(tc.want) {
			t.Errorf("Clip to %d rows: %q, want %q", tc.rows, got, tc.want)
		}
	}
	if lines[3] != "four" {
		t.Error("Clip changed the lines it was given")
	}
}

func TestDoingAndStopped(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		look  look
		width int
		live  transcript.Live
		ran   time.Duration
		want  string
	}{
		{"writing", trueDark(), 80, transcript.Live{Typing: true, Text: "words"}, 0, ""},
		{"nothing", trueDark(), 80, transcript.Live{}, 0, ""},
		{"a call being written", trueDark(), 80, transcript.Live{Typing: true, Starting: "python"}, 0, "⏺ python …"},
		{"a tool running", trueDark(), 80, transcript.Live{Typing: true, Running: "python"}, 2150 * time.Millisecond, "⏺ python  running… 2.1s"},
		{"running wins over starting", trueDark(), 80, transcript.Live{Running: "js", Starting: "python"}, 61 * time.Second, "⏺ js  running… 1m01s"},
		{"ASCII", noneASCII(), 80, transcript.Live{Running: "python"}, time.Second, "* python  running... 1.0s"},
		{"a name that is not a name", trueDark(), 80, transcript.Live{Running: "py\x1b[2Jthon\nx"}, 0, "⏺ py[2Jthon x  running… 0.0s"},
		{"no room", trueDark(), 12, transcript.Live{Running: "python"}, time.Second, "⏺ python  r…"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rd := draw.New(draw.Options{Theme: theme.New(tc.look.caps, theme.Options{}), Width: tc.width})
			got := rd.Doing(tc.live, tc.ran)
			if plain := ansi.Strip(got); plain != tc.want {
				t.Errorf("Doing = %q, want %q", plain, tc.want)
			}
			if strings.Contains(got, "\x1b[2J") {
				t.Errorf("Doing lets a sequence through: %q", got)
			}
		})
	}
	rd := draw.New(draw.Options{Theme: theme.New(trueDark().caps, theme.Options{}), Width: 80})
	if got := ansi.Strip(rd.Stopped()); got != "⏹ interrupted" {
		t.Errorf("Stopped = %q", got)
	}
}
