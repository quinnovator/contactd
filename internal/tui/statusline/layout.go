package statusline

import (
	"cmp"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	// padFrom is the width from which the row has one cell of padding at
	// each end.
	padFrom = 60
	// compactBelow is the width under which only short forms are shown.
	compactBelow = 40
)

// Cell is one segment as it is to be drawn: which segment, its text and its
// tone.
type Cell struct{ ID, Text, Tone string }

// LayoutOptions say how the bar is drawn, so that Layout can tell what fits.
type LayoutOptions struct {
	// SepWidth is the width of the separator between two cells of one
	// side, spaces included. MinGap is the least space between the two
	// sides. The client uses 3 and 2.
	SepWidth, MinGap int
	// Measure returns the width of a text in cells. Nil counts runes.
	Measure func(string) int
}

// Padding is how many cells of padding a bar of this width has at each end:
// one from 60 columns, none under that. Layout leaves room for it.
func Padding(width int) int {
	if width >= padFrom {
		return 1
	}
	return 0
}

// part is one segment on its way to being a cell.
type part struct {
	cell  Cell
	short string // "" when the segment has no short form
	// isShort says the cell already shows the short form.
	isShort bool
	width   int
	drop    int
	right   bool
	// gone marks a part removed during a walk; it takes no room.
	gone bool
}

// Layout returns the cells of each side of a bar that is width cells wide.
// vals holds each segment's value, keyed by Segment.ID. Layout is pure: the
// same arguments give the same cells, and nothing it is given is changed.
//
//  1. A segment is shown when it is on and has a value with something to
//     show: text that Measure gives a width. Each side keeps the config's
//     order.
//  2. The row has Padding(width) cells at each end, SepWidth between cells
//     and at least MinGap between the sides.
//  3. Under 40 columns only segments with a short form are kept, in that
//     form.
//  4. While the row is too wide, the segments are walked by rising Drop.
//     Each in turn switches to its short form, or is removed when it has
//     none or already shows it. Of two with the same Drop, the one further
//     right goes first. A Drop of 0 never gives way. The walk stops as soon
//     as the row fits, and starts again when it ends with the row still too
//     wide. So every segment is shortened before one with a short form is
//     removed.
//  5. If the row is still too wide, the right side is removed, and then the
//     left is cut to fit.
//
// A cell in the warn tone is one whose value has a number under the
// segment's WarnBelow and no tone of its own.
func Layout(c Config, vals map[string]Value, width int, o LayoutOptions) (left, right []Cell) {
	if width <= 0 {
		return nil, nil
	}
	measure := o.Measure
	if measure == nil {
		measure = utf8.RuneCountInString
	}
	row := row{width: width - 2*Padding(width), sep: max(o.SepWidth, 0), gap: max(o.MinGap, 0)}
	parts := gather(c, vals, width < compactBelow, measure)
	for !row.fits(parts) {
		order := walk(parts)
		if len(order) == 0 {
			break
		}
		for _, i := range order {
			if p := &parts[i]; !p.isShort && p.short != "" {
				p.cell.Text, p.width, p.isShort = p.short, measure(p.short), true
			} else {
				p.gone = true
			}
			if row.fits(parts) {
				break
			}
		}
		parts = slices.DeleteFunc(parts, func(p part) bool { return p.gone })
	}
	if !row.fits(parts) {
		parts = lefts(parts)
	}
	if !row.fits(parts) {
		parts = row.cut(parts, measure)
	}
	for _, p := range parts {
		if p.right {
			right = append(right, p.cell)
		} else {
			left = append(left, p.cell)
		}
	}
	return left, right
}

