package tui

import (
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/quinnovator/contactd/internal/client"
	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
	"github.com/quinnovator/contactd/internal/tui/tuitest"
)

// look is one of the four terminals the Compatibility mock draws the same
// conversation in, of falling ability.
type look struct {
	name string
	caps termcaps.Caps
	// colours are the kinds of colour the terminal may be sent: see colourKinds.
	colours []string
	// accent is how the person's own message starts, a colour picked by hand
	// for this terminal.
	accent string
}

func looks() []look {
	return []look{
		{name: "truecolor", caps: trueDark(), colours: []string{"fg 24-bit", "bg 24-bit"}, accent: "\x1b[1;38;2;94;196;200m"},
		{name: "16-light", caps: light16(), colours: []string{"fg 16"}, accent: "\x1b[1;34m"},
		{name: "256-unknown", caps: unknown256(), colours: []string{"fg 256"}, accent: "\x1b[1;38;5;30m"},
		{name: "none-ascii", caps: noneASCII(), accent: "\x1b[1m"},
	}
}

// colourKinds names the kinds of colour the escape sequences in s ask for:
// "fg" or "bg", then "16", "256" or "24-bit". Bold, faint, italic, reverse
// and the resets are not colours.
func colourKinds(t *testing.T, s string) []string {
	t.Helper()
	kinds := make(map[string]bool)
	for _, m := range regexp.MustCompile("\x1b\\[([0-9;:]*)m").FindAllStringSubmatch(s, -1) {
		params := strings.FieldsFunc(m[1], func(r rune) bool { return r == ';' || r == ':' })
		for i := 0; i < len(params); i++ {
			n, err := strconv.Atoi(params[i])
			if err != nil {
				t.Fatalf("the sequence %q has the parameter %q", m[0], params[i])
			}
			side := "fg "
			if n >= 40 && n <= 49 || n >= 100 {
				side = "bg "
			}
			switch {
			case n == 38 || n == 48:
				if i+1 >= len(params) {
					t.Fatalf("the sequence %q ends after %d", m[0], n)
				}
				if params[i+1] == "2" {
					kinds[side+"24-bit"] = true
					i += 4
				} else {
					kinds[side+"256"] = true
					i += 2
				}
			case n >= 30 && n <= 37, n >= 40 && n <= 47, n >= 90 && n <= 97, n >= 100 && n <= 107:
				kinds[side+"16"] = true
			}
		}
	}
	return slices.Sorted(maps.Keys(kinds))
}

// wantColours fails the test when raw holds a kind of colour a terminal of
// this look is not sent, or, with ASCII glyphs, a byte outside ASCII.
func (l look) wantColours(t *testing.T, what, raw string) {
	t.Helper()
	for _, kind := range colourKinds(t, raw) {
		if !slices.Contains(l.colours, kind) {
			t.Errorf("%s is coloured with %q, and this terminal takes only %q:\n%q", what, kind, l.colours, raw)
		}
	}
	if l.caps.Glyphs != termcaps.GlyphsASCII {
		return
	}
	for i := range len(raw) {
		if raw[i] >= 0x80 {
			t.Errorf("%s holds the byte %#x with ASCII glyphs:\n%q", what, raw[i], raw)
			return
		}
	}
}

