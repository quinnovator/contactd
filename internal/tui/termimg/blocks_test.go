package termimg_test

import (
	"image"
	"image/color"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/quinnovator/contactd/internal/tui/termimg"
	"github.com/quinnovator/contactd/internal/tui/tuitest"
)

// cell is what one character of a block image shows: its glyph and the
// colours in force when it was written. An empty colour is the terminal's
// default.
type cell struct{ glyph, fg, bg string }

// readRow plays a row of half blocks the way a terminal does and returns its
// cells. It fails the test on anything but the SGR sequences HalfBlocks may
// write, and returns how many sequences there were and whether the row ended
// with a reset.
func readRow(t *testing.T, row string) (cells []cell, sequences int, reset bool) {
	t.Helper()
	var fg, bg string
	for len(row) > 0 {
		if !strings.HasPrefix(row, "\x1b[") {
			r := []rune(row)[0]
			cells = append(cells, cell{glyph: string(r), fg: fg, bg: bg})
			row = row[len(string(r)):]
			reset = false
			continue
		}
		end := strings.IndexByte(row, 'm')
		if end < 0 {
			t.Fatalf("an escape sequence does not end: %q", row)
		}
		params := row[2:end]
		row = row[end+1:]
		sequences++
		if params == "" {
			fg, bg, reset = "", "", true
			continue
		}
		for params != "" {
			switch {
			case strings.HasPrefix(params, "38;2;"):
				fg, params = takeRGB(t, params[5:])
			case strings.HasPrefix(params, "48;2;"):
				bg, params = takeRGB(t, params[5:])
			case params == "49" || strings.HasPrefix(params, "49;"):
				bg, params = "", strings.TrimPrefix(params[2:], ";")
			default:
				t.Fatalf("unexpected SGR parameters %q", params)
			}
		}
	}
	return cells, sequences, reset
}

// takeRGB cuts "r;g;b" off the front of SGR parameters.
func takeRGB(t *testing.T, params string) (rgb, rest string) {
	t.Helper()
	parts := strings.SplitN(params, ";", 4)
	if len(parts) < 3 {
		t.Fatalf("a colour with fewer than three numbers: %q", params)
	}
	for _, p := range parts[:3] {
		if n, err := strconv.Atoi(p); err != nil || n < 0 || n > 255 {
			t.Fatalf("a colour channel is %q", p)
		}
	}
	if len(parts) == 4 {
		rest = parts[3]
	}
	return strings.Join(parts[:3], ";"), rest
}

// colourOf is a pixel as readRow reports a colour: "" when it is clear.
func colourOf(c color.RGBA) string {
	if c.A < 128 {
		return ""
	}
	return strconv.Itoa(int(c.R)) + ";" + strconv.Itoa(int(c.G)) + ";" + strconv.Itoa(int(c.B))
}

// checkBlocks reads rows back and compares every half cell with the picture
// it was made from. The picture's opaque pixels must be fully opaque.
func checkBlocks(t *testing.T, rows []string, img *image.RGBA) {
	t.Helper()
	size := img.Bounds().Size()
	if want := (size.Y + 1) / 2; len(rows) != want {
		t.Fatalf("%d rows, want %d", len(rows), want)
	}
	for y, row := range rows {
		cells, sequences, reset := readRow(t, row)
		if len(cells) != size.X {
			t.Fatalf("row %d has %d cells, want %d", y, len(cells), size.X)
		}
		if sequences > 0 && !reset {
			t.Errorf("row %d sets a colour and does not end with a reset: %q", y, row)
		}
		if w, wc := ansi.StringWidth(row), ansi.StringWidthWc(row); w != size.X || wc != size.X {
			t.Errorf("row %d is %d cells wide by graphemes and %d by wcwidth, want %d", y, w, wc, size.X)
		}
		for x, c := range cells {
			top := colourOf(img.RGBAAt(x, 2*y))
			bottom := ""
			if 2*y+1 < size.Y {
				bottom = colourOf(img.RGBAAt(x, 2*y+1))
			}
			var gotTop, gotBottom string
			switch c.glyph {
			case "▀":
				gotTop, gotBottom = c.fg, c.bg
			case "▄":
				gotTop, gotBottom = c.bg, c.fg
			case " ":
				gotTop, gotBottom = c.bg, c.bg
			default:
				t.Fatalf("row %d cell %d is %q", y, x, c.glyph)
			}
			if gotTop != top || gotBottom != bottom {
				t.Fatalf("row %d cell %d shows %q over %q, the picture has %q over %q", y, x, gotTop, gotBottom, top, bottom)
			}
		}
	}
}

func TestHalfBlocksGolden(t *testing.T) {
	t.Parallel()
	img := mosaic()
	rows := termimg.HalfBlocks(img)
	if len(rows) != 9 {
		t.Fatalf("%d rows, want the mock-up's 9", len(rows))
	}
	checkBlocks(t, rows, img)
	tuitest.Golden(t, "halfblocks_chart.golden", []byte(strings.Join(rows, "\n")+"\n"))

	// The top row is the ground all the way across: one colour change.
	if _, sequences, _ := readRow(t, rows[0]); sequences != 2 {
		t.Errorf("a row of one colour has %d sequences, want one to set it and one reset: %q", sequences, rows[0])
	}
	if got := ansi.Strip(rows[0]); got != strings.Repeat("▀", 44) {
		t.Errorf("the top row's text is %q", got)
	}
}

