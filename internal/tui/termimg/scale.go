package termimg

import (
	"image"
)

// Fit returns the largest box of whole cells, no more than maxCols by maxRows,
// that keeps the shape of a picture w by h pixels when a cell is cellW by
// cellH pixels. The box is never larger than the picture's own size in
// cells, each side rounded up, so a small picture is not blown up to fill
// the space. The answer is at least 1x1, whatever is passed.
//
// One side of the box is the most it may be, and the other is rounded down,
// so the picture never spills out. A side whose limit is the picture's own
// size is then set again from the other side, to the nearest cell. That
// limit was rounded up, and a picture of a few cells loses its shape between
// one side rounded up and the other rounded down: 24x24 pixels in cells of
// 8x16 would be 3x1, a square drawn half as wide again as it is tall. It is
// 2x1.
func Fit(w, h, cellW, cellH, maxCols, maxRows int) (cols, rows int) {
	maxCols, maxRows = max(maxCols, 1), max(maxRows, 1)
	if w <= 0 || h <= 0 || cellW <= 0 || cellH <= 0 {
		return 1, 1
	}
	// A size past any picture or screen is cut, so that the products below
	// stay within 64 bits whatever is passed.
	const most = 1 << 30
	w, h, cellW, cellH = min(w, most), min(h, most), min(cellW, most), min(cellH, most)
	// The cells the picture covers at its own size are the most it gets.
	ownCols, ownRows := (w-1)/cellW+1, (h-1)/cellH+1
	narrow, short := ownCols < maxCols, ownRows < maxRows
	maxCols, maxRows = min(maxCols, ownCols), min(maxRows, ownRows)
	// Compare the picture's shape with the box's, both in pixels.
	pw, ph := int64(w), int64(h)
	cw, ch := int64(cellW), int64(cellH)
	boxW, boxH := int64(maxCols)*cw, int64(maxRows)*ch
	if pw*boxH <= ph*boxW {
		// The height is what stops it.
		cols, rows = min(max(int(pw*boxH/(ph*cw)), 1), maxCols), maxRows
		if short {
			rows = min(max(nearest(int64(cols)*cw*ph, pw*ch), 1), maxRows)
		}
		return cols, rows
	}
	cols, rows = maxCols, min(max(int(ph*boxW/(pw*ch)), 1), maxRows)
	if narrow {
		cols = min(max(nearest(int64(rows)*ch*pw, ph*cw), 1), maxCols)
	}
	return cols, rows
}

// nearest returns a/b to the nearest whole number, a half going up. Both are
// positive, and twice a stays within 64 bits.
func nearest(a, b int64) int {
	return int((2*a + b) / (2 * b))
}

// Scale returns src drawn w by h pixels. Each pixel of the result is the
// average of the block of source pixels it covers, so every source pixel
// counts once. Scale never enlarges: a side asked for that is longer than the
// source's is the source's. The result starts at (0, 0) and holds
// alpha-premultiplied colour, as image.RGBA does. It is empty when src is, or
// when w or h is less than one.
func Scale(src image.Image, w, h int) *image.RGBA {
	b := src.Bounds()
	w, h = min(w, b.Dx()), min(h, b.Dy())
	if w <= 0 || h <= 0 {
		return image.NewRGBA(image.Rectangle{})
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	xs, ys := spans(b.Dx(), w), spans(b.Dy(), h)
	switch s := src.(type) {
	case *image.RGBA:
		scaleRGBA(dst, s, xs, ys)
	case image.RGBA64Image:
		scaleAny(dst, b, xs, ys, func(x, y int) (r, g, b, a uint32) {
			c := s.RGBA64At(x, y)
			return uint32(c.R), uint32(c.G), uint32(c.B), uint32(c.A)
		})
	default:
		scaleAny(dst, b, xs, ys, func(x, y int) (r, g, b, a uint32) {
			return src.At(x, y).RGBA()
		})
	}
	return dst
}

// spans cuts n source pixels into m runs, m no more than n. Run i is
// [spans[i], spans[i+1]). Every run holds at least one pixel and together
// they hold all n.
func spans(n, m int) []int {
	out := make([]int, m+1)
	for i := range out {
		out[i] = int(int64(i) * int64(n) / int64(m))
	}
	return out
}

// scaleRGBA is Scale for the source type the store scales most: its own
// masters. It reads the pixel bytes directly.
func scaleRGBA(dst, src *image.RGBA, xs, ys []int) {
	w, h := len(xs)-1, len(ys)-1
	if w == src.Rect.Dx() && h == src.Rect.Dy() {
		for y := range h {
			copy(dst.Pix[y*dst.Stride:y*dst.Stride+4*w], src.Pix[y*src.Stride:y*src.Stride+4*w])
		}
		return
	}
	sums := make([]uint64, 4*w)
	for y := range h {
		clear(sums)
		for sy := ys[y]; sy < ys[y+1]; sy++ {
			row := src.Pix[sy*src.Stride:]
			for x := range w {
				var r, g, b, a uint64
				for i := 4 * xs[x]; i < 4*xs[x+1]; i += 4 {
					p := row[i : i+4 : i+4]
					r, g, b, a = r+uint64(p[0]), g+uint64(p[1]), b+uint64(p[2]), a+uint64(p[3])
				}
				sums[4*x], sums[4*x+1], sums[4*x+2], sums[4*x+3] = sums[4*x]+r, sums[4*x+1]+g, sums[4*x+2]+b, sums[4*x+3]+a
			}
		}
		out := dst.Pix[y*dst.Stride:]
		for x := range w {
			n := uint64((xs[x+1] - xs[x]) * (ys[y+1] - ys[y])) //nolint:gosec // G115: a run of pixels inside one image is positive
			for c := range 4 {
				out[4*x+c] = uint8((sums[4*x+c] + n/2) / n) //nolint:gosec // G115: an average of bytes is a byte
			}
		}
	}
}

// scaleAny is Scale for every other source. at returns the pixel at a
// position in the source's own coordinates, alpha-premultiplied, 16 bits a
// channel.
func scaleAny(dst *image.RGBA, b image.Rectangle, xs, ys []int, at func(x, y int) (r, g, b, a uint32)) {
	w, h := len(xs)-1, len(ys)-1
	sums := make([]uint64, 4*w)
	for y := range h {
		clear(sums)
		for sy := ys[y]; sy < ys[y+1]; sy++ {
			for x := range w {
				var sr, sg, sb, sa uint64
				for sx := xs[x]; sx < xs[x+1]; sx++ {
					r, g, bl, a := at(b.Min.X+sx, b.Min.Y+sy)
					sr, sg, sb, sa = sr+uint64(r), sg+uint64(g), sb+uint64(bl), sa+uint64(a)
				}
				sums[4*x], sums[4*x+1], sums[4*x+2], sums[4*x+3] = sums[4*x]+sr, sums[4*x+1]+sg, sums[4*x+2]+sb, sums[4*x+3]+sa
			}
		}
		out := dst.Pix[y*dst.Stride:]
		for x := range w {
			n := uint64((xs[x+1] - xs[x]) * (ys[y+1] - ys[y])) //nolint:gosec // G115: a run of pixels inside one image is positive
			// A byte v is the 16-bit value 257*v, so the average is
			// divided by 257 as well, rounding to the nearest byte. That
			// is the answer scaleRGBA gives for the same pixels.
			n *= 257
			for c := range 4 {
				out[4*x+c] = uint8((sums[4*x+c] + n/2) / n) //nolint:gosec // G115: 16-bit values averaged and divided by 257
			}
		}
	}
}