// compatMock brings the interface to the moment the Compatibility mock
// draws, in a terminal of 12 rows: four past noon, a short conversation with
// ada in which a tool ran, and the next question half written.
func compatMock(t *testing.T, caps termcaps.Caps, width int, draft string) *harness {
	t.Helper()
	h := newHarness(t, caps)
	h.now = time.Date(2026, 10, 12, 12, 4, 0, 0, time.UTC)
	h.size(width, 12)
	h.m.open(client.Contact{ID: 1, Name: "ada", Provider: "deepseek", Model: "deepseek-flash"})
	at := h.now.Add(-time.Minute)
	h.from(h.b.sessions[0], live(), client.Update{Kind: client.UpdateEvents, Events: []core.Event{
		logged(t, 1, at, core.KindUserMsg, core.MsgBody{Text: "plot last week's latency"}),
		logged(t, 2, at, core.KindToolCall, core.ToolCall{ID: "c", Name: "python", Code: "df.plot.bar()"}),
		logged(t, 3, at, core.KindToolResult, core.ToolResult{ID: "c", Output: "saved latency.png\n"}),
		logged(t, 4, at, core.KindAttempt, core.AttemptBody{Route: "deepseek/deepseek-flash", Input: 1000, Cached: 820, MS: 7200}),
		logged(t, 5, at, core.KindAgentMsg, core.MsgBody{Text: "p99 peaked on Thursday at 7.2 s."}),
	}})
	h.typeText(draft)
	return h
}

