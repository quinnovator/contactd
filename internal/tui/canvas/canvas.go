// Package canvas is a small software framebuffer for the terminal.
//
// Every character cell is divided into pixels and drawn with a block glyph:
// the glyph's foreground colours some of the cell's pixels and its background
// the rest. A glyph can only show two colours, so each cell is encoded as the
// two colours that best summarise its pixels plus the glyph that says which
// pixel takes which. Smooth shading survives almost untouched, and edges land
// with several times the precision of the cell grid.
package canvas

import (
	"strconv"
	"strings"
)

// Grid is how finely a character cell is divided into pixels.
type Grid int

const (
	// Quadrants divides a cell 2×2, using block characters every terminal
	// has. The pixels are twice as tall as they are wide.
	Quadrants Grid = iota
	// Octants divides a cell 2×4 into square pixels. It needs a terminal
	// that draws the octant characters added in Unicode 16.
	Octants
)

// Samples per cell. Cells are about twice as tall as they are wide, so this
// makes samples square. An octant shows each one; a quadrant shows two.
const across, down = 2, 4

// Canvas is a grid of shader samples that encodes to character cells.
type Canvas struct {
	grid Grid
	w, h int
	px   []Sample
}

// New returns a canvas that fills cols×rows character cells.
func New(cols, rows int, grid Grid) *Canvas {
	w, h := max(cols, 1)*across, max(rows, 1)*down
	return &Canvas{grid: grid, w: w, h: h, px: make([]Sample, w*h)}
}

// Pixel is the spacing of samples in shader units, which makes it the natural
// width for anti-aliased edges.
func (c *Canvas) Pixel() float64 { return 2 / float64(min(c.w, c.h)) }

// Shade evaluates f at every sample. Coordinates are centred on the canvas
// with y pointing down, scaled so the shorter side spans [-1, 1].
func (c *Canvas) Shade(f func(x, y float64) Sample) {
	step := c.Pixel()
	for j := range c.h {
		y := (float64(j) + 0.5 - float64(c.h)/2) * step
		row := c.px[j*c.w : (j+1)*c.w]
		for i := range row {
			row[i] = f((float64(i)+0.5-float64(c.w)/2)*step, y)
		}
	}
}

// Render encodes the frame as ANSI text, one line per row of cells, resolving
// translucent pixels against bg. Pixels nothing was painted on are left
// unstyled so the terminal's own background shows through.
func (c *Canvas) Render(bg Color) string {
	p := pen{fg: unset, bg: unset}
	p.out.Grow(len(c.px) * 5)
	for row := 0; row < c.h; row += down {
		if row > 0 {
			p.out.WriteString("\n")
		}
		for col := 0; col < c.w; col += across {
			var cell [across * down]Sample
			for i := range cell {
				cell[i] = c.px[(row+i/across)*c.w+col+i%across]
			}
			if c.grid == Quadrants {
				// A quadrant is two samples tall: give both their average,
				// and the cell can only come out as a quadrant pattern.
				for _, i := range [...]int{0, 1, 4, 5} {
					cell[i] = mean(cell[i], cell[i+across])
					cell[i+across] = cell[i]
				}
			}
			p.cell(encode(cell, bg))
		}
		p.reset()
	}
	return p.out.String()
}

