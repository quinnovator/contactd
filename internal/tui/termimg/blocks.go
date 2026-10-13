package termimg

import (
	"image"
	"strconv"

	"github.com/charmbracelet/x/ansi"
)

// The characters a block image is made of. A cell is two pixels, one above
// the other.
const (
	upperHalf = "▀" // the top pixel in the foreground colour
	lowerHalf = "▄" // the bottom pixel in the foreground colour
)

// paint is the colour of one pixel. The zero value is a pixel that is clear:
// what shows there is the terminal's own background.
type paint struct {
	set     bool
	r, g, b uint8
}

// HalfBlocks draws a picture with half-block characters, two pixels to a
// cell: the top pixel is the cell's foreground and the bottom one its
// background. It returns one string per row of cells, each as many cells wide
// as the picture is pixels wide. A picture of odd height gets a last row
// whose lower half is left clear.
//
// Colours are always 24-bit; the renderer brings them down to what the
// terminal shows. A colour is set only where it changes, and a row that set
// any ends with a reset, so each row can be drawn on its own.
//
// A pixel less than half opaque is left clear, so a picture with a
// transparent ground sits on the terminal's background as it would in kitty.
func HalfBlocks(img *image.RGBA) []string {
	if img == nil {
		return nil
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return nil
	}
	rows := make([]string, 0, (h+1)/2)
	var buf []byte
	for y := 0; y < h; y += 2 {
		buf = buf[:0]
		var fg, bg paint // what the terminal has been told so far in this row
		styled := false
		for x := range w {
			top := pixel(img, x, y)
			var bottom paint
			if y+1 < h {
				bottom = pixel(img, x, y+1)
			}
			glyph, wantFg, wantBg := upperHalf, top, bottom
			switch {
			case !top.set && !bottom.set:
				glyph, wantFg = " ", fg
			case !top.set:
				glyph, wantFg, wantBg = lowerHalf, bottom, paint{}
			}
			if wantFg != fg || wantBg != bg {
				buf = appendSGR(buf, fg, wantFg, bg, wantBg)
				fg, bg, styled = wantFg, wantBg, true
			}
			buf = append(buf, glyph...)
		}
		if styled {
			buf = append(buf, ansi.ResetStyle...)
		}
		rows = append(rows, string(buf))
	}
	return rows
}

// pixel reads one pixel of img, counted from its top left corner, and undoes
// the multiplication by alpha that image.RGBA stores.
func pixel(img *image.RGBA, x, y int) paint {
	i := img.PixOffset(img.Rect.Min.X+x, img.Rect.Min.Y+y)
	p := img.Pix[i : i+4 : i+4]
	a := uint32(p[3])
	switch {
	case a < 128:
		return paint{}
	case a == 255:
		return paint{set: true, r: p[0], g: p[1], b: p[2]}
	}
	un := func(c uint8) uint8 {
		return uint8(min(uint32(c)*255/a, 255))
	}
	return paint{set: true, r: un(p[0]), g: un(p[1]), b: un(p[2])}
}

// appendSGR writes one SGR sequence that takes the foreground from fg to
// wantFg and the background from bg to wantBg. It is called only when one of
// them changes. A foreground is never taken back to the default: a clear cell
// is a space, which shows no foreground.
func appendSGR(buf []byte, fg, wantFg, bg, wantBg paint) []byte {
	buf = append(buf, "\x1b["...)
	sep := false
	if wantFg != fg {
		buf = appendRGB(append(buf, "38;2;"...), wantFg)
		sep = true
	}
	if wantBg != bg {
		if sep {
			buf = append(buf, ';')
		}
		if wantBg.set {
			buf = appendRGB(append(buf, "48;2;"...), wantBg)
		} else {
			buf = append(buf, "49"...)
		}
	}
	return append(buf, 'm')
}

// appendRGB writes a colour as "r;g;b".
func appendRGB(buf []byte, p paint) []byte {
	buf = strconv.AppendUint(buf, uint64(p.r), 10)
	buf = append(buf, ';')
	buf = strconv.AppendUint(buf, uint64(p.g), 10)
	buf = append(buf, ';')
	return strconv.AppendUint(buf, uint64(p.b), 10)
}