func TestHalfBlocks(t *testing.T) {
	t.Parallel()
	red, green := color.RGBA{R: 0xff, A: 0xff}, color.RGBA{G: 0xff, A: 0xff}
	navy := color.RGBA{B: 0x80, A: 0xff}
	none := color.RGBA{}
	cases := []struct {
		name string
		img  *image.RGBA
		want []string
	}{
		{
			name: "one cell: the top pixel is the foreground",
			img:  pixels([]color.RGBA{red}, []color.RGBA{navy}),
			want: []string{"\x1b[38;2;255;0;0;48;2;0;0;128m▀\x1b[m"},
		},
		{
			name: "a colour is written once while it lasts",
			img:  pixels([]color.RGBA{red, red, red}, []color.RGBA{navy, navy, navy}),
			want: []string{"\x1b[38;2;255;0;0;48;2;0;0;128m▀▀▀\x1b[m"},
		},
		{
			name: "only the half that changes is written",
			img:  pixels([]color.RGBA{red, green, green}, []color.RGBA{navy, navy, red}),
			want: []string{"\x1b[38;2;255;0;0;48;2;0;0;128m▀\x1b[38;2;0;255;0m▀\x1b[48;2;255;0;0m▀\x1b[m"},
		},
		{
			name: "each row starts from nothing",
			img:  pixels([]color.RGBA{red}, []color.RGBA{navy}, []color.RGBA{red}, []color.RGBA{navy}),
			want: []string{"\x1b[38;2;255;0;0;48;2;0;0;128m▀\x1b[m", "\x1b[38;2;255;0;0;48;2;0;0;128m▀\x1b[m"},
		},
		{
			name: "an odd height leaves the last lower half clear",
			img:  pixels([]color.RGBA{red}, []color.RGBA{navy}, []color.RGBA{green}),
			want: []string{"\x1b[38;2;255;0;0;48;2;0;0;128m▀\x1b[m", "\x1b[38;2;0;255;0m▀\x1b[m"},
		},
		{
			name: "a clear cell is a space and writes nothing",
			img:  pixels([]color.RGBA{none, none}, []color.RGBA{none, none}),
			want: []string{"  "},
		},
		{
			name: "a clear lower half takes the background back",
			img:  pixels([]color.RGBA{red, red}, []color.RGBA{navy, none}),
			want: []string{"\x1b[38;2;255;0;0;48;2;0;0;128m▀\x1b[49m▀\x1b[m"},
		},
		{
			name: "a clear upper half is drawn with the lower block",
			img:  pixels([]color.RGBA{none}, []color.RGBA{green}),
			want: []string{"\x1b[38;2;0;255;0m▄\x1b[m"},
		},
		{
			name: "a clear cell after a coloured one drops the background",
			img:  pixels([]color.RGBA{red, none, red}, []color.RGBA{navy, none, navy}),
			want: []string{"\x1b[38;2;255;0;0;48;2;0;0;128m▀\x1b[49m \x1b[48;2;0;0;128m▀\x1b[m"},
		},
		{
			name: "a translucent pixel is drawn in its own colour, not darkened",
			img:  pixels([]color.RGBA{{R: 0xc0, G: 0x60, B: 0x00, A: 0xc0}}, []color.RGBA{{R: 0x10, A: 0x20}}),
			want: []string{"\x1b[38;2;255;127;0m▀\x1b[m"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := termimg.HalfBlocks(c.img)
			if len(got) != len(c.want) {
				t.Fatalf("%d rows %q, want %d", len(got), got, len(c.want))
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("row %d = %q, want %q", i, got[i], c.want[i])
				}
			}
		})
	}
}

func TestHalfBlocksNothing(t *testing.T) {
	t.Parallel()
	if got := termimg.HalfBlocks(nil); got != nil {
		t.Errorf("HalfBlocks(nil) = %q, want nil", got)
	}
	if got := termimg.HalfBlocks(image.NewRGBA(image.Rectangle{})); got != nil {
		t.Errorf("HalfBlocks of an empty picture = %q, want nil", got)
	}
}

// TestHalfBlocksReadBack draws pictures and reads every cell back.
func TestHalfBlocksReadBack(t *testing.T) {
	t.Parallel()
	holes := noise(31, 17)
	for y := range 17 {
		for x := range 31 {
			if (x/3+y/2)%3 == 0 {
				holes.SetRGBA(x, y, color.RGBA{})
			}
		}
	}
	big := image.NewRGBA(image.Rect(-3, 9, 60, 40))
	for y := range 12 {
		for x := range 20 {
			big.SetRGBA(5+x, 15+y, noise(20, 12).RGBAAt(x, y))
		}
	}
	moved, ok := big.SubImage(image.Rect(5, 15, 25, 27)).(*image.RGBA)
	if !ok {
		t.Fatal("SubImage of an RGBA is not an RGBA")
	}
	cases := []struct {
		name string
		img  *image.RGBA
		// flat is the picture with its rectangle at the origin.
		flat *image.RGBA
	}{
		{name: "noise", img: noise(44, 24), flat: noise(44, 24)},
		{name: "a chart", img: chart(44, 24), flat: chart(44, 24)},
		{name: "odd height with clear pixels", img: holes, flat: holes},
		{name: "a picture that does not start at the origin", img: moved, flat: noise(20, 12)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			checkBlocks(t, termimg.HalfBlocks(c.img), c.flat)
		})
	}
}
