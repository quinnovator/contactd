package stage

import (
	"strings"

	"github.com/quinnovator/contactd/internal/tui/canvas"
)

// Anchor is a place on the stage: one of nine, or wherever there is room.
type Anchor uint8

const (
	// Auto is where there is room: beside the avatar on a wide stage, and
	// over it on a narrow one.
	Auto Anchor = iota
	TopLeft
	Top
	TopRight
	Left
	Center
	Right
	BottomLeft
	Bottom
	BottomRight
)

// Place reads a place as a reply names it: "top", "bottom", "left", "right"
// or "center", or one of the first two with one of the second, in either
// order, with a space or a hyphen between them. "upper", "lower", "middle"
// and "centre" are understood too. ok is false for anything else, and the
// place is then Auto.
func Place(words string) (a Anchor, ok bool) {
	const (
		none = iota
		first
		middle
		last
	)
	row, col, centred := none, none, false
	fields := strings.FieldsFunc(strings.ToLower(words), func(r rune) bool {
		return r == ' ' || r == '-' || r == '_' || r == '\t'
	})
	if len(fields) == 0 || len(fields) > 2 {
		return Auto, false
	}
	for _, f := range fields {
		var (
			at *int
			to int
		)
		switch f {
		case "top", "upper":
			at, to = &row, first
		case "bottom", "lower":
			at, to = &row, last
		case "left":
			at, to = &col, first
		case "right":
			at, to = &col, last
		case "center", "centre", "middle":
			// The middle of whichever way the other word does not go.
			centred = true
			continue
		default:
			return Auto, false
		}
		if *at != none {
			return Auto, false
		}
		*at = to
	}
	if row == none {
		row = middle
	}
	if col == none {
		col = middle
	}
	if row == middle && col == middle && !centred {
		return Auto, false
	}
	return [...][3]Anchor{
		{TopLeft, Top, TopRight},
		{Left, Center, Right},
		{BottomLeft, Bottom, BottomRight},
	}[row-first][col-first], true
}

// across and down say where an anchor stands on each axis: -1 at the near
// edge, 0 in the middle, 1 at the far one.
func (a Anchor) across() int {
	switch a {
	case TopLeft, Left, BottomLeft:
		return -1
	case TopRight, Right, BottomRight:
		return 1
	case Auto, Top, Center, Bottom:
	}
	return 0
}

func (a Anchor) down() int {
	switch a {
	case TopLeft, Top, TopRight:
		return -1
	case BottomLeft, Bottom, BottomRight:
		return 1
	case Auto, Left, Center, Right:
	}
	return 0
}

const (
	// minFaceCols and minFaceRows are the smallest picture of the avatar
	// that is still a face. A stage with less room has the words alone.
	minFaceCols = 10
	minFaceRows = 4
	// maxFaceRows is the tallest the avatar is drawn.
	maxFaceRows = 34
	// maxTextCols and maxTextRows are the measure of the words under the
	// avatar, and textMargin the cells kept clear beside them.
	maxTextCols = 68
	maxTextRows = 8
	textMargin  = 4
	// gap is the rows between the avatar and its words.
	gap = 1
	// minBlockCols and minBlockRows are the room the avatar and its words
	// are left beside or under an exhibit.
	minBlockCols = 26
	minBlockRows = 7
	// wideFrom is the width from which an exhibit with no place of its own
	// stands beside the avatar.
	wideFrom = 72
	// cardMargin is the cells kept clear between an exhibit and the edge of
	// the stage it stands at.
	cardMargin = 2
	// minStripCols and minStripRows are the least room the words can be
	// read in.
	minStripCols = 12
	minStripRows = 2
)

// rect is a box of cells on the stage.
type rect struct{ x, y, w, h int }

func (r rect) meets(o rect) bool {
	return r.x < o.x+o.w && o.x < r.x+r.w && r.y < o.y+o.h && o.y < r.y+r.h
}

