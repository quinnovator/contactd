package sledit_test

import (
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/quinnovator/contactd/internal/tui/sledit"
	"github.com/quinnovator/contactd/internal/tui/statusline"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
	"github.com/quinnovator/contactd/internal/tui/theme"
	"github.com/quinnovator/contactd/internal/tui/tuitest"
)

func TestMain(m *testing.M) { tuitest.Main(m) }

// path is the file the mock-up's header names.
const path = "~/.config/contactd/statusline.toml"

// idle is the state the mock-ups draw: connected, one model call behind,
// nothing running.
func idle() statusline.Snapshot {
	return statusline.Snapshot{
		Conn: statusline.ConnLive, Contact: "ada", Provider: "deepseek", Model: "deepseek-flash",
		HasCall: true, CacheReported: true, Input: 3700, Cached: 3034, Output: 210, MS: 7200,
		Clock: "12:04",
	}
}

func preset(t *testing.T, name string) statusline.Config {
	t.Helper()
	c, ok := statusline.Preset(name)
	if !ok {
		t.Fatalf("no preset %q", name)
	}
	return c
}

// drawing returns the segments of the status-line editor mock: the ops
// preset with tokens and agents off, and the git mod's branch.
func drawing(t *testing.T) statusline.Config {
	t.Helper()
	c := preset(t, "ops")
	for i, s := range c.Segments {
		if s.Name == "tokens" || s.Name == "agents" {
			c.Segments[i].On = false
		}
	}
	return c.WithMods([]statusline.ModSegment{{Mod: "git", Name: "branch"}})
}

// live returns the values the client would hand the editor in the idle
// state, with the git mod's branch.
func live(th *theme.Theme) map[string]statusline.Value {
	vals := statusline.CoreValues(idle(), statusline.Marks{Dot: th.G.Dot, Newer: th.G.Newer})
	branch := "⎇ main"
	if th.Caps.Glyphs != termcaps.GlyphsUnicode {
		branch = "main"
	}
	vals["mod:git/branch"] = statusline.ModValue(branch, "", "")
	return vals
}

// The four terminals every screen is checked on.
func trueDark() termcaps.Caps {
	return termcaps.Caps{
		Color: termcaps.ColorTrue, Background: termcaps.BgDark,
		Glyphs: termcaps.GlyphsUnicode, Width: termcaps.WidthWc,
	}
}

func light16() termcaps.Caps {
	return termcaps.Caps{
		Color: termcaps.Color16, Background: termcaps.BgLight,
		Glyphs: termcaps.GlyphsUnicode, Width: termcaps.WidthWc,
	}
}

func unknown256() termcaps.Caps {
	return termcaps.Caps{
		Color: termcaps.Color256, Background: termcaps.BgUnknown,
		Glyphs: termcaps.GlyphsUnicode, Width: termcaps.WidthGrapheme,
	}
}

func plainASCII() termcaps.Caps {
	return termcaps.Caps{
		Color: termcaps.ColorNone, Background: termcaps.BgUnknown,
		Glyphs: termcaps.GlyphsASCII, Width: termcaps.WidthWc,
	}
}

type terminal struct {
	name string
	caps termcaps.Caps
}

func terminals() []terminal {
	return []terminal{
		{"truecolor dark", trueDark()},
		{"16 light", light16()},
		{"256 unknown, grapheme", unknown256()},
		{"none ascii", plainASCII()},
	}
}

func themeOf(c termcaps.Caps) *theme.Theme { return theme.New(c, theme.Options{}) }

// newDrawing opens the editor on the mock-up's segments with cache
// selected, as drawn.
func newDrawing(t *testing.T, th *theme.Theme) *sledit.Model {
	t.Helper()
	m := sledit.New(drawing(t), path, live(th))
	sel(t, m, "core:cache")
	return m
}

// Key presses, as Bubble Tea delivers them.
func key(code rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: code} }

func char(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Text: string(r)} }

