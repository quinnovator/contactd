package stage

import (
	"strings"

	"github.com/quinnovator/contactd/internal/tui/canvas"
)

// Anchor is a place on the stage: one of nine, or wherever there is room.
type Anchor uint8

const (
	// Auto chooses columns, stacks or a grid from the content dimensions
	// and the available stage space.
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
	clipped    bool
	sample     Size
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
	overflow          string
	a2a               rect
	peers             []peerPlace
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
	b.rows = min(max(room-maxTextRows, room*2/3, minFaceRows), maxFaceRows)
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

	p.block, p.overflow = s.compose(area)
	body := p.block
	if len(s.peers) > 0 && body.h >= 3 {
		p.a2a = rect{body.x, body.y, body.w, 1}
		body.y++
		body.h--
	}
	b := share(body.w, body.h, p.below)
	p.belowY = body.y + b.top
	if b.rows > 0 {
		p.face = rect{x: body.x + (body.w-b.cols)/2, y: body.y + b.top, w: b.cols, h: b.rows}
		p.belowY = p.face.y + b.rows + gap
		s.placePeers(&p)
		s.canvas = canvas.New(p.face.w, p.face.h, s.grid)
	}
	p.text = rect{x: p.block.x + max(p.block.w-b.textW, 0)/2, y: p.belowY + p.below, w: b.textW, h: b.textH}
	s.plan = p
	s.glance()
}

func (c *card) box() rect { return rect{c.x, c.y, c.w, c.h} }

// draw has a card's exhibit drawn for a box, unless what it drew for that
// box is still good, and measures it.
func (s *Stage) draw(c *card, cols, most int) {
	cols, most = max(cols, 1), max(most, 0)
	if c.drawn && c.cols == cols && c.most == most {
		return
	}
	c.rows, c.cols, c.most, c.drawn, c.w = nil, cols, most, true, 0
	c.clipped = false
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
