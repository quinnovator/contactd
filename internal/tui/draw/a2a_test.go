package draw_test

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/quinnovator/contactd/internal/transcript"
	"github.com/quinnovator/contactd/internal/tui/draw"
	"github.com/quinnovator/contactd/internal/tui/helperboard"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
	"github.com/quinnovator/contactd/internal/tui/theme"
)

func TestA2ADrawing(t *testing.T) {
	t.Parallel()
	for _, glyphs := range []termcaps.Glyphs{termcaps.GlyphsUnicode, termcaps.GlyphsASCII} {
		t.Run(glyphs.String(), func(t *testing.T) {
			t.Parallel()
			th := theme.New(termcaps.Caps{Glyphs: glyphs}, theme.Options{})
			r := draw.New(draw.Options{Theme: th, Width: 100, Contact: "Ada"})
			cases := []struct {
				item transcript.Item
				want string
			}{
				{transcript.Item{Kind: transcript.ToolCall, Tool: "start_helper", Text: `{"title":"Watch","brief":"Read the page","every":"1m","duration":"1h"}`}, "A2A SendMessage"},
				{transcript.Item{Kind: transcript.Notice, Source: "helper-update", Helper: 1, Text: "Helper 1 reports:\nChanged"}, "report"},
				{transcript.Item{Kind: transcript.Notice, Source: "helper-update", Helper: 1, Text: "Helper 1 needs an answer:\nWhich page?"}, "question"},
				{transcript.Item{Kind: transcript.Notice, Source: "helper-update", Helper: 1, Text: "Helper 1 has stopped (COMPLETED): done"}, "stopped"},
				{transcript.Item{Kind: transcript.ToolCall, Tool: "stay_quiet", Text: `{"reason":"unchanged"}`}, "Ada stayed quiet: unchanged"},
			}
			for _, tc := range cases {
				got := ansi.Strip(strings.Join(r.Item(&tc.item), "\n"))
				if !strings.Contains(got, tc.want) {
					t.Fatalf("%q lacks %q", got, tc.want)
				}
				if glyphs == termcaps.GlyphsASCII && strings.Contains(got, "⇄") {
					t.Fatal("Unicode wire glyph in ASCII mode")
				}
			}
			if rows := r.Item(&transcript.Item{Kind: transcript.ToolResult, Tool: "stay_quiet", Text: "Staying quiet."}); len(rows) != 0 {
				t.Fatal("quiet result visible")
			}
			for width := 1; width < 120; width++ {
				row := r.HelperRow(helperboard.Helper{Handle: 1, Title: strings.Repeat("long title", 20), State: "TASK_STATE_WORKING", NextCheck: 10000}, 1000, width)
				if th.Width(row) > width {
					t.Fatalf("row overflows %d", width)
				}
			}
		})
	}
}