// glyphs maps a mask of foreground pixels to the character that draws it. The
// low bit is the upper-left pixel; bits run left to right, then top to
// bottom.
//
// Unicode 16 added a block of octant characters in exactly this order, but
// only for the patterns that had no character yet. The rest, among them
// every quadrant pattern, keep the characters they already had.
//
//nolint:gochecknoglobals // a table, made once and only read
var glyphs = func() (table [1 << (across * down)]rune) {
	existing := map[int]rune{
		0x00: ' ', 0xFF: '█',
		0x0F: '▀', 0xF0: '▄', 0x55: '▌', 0xAA: '▐',
		0x05: '▘', 0x0A: '▝', 0x50: '▖', 0xA0: '▗',
		0x5A: '▞', 0xA5: '▚', 0x5F: '▛', 0xAF: '▜', 0xF5: '▙', 0xFA: '▟',
		0x03: '\U0001FB82', 0x3F: '\U0001FB85', 0xC0: '▂', 0xFC: '▆', // top or bottom quarter, three quarters
		0x01: '\U0001CEA8', 0x02: '\U0001CEAB', 0x40: '\U0001CEA3', 0x80: '\U0001CEA0', // one corner eighth
		0x14: '\U0001FBE6', 0x28: '\U0001FBE7', // middle of the left or right half
	}
	octant := rune(0x1CD00)
	for mask := range table {
		if r, ok := existing[mask]; ok {
			table[mask] = r
		} else {
			table[mask] = octant
			octant++
		}
	}
	return table
}()

const (
	clear = 1.0 / 255         // coverage below which a pixel cannot change an 8-bit colour
	flat  = 3 * clear * clear // squared colour distance too small to be worth a glyph
)

// encode reduces the pixels of a cell to a glyph and two colours. The two
// most different pixels seed two groups, every pixel joins the group it is
// nearer to, and each group is drawn in its average colour.
func encode(px [across * down]Sample, backdrop Color) (glyph rune, fg, bg int32) {
	var col [len(px)]Color
	for i, s := range px {
		col[i] = s.on(backdrop)
	}
	a, b, far := 0, 0, flat
	for i := range col {
		for j := i + 1; j < len(col); j++ {
			if d := dist2(col[i], col[j]); d > far {
				a, b, far = i, j, d
			}
		}
	}

	var mask int
	var groups [2]struct {
		sum   Color
		n     float64
		paint bool // some pixel in the group was painted on
	}
	for i, c := range col {
		k := 0
		if a != b && dist2(c, col[b]) < dist2(c, col[a]) {
			k = 1
			mask |= 1 << i
		}
		g := &groups[k]
		g.sum.R, g.sum.G, g.sum.B, g.n = g.sum.R+c.R, g.sum.G+c.G, g.sum.B+c.B, g.n+1
		g.paint = g.paint || px[i].A >= clear
	}

	colour := func(k int) int32 {
		g := groups[k]
		if !g.paint {
			return unset
		}
		return Color{g.sum.R / g.n, g.sum.G / g.n, g.sum.B / g.n}.pack()
	}
	fg, bg = colour(1), colour(0)
	if fg == unset && mask != 0 {
		// Only a background can be left unset, so swap the groups' roles.
		mask, fg, bg = ^mask&(len(glyphs)-1), bg, fg
	}
	return glyphs[mask], fg, bg
}

func dist2(a, b Color) float64 {
	dr, dg, db := a.R-b.R, a.G-b.G, a.B-b.B
	return dr*dr + dg*dg + db*db
}

// unset stands for the terminal's default colour.
const unset int32 = -1

// pen writes cells, emitting a colour sequence only when the colour changes.
type pen struct {
	out    strings.Builder
	fg, bg int32
}

func (p *pen) cell(glyph rune, fg, bg int32) {
	if fg != p.fg && glyph != ' ' { // a space shows no foreground
		p.colour("38", "39", fg)
		p.fg = fg
	}
	if bg != p.bg {
		p.colour("48", "49", bg)
		p.bg = bg
	}
	p.out.WriteRune(glyph)
}

func (p *pen) colour(set, clear string, c int32) {
	p.out.WriteString("\x1b[")
	if c == unset {
		p.out.WriteString(clear)
	} else {
		var buf [3]byte
		p.out.WriteString(set)
		p.out.WriteString(";2")
		for shift := 16; shift >= 0; shift -= 8 {
			p.out.WriteString(";")
			p.out.WriteString(string(strconv.AppendInt(buf[:0], int64(c>>shift&0xff), 10)))
		}
	}
	p.out.WriteString("m")
}

func (p *pen) reset() {
	if p.fg != unset || p.bg != unset {
		p.out.WriteString("\x1b[m")
		p.fg, p.bg = unset, unset
	}
}