// The four panels of the Compatibility mock, at the 46 columns they are
// drawn in and at a phone's 30. Each is the same conversation; what changes
// is the glyphs, the colours, and whether the bar has a fill or a rule.
//
// The build differs from the drawing in two places. The eight rows of the
// transcript end at the last line of the reply, so the spare row is the
// first and not the last. And at 30 columns the text is wrapped where it
// stops fitting, a word later than it was drawn.
func TestCompatibilityMock(t *testing.T) {
	t.Parallel()
	spread := func(left, right string, width int) string {
		return left + strings.Repeat(" ", width-ansi.StringWidth(left)-ansi.StringWidth(right)) + right
	}
	// screen is the whole panel for a glyph set at a width. filled says the
	// bar has a background, and so no rule above it and a row more of the
	// transcript.
	screen := func(ascii, filled bool, width int) (rows []string, prompt, draft string) {
		prompt, draft = "❯", "compare with the week before"
		tool, elbow, bar, narrowBar, rule := "⏺", "⎿", "● live │ cache 82% │ 7.2s", "● live │ 82%", "─"
		if ascii {
			prompt, tool, elbow, bar, narrowBar, rule = ">", "*", "|_", "* live | cache 82% | 7.2s", "* live | 82%", "-"
		}
		header := spread("ada  deepseek-flash", "cache 82%", width)
		transcript := []string{
			prompt + " plot last week's latency",
			"",
			tool + " python",
			"    df.plot.bar()",
			"  " + elbow + " saved latency.png",
			"",
			"  p99 peaked on Thursday at 7.2 s.",
		}
		if width < 40 {
			// No room for the model, the word "cache" or the tool's code.
			header, draft, bar = spread("ada", "82%", width), "compare with the week", narrowBar
			transcript = []string{
				prompt + " plot last week's latency",
				"",
				tool + " python",
				"  " + elbow + " saved latency.png",
				"",
				"  p99 peaked on Thursday at",
				"  7.2 s.",
			}
		}
		rows = []string{header, strings.Repeat(rule, width)}
		if filled {
			rows = append(rows, "")
		}
		rows = append(rows, transcript...)
		if !filled {
			rows = append(rows, strings.Repeat(rule, width))
		}
		return append(rows, spread(bar, "12:04", width), spread(prompt+" "+draft, "", width)), prompt, draft
	}

	for _, l := range looks() {
		for _, width := range []int{46, 30} {
			name := l.name + "-" + strconv.Itoa(width) + "x12"
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				ascii, filled := l.caps.Glyphs == termcaps.GlyphsASCII, l.name == "truecolor"
				want, prompt, draft := screen(ascii, filled, width)
				h := compatMock(t, l.caps, width, draft)
				tuitest.Golden(t, "compat/"+name+".golden", []byte(h.screen()+"\n"))
				h.wantFits("the panel", width, 12)
				got := h.rows()
				if len(got) != 12 {
					t.Fatalf("the panel has %d rows, want 12:\n%s", len(got), h.screen())
				}
				for i := range want {
					if got[i] != want[i] {
						t.Errorf("row %d is\n%q, want\n%q", i, got[i], want[i])
					}
				}
				// No box and no hint line at this height: the prompt and the
				// draft alone, with the terminal's own cursor after the draft.
				v := h.m.View()
				if v.Cursor == nil || v.Cursor.Y != 11 || v.Cursor.X != ansi.StringWidth(prompt+" "+draft) {
					t.Errorf("the cursor is at %+v, want the cell after the draft on the last row", v.Cursor)
				}
				// The colours are the ones picked for this terminal.
				if !strings.Contains(v.Content, l.accent+prompt+" plot") {
					t.Errorf("the person's message does not start with %q:\n%q", l.accent, v.Content)
				}
				l.wantColours(t, "the panel", v.Content)
				if filled != strings.Contains(v.Content, "\x1b[48;2;27;33;38m") {
					t.Errorf("the bar's fill is wrong for a terminal where filled is %v:\n%q", filled, v.Content)
				}
			})
		}
	}

	// The rows the mock is known by, as it draws them.
	t.Run("the drawn rows", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			caps  termcaps.Caps
			width int
			rows  map[int]string
			lacks string
		}{
			{caps: trueDark(), width: 46, rows: map[int]string{
				0:  "ada  deepseek-flash                  cache 82%",
				1:  "──────────────────────────────────────────────",
				5:  "⏺ python",
				6:  "    df.plot.bar()",
				7:  "  ⎿ saved latency.png",
				10: "● live │ cache 82% │ 7.2s                12:04",
				11: "❯ compare with the week before                ",
			}},
			{caps: trueDark(), width: 30, rows: map[int]string{
				0:  "ada                        82%",
				5:  "⏺ python",
				6:  "  ⎿ saved latency.png",
				10: "● live │ 82%             12:04",
				11: "❯ compare with the week       ",
			}, lacks: "df.plot.bar()"},
			{caps: light16(), width: 46, rows: map[int]string{
				9:  "──────────────────────────────────────────────",
				10: "● live │ cache 82% │ 7.2s                12:04",
			}},
			{caps: unknown256(), width: 30, rows: map[int]string{
				4:  "⏺ python",
				5:  "  ⎿ saved latency.png",
				9:  "──────────────────────────────",
				10: "● live │ 82%             12:04",
			}, lacks: "df.plot.bar()"},
			{caps: noneASCII(), width: 46, rows: map[int]string{
				0:  "ada  deepseek-flash                  cache 82%",
				1:  "----------------------------------------------",
				2:  "> plot last week's latency",
				4:  "* python",
				5:  "    df.plot.bar()",
				6:  "  |_ saved latency.png",
				8:  "  p99 peaked on Thursday at 7.2 s.",
				9:  "----------------------------------------------",
				10: "* live | cache 82% | 7.2s                12:04",
				11: "> compare with the week before                ",
			}},
			{caps: noneASCII(), width: 30, rows: map[int]string{
				1:  "------------------------------",
				4:  "* python",
				5:  "  |_ saved latency.png",
				9:  "------------------------------",
				10: "* live | 82%             12:04",
			}, lacks: "df.plot.bar()"},
		} {
			draft := "compare with the week before"
			if tc.width < 40 {
				draft = "compare with the week"
			}
			h := compatMock(t, tc.caps, tc.width, draft)
			got := h.rows()
			for i, want := range tc.rows {
				if i >= len(got) || got[i] != want {
					t.Errorf("%v colours at %d columns: row %d is not\n%q:\n%s", tc.caps.Color, tc.width, i, want, h.screen())
				}
			}
			if tc.lacks != "" && strings.Contains(h.screen(), tc.lacks) {
				t.Errorf("%v colours at %d columns: the screen shows %q:\n%s", tc.caps.Color, tc.width, tc.lacks, h.screen())
			}
		}
	})
}