// card is an exhibit on the stage.
type card struct {
	ex   Exhibit
	turn uint64
	// rows is what the exhibit drew, for a box of cols by most cells, and
	// drawn says that is still good.
	rows       []string
	cols, most int
	drawn      bool
	// on says the card has a place in the plan: x and y, w cells wide and h
	// of its rows high.
	on         bool
	x, y, w, h int
}

// plan is where everything on the stage stands.
type plan struct {
	// above says the first row is the line over everything.
	above bool
	// block is the room the avatar and its words have, face the avatar's
	// picture and text the words. below is how many of the caller's lines
	// stand between the two, the first of them on the row belowY.
	block, face, text rect
	below, belowY     int
}

// block is how the avatar and its words share a box.
type block struct {
	// top is the empty rows above the avatar, cols and rows its picture, and
	// textW and textH the words under it.
	top, cols, rows, textW, textH int
}

// share stacks the avatar over its words in a box of w by h cells, of which
// below rows are taken by the caller's lines between the two. The avatar takes all
// the room it can use and leaves the words a comfortable measure and a
// handful of lines. In a box with no room for a face the words stand alone.
func share(w, h, below int) block {
	h = max(h-below, 0)
	b := block{textW: max(min(w-textMargin, maxTextCols), 1)}
	room := h - gap
	if w < minFaceCols || room < minFaceRows+1 {
		b.textH = min(h, maxTextRows)
		b.top = (h - b.textH) / 2
		return b
	}
	b.rows = min(max(room-6, room*2/3, minFaceRows), maxFaceRows)
	// The picture is a little wider than it is tall, and a cell twice as
	// tall as it is wide. A box too narrow for that has a smaller avatar.
	half := min(b.rows*5/4, w/2)
	b.rows = min(b.rows, max(half*4/5, minFaceRows))
	b.cols = half * 2
	b.textH = min(max(room-b.rows, 1), maxTextRows)
	b.top = max(room-b.rows-b.textH, 0) / 2
	return b
}

// arrange makes the plan again, if anything it follows from has changed.
func (s *Stage) arrange() {
	if !s.stale {
		return
	}
	s.stale = false
	p := plan{above: s.above != "" && s.height >= 3}
	area := rect{w: s.width, h: s.height}
	if p.above {
		area.y, area.h = 1, s.height-1
	}
	p.below = min(len(s.below), max(area.h-1, 0))

	p.block = s.place(area)
	b := share(p.block.w, p.block.h, p.below)
	p.belowY = p.block.y + b.top
	if b.rows > 0 {
		p.face = rect{x: p.block.x + (p.block.w-b.cols)/2, y: p.block.y + b.top, w: b.cols, h: b.rows}
		s.canvas = canvas.New(b.cols, b.rows, s.grid)
		p.belowY = p.face.y + b.rows + gap
	}
	p.text = rect{x: p.block.x + max(p.block.w-b.textW, 0)/2, y: p.belowY + p.below, w: b.textW, h: b.textH}
	s.plan = p
	s.glance()
}

// place gives every card its place in the area, and returns the room that
// is left for the avatar and its words: the best of the four strips around
// the cards. A card that stands where a later one does is taken down, and
// so is the oldest, for as long as no strip is left to read the words in.
func (s *Stage) place(area rect) rect {
	for _, c := range s.cards {
		c.on = false
	}
	// A stage with no room to spare shows nothing but the avatar's words.
	if area.h < minBlockRows+1 || area.w < minStripCols {
		return area
	}
	var placed []*card
	for _, c := range s.cards {
		if !s.fit(c, area) {
			continue
		}
		kept := placed[:0]
		for _, o := range placed {
			if !o.box().meets(c.box()) {
				kept = append(kept, o)
			}
		}
		placed = append(kept, c)
	}
	for {
		if len(placed) == 0 {
			return area
		}
		if strip, ok := best(area, placed); ok {
			for _, c := range placed {
				c.on = true
			}
			return strip
		}
		placed = placed[1:]
	}
}

