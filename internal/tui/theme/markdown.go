package theme

import (
	"strings"

	glamouransi "charm.land/glamour/v2/ansi"

	"github.com/quinnovator/contactd/internal/tui/termcaps"
)

// The two ways code is highlighted, and the chroma theme for each background.
const (
	formatterTrue = "terminal16m"
	formatter256  = "terminal256"
	chromaDark    = "github-dark"
	chromaLight   = "github"
	// ruleCells is the length of a horizontal rule in a reply.
	ruleCells = 8
)

// ChromaFormatter names the chroma formatter that code blocks are highlighted
// with: "terminal16m" with 24-bit colour and "terminal256" with 256 colours.
// It is "" for no highlighting: with 16 colours or none, where a syntax
// theme's colours come out wrong, and while the background is unknown, where
// no theme can be chosen.
func (t *Theme) ChromaFormatter() string {
	if t.Caps.Background != termcaps.BgDark && t.Caps.Background != termcaps.BgLight {
		return ""
	}
	switch t.Caps.Color {
	case termcaps.ColorTrue:
		return formatterTrue
	case termcaps.Color256:
		return formatter256
	case termcaps.Color16, termcaps.ColorNone:
		return ""
	}
	return ""
}

// Markdown returns the style replies are rendered with. Body text keeps the
// terminal's own colour: the document has none. Headings are in the accent
// tone, link text in the tool tone, quotes and addresses in the dim colour,
// and inline code in the accent colour on the code background. Where a role
// has no colour the shape still tells: a heading keeps its "#" and is bold, a
// quote keeps its bar, an address is underlined, and inline code keeps its
// backquotes.
//
// The document has no margin: whoever draws a reply indents it. A code block
// names a chroma theme only when ChromaFormatter names a formatter, and never
// sets colours of its own (CodeBlock.Chroma is nil): glamour keeps such a set
// for the life of the process, so it could not follow a change of Theme.
func (t *Theme) Markdown() glamouransi.StyleConfig {
	accent := t.pal.accent
	heading := func(prefix string) glamouransi.StyleBlock {
		return glamouransi.StyleBlock{StylePrimitive: glamouransi.StylePrimitive{Prefix: prefix}}
	}

	code := glamouransi.StylePrimitive{Color: colour(accent.fg), BackgroundColor: colour(t.pal.codeFill)}
	if accent.fg == "" {
		code = glamouransi.StylePrimitive{Prefix: "`", Suffix: "`"}
	}

	block := glamouransi.StyleCodeBlock{}
	if t.ChromaFormatter() != "" {
		block.Theme = chromaLight
		if t.Caps.Background == termcaps.BgDark {
			block.Theme = chromaDark
		}
	}

	cfg := glamouransi.StyleConfig{
		BlockQuote: glamouransi.StyleBlock{
			StylePrimitive: primitive(t.pal.dim),
			Indent:         new(uint(1)),
			IndentToken:    new(t.G.Sep + " "),
		},
		List: glamouransi.StyleList{LevelIndent: 2},
		Heading: glamouransi.StyleBlock{StylePrimitive: glamouransi.StylePrimitive{
			BlockSuffix: "\n",
			Color:       colour(accent.fg),
			Bold:        new(true),
		}},
		H1:            heading("# "),
		H2:            heading("## "),
		H3:            heading("### "),
		H4:            heading("#### "),
		H5:            heading("##### "),
		H6:            heading("###### "),
		Strikethrough: glamouransi.StylePrimitive{CrossedOut: new(true)},
		Emph:          glamouransi.StylePrimitive{Italic: new(true)},
		Strong:        glamouransi.StylePrimitive{Bold: new(true)},
		HorizontalRule: with(primitive(t.pal.rule), func(p *glamouransi.StylePrimitive) {
			p.Format = "\n" + strings.Repeat(t.G.HRule, ruleCells) + "\n"
		}),
		Item:        glamouransi.StylePrimitive{BlockPrefix: t.G.Bullet + " "},
		Enumeration: glamouransi.StylePrimitive{BlockPrefix: ". "},
		Task:        glamouransi.StyleTask{Ticked: t.G.CheckOn + " ", Unticked: t.G.CheckOff + " "},
		Link: with(primitive(t.pal.dim), func(p *glamouransi.StylePrimitive) {
			p.Underline = new(true)
		}),
		LinkText: primitive(t.pal.tool),
		Image: with(primitive(t.pal.dim), func(p *glamouransi.StylePrimitive) {
			p.Underline = new(true)
		}),
		ImageText: with(primitive(t.pal.dim), func(p *glamouransi.StylePrimitive) {
			p.Format = "Image: {{.text}} " + t.G.Arrow
		}),
		Code:                  glamouransi.StyleBlock{StylePrimitive: code},
		CodeBlock:             block,
		DefinitionDescription: glamouransi.StylePrimitive{BlockPrefix: "\n" + t.G.Arrow + " "},
	}
	if t.Caps.Glyphs != termcaps.GlyphsUnicode {
		// Left unset, a table is ruled with box-drawing characters.
		cfg.Table = glamouransi.StyleTable{
			CenterSeparator: new("+"),
			ColumnSeparator: new("|"),
			RowSeparator:    new("-"),
		}
	}
	return cfg
}

// primitive is a role as glamour takes it: its colour, and bold. glamour draws
// nothing faint, so a role that is only faint is plain text in a reply.
func primitive(l look) glamouransi.StylePrimitive {
	p := glamouransi.StylePrimitive{Color: colour(l.fg)}
	if l.bold {
		p.Bold = new(true)
	}
	return p
}

// with returns p after change has set more of it.
func with(p glamouransi.StylePrimitive, change func(*glamouransi.StylePrimitive)) glamouransi.StylePrimitive {
	change(&p)
	return p
}

// colour is a colour as glamour takes it: nil for none.
func colour(c string) *string {
	if c == "" {
		return nil
	}
	return &c
}
