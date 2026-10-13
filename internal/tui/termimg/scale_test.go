package termimg_test

import (
	"image"
	"image/color"
	"math"
	"testing"

	"github.com/quinnovator/contactd/internal/tui/termimg"
)

func TestFit(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name               string
		w, h, cellW, cellH int
		maxCols, maxRows   int
		wantCols, wantRows int
	}{
		{name: "the mock-up's chart: 16:9 is stopped by the height", w: 640, h: 360, cellW: 8, cellH: 16, maxCols: 44, maxRows: 12, wantCols: 42, wantRows: 12},
		{name: "twice the pixels in the picture and in the cell", w: 1280, h: 720, cellW: 16, cellH: 32, maxCols: 44, maxRows: 12, wantCols: 42, wantRows: 12},
		{name: "a narrower cell takes more columns", w: 640, h: 360, cellW: 7, cellH: 15, maxCols: 44, maxRows: 12, wantCols: 44, wantRows: 11},
		{name: "a taller cell", w: 640, h: 360, cellW: 8, cellH: 20, maxCols: 44, maxRows: 12, wantCols: 44, wantRows: 9},
		{name: "the old 44x9 box", w: 640, h: 360, cellW: 8, cellH: 16, maxCols: 44, maxRows: 9, wantCols: 32, wantRows: 9},
		{name: "a wide strip is stopped by the width", w: 1000, h: 100, cellW: 8, cellH: 16, maxCols: 44, maxRows: 12, wantCols: 44, wantRows: 2},
		{name: "a square", w: 500, h: 500, cellW: 8, cellH: 16, maxCols: 44, maxRows: 12, wantCols: 24, wantRows: 12},
		{name: "a tall strip", w: 100, h: 1000, cellW: 8, cellH: 16, maxCols: 44, maxRows: 12, wantCols: 2, wantRows: 12},
		{name: "a picture exactly the box's shape", w: 352, h: 192, cellW: 8, cellH: 16, maxCols: 44, maxRows: 12, wantCols: 44, wantRows: 12},
		// A picture smaller than the box keeps to the cells it covers at its
		// own size.
		{name: "a small icon is not blown up", w: 16, h: 16, cellW: 8, cellH: 16, maxCols: 44, maxRows: 12, wantCols: 2, wantRows: 1},
		{name: "a larger icon", w: 32, h: 32, cellW: 8, cellH: 16, maxCols: 44, maxRows: 12, wantCols: 4, wantRows: 2},
		{name: "an icon on a display with twice the pixels", w: 64, h: 64, cellW: 16, cellH: 32, maxCols: 44, maxRows: 12, wantCols: 4, wantRows: 2},
		{name: "one pixel", w: 1, h: 1, cellW: 8, cellH: 16, maxCols: 44, maxRows: 12, wantCols: 1, wantRows: 1},
		{name: "exactly one cell", w: 8, h: 16, cellW: 8, cellH: 16, maxCols: 44, maxRows: 12, wantCols: 1, wantRows: 1},
		// A side the picture is shorter than follows the other side to the
		// nearest cell. Its own size, rounded up, beside the other side
		// rounded down would be 3x1 for the next two: a square drawn half
		// as wide again as it is tall.
		{name: "an icon between two cell sizes stays square", w: 24, h: 24, cellW: 8, cellH: 16, maxCols: 44, maxRows: 12, wantCols: 2, wantRows: 1},
		{name: "another", w: 20, h: 20, cellW: 8, cellH: 16, maxCols: 44, maxRows: 12, wantCols: 2, wantRows: 1},
		{name: "a square of two and a half rows", w: 40, h: 40, cellW: 8, cellH: 16, maxCols: 44, maxRows: 12, wantCols: 4, wantRows: 2},
		{name: "a square in cells that are not 8x16", w: 48, h: 48, cellW: 10, cellH: 20, maxCols: 44, maxRows: 12, wantCols: 4, wantRows: 2},
		{name: "a square in cells that are not one by two", w: 40, h: 40, cellW: 11, cellH: 23, maxCols: 44, maxRows: 12, wantCols: 2, wantRows: 1},
		{name: "a pixel more than one cell is one cell", w: 9, h: 17, cellW: 8, cellH: 16, maxCols: 44, maxRows: 12, wantCols: 1, wantRows: 1},
		{name: "a small banner", w: 200, h: 50, cellW: 9, cellH: 18, maxCols: 44, maxRows: 12, wantCols: 16, wantRows: 2},
		{name: "a small column", w: 20, h: 150, cellW: 8, cellH: 16, maxCols: 44, maxRows: 12, wantCols: 2, wantRows: 8},
		{name: "a strip of less than a row keeps its width", w: 100, h: 3, cellW: 8, cellH: 16, maxCols: 44, maxRows: 12, wantCols: 13, wantRows: 1},
		{name: "a strip of less than a column keeps its height", w: 3, h: 100, cellW: 8, cellH: 16, maxCols: 44, maxRows: 12, wantCols: 1, wantRows: 7},
		{name: "a part of a cell counts as a cell, then the shape is kept", w: 100, h: 100, cellW: 8, cellH: 16, maxCols: 44, maxRows: 12, wantCols: 12, wantRows: 6},
		{name: "a picture just inside the box", w: 320, h: 180, cellW: 8, cellH: 16, maxCols: 44, maxRows: 12, wantCols: 39, wantRows: 11},
		{name: "the chart where cells have twice the pixels", w: 640, h: 360, cellW: 16, cellH: 32, maxCols: 44, maxRows: 12, wantCols: 39, wantRows: 11},
		{name: "shorter than the box and wider than it", w: 400, h: 40, cellW: 8, cellH: 16, maxCols: 44, maxRows: 12, wantCols: 44, wantRows: 2},
		{name: "narrower than the box and taller than it", w: 40, h: 400, cellW: 8, cellH: 16, maxCols: 44, maxRows: 12, wantCols: 2, wantRows: 12},
		{name: "a short strip wider than the box", w: 400, h: 20, cellW: 8, cellH: 16, maxCols: 44, maxRows: 12, wantCols: 44, wantRows: 1},
		{name: "a thin strip taller than the box", w: 10, h: 400, cellW: 8, cellH: 16, maxCols: 44, maxRows: 12, wantCols: 1, wantRows: 12},
		{name: "a small picture in a smaller box", w: 32, h: 32, cellW: 8, cellH: 16, maxCols: 2, maxRows: 2, wantCols: 2, wantRows: 1},
		{name: "a picture exactly its own cells", w: 336, h: 192, cellW: 8, cellH: 16, maxCols: 44, maxRows: 12, wantCols: 42, wantRows: 12},
		{name: "a narrow box", w: 640, h: 360, cellW: 8, cellH: 16, maxCols: 26, maxRows: 12, wantCols: 26, wantRows: 7},
		{name: "a hairline across keeps one row", w: 5000, h: 1, cellW: 8, cellH: 16, maxCols: 44, maxRows: 12, wantCols: 44, wantRows: 1},
		{name: "a hairline down keeps one column", w: 1, h: 5000, cellW: 8, cellH: 16, maxCols: 44, maxRows: 12, wantCols: 1, wantRows: 12},
		{name: "a box of one cell", w: 640, h: 360, cellW: 8, cellH: 16, maxCols: 1, maxRows: 1, wantCols: 1, wantRows: 1},
		{name: "no picture", w: 0, h: 0, cellW: 8, cellH: 16, maxCols: 44, maxRows: 12, wantCols: 1, wantRows: 1},
		{name: "no cell size", w: 640, h: 360, cellW: 0, cellH: 0, maxCols: 44, maxRows: 12, wantCols: 1, wantRows: 1},
		{name: "no box", w: 640, h: 360, cellW: 8, cellH: 16, maxCols: 0, maxRows: -3, wantCols: 1, wantRows: 1},
		{name: "sizes whose product needs more than 32 bits", w: 1 << 30, h: 1 << 29, cellW: 8, cellH: 16, maxCols: 44, maxRows: 12, wantCols: 44, wantRows: 11},
		// Before sizes were cut, these products overflowed and Fit divided by
		// zero.
		{name: "a picture past any size", w: math.MaxInt, h: math.MaxInt, cellW: 8, cellH: 16, maxCols: 44, maxRows: 12, wantCols: 24, wantRows: 12},
		{name: "a cell past any size", w: 640, h: 360, cellW: math.MaxInt, cellH: math.MaxInt, maxCols: 44, maxRows: 12, wantCols: 1, wantRows: 1},
		{name: "everything past any size", w: math.MaxInt, h: math.MaxInt, cellW: math.MaxInt, cellH: math.MaxInt, maxCols: math.MaxInt, maxRows: math.MaxInt, wantCols: 1, wantRows: 1},
		{name: "a box past any size", w: 640, h: 360, cellW: 8, cellH: 16, maxCols: math.MaxInt, maxRows: math.MaxInt, wantCols: 78, wantRows: 22},
		{name: "a small picture past any size of cell", w: 1 << 30, h: 1 << 30, cellW: 1 << 29, cellH: 1 << 30, maxCols: 44, maxRows: 12, wantCols: 2, wantRows: 1},
		{name: "a picture of the largest size in cells of one pixel", w: math.MaxInt, h: math.MaxInt, cellW: 1, cellH: 1, maxCols: math.MaxInt, maxRows: math.MaxInt, wantCols: 1 << 30, wantRows: 1 << 30},
		{name: "the same, a pixel short of square", w: math.MaxInt, h: 1<<30 - 1, cellW: 1, cellH: 2, maxCols: math.MaxInt, maxRows: math.MaxInt, wantCols: 1<<30 - 1, wantRows: 1<<29 - 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			cols, rows := termimg.Fit(c.w, c.h, c.cellW, c.cellH, c.maxCols, c.maxRows)
			if cols != c.wantCols || rows != c.wantRows {
				t.Errorf("Fit(%dx%d, cell %dx%d, box %dx%d) = %dx%d, want %dx%d",
					c.w, c.h, c.cellW, c.cellH, c.maxCols, c.maxRows, cols, rows, c.wantCols, c.wantRows)
			}
		})
	}
}