// gather turns the segments that have something to show into parts: the
// left side's first, then the right's, each in the config's order.
func gather(c Config, vals map[string]Value, compact bool, measure func(string) int) []part {
	parts := make([]part, 0, len(c.Segments))
	for _, side := range []Side{Left, Right} {
		for _, s := range c.Segments {
			if !s.On || s.side() != side {
				continue
			}
			id := s.ID()
			v, ok := vals[id]
			if !ok {
				continue
			}
			full, short := s.Text(v)
			// A short form that takes no cell, such as a zero-width space,
			// is no short form.
			if short != "" && measure(short) <= 0 {
				short = ""
			}
			if full == "" || (compact && short == "") {
				continue
			}
			p := part{
				cell:  Cell{ID: id, Text: full, Tone: v.Tone},
				short: short,
				drop:  s.Drop,
				right: side == Right,
			}
			if compact {
				p.cell.Text, p.isShort = short, true
			}
			if v.Tone == "" && s.HasWarn && v.HasNum && v.Num < float64(s.WarnBelow) {
				p.cell.Tone = toneWarn
			}
			p.width = measure(p.cell.Text)
			if p.width <= 0 {
				// Nothing to see: it would be two separators side by side.
				continue
			}
			parts = append(parts, p)
		}
	}
	return parts
}

// walk returns the parts that may give way, in the order they do: by rising
// Drop, and of equals the one furthest right first. Parts are in screen
// order, so further right is a higher index.
func walk(parts []part) []int {
	order := make([]int, 0, len(parts))
	for i, p := range parts {
		if p.drop > 0 {
			order = append(order, i)
		}
	}
	slices.SortFunc(order, func(a, b int) int {
		if c := cmp.Compare(parts[a].drop, parts[b].drop); c != 0 {
			return c
		}
		return cmp.Compare(b, a)
	})
	return order
}

// lefts returns the parts of the left side.
func lefts(parts []part) []part {
	for i, p := range parts {
		if p.right {
			return parts[:i]
		}
	}
	return parts
}

// row is the room the cells have, and what goes between them.
type row struct{ width, sep, gap int }

// need is the width the parts take, with separators and the gap.
func (r row) need(parts []part) int {
	var total, nLeft, nRight int
	for _, p := range parts {
		if p.gone {
			continue
		}
		total += p.width
		if p.right {
			nRight++
		} else {
			nLeft++
		}
	}
	total += r.sep * (max(nLeft-1, 0) + max(nRight-1, 0))
	if nLeft > 0 && nRight > 0 {
		total += r.gap
	}
	return total
}

func (r row) fits(parts []part) bool { return r.need(parts) <= r.width }

// cut shortens one side's parts to the row: the cells that fit whole are
// kept, the next is cut, and the rest go.
func (r row) cut(parts []part, measure func(string) int) []part {
	used := 0
	for i := range parts {
		p := &parts[i]
		room := r.width - used
		if i > 0 {
			room -= r.sep
		}
		if p.width <= room {
			used = r.width - room + p.width
			continue
		}
		p.cell.Text = cutText(p.cell.Text, room, measure)
		if p.cell.Text == "" {
			return parts[:i]
		}
		return parts[:i+1]
	}
	return parts
}

// cutText returns the longest start of s that is at most room cells wide,
// with no space at its end. It is "" when nothing fits.
func cutText(s string, room int, measure func(string) int) string {
	if room <= 0 {
		return ""
	}
	for len(s) > 0 {
		_, size := utf8.DecodeLastRuneInString(s)
		s = strings.TrimRight(s[:len(s)-size], " ")
		if s != "" && measure(s) <= room {
			return s
		}
	}
	return ""
}

// DropOrdinal is the segment's place in the order its side gives way in:
// 1 for the first to go, as in "3rd when narrow". It counts, on the side of
// segment i, the segments that are on and give way before it, plus one. It
// is 0 when the segment never gives way, and when there is no segment i.
func DropOrdinal(c Config, i int) int {
	if i < 0 || i >= len(c.Segments) || c.Segments[i].Drop <= 0 {
		return 0
	}
	s := c.Segments[i]
	n := 1
	for j, o := range c.Segments {
		if j == i || !o.On || o.Drop <= 0 || o.side() != s.side() {
			continue
		}
		if o.Drop < s.Drop || (o.Drop == s.Drop && j > i) {
			n++
		}
	}
	return n
}
