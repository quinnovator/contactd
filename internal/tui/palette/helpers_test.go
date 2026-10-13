package palette_test

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/quinnovator/contactd/internal/tui/commands"
	"github.com/quinnovator/contactd/internal/tui/palette"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
	"github.com/quinnovator/contactd/internal/tui/theme"
)

// mockRegistry is the registry of the Commands mock-up: the core commands and
// four from mods, 23 in all. Two of the mods' commands match "/st".
func mockRegistry() *commands.Registry {
	reg := commands.NewRegistry(commands.Core())
	reg.SetMod("standup", []commands.Spec{{Name: "standup", Summary: "summarize yesterday for the team"}})
	reg.SetMod("cache", []commands.Spec{{Name: "stats", Summary: "token use and cache share by day"}})
	reg.SetMod("git", []commands.Spec{
		{Name: "branch", Summary: "the branch this contact works on"},
		{Name: "log", Usage: "[n]", Summary: "the last commits"},
	})
	return reg
}

// hostileRegistry holds what a careless or hostile mod could register: escape
// sequences, text the two ways of counting cells disagree about, wide text,
// and names and summaries far longer than their columns.
func hostileRegistry() *commands.Registry {
	reg := commands.NewRegistry(commands.Core())
	reg.SetMod("evil\x1b[2J\x1b]0;owned\x07\u009b31m", []commands.Spec{
		{Name: "stain", Summary: "\x1b]0;owned\x07red \x1b[31malert\x1b[2J \u009b5mblink\tand a tab\r\nand a line"},
		{Name: "stopwatch", Summary: "an open title \x1b]2;never closed"},
	})
	reg.SetMod("wide", []commands.Spec{
		{Name: "status", Summary: "👨\u200d👩\u200d👧 family at the table, 👍🏽 twice 👍🏽, and then a great deal more text than any column has room for"},
		{Name: "stroke", Summary: "这是一个很长的中文句子用来测试这是一个很长的中文句子用来测试这是一个很长的中文句子用来测试"},
		{Name: "a-name-far-longer-than-the-twenty-cells-its-column-has-st", Summary: "e\u0301 and \u202eright to left"},
	})
	reg.SetMod("a-mod-with-a-name-longer-than-sixteen-cells", []commands.Spec{{Name: "stretch", Summary: ""}})
	return reg
}

// look is one terminal the palette is drawn for.
type look struct {
	name string
	caps termcaps.Caps
	opts theme.Options
}

func (l look) theme() *theme.Theme { return theme.New(l.caps, l.opts) }

// mockLook is the terminal of the mock-ups: 24-bit colour on a dark
// background, Unicode glyphs.
func mockLook() look {
	return look{name: "truecolor dark unicode", caps: termcaps.Caps{
		Color: termcaps.ColorTrue, Background: termcaps.BgDark, Glyphs: termcaps.GlyphsUnicode, Width: termcaps.WidthWc,
	}}
}

// asciiLook is a terminal with no colour and ASCII glyphs.
func asciiLook() look {
	return look{name: "none ascii", caps: termcaps.Caps{
		Color: termcaps.ColorNone, Background: termcaps.BgUnknown, Glyphs: termcaps.GlyphsASCII, Width: termcaps.WidthWc,
	}}
}

// looks are the four terminals every screen is tested on, one that counts
// cells by grapheme cluster, one whose box is drawn with wider glyphs, and
// one whose abilities were never filled in.
func looks() []look {
	return []look{
		mockLook(),
		{name: "16 light", caps: termcaps.Caps{
			Color: termcaps.Color16, Background: termcaps.BgLight, Glyphs: termcaps.GlyphsUnicode, Width: termcaps.WidthWc,
		}},
		{name: "256 unknown", caps: termcaps.Caps{
			Color: termcaps.Color256, Background: termcaps.BgUnknown, Glyphs: termcaps.GlyphsUnicode, Width: termcaps.WidthWc,
		}},
		asciiLook(),
		{name: "truecolor dark grapheme", caps: termcaps.Caps{
			Color: termcaps.ColorTrue, Background: termcaps.BgDark, Glyphs: termcaps.GlyphsUnicode, Width: termcaps.WidthGrapheme,
		}},
		{
			name: "wide glyphs", caps: termcaps.Caps{
				Color: termcaps.Color256, Background: termcaps.BgDark, Glyphs: termcaps.GlyphsUnicode, Width: termcaps.WidthWc,
			},
			opts: theme.Options{Glyph: map[string]string{"boxv": "||", "pointer": "=>", "updown": "u/d", "mid": "::"}},
		},
		{name: "zero caps"},
	}
}

// wideLooks are terminals whose glyphs were replaced by the widest a theme
// takes: three cells, and two-cell characters. They are drawn to no golden
// file; a line must still be the width.
func wideLooks() []look {
	caps := termcaps.Caps{
		Color: termcaps.Color256, Background: termcaps.BgDark, Glyphs: termcaps.GlyphsUnicode, Width: termcaps.WidthWc,
	}
	clusters := caps
	clusters.Width = termcaps.WidthGrapheme
	three := map[string]string{
		"boxv": "|||", "boxh": "===", "boxtl": "+++", "boxtr": "+++", "boxbl": "+++", "boxbr": "+++",
		"pointer": "=>>", "ellipsis": "...", "updown": "^v^", "mid": ":::",
	}
	return []look{
		{name: "three-cell glyphs", caps: caps, opts: theme.Options{Glyph: three}},
		{name: "three-cell glyphs, grapheme", caps: clusters, opts: theme.Options{Glyph: three}},
		{name: "two-cell characters", caps: caps, opts: theme.Options{
			Glyph: map[string]string{"boxv": "日", "pointer": "本", "ellipsis": "語", "mid": "中"},
		}},
	}
}

// opened returns a palette that has been told the draft.
func opened(t *testing.T, reg *commands.Registry, draft string) *palette.Model {
	t.Helper()
	m := palette.New()
	if !m.Set(reg, draft) {
		t.Fatalf("the palette is closed on the draft %q", draft)
	}
	return m
}

// plain returns the lines without their styles.
func plain(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = ansi.Strip(l)
	}
	return out
}

// quoted writes lines so that escape sequences and trailing spaces can be
// read in a golden file.
func quoted(lines []string) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(strconv.Quote(l))
		b.WriteByte('\n')
	}
	return b.String()
}

// names returns the command names in the lines, in order: the word after the
// slash of each line that has one in its name column. A title and a row of
// keys have none there.
func names(lines []string) []string {
	var out []string
	find := regexp.MustCompile(`^[^/]{0,5} /([a-z0-9._-]+)`)
	for _, l := range plain(lines) {
		if strings.Contains(l, "commands matching") {
			continue
		}
		if m := find.FindStringSubmatch(l); m != nil {
			out = append(out, m[1])
		}
	}
	return out
}

// specNames returns the names of some commands.
func specNames(specs []commands.Spec) []string {
	out := make([]string, len(specs))
	for i, s := range specs {
		out[i] = s.Name
	}
	return out
}
