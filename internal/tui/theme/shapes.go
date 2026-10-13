package theme

import "strings"

// barPadFrom is the width from which the status bar has one cell of padding
// at each end. The status line's layout leaves room for it by the same rule.
const barPadFrom = 60

// minRuleLabel is the narrowest rule that still has a line on both sides of
// its label: two cells of line and a space, twice, and one cell of label.
const minRuleLabel = 7

// Selected draws a selected list row, padded to exactly w cells. Where the
// status bar has a fill the row has the same fill; elsewhere it is shown in
// reverse video. The row may be styled already: its colours are kept.
func (t *Theme) Selected(s string, w int) string {
	if t.BarFilled {
		return t.fill(s, w, t.barOn, backgroundOff)
	}
	return t.fill(s, w, reverseOn, reverseOff)
}

// CodeFill draws one row of a code block on the code background, padded to
// exactly w cells. The row may be highlighted already. Where there is no code
// background the row is returned as it is, cut to w cells and not padded.
func (t *Theme) CodeFill(s string, w int) string {
	if t.codeOn == "" {
		return t.Truncate(s, w)
	}
	return t.fill(s, w, t.codeOn, backgroundOff)
}

// Box draws a border around lines. The result is len(lines)+2 rows, each
// exactly w cells wide: a top edge, one row for each line with a cell of
// space on both sides of it, and a bottom edge. A line is truncated or padded
// to the w-4 cells inside (less when a border glyph was replaced by a wider
// one).
//
// With a title or a count there is one more row, the first inside the box:
// the title at the left and the count at the right, both dim.
//
// The border is drawn by hand and every row is measured with Width, so the
// right edge stays where it belongs under text the terminal and a library
// might count differently. A line is made one line as Truncate makes it, and
// a style it switches on and never off ends before the right border, so it
// reaches neither the border nor the next row. Under 5 cells there is no room for a border: the
// edges are plain rules and the lines fill the width, in as many rows.
func (t *Theme) Box(lines []string, w int, title, count string) []string {
	titled := title != "" || count != ""
	rows := len(lines) + 2
	if titled {
		rows++
	}
	out := make([]string, 0, rows)
	if w <= 0 {
		return out[:rows]
	}

	vw := t.Width(t.G.BoxV)
	inner := w - 2*vw - 2
	corners := max(t.Width(t.G.BoxTL)+t.Width(t.G.BoxTR), t.Width(t.G.BoxBL)+t.Width(t.G.BoxBR))
	if inner < 1 || corners > w {
		edge := t.border.Render(t.hline(t.G.BoxH, w))
		out = append(out, edge)
		if titled {
			out = append(out, t.Spread(t.tint(toneDim, title), t.tint(toneDim, count), w))
		}
		for _, line := range lines {
			out = append(out, t.Pad(closed(t.Truncate(line, w)), w))
		}
		return append(out, edge)
	}

	// A line is cut to the room inside, then padded with its left border on
	// and up to the right one, so that the row is measured as it will be
	// drawn: see Width. A style the line leaves on ends with the line.
	side := t.border.Render(t.G.BoxV)
	row := func(line string) string {
		return t.Pad(side+" "+closed(t.Truncate(line, inner)), w-vw) + side
	}
	out = append(out, t.border.Render(t.edge(t.G.BoxTL, t.G.BoxTR, w)))
	if titled {
		out = append(out, row(t.Spread(t.tint(toneDim, title), t.tint(toneDim, count), inner)))
	}
	for _, line := range lines {
		out = append(out, row(line))
	}
	return append(out, t.border.Render(t.edge(t.G.BoxBL, t.G.BoxBR, w)))
}

// Rule draws a rule of exactly w cells. With a label the label stands in the
// middle of it, dim, with a space on both sides; a label too long for the
// rule is truncated. Under 7 cells a labelled rule is the label alone.
func (t *Theme) Rule(w int, label string) string {
	if w <= 0 {
		return ""
	}
	if label == "" {
		return t.rule.Render(t.hline(t.G.HRule, w))
	}
	if w < minRuleLabel {
		return t.Pad(t.dim.Render(t.Truncate(label, w)), w)
	}
	label = t.Truncate(label, w-minRuleLabel+1)
	left := (w - t.Width(label) - 2) / 2
	head := t.rule.Render(t.hline(t.G.HRule, left)) + " " + t.dim.Render(label) + " "
	return t.Pad(head+t.tint(toneRule, t.hline(t.G.HRule, w-t.Width(head))), w)
}

// Bar draws the status bar: one row of exactly w cells with the left cells
// at the left edge and the right cells at the right. Cells are joined by a dim
// separator with a space on both sides, a cell with no text is left out, and
// from 60 cells up the row has one cell of padding at each end. When the two
// sides do not fit, the right one is left out, then the left is truncated.
//
// Where the bar is filled (BarFilled) the row has the bar's background and
// rule is "". Elsewhere rule is a rule row to draw above the bar.
func (t *Theme) Bar(left, right []Cell, w int) (rule, row string) {
	if w <= 0 {
		return "", ""
	}
	pad := ""
	if w >= barPadFrom {
		pad = " "
	}
	row = t.Pad(pad+t.Spread(t.cells(left), t.cells(right), w-2*len(pad)), w)
	if t.BarFilled {
		return "", t.fill(row, w, t.barOn, backgroundOff)
	}
	return t.Rule(w, ""), row
}

// cells draws one side of the status bar.
func (t *Theme) cells(cells []Cell) string {
	sep := " " + t.dim.Render(t.G.Sep) + " "
	var b strings.Builder
	for _, c := range cells {
		if c.Text == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString(sep)
		}
		b.WriteString(t.Style(c.Tone).Render(c.Text))
	}
	return b.String()
}

// tint draws s in a tone. Nothing drawn is nothing: not a pair of escape
// sequences around no text.
func (t *Theme) tint(tone, s string) string {
	if s == "" {
		return ""
	}
	return t.Style(tone).Render(s)
}

// hline is n cells of a line glyph. A glyph that was replaced by a wider one
// may not divide n; the cells left over are spaces.
func (t *Theme) hline(glyph string, n int) string {
	gw := t.Width(glyph)
	if n <= 0 || gw <= 0 {
		return strings.Repeat(" ", max(n, 0))
	}
	return strings.Repeat(glyph, n/gw) + strings.Repeat(" ", n%gw)
}

// edge is the top or the bottom of a box, w cells wide: a corner, the line,
// and the other corner.
func (t *Theme) edge(left, right string, w int) string {
	return left + t.hline(t.G.BoxH, w-t.Width(left)-t.Width(right)) + right
}
