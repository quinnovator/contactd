package modscreen_test

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/quinnovator/contactd/internal/tui/mods"
	"github.com/quinnovator/contactd/internal/tui/modscreen"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
	"github.com/quinnovator/contactd/internal/tui/theme"
)

// dirLabel is the mods directory as the root hands it over.
const dirLabel = "~/.config/contactd/mods"

// The words of the spec for the line under tmux, and the screen's own for how
// a mod is installed.
const (
	tmuxLine   = "images are half blocks here: tmux allow-passthrough is off"
	howInstall = "press i and give the path of a directory that holds a mod.toml and its module"
)

// noteRows is the number of rows the tmux line takes at a width: as many as
// it wraps to, and at most three.
func noteRows(th *theme.Theme, w int) int { return min(len(th.Wrap(tmuxLine, w)), 3) }

// words returns text as its words with one space between them, so that text
// can be looked for whatever way it was wrapped.
func words(s string) string { return strings.Join(strings.Fields(s), " ") }

// namedCaps is one terminal a screen is drawn for.
type namedCaps struct {
	name string
	caps termcaps.Caps
}

// fourCaps are the terminals every screen is tried on: the best one, a
// light one with 16 colours, one that never said what its background is, and
// one with no colour and ASCII glyphs.
func fourCaps() []namedCaps {
	return []namedCaps{
		{"truecolor dark unicode", termcaps.Caps{
			Color: termcaps.ColorTrue, Background: termcaps.BgDark, Glyphs: termcaps.GlyphsUnicode, Width: termcaps.WidthWc,
		}},
		{"16 light", termcaps.Caps{
			Color: termcaps.Color16, Background: termcaps.BgLight, Glyphs: termcaps.GlyphsUnicode, Width: termcaps.WidthWc,
		}},
		{"256 unknown", termcaps.Caps{
			Color: termcaps.Color256, Background: termcaps.BgUnknown, Glyphs: termcaps.GlyphsUnicode, Width: termcaps.WidthGrapheme,
		}},
		{"none ascii", termcaps.Caps{
			Color: termcaps.ColorNone, Background: termcaps.BgUnknown, Glyphs: termcaps.GlyphsASCII, Width: termcaps.WidthWc,
		}},
	}
}

// darkTheme is the theme of the drawing: 24-bit colour on a dark background.
func darkTheme() *theme.Theme { return theme.New(fourCaps()[0].caps, theme.Options{}) }

// asciiTheme has no colour and ASCII glyphs.
func asciiTheme() *theme.Theme { return theme.New(fourCaps()[3].caps, theme.Options{}) }

// info builds one mod as the host would list it.
func info(name, version, description string, status mods.Status, read ...mods.Grant) mods.Info {
	return mods.Info{
		Manifest: mods.Manifest{
			ABI: 1, Name: name, Version: version, Description: description, Module: name + ".wasm", Read: read,
		},
		Status: status,
	}
}

// cacheMod is the cache example as the drawing shows it: on, with a segment
// and a command, and a hundred calls behind it.
func cacheMod() mods.Info {
	in := info("cache", "0.3.1", "How much of each prompt the provider served from its cache.",
		mods.StatusOn, mods.GrantModelCalls)
	in.Manifest.Budget = 5 * time.Millisecond
	in.Manifest.Segments = []mods.SegmentDecl{{Name: "cache", Refresh: mods.RefreshModelCall}}
	in.Manifest.Commands = []mods.CommandDecl{{Name: "stats", Usage: "[days]", Summary: "token use and cache share by day"}}
	in.Median, in.Slowest, in.Calls = 400*time.Microsecond, 1900*time.Microsecond, 100
	return in
}