// TestFitProperties checks, over many shapes, that the grid stays in the box
// and in the cells the picture covers at its own size, is no smaller than it
// has to be, and has the picture's shape to within a cell.
func TestFitProperties(t *testing.T) {
	t.Parallel()
	cells := [][2]int{{8, 16}, {9, 18}, {10, 22}, {7, 15}, {11, 23}, {16, 32}, {12, 12}}
	for w := 1; w <= 2000; w += 37 {
		for h := 1; h <= 2000; h += 41 {
			for _, cell := range cells {
				for _, box := range [][2]int{{44, 12}, {28, 12}, {44, 5}, {1, 1}, {10, 3}} {
					checkFit(t, w, h, cell, box)
				}
			}
		}
	}
	// Every small picture, where a cell is a large part of the whole.
	for w := 1; w <= 130; w++ {
		for h := 1; h <= 130; h++ {
			for _, cell := range cells {
				checkFit(t, w, h, cell, [2]int{44, 12})
			}
		}
	}
}

// checkFit checks one answer of Fit.
func checkFit(t *testing.T, w, h int, cell, box [2]int) {
	t.Helper()
	cols, rows := termimg.Fit(w, h, cell[0], cell[1], box[0], box[1])
	fail := func(why string) {
		t.Helper()
		t.Fatalf("Fit(%dx%d, cell %v, box %v) = %dx%d: %s", w, h, cell, box, cols, rows, why)
	}
	if cols < 1 || rows < 1 || cols > box[0] || rows > box[1] {
		fail("outside the box")
	}
	// The picture's own size in cells, a part of a cell counting as one.
	ownCols, ownRows := (w+cell[0]-1)/cell[0], (h+cell[1]-1)/cell[1]
	if cols > ownCols || rows > ownRows {
		fail("larger than the picture's own cells")
	}
	// Not smaller than it has to be: one side is at the box's, or covers
	// every whole cell the picture has that way.
	if cols < min(box[0], w/cell[0]) && rows < min(box[1], h/cell[1]) {
		fail("both sides are short of the box and of the picture")
	}
	// The shape. The picture is w*cell[1] columns to every h*cell[0] rows.
	// One side of the grid is within a cell of what the other side asks
	// for, unless the picture is thinner than one cell that way.
	a, b := w*cell[1], h*cell[0]
	colsOff, rowsOff := abs(cols*b-rows*a), abs(rows*a-cols*b)
	if cols > 1 && rows > 1 && colsOff >= b && rowsOff >= a {
		fail("not the picture's shape to within a cell")
	}
	switch {
	case ownCols >= box[0] && ownRows >= box[1]:
		// The picture covers the box: one side is the box's, and the
		// other is rounded down, so one more cell there would be too many.
		if cols != box[0] && rows != box[1] {
			fail("touches neither side of the box")
		}
		if rows == box[1] && cols < box[0] && (cols+1)*b <= rows*a {
			fail("a column more would fit")
		}
		if cols == box[0] && rows < box[1] && (rows+1)*a <= cols*b {
			fail("a row more would fit")
		}
	case ownCols < box[0] && ownRows < box[1] && cols > 1 && rows > 1:
		// The picture is smaller than the box both ways: one side follows
		// the other to the nearest cell, so it is within half a cell.
		if 2*colsOff > b && 2*rowsOff > a {
			fail("not the picture's shape to within half a cell")
		}
	}
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// TestFitLargePicturesUnchanged checks that the picture's own size matters
// only when it is the smaller limit: a picture that covers the whole box
// gets the grid it would get whatever its size, as one of the same shape and
// many times the pixels does.
func TestFitLargePicturesUnchanged(t *testing.T) {
	t.Parallel()
	for _, cell := range [][2]int{{8, 16}, {9, 18}, {10, 22}, {7, 15}, {16, 32}} {
		for _, box := range [][2]int{{44, 12}, {28, 12}, {44, 5}, {10, 3}} {
			for w := box[0] * cell[0]; w <= 3000; w += 53 {
				for h := box[1] * cell[1]; h <= 3000; h += 59 {
					cols, rows := termimg.Fit(w, h, cell[0], cell[1], box[0], box[1])
					bigCols, bigRows := termimg.Fit(1000*w, 1000*h, cell[0], cell[1], box[0], box[1])
					if cols != bigCols || rows != bigRows {
						t.Fatalf("Fit(%dx%d, cell %v, box %v) = %dx%d, but %dx%d at 1000 times the size",
							w, h, cell, box, cols, rows, bigCols, bigRows)
					}
				}
			}
		}
	}
}

// pixels builds a picture from rows of colours.
func pixels(rows ...[]color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, len(rows[0]), len(rows)))
	for y, row := range rows {
		for x, c := range row {
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

func grey(v uint8) color.RGBA { return color.RGBA{R: v, G: v, B: v, A: 0xff} }

func TestScale(t *testing.T) {
	t.Parallel()
	red, green := color.RGBA{R: 0xff, A: 0xff}, color.RGBA{G: 0xff, A: 0xff}
	clearPx := color.RGBA{}
	cases := []struct {
		name string
		src  *image.RGBA
		w, h int
		want *image.RGBA
	}{
		{
			name: "four pixels into one is their average",
			src:  pixels([]color.RGBA{grey(0), grey(100)}, []color.RGBA{grey(200), grey(40)}),
			w:    1, h: 1,
			want: pixels([]color.RGBA{grey(85)}),
		},
		{
			name: "each half is averaged on its own",
			src:  pixels([]color.RGBA{grey(10), grey(30), grey(200), grey(100)}, []color.RGBA{grey(20), grey(40), grey(0), grey(100)}),
			w:    2, h: 1,
			want: pixels([]color.RGBA{grey(25), grey(100)}),
		},
		{
			name: "three into two: one pixel, then two",
			src:  pixels([]color.RGBA{grey(90), grey(10), grey(30)}),
			w:    2, h: 1,
			want: pixels([]color.RGBA{grey(90), grey(20)}),
		},
		{
			name: "rows are averaged as columns are",
			src:  pixels([]color.RGBA{grey(0)}, []color.RGBA{grey(50)}, []color.RGBA{grey(100)}, []color.RGBA{grey(250)}),
			w:    1, h: 2,
			want: pixels([]color.RGBA{grey(25)}, []color.RGBA{grey(175)}),
		},
		{
			name: "the same size is a copy",
			src:  pixels([]color.RGBA{red, green}, []color.RGBA{green, red}),
			w:    2, h: 2,
			want: pixels([]color.RGBA{red, green}, []color.RGBA{green, red}),
		},
		{
			name: "a larger size is never made",
			src:  pixels([]color.RGBA{red, green}, []color.RGBA{green, red}),
			w:    40, h: 30,
			want: pixels([]color.RGBA{red, green}, []color.RGBA{green, red}),
		},
		{
			name: "each side is held back on its own",
			src:  pixels([]color.RGBA{grey(10), grey(30)}, []color.RGBA{grey(50), grey(70)}),
			w:    1, h: 9,
			want: pixels([]color.RGBA{grey(20)}, []color.RGBA{grey(60)}),
		},
		{
			name: "colour channels do not mix",
			src:  pixels([]color.RGBA{red, green}),
			w:    1, h: 1,
			want: pixels([]color.RGBA{{R: 0x80, G: 0x80, A: 0xff}}),
		},
		{
			name: "a clear pixel adds no colour, only less alpha",
			src:  pixels([]color.RGBA{red, clearPx}),
			w:    1, h: 1,
			want: pixels([]color.RGBA{{R: 0x80, A: 0x80}}),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			same(t, termimg.Scale(c.src, c.w, c.h), c.want)
		})
	}
}

func TestScaleEmpty(t *testing.T) {
	t.Parallel()
	src := chart(8, 8)
	cases := []struct {
		name string
		src  image.Image
		w, h int
	}{
		{name: "no width", src: src, w: 0, h: 4},
		{name: "no height", src: src, w: 4, h: 0},
		{name: "a negative size", src: src, w: -1, h: -1},
		{name: "an empty source", src: image.NewRGBA(image.Rectangle{}), w: 4, h: 4},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := termimg.Scale(c.src, c.w, c.h)
			if got == nil || !got.Bounds().Empty() {
				t.Errorf("Scale = %v, want an empty picture", got)
			}
		})
	}
}

