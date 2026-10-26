package theme

import (
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"

	"github.com/quinnovator/contactd/internal/tui/safe"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
)

// maxGlyphCells is the widest a replacement glyph may be.
const maxGlyphCells = 3

// Glyphs are the symbols the client draws with. Each is one string, and not
// always one cell: an ASCII stand-in can be wider than the symbol it stands
// for, so a layout measures the glyph it places with Theme.Width.
type Glyphs struct {
	Wire string
	// Prompt marks the input and your own messages. Task marks the message
	// that gave a sub-agent its task.
	Prompt, Task string
	// Tool marks a tool call and Elbow its result.
	Tool, Elbow string
	// Stopped marks a reply that was interrupted, Notice a notice from the
	// runtime, and Fail a turn that failed.
	Stopped, Notice, Fail string
	// Pointer is the cursor of a list. Dot shows the connection's state.
	Pointer, Dot string
	// Sep stands between status-bar cells and between columns. HRule is
	// what a rule is drawn with.
	Sep, HRule string
	// Ellipsis ends text that was cut, and "running…". Mid stands between
	// key hints. Bullet starts a list item.
	Ellipsis, Mid, Bullet string
	// Newer says there are newer lines below. UpDown and LeftRight name the
	// arrow keys in a hint.
	Newer, UpDown, LeftRight string
	// Dash joins the two ends of a date range. Times joins the two sides of
	// a size. Arrow follows an image's or a link's text.
	Dash, Times, Arrow string
	// CheckOn and CheckOff are a toggle that is on and one that is off.
	CheckOn, CheckOff string
	// HalfBlock is the upper half block that block images are made of. It
	// is "" in the ASCII set, where images are text.
	HalfBlock string
	// BoxTL, BoxTR, BoxBL and BoxBR are the corners of a box, BoxH its
	// horizontal edge and BoxV its vertical one.
	BoxTL, BoxTR, BoxBL, BoxBR, BoxH, BoxV string
}

// GlyphSet returns a glyph set as designed, before any override. The ASCII
// set holds no byte above 0x7F. No glyph is followed by a variation selector
// (U+FE0F), which would make some terminals draw it two cells wide.
func GlyphSet(g termcaps.Glyphs) Glyphs {
	switch g {
	case termcaps.GlyphsUnicode:
		return Glyphs{
			Wire: "⇄", Prompt: "❯", Task: "⇢", Tool: "⏺", Elbow: "⎿",
			Stopped: "⏹", Notice: "◆", Fail: "✗", Pointer: "▸", Dot: "●",
			Sep: "│", HRule: "─", Ellipsis: "…", Mid: "·", Bullet: "•",
			Newer: "↓", UpDown: "↑↓", LeftRight: "←→",
			Dash: "–", Times: "×", Arrow: "→",
			CheckOn: "[x]", CheckOff: "[ ]", HalfBlock: "▀",
			BoxTL: "╭", BoxTR: "╮", BoxBL: "╰", BoxBR: "╯", BoxH: "─", BoxV: "│",
		}
	case termcaps.GlyphsASCII:
		return asciiGlyphs()
	}
	return asciiGlyphs()
}

// asciiGlyphs is the set for a terminal, a font or a locale that cannot be
// trusted with anything else.
func asciiGlyphs() Glyphs {
	return Glyphs{
		Wire: "<>", Prompt: ">", Task: "->", Tool: "*", Elbow: "|_",
		Stopped: "--", Notice: "+", Fail: "x", Pointer: ">", Dot: "*",
		Sep: "|", HRule: "-", Ellipsis: "...", Mid: "|", Bullet: "-",
		Newer: "v", UpDown: "up/down", LeftRight: "left/right",
		Dash: "-", Times: "x", Arrow: "->",
		CheckOn: "[x]", CheckOff: "[ ]", HalfBlock: "",
		BoxTL: "+", BoxTR: "+", BoxBL: "+", BoxBR: "+", BoxH: "-", BoxV: "|",
	}
}

