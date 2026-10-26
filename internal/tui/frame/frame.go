// Package frame is the shape of the chat screen: which rows it has at a given
// terminal size, what the header and the hint line say, and how the drawn
// regions are joined into one screen.
//
// The screen is a single column. From the top: a header, a rule, the
// transcript, the command palette while it is open, the status bar (with a
// rule above it where the bar has no fill), the input, and a line of key
// hints. Layout decides how many rows each gets, Compose joins what was drawn
// for them, and neither knows anything about a conversation. Everything here
// is a pure function of its arguments.
package frame

import "strings"

// The heights and the width from which a part of the screen is shown. Below
// them the part gives its row to the transcript.
const (
	// headerFrom is the height from which the header is shown.
	headerFrom = 6
	// ruleFrom is the height from which the rule under the header is drawn,
	// and the rule above a status bar that has no fill.
	ruleFrom = 8
	// roomyFrom is the height from which the input may have its box and the
	// hint line is shown.
	roomyFrom = 16
	// boxFrom is the width from which the input may have its box.
	boxFrom = 40
	// barFrom is the height from which the status bar is shown. Under it
	// there is room for the transcript and the input only.
	barFrom = 3
)

// How the input and the palette share the screen with the transcript.
const (
	// maxInputText is the most rows of text the input ever shows.
	maxInputText = 8
	// inputBase and inputShare give the input a third of what is left of
	// the height once inputBase rows are set aside.
	inputBase  = 6
	inputShare = 3
	// boxBorders is the rows a box adds to the input: its top and bottom.
	boxBorders = 2
	// bodyKeep is the transcript rows the palette never takes.
	bodyKeep = 3
)

// The image box: the most cells a picture in a reply may take.
const (
	// imageCols and imageRows are the box on a roomy terminal.
	imageCols = 44
	imageRows = 12
	// imageMargin is the columns kept clear of a picture: the reply's indent
	// of two and as much on the right.
	imageMargin = 4
	// imageMinRows is the fewest rows the box has, however short the
	// terminal.
	imageMinRows = 2
	// imageShare gives a picture a third of the terminal's height.
	imageShare = 3
)

// Rows is the row plan of the chat screen: how many rows each part gets, from
// the top. A part that is not shown has zero rows.
type Rows struct {
	// Header and HeaderRule are the header and the rule under it. Body is
	// the transcript. Palette is the command palette, zero while it is
	// closed. BarRule is the rule above a status bar that has no fill, and
	// Bar the status bar. Input is the input with its box borders, when it
	// has a box. Hint is the line of key hints.
	Header, HeaderRule, Body, Helpers, Palette, BarRule, Bar, Input, Hint int
	// Boxed says the input is drawn in a box. Otherwise it is the prompt and
	// the text alone.
	Boxed bool
	// InputText is the rows of text inside the input.
	InputText int
}

// Height returns the number of rows in the plan. For a plan made by Layout it
// is the height Layout was given, or zero for a height under one.
func (r Rows) Height() int {
	return r.Header + r.HeaderRule + r.Body + r.Helpers + r.Palette + r.BarRule + r.Bar + r.Input + r.Hint
}

// InputTop returns the screen row of the input's first line, counted from
// zero: the number of rows above the input. The cursor's row is this plus its
// row inside the input.
func (r Rows) InputTop() int {
	return r.Header + r.HeaderRule + r.Body + r.Helpers + r.Palette + r.BarRule + r.Bar
}

// Layout plans the rows of the chat screen for a terminal of width by height
// cells. inputLines is the number of lines in the draft, paletteWant the rows
// the command palette asks for (zero while it is closed), and barFilled
// whether the status bar has a background of its own (theme.Theme.BarFilled).
//
// Layout is pure. For a height of three or more the rows sum to the height,
// Body is at least one and InputText is at least one. A part is shown from a
// size up, and gives its row to the transcript below it:
//
//   - the header from 6 rows, and the rule under it from 8;
//   - the rule above the bar from 8 rows, and only when the bar has no fill;
//   - the box around the input from 40 columns and 16 rows;
//   - the hint line from 16 rows.
//
// The input shows every line of the draft up to a third of the height over
// six, and never more than eight; it is never the part that is clipped. The
// palette gets what it asks for, less whatever keeps three rows of
// transcript. The transcript gets the rest.
//
// A terminal of two rows has one row of transcript and the input, one of one
// row has the input alone, and a height under one has no rows at all. No count
// is ever negative.
func Layout(width, height, inputLines, paletteWant int, barFilled bool, helpersWant ...int) Rows {
	var r Rows
	if height <= 0 {
		return r
	}
	r.Boxed = BoxedAt(width, height)
	limit := min(maxInputText, max(1, (height-inputBase)/inputShare))
	r.InputText = min(max(inputLines, 1), limit)
	r.Input = r.InputText
	if r.Boxed {
		r.Input += boxBorders
	}
	if height >= barFrom {
		r.Bar = 1
	}
	if height >= headerFrom {
		r.Header = 1
	}
	if height >= ruleFrom {
		r.HeaderRule = 1
		if !barFilled {
			r.BarRule = 1
		}
	}
	if height >= roomyFrom {
		r.Hint = 1
	}
	rest := max(height-r.Height(), 0)
	r.Palette = min(max(paletteWant, 0), max(rest-bodyKeep, 0))
	r.Body = rest - r.Palette
	if height >= 16 && len(helpersWant) > 0 {
		r.Helpers = min(max(helpersWant[0], 0), 4, max(r.Body-5, 0))
		r.Body -= r.Helpers
	}
	return r
}