// fiveMods are five installed mods, four on and one off, in the order the
// host lists them.
func fiveMods() []mods.Info {
	diff := info("diff", "1.0.0", "Draws a diff with its added and removed lines marked.", mods.StatusOn, mods.GrantMessages)
	diff.Manifest.Renderers = []mods.RendererDecl{{Kind: "code:diff"}}
	diff.Median, diff.Slowest, diff.Calls = 900*time.Microsecond, 3200*time.Microsecond, 41

	git := info("git", "0.1.0", "The git branch checked out where the client started.", mods.StatusOn, mods.GrantWorkspace)
	git.Manifest.Segments = []mods.SegmentDecl{{Name: "branch", Refresh: mods.RefreshTurn}}
	git.Median, git.Slowest, git.Calls = 70*time.Microsecond, 110*time.Microsecond, 12

	mermaid := info("mermaid", "0.1.2", "Draws a mermaid diagram as boxes and arrows.", mods.StatusOff, mods.GrantMessages)
	mermaid.Manifest.Renderers = []mods.RendererDecl{{Kind: "code:mermaid"}}

	standup := info("standup", "0.2.0", "Drafts a message that asks for yesterday's work, ready to edit and send.",
		mods.StatusOn, mods.GrantClock)
	standup.Manifest.Commands = []mods.CommandDecl{{Name: "standup", Summary: "summarize yesterday for the team"}}

	return []mods.Info{cacheMod(), diff, git, mermaid, standup}
}

// manyMods are n mods named mod-00, mod-01 and so on, every third one off.
func manyMods(n int) []mods.Info {
	out := make([]mods.Info, 0, n)
	for i := range n {
		status := mods.StatusOn
		if i%3 == 2 {
			status = mods.StatusOff
		}
		name := "mod-" + strconv.Itoa(i/10) + strconv.Itoa(i%10)
		in := info(name, "1."+strconv.Itoa(i)+".0", "Mod number "+strconv.Itoa(i)+".", status, mods.GrantClock)
		in.Manifest.Segments = []mods.SegmentDecl{{Name: name, Refresh: mods.RefreshTurn}}
		out = append(out, in)
	}
	return out
}

// key builds the key press a terminal sends for a key named as Bubble Tea
// names it ("up", "space", "enter", "esc") or for one character.
func key(name string) tea.KeyPressMsg {
	switch name {
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "space", " ":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	}
	r := []rune(name)
	return tea.KeyPressMsg{Code: r[0], Text: name}
}

// press sends keys and fails the test if any of them leaves the screen, asks
// for an action or returns a command.
func press(t *testing.T, m *modscreen.Model, names ...string) {
	t.Helper()
	for _, name := range names {
		done, act, cmd := m.Update(key(name))
		if done || act != nil || cmd != nil {
			t.Fatalf("key %q: done %v, action %+v, command set %v; want none of them", name, done, act, cmd != nil)
		}
	}
}

// typeText types text into an open input, one key press for each character.
func typeText(t *testing.T, m *modscreen.Model, text string) {
	t.Helper()
	for _, r := range text {
		press(t, m, string(r))
	}
}

// screen draws the model and returns its rows without their styles.
func screen(m *modscreen.Model, th *theme.Theme, width, height int) []string {
	content, _ := m.View(th, width, height)
	return strings.Split(ansi.Strip(content), "\n")
}

// text draws the model and returns the screen without its styles.
func text(m *modscreen.Model, th *theme.Theme, width, height int) string {
	return strings.Join(screen(m, th, width, height), "\n")
}

// details returns the rows of the detail pane without their styles and
// without their margin: what follows the column rule on a wide screen, the
// whole row on a narrow one where the details have the focus.
func details(th *theme.Theme, rows []string) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		if _, after, ok := strings.Cut(row, th.G.Sep); ok {
			row = strings.TrimPrefix(after, "  ")
		} else {
			row = strings.TrimPrefix(row, " ")
		}
		out = append(out, strings.TrimRight(row, " "))
	}
	return out
}

// section returns the rows under a heading of the detail pane, up to the
// next blank row. It fails the test when the heading is not there.
func section(t *testing.T, rows []string, heading string) []string {
	t.Helper()
	for i, row := range rows {
		if row != heading {
			continue
		}
		var out []string
		for _, next := range rows[i+1:] {
			if next == "" {
				break
			}
			out = append(out, next)
		}
		return out
	}
	t.Fatalf("no %q block in the details:\n%s", heading, strings.Join(rows, "\n"))
	return nil
}

// sgr matches the one kind of escape sequence a screen may hold: a style.
func sgr() *regexp.Regexp { return regexp.MustCompile(`\x1b\[[0-9;:]*m`) }

// hasRow reports whether any row holds every one of the parts.
func hasRow(rows []string, parts ...string) bool {
	for _, row := range rows {
		all := true
		for _, part := range parts {
			all = all && strings.Contains(row, part)
		}
		if all {
			return true
		}
	}
	return false
}
