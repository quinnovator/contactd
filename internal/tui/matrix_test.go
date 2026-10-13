package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/quinnovator/contactd/internal/client"
	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
)

// Every screen the client has, at every size from a wide window down to ten
// columns by five rows, for each of the four terminals it is drawn for: no
// row is wider than the terminal by either way of counting cells, there are
// never more rows than the terminal has, and a conversation always has the
// row it is typed in. Everything in it is written in ASCII, so that with
// ASCII glyphs every byte of every screen is ASCII too.
//
// The screens are the list, the two forms, a conversation, a conversation
// with the list of commands open, the status line's editor and the list of
// mods. Each has a test of its own with more states and odder sizes; this is
// the one table that holds them all to the same rule.
func TestEveryScreenAtEverySize(t *testing.T) {
	t.Parallel()
	contacts := []client.Contact{
		{ID: 2, Name: "a contact with a name that goes on and on", Provider: "a-provider", Model: "a-model-with-a-long-name-2026-10-12", LastActive: 1},
		{ID: 3, Provider: "fake", Model: "fake-1", LastActive: fixedNow().Add(-3 * time.Minute).UnixMilli()},
	}
	chart := chartPNG(t, 640, 360)
	for _, l := range looks() {
		t.Run(l.name, func(t *testing.T) {
			t.Parallel()
			for _, width := range []int{100, 60, 40, 30, 20, 10} {
				for _, height := range []int{30, 16, 12, 8, 5} {
					host := newFakeMods(cacheMod(), gitMod(), standupMod())
					host.answers("git", "segment", "branch", branchAnswer)
					host.answers("cache", "segment", "cache", cacheAnswer)
					r := newModRig(t, l.caps, host, Options{WorkDir: repo(t, "main")})
					r.b.contacts = append(r.b.contacts, contacts...)
					r.b.files = map[string]client.File{"latency.png": {Data: chart, ETag: `"1"`, ContentType: "image/png"}}
					r.load()
					r.size(width, height)

					// fits holds one screen to the rule. exact says the screen
					// fills the terminal, as every screen over a conversation does.
					fits := func(what string, exact bool) tea.View {
						t.Helper()
						what = fmt.Sprintf("%s, %s at %dx%d", what, l.name, width, height)
						v := r.m.View()
						rows := strings.Split(v.Content, "\n")
						if len(rows) > height || exact && len(rows) != height {
							t.Errorf("%s: %d rows", what, len(rows))
						}
						for i, row := range rows {
							if w := max(ansi.StringWidth(row), ansi.StringWidthWc(row)); w > width {
								t.Errorf("%s: row %d is %d cells wide: %q", what, i, w, ansi.Strip(row))
							}
						}
						if c := v.Cursor; c != nil && (c.X < 0 || c.X >= width || c.Y < 0 || c.Y >= height) {
							t.Errorf("%s: the cursor is at %d,%d", what, c.X, c.Y)
						}
						if v.MouseMode != tea.MouseModeNone {
							t.Errorf("%s: the mouse is asked for", what)
						}
						if l.caps.Glyphs == termcaps.GlyphsASCII {
							for i := range len(v.Content) {
								if v.Content[i] >= 0x80 {
									t.Fatalf("%s: with ASCII glyphs the screen holds the byte %#x at %d:\n%s", what, v.Content[i], i, ansi.Strip(v.Content))
								}
							}
						}
						return v
					}
					// typed holds a conversation to its own rule: the row that is
					// typed in is on the screen, and the cursor is on it. tail is
					// the end of the draft.
					typed := func(what, tail string) {
						t.Helper()
						v := fits(what, true)
						c := r.m.chat
						top := c.rows.InputTop()
						if c.rows.Input < 1 || c.rows.InputText < 1 || c.rows.Body < 1 || top+c.rows.Input > height {
							t.Fatalf("%s at %dx%d: the rows are %+v", what, width, height, c.rows)
						}
						if v.Cursor == nil || v.Cursor.Y < top || v.Cursor.Y >= top+c.rows.Input {
							t.Fatalf("%s at %dx%d: the cursor is at %+v and the input on rows %d to %d", what, width, height, v.Cursor, top, top+c.rows.Input-1)
						}
						// That row is the draft's: it holds the words typed last.
						if row := ansi.Strip(strings.Split(v.Content, "\n")[v.Cursor.Y]); !strings.Contains(row, tail) {
							t.Errorf("%s at %dx%d: the cursor's row does not end the draft with %q: %q", what, width, height, tail, row)
						}
					}

					r.keep = false
					r.send(r.m.showContacts()())
					r.keep = true
					fits("the list", false)
					if width >= 30 && height >= 8 && !strings.Contains(r.screen(), "ada") {
						t.Errorf("the list at %dx%d does not name the first contact:\n%s", width, height, r.screen())
					}

					r.typeText("n")
					r.typeText("carol with a long name")
					r.send(enter())
					r.typeText("You are Carol.")
					r.send(ctrl('j'))
					r.typeText("You answer in one line, however long the question, and you never run out of words.")
					if r.m.screen != screenNew {
						t.Fatalf("at %dx%d the new-contact form is not open", width, height)
					}
					fits("the new-contact form", false)
					r.send(esc())

					r.typeText("r")
					if r.m.screen != screenRename {
						t.Fatalf("at %dx%d the rename prompt is not open", width, height)
					}
					fits("the rename prompt", false)
					r.send(esc())

					sess := r.talk()
					r.from(sess, client.Update{Kind: client.UpdateEvents, Events: []core.Event{
						logged(t, 6, fixedNow(), core.KindUserMsg, core.MsgBody{Text: "plot last week's latency"}),
						logged(t, 7, fixedNow(), core.KindAgentMsg, core.MsgBody{Text: "p99 **peaked** on `Thursday`:\n\n```go\nx := 1 // a line of code that goes on and on and on and on and on and on and on\n```\n\n![chart](latency.png)\n"}),
					}})
					r.settle()
					// A tool is running, too. Its tick is not waited for.
					r.keep = false
					r.from(sess, typing(true), delta(core.Delta{Type: core.DeltaToolCall, Tool: "python", Text: "import time", Seq: 8}))
					r.keep = true
					r.typeText("compare with the week before")
					typed("the conversation", "before")
					if width >= 60 && height >= 16 {
						r.wantOnScreen("[image: latency.png, 640")
					}

					r.send(ctrl('c'))
					r.typeText("/st")
					if !r.m.chat.pal.Open() {
						t.Fatalf("at %dx%d the list of commands is not open", width, height)
					}
					typed("the conversation with the list of commands", "/st")

					r.send(ctrl('c'))
					r.command("/statusline")
					if r.m.screen != screenStatusLine {
						t.Fatalf("at %dx%d the status line's editor is not open", width, height)
					}
					fits("the status line's editor", true)
					r.send(esc())
					if r.m.screen != screenChat {
						t.Fatalf("at %dx%d esc did not leave the editor", width, height)
					}

					r.command("/mods")
					if r.m.screen != screenMods {
						t.Fatalf("at %dx%d the list of mods is not open", width, height)
					}
					r.settle()
					fits("the list of mods", true)
					r.send(esc())
					typed("the conversation again", r.m.th.G.Prompt)
				}
			}
		})
	}
}