func (c *card) box() rect { return rect{c.x, c.y, c.w, c.h} }

// fit draws a card for the area and stands it at its anchor. It reports
// false for a card that has nothing to show.
func (s *Stage) fit(c *card, area rect) bool {
	where := c.ex.Where
	if where == Auto {
		where = Top
		if area.w >= wideFrom {
			where = Right
		}
	}
	// Beside the avatar a card may be as tall as the stage. One that leaves
	// no room beside it stands over or under the avatar, and is cut to
	// leave the avatar its rows.
	cols, most := area.w, area.h
	if where.across() != 0 && area.w >= minBlockCols+minStripCols {
		cols = area.w - minBlockCols - cardMargin
	}
	s.draw(c, cols, most)
	if c.w == 0 {
		return false
	}
	if area.w-c.w-cardMargin < minBlockCols {
		s.draw(c, cols, area.h-minBlockRows)
	}
	c.h = min(len(c.rows), c.most)
	if c.w == 0 || c.h == 0 {
		return false
	}
	switch where.across() {
	case -1:
		c.x = area.x + min(cardMargin, area.w-c.w)
	case 1:
		c.x = area.x + max(area.w-c.w-cardMargin, 0)
	default:
		c.x = area.x + (area.w-c.w)/2
	}
	switch where.down() {
	case -1:
		c.y = area.y
	case 1:
		c.y = area.y + area.h - c.h
	default:
		c.y = area.y + (area.h-c.h)/2
	}
	return true
}

// draw has a card's exhibit drawn for a box, unless what it drew for that
// box is still good, and measures it.
func (s *Stage) draw(c *card, cols, most int) {
	cols, most = max(cols, 1), max(most, 0)
	if c.drawn && c.cols == cols && c.most == most {
		return
	}
	c.rows, c.cols, c.most, c.drawn, c.w = nil, cols, most, true, 0
	if c.ex.Draw != nil && most > 0 {
		c.rows = c.ex.Draw(cols, most)
	}
	for i, row := range c.rows {
		w := s.th.Width(row)
		if w > cols {
			row = s.th.Truncate(row, cols)
			c.rows[i], w = row, s.th.Width(row)
		}
		c.w = max(c.w, w)
	}
}

// best is the strip around the cards that suits the avatar best: the one it
// can be drawn largest in, and the roomiest among equals. ok is false when
// none has room for the words.
func best(area rect, cards []*card) (strip rect, ok bool) {
	// The box that holds every card.
	hold := cards[0].box()
	for _, c := range cards[1:] {
		b := c.box()
		x, y := min(hold.x, b.x), min(hold.y, b.y)
		hold = rect{x, y, max(hold.x+hold.w, b.x+b.w) - x, max(hold.y+hold.h, b.y+b.h) - y}
	}
	right, bottom := hold.x+hold.w, hold.y+hold.h
	score := -1
	for _, r := range [...]rect{
		{area.x, area.y, hold.x - area.x, area.h},
		{right, area.y, area.x + area.w - right, area.h},
		{area.x, area.y, area.w, hold.y - area.y},
		{area.x, bottom, area.w, area.y + area.h - bottom},
	} {
		if r.w < minStripCols || r.h < minStripRows {
			continue
		}
		if n := share(r.w, r.h, 0).rows*1_000_000 + r.w*r.h; n > score {
			strip, score, ok = r, n, true
		}
	}
	return strip, ok
}

// glance has the avatar look at what was just put on the stage.
func (s *Stage) glance() {
	c, f := s.look, s.plan.face
	s.look = nil
	if c == nil || !c.on || f.h == 0 {
		return
	}
	// A cell is twice as tall as it is wide.
	dx := float64(2*c.x+c.w-2*f.x-f.w) / float64(f.w)
	dy := float64(2*c.y+c.h-2*f.y-f.h) / float64(f.h)
	s.avatar.LookAt(dx, dy)
}