// slot is one glyph that can be replaced: the name chat.toml uses for it, and
// where it is kept.
type slot struct {
	name string
	at   *string
}

// slots lists the glyphs of g that can be replaced. HalfBlock is not among
// them: a block image is only a picture when each cell is that one character.
func (g *Glyphs) slots() []slot {
	return []slot{
		{"wire", &g.Wire},
		{"prompt", &g.Prompt},
		{"task", &g.Task},
		{"tool", &g.Tool},
		{"elbow", &g.Elbow},
		{"stopped", &g.Stopped},
		{"notice", &g.Notice},
		{"fail", &g.Fail},
		{"pointer", &g.Pointer},
		{"dot", &g.Dot},
		{"sep", &g.Sep},
		{"hrule", &g.HRule},
		{"ellipsis", &g.Ellipsis},
		{"mid", &g.Mid},
		{"bullet", &g.Bullet},
		{"newer", &g.Newer},
		{"updown", &g.UpDown},
		{"leftright", &g.LeftRight},
		{"dash", &g.Dash},
		{"times", &g.Times},
		{"arrow", &g.Arrow},
		{"checkon", &g.CheckOn},
		{"checkoff", &g.CheckOff},
		{"boxtl", &g.BoxTL},
		{"boxtr", &g.BoxTR},
		{"boxbl", &g.BoxBL},
		{"boxbr", &g.BoxBR},
		{"boxh", &g.BoxH},
		{"boxv", &g.BoxV},
	}
}

// Check lists the glyph overrides that New does not apply, one line each, in
// name order: a name that is not a glyph's, or a value that cannot be drawn
// safely. It returns nil when every override is applied.
//
// A name is a field of Glyphs other than HalfBlock, in any case, with or
// without "_" or "-" between its words: "tool", "box_tl", "check-on". A value
// is accepted when it is 1 to 3 cells wide and holds no control character,
// no character that changes the direction of text, no joiner and no variation
// selector.
func Check(o Options) []string {
	var g Glyphs
	return override(&g, o)
}

// override puts the accepted overrides into g and returns a line for each
// one it refused. Names are taken in order, so the result does not depend on
// the order a map gives them in.
func override(g *Glyphs, o Options) []string {
	if len(o.Glyph) == 0 {
		return nil
	}
	slots := g.slots()
	names := make([]string, 0, len(o.Glyph))
	for name := range o.Glyph {
		names = append(names, name)
	}
	slices.Sort(names)

	var refused []string
	for _, name := range names {
		value := o.Glyph[name]
		i := slices.IndexFunc(slots, func(s slot) bool { return s.name == glyphName(name) })
		if i < 0 {
			refused = append(refused, "glyph "+strconv.QuoteToASCII(name)+": there is no glyph of that name")
			continue
		}
		if why := glyphFault(value); why != "" {
			refused = append(refused, "glyph "+strconv.QuoteToASCII(name)+": "+strconv.QuoteToASCII(value)+" "+why)
			continue
		}
		*slots[i].at = value
	}
	return refused
}

// glyphName is the form override names are compared in: lower case, with no
// "_" or "-".
func glyphName(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "_", "")
	return strings.ReplaceAll(s, "-", "")
}

// glyphFault says why a value cannot replace a glyph, or "" when it can. The
// width is checked both ways a terminal may count, since the client changes
// from one to the other when the terminal says how it counts.
func glyphFault(v string) string {
	if v == "" {
		return "is empty"
	}
	if !utf8.ValidString(v) {
		return "is not UTF-8"
	}
	if safe.Clean(v, true) != v || strings.ContainsFunc(v, func(r rune) bool {
		return unicode.IsControl(r) || unicode.Is(unicode.Cf, r)
	}) {
		return "holds a character that is not drawn"
	}
	for _, w := range []int{ansi.StringWidthWc(v), ansi.StringWidth(v)} {
		if w < 1 || w > maxGlyphCells {
			return "is " + strconv.Itoa(w) + " cells wide; a glyph is 1 to " + strconv.Itoa(maxGlyphCells)
		}
	}
	return ""
}