// plain hides a picture's type, so that Scale has to read it one pixel at a
// time through the image.Image methods alone.
type plain struct{ img image.Image }

func (p plain) ColorModel() color.Model { return p.img.ColorModel() }
func (p plain) Bounds() image.Rectangle { return p.img.Bounds() }
func (p plain) At(x, y int) color.Color { return p.img.At(x, y) }

// TestScaleSources checks that the answer does not depend on how the source
// stores its pixels, nor on where its rectangle starts.
func TestScaleSources(t *testing.T) {
	t.Parallel()
	base := chart(88, 54)
	want := termimg.Scale(base, 44, 18)
	same(t, want, mosaic()) // 88x54 is the mosaic with each pixel doubled and tripled

	nrgba := image.NewNRGBA(base.Bounds())
	pal := image.NewPaletted(base.Bounds(), color.Palette{ground(), axis(), blue(), orange()})
	rgba64 := image.NewRGBA64(base.Bounds())
	// The same picture inside a larger one, away from the origin.
	big := image.NewRGBA(image.Rect(-7, 5, 200, 100))
	for y := range 54 {
		for x := range 88 {
			c := base.RGBAAt(x, y)
			nrgba.Set(x, y, c)
			pal.Set(x, y, c)
			rgba64.Set(x, y, c)
			big.SetRGBA(13+x, 21+y, c)
		}
	}
	moved, ok := big.SubImage(image.Rect(13, 21, 13+88, 21+54)).(*image.RGBA)
	if !ok {
		t.Fatal("SubImage of an RGBA is not an RGBA")
	}
	cases := []struct {
		name string
		src  image.Image
	}{
		{name: "NRGBA", src: nrgba},
		{name: "Paletted", src: pal},
		{name: "RGBA64", src: rgba64},
		{name: "an RGBA that does not start at the origin", src: moved},
		{name: "a type Scale knows nothing about", src: plain{base}},
		{name: "the same, away from the origin", src: plain{moved}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := termimg.Scale(c.src, 44, 18)
			if got.Bounds() != image.Rect(0, 0, 44, 18) {
				t.Fatalf("bounds = %v, want to start at the origin", got.Bounds())
			}
			same(t, got, want)
		})
	}
}