// Every screen is drawn for the terminal it is on: the list, both forms and
// the conversation use the glyph set and the kinds of colour that terminal
// takes, and nothing else. A terminal with no colour is sent none, and one
// with ASCII glyphs no byte outside ASCII.
func TestEveryScreenIsDrawnForItsTerminal(t *testing.T) {
	t.Parallel()
	for _, l := range looks() {
		t.Run(l.name, func(t *testing.T) {
			t.Parallel()
			ascii := l.caps.Glyphs == termcaps.GlyphsASCII
			pick := func(unicode, plain string) string {
				if ascii {
					return plain
				}
				return unicode
			}
			h := newHarness(t, l.caps,
				client.Contact{ID: 1, Name: "ada", Provider: "deepseek", Model: "deepseek-flash", LastActive: fixedNow().Add(-time.Hour).UnixMilli()},
				client.Contact{ID: 2, Name: "bob", Provider: "fake", Model: "fake-1"},
			)
			h.size(100, 30)
			h.send(h.m.showContacts()())
			h.wantOnScreen("Contacts", pick("▸ ada", "> ada"), "deepseek/deepseek-flash  1h ago", pick("enter open · n new", "enter open | n new"))
			l.wantColours(t, "the list", h.m.View().Content)

			// The new-contact form, and what it says when the server says no.
			h.typeText("n")
			h.typeText("carol")
			h.key(enter())
			h.key(enter())
			h.wantOnScreen("New contact", pick("❯ carol", "> carol"), "say what this contact is", pick("tab switch field · enter", "tab switch field | enter"))
			l.wantColours(t, "the new-contact form", h.m.View().Content)
			if raw := h.m.View().Content; !strings.Contains(raw, l.accent+pick("❯", ">")) {
				t.Errorf("the form's prompt does not start with %q:\n%q", l.accent, raw)
			}
			h.key(esc())

			h.typeText("r")
			h.wantOnScreen("Rename ada", pick("❯ ada", "> ada"), pick("enter rename · esc cancel", "enter rename | esc cancel"))
			l.wantColours(t, "the rename prompt", h.m.View().Content)
			h.key(esc())

			// A conversation with one of everything in it, and a note.
			h.key(enter())
			at := fixedNow().Add(-time.Minute)
			h.from(h.b.sessions[0], live(), client.Update{Kind: client.UpdateEvents, Start: true, Events: []core.Event{
				logged(t, 1, at, core.KindUserMsg, core.MsgBody{Text: "plot last week's latency"}),
				logged(t, 2, at, core.KindToolCall, core.ToolCall{ID: "c", Name: "python", Code: "df.plot.bar()"}),
				logged(t, 3, at, core.KindToolResult, core.ToolResult{ID: "c", Output: "no such column\n", IsError: true}),
				logged(t, 4, at, core.KindAttempt, core.AttemptBody{Route: "deepseek/deepseek-flash", Input: 1000, Cached: 820, MS: 7200}),
				logged(t, 5, at, core.KindAgentMsg, core.MsgBody{Text: "p99 **peaked** on `Thursday`:\n\n```go\nx := 1\n```\n"}),
			}}, delta(core.Delta{Type: core.DeltaTyping, Typing: true}))
			h.typeText("compare")
			h.wantOnScreen(
				"the beginning", pick("❯ plot last week's latency", "> plot last week's latency"),
				pick("⏺ python", "* python"), pick("⎿ no such column", "|_ no such column"), "x := 1",
				pick("● live │ deepseek-flash │ cache 82%", "* live | deepseek-flash | cache 82%"), pick("replying │ esc stop │ 12:00", "replying | esc stop | 12:00"),
				pick("│ ❯ compare", "| > compare"), pick("╭─", "+-"), pick("enter send · ctrl+j new line", "enter send | ctrl+j new line"),
			)
			l.wantColours(t, "the conversation", h.m.View().Content)
			h.m.chat.setNote("not sent: not connected")
			h.wantOnScreen("not sent: not connected")
			l.wantColours(t, "the conversation with a note", h.m.View().Content)
		})
	}
}
