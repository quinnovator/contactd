package stage

import (
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/quinnovator/contactd/internal/tui/canvas"
	"github.com/quinnovator/contactd/internal/tui/motion"
	"github.com/quinnovator/contactd/internal/tui/speech"
	"github.com/quinnovator/contactd/internal/tui/theme"
)

// drying is how long a freshly spoken rune takes to fade from the accent
// colour to ordinary text.
const drying = 0.55

// cursor is drawn where the next word will be.
const cursor = "▍"

// line is a half-open range of runes that share a row.
type line struct{ from, to int }

// page lays the words out into lines, and remembers the result until the
// words or the width change.
type page struct {
	lines []line
	// cells is how many cells each rune takes.
	cells []uint8
	runes int
	width int
	laid  bool
}

func (p *page) reset() { p.laid = false }

// layout wraps the whole of the words, including the part not yet spoken.
// Because the words run ahead of the voice, a word's line is settled before
// its first letter appears, and words never jump rows half-revealed.
func (p *page) layout(th *theme.Theme, text []rune, width int) []line {
	if !p.laid || p.runes != len(text) || p.width != width {
		p.cells = p.cells[:0]
		for _, r := range text {
			p.cells = append(p.cells, uint8(min(max(th.Width(string(r)), 0), 2)))
		}
		p.lines, p.runes, p.width, p.laid = wrap(text, p.cells, width), len(text), width, true
	}
	return p.lines
}

// wrap breaks text into lines at most width cells wide, at spaces where it
// can and mid-word where it must. The space or newline a line breaks on
// belongs to no line.
func wrap(text []rune, cells []uint8, width int) []line {
	var lines []line
	from, w := 0, 0
	space, spaceW := -1, 0 // the last space on this line, and the width up to and including it
	for i, r := range text {
		if r == '\n' {
			lines = append(lines, line{from, i})
			from, w, space = i+1, 0, -1
			continue
		}
		rw := int(cells[i])
		if w+rw > width && i > from {
			switch {
			case r == ' ':
				lines = append(lines, line{from, i})
				from, w, space = i+1, 0, -1
				continue
			case space >= 0:
				lines = append(lines, line{from, space})
				from, w, space = space+1, w-spaceW, -1
			default:
				lines = append(lines, line{from, i})
				from, w = i, 0
			}
		}
		w += rw
		if r == ' ' {
			space, spaceW = i, w
		}
	}
	return append(lines, line{from, len(text)})
}

// inks are the colours the words are written in.
type inks struct {
	paper, text, accent canvas.Color
}

func (s *Stage) inks() inks {
	pal := s.avatar.Palette()
	if !s.dark {
		return inks{paper: s.paper, text: canvas.Hex(0x2B2622), accent: pal.Core.Mix(pal.Deep, 0.5)}
	}
	return inks{paper: s.paper, text: canvas.Hex(0xEAE6DF), accent: pal.Light.Mix(pal.Glow, 0.35)}
}

// pulse is a slow oscillation in [0.35, 1], shared by everything on the
// stage that breathes.
func (s *Stage) pulse() float64 { return 0.675 + 0.325*math.Sin(5*s.elapsed) }

// caption draws the spoken part of the words in a box of width by height
// cells: the line being spoken and as many before it as fit, with fresh
// runes glowing and the oldest lines in view fading out as they scroll
// away. With nothing said and nothing on its way there are no rows.
func (s *Stage) caption(width, height int) []piece {
	if height <= 0 || width <= 0 {
		return nil
	}
	text, head := s.voice.Text(), s.voice.Head()
	state := s.voice.State()
	waiting := state != speech.Finished && (len(text) > 0 || s.turn.Busy)
	if len(text) == 0 && !waiting {
		return nil
	}
	ink := s.inks()
	lines := s.page.layout(s.th, text, width)
	// The current line is the first one the read head has not passed.
	current := min(sort.Search(len(lines), func(i int) bool { return lines[i].to >= head }), len(lines)-1)
	top := max(current-height+1, 0)
	fresh := s.voice.Fresh(drying)

	rows := make([]piece, 0, height)
	for i := top; i <= current; i++ {
		ln, shade := lines[i], ink.text
		if top > 0 && i < current {
			// Scrolled: lines dissolve into the paper toward the top edge.
			shade = ink.paper.Mix(ink.text, motion.Smooth(float64(i-top+1)/3))
		}
		end := min(ln.to, head)
		dry := min(max(fresh, ln.from), end)

		var b strings.Builder
		used := 0
		if dry > ln.from {
			paint(&b, shade, string(text[ln.from:dry]))
		}
		for j := dry; j < end; j++ {
			paint(&b, ink.accent.Mix(shade, motion.Smooth(s.voice.Age(j)/drying)), string(text[j]))
		}
		for j := ln.from; j < end; j++ {
			used += int(s.page.cells[j])
		}
		if i == current && used < width && waiting {
			paint(&b, ink.paper.Mix(ink.accent, s.pulse()), cursor)
			used++
		}
		if b.Len() > 0 {
			b.WriteString(reset)
		}
		rows = append(rows, piece{w: used, s: b.String()})
	}
	return rows
}

// paint writes s in a colour. The caller switches the colour off again.
func paint(b *strings.Builder, c canvas.Color, s string) {
	if s == "" {
		return
	}
	red, green, blue, _ := c.RGBA()
	var buf [3]byte
	b.WriteString("\x1b[38;2")
	for _, v := range [...]uint32{red, green, blue} {
		b.WriteByte(';')
		b.Write(strconv.AppendUint(buf[:0], uint64(v>>8), 10))
	}
	b.WriteByte('m')
	b.WriteString(s)
}