// TestScaleRounding checks that averages which are not whole numbers round
// the same way for every kind of source.
func TestScaleRounding(t *testing.T) {
	t.Parallel()
	src := noise(97, 61)
	nrgba := image.NewNRGBA(src.Bounds())
	for y := range 61 {
		for x := range 97 {
			nrgba.Set(x, y, src.RGBAAt(x, y))
		}
	}
	for _, size := range [][2]int{{44, 24}, {13, 7}, {96, 60}, {1, 1}} {
		want := termimg.Scale(src, size[0], size[1])
		same(t, termimg.Scale(nrgba, size[0], size[1]), want)
		same(t, termimg.Scale(plain{src}, size[0], size[1]), want)
	}
}

// TestScaleAlphaSources checks a translucent picture stored without the
// multiplication by alpha: the result holds multiplied colour, as an RGBA
// must.
func TestScaleAlphaSources(t *testing.T) {
	t.Parallel()
	src := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	src.SetNRGBA(0, 0, color.NRGBA{R: 0xff, A: 0xff})
	src.SetNRGBA(1, 0, color.NRGBA{R: 0xff, G: 0xff, A: 0x00})
	got := termimg.Scale(src, 1, 1).RGBAAt(0, 0)
	if want := (color.RGBA{R: 0x80, A: 0x80}); got != want {
		t.Errorf("pixel = %v, want %v", got, want)
	}
}

// TestScaleKeepsTheMean checks the property an area average has and a
// sampling scaler lacks: every source pixel counts once, so shrinking by a
// whole factor leaves the picture's mean colour where it was.
func TestScaleKeepsTheMean(t *testing.T) {
	t.Parallel()
	src := noise(240, 120)
	for _, size := range [][2]int{{120, 60}, {60, 40}, {48, 24}, {1, 1}, {240, 1}} {
		got := termimg.Scale(src, size[0], size[1])
		for c := range 3 {
			a, b := mean(src, c), mean(got, c)
			if diff := a - b; diff < -1 || diff > 1 {
				t.Errorf("scaled to %v: channel %d has mean %.2f, the source %.2f", size, c, b, a)
			}
		}
	}
}

func mean(img *image.RGBA, channel int) float64 {
	sum := 0
	for i := channel; i < len(img.Pix); i += 4 {
		sum += int(img.Pix[i])
	}
	return float64(sum) / float64(len(img.Pix)/4)
}
