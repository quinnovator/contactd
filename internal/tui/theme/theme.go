// Package theme is how the terminal client looks: its glyphs, its colours, and
// the few shapes every screen is built from.
//
// A Theme is made from one termcaps.Caps value and nothing else, so two
// terminals that can do the same things are drawn the same way, and a late
// answer from the terminal is handled by making a new Theme. Every colour is
// picked by hand for the number of colours the terminal shows and for its
// background; nothing is left to be approximated on the way to the screen.
//
// Text is measured the way the terminal counts cells (Theme.Width), and every
// function here that pads, cuts, wraps or frames text uses that one measure.
// A screen that lays out its rows through the Theme keeps its right edge.
package theme

import (
	"charm.land/bubbles/v2/textarea"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/quinnovator/contactd/internal/tui/termcaps"
)

// The tone names. A mod's reply and a status-line segment name a tone with
// one of these words.
const (
	toneDim    = "dim"
	toneAccent = "accent"
	toneTool   = "tool"
	toneOK     = "ok"
	toneWarn   = "warn"
	toneBad    = "bad"
	toneMod    = "mod"
	toneRule   = "rule"
	toneBorder = "border"
)

// Options are the settings a Theme takes besides the terminal's abilities.
type Options struct {
	// Glyph is the [glyph] table of chat.toml: replacements for single
	// glyphs of the Unicode set, by glyph name. Check says which of them
	// are not applied.
	Glyph map[string]string
}

// Cell is one status-bar cell: its text, on one line, and the tone to draw it
// in.
type Cell struct{ Text, Tone string }

// Theme is everything drawn from one Caps value. It is not changed after New,
// so one Theme may be used from several goroutines.
type Theme struct {
	// Caps is what the terminal can do, as given to New.
	Caps termcaps.Caps
	// G is the glyph set, with the overrides applied.
	G Glyphs
	// BarFilled says the status bar has a background of its own. Where it is
	// false a rule row is drawn above the bar, a selected row is shown in
	// reverse video, and a code block has no background.
	BarFilled bool

	pal     palette
	plain   lipgloss.Style
	title   lipgloss.Style
	pending lipgloss.Style
	accent  lipgloss.Style
	tool    lipgloss.Style
	dim     lipgloss.Style
	ok      lipgloss.Style
	warn    lipgloss.Style
	bad     lipgloss.Style
	mod     lipgloss.Style
	rule    lipgloss.Style
	border  lipgloss.Style
	// barOn and codeOn switch the two fills on; each is "" where there is
	// no fill.
	barOn, codeOn string
}

// New makes the Theme for a terminal. It is pure: the same Caps and Options
// always give the same Theme, and nothing is read from the environment.
//
// The glyph overrides apply to the Unicode set only; with ASCII glyphs they
// are ignored. A Caps that was never filled in is drawn the safest way: ASCII
// glyphs and no colour.
func New(c termcaps.Caps, o Options) *Theme {
	g := GlyphSet(c.Glyphs)
	if c.Glyphs == termcaps.GlyphsUnicode {
		override(&g, o)
	}
	p := paletteFor(c.Color, c.Background)
	return &Theme{
		Caps:      c,
		G:         g,
		BarFilled: p.barFill != "",
		pal:       p,
		plain:     lipgloss.NewStyle(),
		title:     lipgloss.NewStyle().Bold(true),
		pending:   lipgloss.NewStyle().Faint(true).Italic(true),
		accent:    p.accent.style(),
		tool:      p.tool.style(),
		dim:       p.dim.style(),
		ok:        p.ok.style(),
		warn:      p.warn.style(),
		bad:       p.bad.style(),
		mod:       p.mod.style(),
		rule:      p.rule.style(),
		border:    p.border.style(),
		barOn:     background(p.barFill),
		codeOn:    background(p.codeFill),
	}
}

// background returns the escape sequence that switches a background colour
// on, or "" for no colour.
func background(c string) string {
	if c == "" {
		return ""
	}
	return ansi.NewStyle().BackgroundColor(lipgloss.Color(c)).String()
}

// Style returns the style of a tone: "", "dim", "accent", "tool", "ok",
// "warn", "bad" or "mod". Two more name the lines a screen is ruled with:
// "rule" and "border". Unknown names are the plain style, which is also what
// "" gives: body text is never coloured.
//
// A tone is a colour where the terminal has one that reads on its background,
// and bold or faint where it has not. So a tone never carries meaning alone:
// whatever is drawn in one says the same thing in words.
func (t *Theme) Style(tone string) lipgloss.Style {
	switch tone {
	case toneDim:
		return t.dim
	case toneAccent:
		return t.accent
	case toneTool:
		return t.tool
	case toneOK:
		return t.ok
	case toneWarn:
		return t.warn
	case toneBad:
		return t.bad
	case toneMod:
		return t.mod
	case toneRule:
		return t.rule
	case toneBorder:
		return t.border
	default:
		return t.plain
	}
}

// Title is the style of a screen's title and of a contact's name: bold.
func (t *Theme) Title() lipgloss.Style { return t.title }

// Pending is the style of a message that was sent and is not yet
// acknowledged: faint italic.
func (t *Theme) Pending() lipgloss.Style { return t.pending }

// Textarea returns the styles of the input box. The prompt is in the accent
// tone and the placeholder dim; the text itself is not styled. Focused.Base
// is left empty: the box around the input is drawn with Box, never by the
// textarea. The cursor is the terminal's own, in its own colour, and does not
// blink.
func (t *Theme) Textarea() textarea.Styles {
	selection := lipgloss.NewStyle().Reverse(true)
	if t.BarFilled {
		selection = lipgloss.NewStyle().Background(lipgloss.Color(t.pal.barFill))
	}
	focused := textarea.StyleState{
		Base:             lipgloss.NewStyle(),
		Text:             t.plain,
		LineNumber:       t.dim,
		CursorLineNumber: t.dim,
		CursorLine:       t.plain,
		EndOfBuffer:      t.dim,
		Placeholder:      t.dim,
		Prompt:           t.accent,
		Selection:        selection,
	}
	blurred := focused
	blurred.Prompt = t.dim
	return textarea.Styles{Focused: focused, Blurred: blurred}
}