func up() tea.KeyPressMsg    { return key(tea.KeyUp) }
func down() tea.KeyPressMsg  { return key(tea.KeyDown) }
func left() tea.KeyPressMsg  { return key(tea.KeyLeft) }
func right() tea.KeyPressMsg { return key(tea.KeyRight) }
func enter() tea.KeyPressMsg { return key(tea.KeyEnter) }
func esc() tea.KeyPressMsg   { return key(tea.KeyEscape) }
func space() tea.KeyPressMsg { return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "} }

// shift is a capital letter typed with shift held, as a terminal with key
// enhancements reports it.
func shift(r rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: r, ShiftedCode: r - 'a' + 'A', Mod: tea.ModShift, Text: string(r - 'a' + 'A')}
}

// send passes messages to the editor and returns what the last one did.
func send(t *testing.T, m *sledit.Model, msgs ...tea.Msg) sledit.Result {
	t.Helper()
	var res sledit.Result
	for _, msg := range msgs {
		res, _ = m.Update(msg)
	}
	return res
}

// typed is the key presses of a text.
func typed(text string) []tea.Msg {
	msgs := make([]tea.Msg, 0, len(text))
	for _, r := range text {
		if r == ' ' {
			msgs = append(msgs, space())
			continue
		}
		msgs = append(msgs, char(r))
	}
	return msgs
}

// wipe empties the input of the field being edited, wherever its cursor is.
func wipe(t *testing.T, m *sledit.Model) {
	t.Helper()
	send(t, m, key(tea.KeyEnd), tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
}

// retype replaces the field's text.
func retype(t *testing.T, m *sledit.Model, text string) {
	t.Helper()
	wipe(t, m)
	send(t, m, typed(text)...)
}

// sel moves the selection to a segment.
func sel(t *testing.T, m *sledit.Model, id string) {
	t.Helper()
	for range len(m.Config().Segments) {
		send(t, m, up())
	}
	for range len(m.Config().Segments) {
		if m.Selected() == id {
			return
		}
		send(t, m, down())
	}
	t.Fatalf("no segment %q to select", id)
}

// segment returns a segment of the config being edited.
func segment(t *testing.T, m *sledit.Model, id string) statusline.Segment {
	t.Helper()
	for _, s := range m.Config().Segments {
		if s.ID() == id {
			return s
		}
	}
	t.Fatalf("no segment %q", id)
	return statusline.Segment{}
}

// ids lists the segments of a config in order.
func ids(c statusline.Config) []string {
	out := make([]string, 0, len(c.Segments))
	for _, s := range c.Segments {
		out = append(out, s.ID())
	}
	return out
}

// plainText is a screen without its styles.
func plainText(content string) string { return ansi.Strip(content) }

// screen draws the editor and returns its rows without their styles.
func screen(t *testing.T, m *sledit.Model, th *theme.Theme, width, height int) []string {
	t.Helper()
	content, _ := m.View(th, width, height)
	return strings.Split(plainText(content), "\n")
}

// rowWith returns the first row that holds a text, with the space at its
// end removed.
func rowWith(t *testing.T, rows []string, text string) string {
	t.Helper()
	for _, row := range rows {
		if strings.Contains(row, text) {
			return strings.TrimRight(row, " ")
		}
	}
	t.Fatalf("no row holds %q in:\n%s", text, strings.Join(rows, "\n"))
	return ""
}

// plain writes a screen for a golden file a person can read: no styles, and
// a bar at the end of each row so that the space there shows.
func plain(content string) []byte {
	var b strings.Builder
	for row := range strings.SplitSeq(plainText(content), "\n") {
		b.WriteString(row)
		b.WriteString("|\n")
	}
	return []byte(b.String())
}

// quoted writes a screen with its escape sequences spelled out.
func quoted(content string) []byte {
	var b strings.Builder
	for row := range strings.SplitSeq(content, "\n") {
		b.WriteString(strconv.Quote(row))
		b.WriteByte('\n')
	}
	return []byte(b.String())
}
