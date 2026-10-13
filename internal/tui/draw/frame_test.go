package draw_test

import (
	"strings"
	"testing"
	"time"

	"github.com/quinnovator/contactd/internal/transcript"
	"github.com/quinnovator/contactd/internal/tui/tuitest"
)

// frameReply is the reply of the Frame mock.
const frameReply = "Two things overlapped:\n\n" +
	"- Turn 6 ran three model calls with tool runs in between.\n" +
	"- The turn after it re-read the tool results uncached.\n\n" +
	"The fix is small. Put the time note after the tool steps:\n\n" +
	"```go\nliveMsgs = append(liveMsgs, toolSteps...)\nliveMsgs = append(liveMsgs, timeNote(now))\n```\n"

// frameTranscript draws the transcript of the Frame mock the way the chat
// puts it together: a blank row above every block, and the running tool
// directly under its call.
func frameTranscript(l look, width int) []string {
	r := l.renderer(width)
	call := item(transcript.ToolCall, 4, `print(df[df.turn == 7][["input", "cached", "ms"]])`)
	call.Tool = "python"
	var out []string
	for i, it := range []*transcript.Item{
		item(transcript.User, 1, "why did Thursday spike?"),
		agent(2, frameReply),
		item(transcript.User, 3, "show me the numbers for turn 7"),
		call,
	} {
		if i > 0 {
			out = append(out, "")
		}
		out = append(out, r.Item(it)...)
	}
	return append(out, r.Live(transcript.Live{Typing: true, Running: "python"}, 2100*time.Millisecond)...)
}

func TestFrameMock(t *testing.T) {
	t.Parallel()
	got := frameTranscript(trueDark(), 100)
	fits(t, "frame", got, 100)

	// The rows of the mock-up, top to bottom. A code row is the code
	// background, 60 cells of it, two cells in.
	fill := func(s string) string { return "  " + s + strings.Repeat(" ", 60-len(s)) }
	equal(t, "the Frame transcript", stripped(got), []string{
		"❯ why did Thursday spike?",
		"",
		"  Two things overlapped:",
		"",
		"  • Turn 6 ran three model calls with tool runs in between.",
		"  • The turn after it re-read the tool results uncached.",
		"",
		"  The fix is small. Put the time note after the tool steps:",
		"",
		fill(" go"),
		fill(" liveMsgs = append(liveMsgs, toolSteps...)"),
		fill(" liveMsgs = append(liveMsgs, timeNote(now))"),
		"",
		"❯ show me the numbers for turn 7",
		"",
		"⏺ python",
		`    print(df[df.turn == 7][["input", "cached", "ms"]])`,
		"  ⎿ running… 2.1s",
	})

	// The colours of the mock-up: the accent, bold, for what was asked; the
	// tool tone for the call and the running row; dim for the code a tool
	// runs and for the language of a code block.
	for _, c := range find(t, got[0], "❯ why did Thursday spike?") {
		if c.fg != darkAccent || !c.bold {
			t.Fatalf("the question is drawn %+v, want the accent, bold", c)
		}
	}
	for _, c := range find(t, got[2], "Two things overlapped:") {
		if c != (cell{text: c.text}) {
			t.Fatalf("body text is styled: %+v", c)
		}
	}
	for _, c := range find(t, got[9], "go") {
		if c.fg != darkDim || c.bg != darkCodeFill {
			t.Fatalf("the language is drawn %+v, want dim on the code background", c)
		}
	}
	for _, c := range find(t, got[15], "⏺ python") {
		if c.fg != darkTool {
			t.Fatalf("the tool call is drawn %+v, want the tool tone", c)
		}
	}
	for _, c := range find(t, got[16], "print(df") {
		if c.fg != darkDim {
			t.Fatalf("the code of a tool call is drawn %+v, want dim", c)
		}
	}
	for _, c := range find(t, got[17], "⎿ running… 2.1s") {
		if c.fg != darkTool {
			t.Fatalf("the running row is drawn %+v, want the tool tone", c)
		}
	}
}

func TestFrameGoldens(t *testing.T) {
	t.Parallel()
	// The Frame mock's transcript on each of the four terminals, with every
	// escape sequence as it is written.
	for _, l := range looks() {
		got := frameTranscript(l, 100)
		fits(t, l.name, got, 100)
		tuitest.Golden(t, "frame/"+l.name+".golden", []byte(quoted(got)))
	}
}

// compatTranscript draws the transcript of a Compatibility panel.
func compatTranscript(l look, width int) []string {
	r := l.renderer(width)
	call := item(transcript.ToolCall, 2, "df.plot.bar()")
	call.Tool = "python"
	result := item(transcript.ToolResult, 3, "saved latency.png\n")
	result.Tool = "python"
	out := r.Item(item(transcript.User, 1, "plot last week's latency"))
	out = append(out, "")
	out = append(out, r.Item(call)...)
	out = append(out, r.Item(result)...) // no blank row between a call and its result
	out = append(out, "")
	return append(out, r.Item(agent(4, "p99 peaked on Thursday at 7.2 s."))...)
}

func TestCompatibilityMock(t *testing.T) {
	t.Parallel()
	unicode := []string{
		"❯ plot last week's latency",
		"",
		"⏺ python",
		"    df.plot.bar()",
		"  ⎿ saved latency.png",
		"",
		"  p99 peaked on Thursday at 7.2 s.",
	}
	cases := []struct {
		name  string
		look  look
		width int
		want  []string
	}{
		{"truecolor at 46 columns", trueDark(), 46, unicode},
		{"16 colours on light at 46 columns", light16(), 46, unicode},
		{"256 colours at 46 columns", unknown256(), 46, unicode},
		{"no colour, ASCII, at 46 columns", noneASCII(), 46, []string{
			"> plot last week's latency",
			"",
			"* python",
			"    df.plot.bar()",
			"  |_ saved latency.png",
			"",
			"  p99 peaked on Thursday at 7.2 s.",
		}},
		// Under 40 columns a tool call shows no code until ctrl+o.
		{"a phone at 30 columns", trueDark(), 30, []string{
			"❯ plot last week's latency",
			"",
			"⏺ python",
			"  ⎿ saved latency.png",
			"",
			"  p99 peaked on Thursday at",
			"  7.2 s.",
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := compatTranscript(c.look, c.width)
			fits(t, c.name, got, c.width)
			equal(t, c.name, stripped(got), c.want)
		})
	}
}