// BoxedAt reports whether the input is drawn in a box on a terminal of width
// by height cells: from 40 columns and 16 rows. On anything smaller it is the
// prompt and the text alone.
func BoxedAt(width, height int) bool {
	return width >= boxFrom && height >= roomyFrom
}

// Regions is what was drawn for each part of the screen. A part of one row is
// a string, a part of several a slice with one string for each row.
type Regions struct {
	// Header is the header row, HeaderRule the rule under it, BarRule the
	// rule above the status bar, Bar the status bar and Hint the hint line.
	Header, HeaderRule, BarRule, Bar, Hint string
	// Body is the transcript's rows, Palette the command palette's and
	// Input the input's, box borders included.
	Body, Helpers, Palette, Input []string
}

// Compose joins the regions into one screen, top to bottom, with a newline
// between rows and none at the end. The result has exactly the rows the plan
// gives each part, whatever the regions hold: a part with no rows in the plan
// is left out, a part with fewer lines than planned is filled up with empty
// rows below it, and one with more is cut after its first planned rows. Only
// the first line of a string that holds a newline is drawn, so one region can
// never push the others down the screen.
func Compose(r Rows, g Regions) string {
	var j joiner
	j.b.Grow(size(g))
	j.one(r.Header, g.Header)
	j.one(r.HeaderRule, g.HeaderRule)
	j.many(r.Body, g.Body)
	j.many(r.Helpers, g.Helpers)
	j.many(r.Palette, g.Palette)
	j.one(r.BarRule, g.BarRule)
	j.one(r.Bar, g.Bar)
	j.many(r.Input, g.Input)
	j.one(r.Hint, g.Hint)
	return j.b.String()
}

// size is the bytes the regions hold, with one more for each row: enough for
// the screen they are joined into.
func size(g Regions) int {
	const single = 5
	n := len(g.Header) + len(g.HeaderRule) + len(g.BarRule) + len(g.Bar) + len(g.Hint) + single
	for _, lines := range [][]string{g.Body, g.Helpers, g.Palette, g.Input} {
		for _, l := range lines {
			n += len(l) + 1
		}
	}
	return n
}

// joiner writes the rows of a screen one under the other.
type joiner struct {
	b     strings.Builder
	begun bool
}

// row writes one row: the first line of s.
func (j *joiner) row(s string) {
	if j.begun {
		j.b.WriteByte('\n')
	}
	j.begun = true
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	j.b.WriteString(s)
}

// one writes a part that is drawn as one row. A plan that gives it more rows
// gets empty rows under it.
func (j *joiner) one(n int, s string) {
	for i := range n {
		if i > 0 {
			s = ""
		}
		j.row(s)
	}
}

// many writes n rows of a part: its lines, cut or filled up with empty rows.
func (j *joiner) many(n int, lines []string) {
	for i := range n {
		if i < len(lines) {
			j.row(lines[i])
		} else {
			j.row("")
		}
	}
}

// ImageBox returns the most cells a picture in a reply may take on a terminal
// of width by height cells: 44 columns or the width less four, and 12 rows or
// a third of the height, but never under two rows. It depends on the terminal
// size only, so a growing draft or an open palette never changes the size of
// a picture. The answer is at least one column by two rows, whatever is
// passed.
func ImageBox(width, height int) (maxCols, maxRows int) {
	maxCols = max(1, min(imageCols, width-imageMargin))
	maxRows = min(imageRows, max(imageMinRows, height/imageShare))
	return maxCols, maxRows
}
