package termimg_test

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"testing"

	"github.com/quinnovator/contactd/internal/tui/termimg"
)

func TestDefaultLimits(t *testing.T) {
	t.Parallel()
	want := termimg.Limits{MaxBytes: 8 << 20, MaxSide: 8192, MaxPixels: 12_000_000}
	if got := termimg.DefaultLimits(); got != want {
		t.Errorf("DefaultLimits() = %+v, want %+v", got, want)
	}
}

func TestInspect(t *testing.T) {
	t.Parallel()
	pic := chart(64, 40)
	pngFile, jpegFile := pngBytes(t, pic), jpegBytes(t, pic)
	gifFile := gifBytes(t, 64, 40, flat(image.Rect(0, 0, 64, 40), blue()))
	neither := errors.New("neither ErrFormat nor ErrTooLarge")

	cases := []struct {
		name   string
		data   []byte
		limits termimg.Limits
		want   termimg.Info
		err    error
	}{
		{name: "png", data: pngFile, want: termimg.Info{Format: "png", W: 64, H: 40}},
		{name: "jpeg", data: jpegFile, want: termimg.Info{Format: "jpeg", W: 64, H: 40}},
		{name: "gif", data: gifFile, want: termimg.Info{Format: "gif", W: 64, H: 40}},
		{name: "text", data: []byte("p50 0.8s\np99 3.1s\n"), err: termimg.ErrFormat},
		{name: "html", data: []byte("<!doctype html><img src=x>"), err: termimg.ErrFormat},
		{name: "svg", data: []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), err: termimg.ErrFormat},
		{name: "webp", data: []byte("RIFF\x24\x00\x00\x00WEBPVP8 "), err: termimg.ErrFormat},
		{name: "bmp", data: []byte("BM\x46\x00\x00\x00\x00\x00\x00\x00"), err: termimg.ErrFormat},
		{name: "a png's bytes after one byte of something else", data: append([]byte{'x'}, pngFile...), err: termimg.ErrFormat},
		{name: "empty", data: nil, err: neither},
		{name: "a gif of no size", data: []byte("GIF89a\x00\x00\x00\x00\x00\x00\x00;"), err: neither},
		{name: "a gif of no height", data: []byte("GIF89a\x40\x00\x00\x00\x00\x00\x00;"), err: neither},
		{name: "a png of no size", data: emptyPNG(), err: neither},
		{name: "a png cut inside its header", data: pngFile[:20], err: neither},
		{name: "a jpeg cut before its size", data: jpegFile[:12], err: neither},
		{name: "a gif cut inside its header", data: gifFile[:8], err: neither},
		{name: "a png cut after its header is still measured", data: pngFile[:40], want: termimg.Info{Format: "png", W: 64, H: 40}},
		{name: "one byte over the byte limit", data: pngFile, limits: termimg.Limits{MaxBytes: int64(len(pngFile)) - 1}, err: termimg.ErrTooLarge},
		{name: "exactly the byte limit", data: pngFile, limits: termimg.Limits{MaxBytes: int64(len(pngFile))}, want: termimg.Info{Format: "png", W: 64, H: 40}},
		{name: "wider than the side limit", data: pngFile, limits: termimg.Limits{MaxSide: 63}, err: termimg.ErrTooLarge},
		{name: "taller than the side limit", data: pngFile, limits: termimg.Limits{MaxSide: 39, MaxPixels: 1 << 30}, err: termimg.ErrTooLarge},
		{name: "exactly the side limit", data: pngFile, limits: termimg.Limits{MaxSide: 64}, want: termimg.Info{Format: "png", W: 64, H: 40}},
		{name: "one pixel over the pixel limit", data: pngFile, limits: termimg.Limits{MaxPixels: 64*40 - 1}, err: termimg.ErrTooLarge},
		{name: "exactly the pixel limit", data: pngFile, limits: termimg.Limits{MaxPixels: 64 * 40}, want: termimg.Info{Format: "png", W: 64, H: 40}},
		{name: "too many bytes is said before the format is looked at", data: bytes.Repeat([]byte("x"), 100), limits: termimg.Limits{MaxBytes: 99}, err: termimg.ErrTooLarge},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got, err := termimg.Inspect(c.data, c.limits)
			checkErr(t, err, c.err, neither)
			if got != c.want {
				t.Errorf("Inspect = %+v, want %+v", got, c.want)
			}
		})
	}
}

// checkErr compares an error with what a case expects: nil, one of the
// package's two errors, or (as neither) an error that is not one of them.
func checkErr(t *testing.T, err, want, neither error) {
	t.Helper()
	switch {
	case want == nil:
		if err != nil {
			t.Errorf("error = %v, want none", err)
		}
	case errors.Is(want, neither):
		if err == nil || errors.Is(err, termimg.ErrFormat) || errors.Is(err, termimg.ErrTooLarge) {
			t.Errorf("error = %v, want one that is neither ErrFormat nor ErrTooLarge", err)
		}
	case !errors.Is(err, want):
		t.Errorf("error = %v, want %v", err, want)
	}
}

// pngChunk writes one chunk of a PNG file.
func pngChunk(b *bytes.Buffer, kind string, data []byte) {
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(data)))
	b.Write(n[:])
	body := append([]byte(kind), data...)
	b.Write(body)
	binary.BigEndian.PutUint32(n[:], crc32.ChecksumIEEE(body))
	b.Write(n[:])
}

// emptyPNG returns a PNG whose header, sound in every other way, says the
// picture is 0 by 0 pixels.
func emptyPNG() []byte {
	var b bytes.Buffer
	b.WriteString("\x89PNG\r\n\x1a\n")
	ihdr := make([]byte, 13)
	ihdr[8], ihdr[9] = 8, 6 // eight bits a channel, RGBA
	pngChunk(&b, "IHDR", ihdr)
	pngChunk(&b, "IEND", nil)
	return b.Bytes()
}

// pngBomb returns a whole, valid PNG of side by side pixels, one bit each,
// all black: 16000 by 16000 is 31 kB of file and 256 MB once decoded. head is
// the length of its signature and header chunk.
func pngBomb(t *testing.T, side int) (file []byte, head int) {
	t.Helper()
	var b bytes.Buffer
	b.WriteString("\x89PNG\r\n\x1a\n")
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], uint32(side))
	binary.BigEndian.PutUint32(ihdr[4:], uint32(side))
	ihdr[8] = 1 // one bit a pixel; colour type 0 is grey
	pngChunk(&b, "IHDR", ihdr)
	head = b.Len()

	var body bytes.Buffer
	z, err := zlib.NewWriterLevel(&body, zlib.BestSpeed)
	if err != nil {
		t.Fatalf("zlib: %v", err)
	}
	row := make([]byte, 1+(side+7)/8) // a filter byte, then the row's bits
	for range side {
		if _, err := z.Write(row); err != nil {
			t.Fatalf("zlib: %v", err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatalf("zlib: %v", err)
	}
	pngChunk(&b, "IDAT", body.Bytes())
	pngChunk(&b, "IEND", nil)
	return b.Bytes(), head
}

// jpegBomb returns a JPEG whose header claims side by side pixels, and where
// that header ends. The picture behind it is 16 by 16.
func jpegBomb(t *testing.T, side int) (file []byte, head int) {
	t.Helper()
	file = jpegBytes(t, chart(16, 16))
	// The frame header: FF C0, a length, the precision, height, width.
	at := bytes.Index(file, []byte{0xff, 0xc0})
	if at < 0 {
		t.Fatal("the encoded JPEG has no baseline frame header")
	}
	binary.BigEndian.PutUint16(file[at+5:], uint16(side))
	binary.BigEndian.PutUint16(file[at+7:], uint16(side))
	// The decoder reads on to the start of the scan before it answers, so
	// the header ends with that marker and its two bytes of length.
	scan := bytes.Index(file[at:], []byte{0xff, 0xda})
	if scan < 0 {
		t.Fatal("the encoded JPEG has no scan")
	}
	return file, at + scan + 4
}

// gifBomb returns an animation of 200 frames on a canvas side by side, and
// where its header ends. Decoded whole and composed, each frame would cost
// the canvas again.
func gifBomb(t *testing.T, side int) (file []byte, head int) {
	t.Helper()
	frames := make([]*image.Paletted, 200)
	for i := range frames {
		frames[i] = flat(image.Rect(0, 0, 4, 4), blue())
	}
	file = gifBytes(t, side, side, frames...)
	// The header: the signature, the canvas and three more bytes, then the
	// colour table those bytes announce.
	head = 13
	if file[10]&0x80 != 0 {
		head += 3 << (file[10]&7 + 1)
	}
	return file, head
}

// TestBombs checks that a file whose header claims a huge picture is refused
// from the header alone. Each bomb is refused whole, and refused the same way
// with everything after its header cut off: had the refusal come from
// decoding, the cut file would fail differently, as the last check shows it
// does when the limits let it through.
func TestBombs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		make func(t *testing.T) (file []byte, head int)
		side int
	}{
		{name: "a 16000x16000 one-bit PNG", make: func(t *testing.T) ([]byte, int) { t.Helper(); return pngBomb(t, 16000) }, side: 16000},
		{name: "a JPEG claiming 20000x20000", make: func(t *testing.T) ([]byte, int) { t.Helper(); return jpegBomb(t, 20000) }, side: 20000},
		{name: "a 200-frame GIF on a 9000x9000 canvas", make: func(t *testing.T) ([]byte, int) { t.Helper(); return gifBomb(t, 9000) }, side: 9000},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			file, head := c.make(t)
			if int64(len(file)) > termimg.DefaultLimits().MaxBytes {
				t.Fatalf("the bomb is %d bytes: it would be refused for its size, which is not the point", len(file))
			}
			for _, data := range [][]byte{file, file[:head]} {
				if _, err := termimg.Inspect(data, termimg.DefaultLimits()); !errors.Is(err, termimg.ErrTooLarge) {
					t.Errorf("Inspect of %d bytes: error = %v, want ErrTooLarge", len(data), err)
				}
				img, info, err := termimg.Decode(data, termimg.DefaultLimits())
				if !errors.Is(err, termimg.ErrTooLarge) || img != nil || info != (termimg.Info{}) {
					t.Errorf("Decode of %d bytes = %v, %+v, %v; want nothing and ErrTooLarge", len(data), img, info, err)
				}
			}

			// The header really does claim that size, and with limits
			// that allow it the cut file gets as far as the decoder.
			open := termimg.Limits{MaxSide: 1 << 20, MaxPixels: 1 << 40}
			info, err := termimg.Inspect(file[:head], open)
			if err != nil || info.W != c.side || info.H != c.side {
				t.Errorf("Inspect with open limits = %+v, %v; want %dx%d", info, err, c.side, c.side)
			}
			if _, _, err := termimg.Decode(file[:head], open); err == nil || errors.Is(err, termimg.ErrTooLarge) || errors.Is(err, termimg.ErrFormat) {
				t.Errorf("Decode of the cut file with open limits: error = %v, want a decoding error", err)
			}
		})
	}
}

// TestPNGBombIsValid checks the test's own bomb: the standard decoder reads
// its header as a 16000x16000 picture, so it is the real thing.
func TestPNGBombIsValid(t *testing.T) {
	t.Parallel()
	file, _ := pngBomb(t, 16000)
	cfg, err := png.DecodeConfig(bytes.NewReader(file))
	if err != nil || cfg.Width != 16000 || cfg.Height != 16000 || cfg.ColorModel != color.GrayModel {
		t.Errorf("DecodeConfig = %+v, %v; want a 16000x16000 grey picture", cfg, err)
	}
	if len(file) > 64<<10 {
		t.Errorf("the bomb is %d bytes, want a small file", len(file))
	}
}

func TestDecode(t *testing.T) {
	t.Parallel()
	pic := chart(64, 40)
	neither := errors.New("neither ErrFormat nor ErrTooLarge")
	pngFile := pngBytes(t, pic)
	cases := []struct {
		name   string
		data   []byte
		limits termimg.Limits
		want   termimg.Info
		exact  bool // the format keeps every pixel
		err    error
	}{
		{name: "png", data: pngFile, want: termimg.Info{Format: "png", W: 64, H: 40}, exact: true},
		{name: "jpeg", data: jpegBytes(t, pic), want: termimg.Info{Format: "jpeg", W: 64, H: 40}},
		{name: "not an image", data: []byte("hello"), err: termimg.ErrFormat},
		{name: "over a limit", data: pngFile, limits: termimg.Limits{MaxSide: 10}, err: termimg.ErrTooLarge},
		{name: "a png cut in its body, as one being written is", data: pngFile[:len(pngFile)/2], err: neither},
		{name: "a png with a damaged body", data: damaged(pngFile), err: neither},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			img, info, err := termimg.Decode(c.data, c.limits)
			checkErr(t, err, c.err, neither)
			if info != c.want {
				t.Errorf("info = %+v, want %+v", info, c.want)
			}
			if c.err != nil {
				if img != nil {
					t.Errorf("a failed Decode returned a picture")
				}
				return
			}
			if img == nil {
				t.Fatal("no picture")
			}
			if got := img.Bounds().Size(); got != image.Pt(c.want.W, c.want.H) {
				t.Errorf("picture is %v, want %dx%d", got, c.want.W, c.want.H)
			}
			if c.exact {
				same(t, termimg.Scale(img, 64, 40), pic)
			}
		})
	}
}

// damaged returns a file with a run of bytes in its second half overwritten.
func damaged(file []byte) []byte {
	out := bytes.Clone(file)
	for i := len(out) / 2; i < len(out)/2+16; i++ {
		out[i] ^= 0x5a
	}
	return out
}

// TestDecodeGIFFirstFrame checks that an animation costs one frame: the
// picture is the first frame, and nothing after it is read.
func TestDecodeGIFFirstFrame(t *testing.T) {
	t.Parallel()
	frames := make([]*image.Paletted, 200)
	frames[0] = flat(image.Rect(0, 0, 32, 20), orange())
	for i := 1; i < len(frames); i++ {
		frames[i] = flat(image.Rect(0, 0, 32, 20), blue())
	}
	file := gifBytes(t, 32, 20, frames...)

	img, info, err := termimg.Decode(file, termimg.Limits{})
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if want := (termimg.Info{Format: "gif", W: 32, H: 20}); info != want {
		t.Errorf("info = %+v, want %+v", info, want)
	}
	if got := termimg.Scale(img, 1, 1).RGBAAt(0, 0); got != orange() {
		t.Errorf("the picture is %v all over, want the first frame's %v", got, orange())
	}

	// One frame, then bytes no decoder accepts: a frame said to be 60000
	// pixels a side on a canvas of 32 by 20.
	one := gifBytes(t, 32, 20, frames[0])
	poisoned := append(bytes.Clone(one[:len(one)-1]), 0x2c, 0, 0, 0, 0, 0x60, 0xea, 0x60, 0xea, 0)
	if _, err := gif.DecodeAll(bytes.NewReader(poisoned)); err == nil {
		t.Fatal("the test's poisoned file decodes whole; it proves nothing")
	}
	img, _, err = termimg.Decode(poisoned, termimg.Limits{})
	if err != nil {
		t.Fatalf("Decode read past the first frame: %v", err)
	}
	if got := termimg.Scale(img, 1, 1).RGBAAt(0, 0); got != orange() {
		t.Errorf("the picture is %v, want the first frame's %v", got, orange())
	}
}

// TestDecodeGIFSmallFrame checks a GIF whose first frame covers part of its
// canvas: the Info describes the picture that came back.
func TestDecodeGIFSmallFrame(t *testing.T) {
	t.Parallel()
	file := gifBytes(t, 64, 40, flat(image.Rect(8, 4, 24, 14), orange()))
	if info, err := termimg.Inspect(file, termimg.Limits{}); err != nil || info != (termimg.Info{Format: "gif", W: 64, H: 40}) {
		t.Errorf("Inspect = %+v, %v; want the 64x40 canvas", info, err)
	}
	img, info, err := termimg.Decode(file, termimg.Limits{})
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if want := (termimg.Info{Format: "gif", W: 16, H: 10}); info != want {
		t.Errorf("info = %+v, want %+v", info, want)
	}
	scaled := termimg.Scale(img, 16, 10)
	if scaled.Bounds() != image.Rect(0, 0, 16, 10) || scaled.RGBAAt(0, 0) != orange() || scaled.RGBAAt(15, 9) != orange() {
		t.Errorf("the scaled frame is %v with corners %v and %v", scaled.Bounds(), scaled.RGBAAt(0, 0), scaled.RGBAAt(15, 9))
	}
}

// countdown is a context that ends once its Err has been asked a number of
// times. It stands for a deadline that passes while a file is being decoded,
// without a clock.
type countdown struct {
	context.Context //nolint:containedctx // the type is a context: it wraps one to change its Err

	left, asked int
}

func (c *countdown) Err() error {
	c.asked++
	if c.left > 0 {
		c.left--
		return nil
	}
	return context.Canceled
}

// scanBomb returns a valid progressive JPEG, grey, side by side pixels, with
// that many scans after its first. Each scan is 34 bytes of file and a pass
// over every block of the picture, so the time to decode it grows with the
// file's length and not with the picture's size: at the default limits, 8 MiB
// of scans keep a decoder busy for minutes.
func scanBomb(side, scans int) []byte {
	var b bytes.Buffer
	b.Write([]byte{0xff, 0xd8})
	b.Write([]byte{0xff, 0xdb, 0, 67, 0}) // one quantisation table
	b.Write(bytes.Repeat([]byte{1}, 64))
	b.Write([]byte{0xff, 0xc2, 0, 11, 8}) // a progressive frame
	var size [2]byte
	binary.BigEndian.PutUint16(size[:], uint16(side))
	b.Write(size[:])
	b.Write(size[:])
	b.Write([]byte{1, 1, 0x11, 0}) // one component
	// Two Huffman tables of one code each, a single zero bit: for the first
	// scan "no change", and for the others "the next 16384 blocks are empty".
	for _, table := range [][2]byte{{0x00, 0x00}, {0x10, 0xe0}} {
		b.Write([]byte{0xff, 0xc4, 0, 20, table[0], 1})
		b.Write(make([]byte, 15))
		b.WriteByte(table[1])
	}
	blocks := ((side + 7) / 8) * ((side + 7) / 8)
	b.Write([]byte{0xff, 0xda, 0, 8, 1, 1, 0x00, 0, 0, 0})
	b.Write(make([]byte, blocks/8+2))
	run := make([]byte, 24)
	if need := (blocks/16384+1)*15/8 + 2; need > len(run) {
		run = make([]byte, need)
	}
	for range scans {
		b.Write([]byte{0xff, 0xda, 0, 8, 1, 1, 0x00, 1, 63, 0})
		b.Write(run)
	}
	b.Write([]byte{0xff, 0xd9})
	return b.Bytes()
}

// TestDecodeStops checks that a decode ends when its context does, at the
// decoder's next read, although the file is whole and would decode. A job
// relies on it to give up on a file made to be slow.
func TestDecodeStops(t *testing.T) {
	t.Parallel()
	pic := chart(640, 360)
	cases := []struct {
		name string
		data []byte
	}{
		{name: "png", data: pngBytes(t, noise(200, 120))},
		{name: "jpeg", data: jpegBytes(t, pic)},
		{name: "gif", data: gifBytes(t, 200, 120, noisy(200, 120))},
		// 4000 scans of a small picture: quick to decode whole, and long
		// enough as a file to be stopped in the middle of.
		{name: "a jpeg of many scans", data: scanBomb(64, 4000)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			const chunk = 512 // the most a decoder is handed at a time
			if len(c.data) < 20*chunk {
				t.Fatalf("the file is %d bytes: too short to be stopped inside", len(c.data))
			}
			// With time enough, the file decodes, and Decode agrees.
			live := &countdown{Context: t.Context(), left: 1 << 30}
			img, info, err := termimg.DecodeContext(live, c.data, termimg.Limits{})
			if err != nil || img == nil {
				t.Fatalf("the file does not decode: %v", err)
			}
			if _, plain, err := termimg.Decode(c.data, termimg.Limits{}); err != nil || plain != info {
				t.Errorf("Decode = %+v, %v; want %+v", plain, err, info)
			}
			if live.asked < len(c.data)/chunk {
				t.Errorf("the context was asked %d times over %d bytes, want once every %d at least", live.asked, len(c.data), chunk)
			}

			// With time for five reads, it stops there.
			short := &countdown{Context: t.Context(), left: 5}
			img, info, err = termimg.DecodeContext(short, c.data, termimg.Limits{})
			if err == nil || errors.Is(err, termimg.ErrFormat) || errors.Is(err, termimg.ErrTooLarge) {
				t.Errorf("error = %v, want one that says the file could not be read", err)
			}
			if img != nil || info != (termimg.Info{}) {
				t.Errorf("a stopped decode returned %v, %+v", img, info)
			}
			if short.asked > 8 {
				t.Errorf("the decoder read on %d times after the context ended", short.asked-6)
			}
		})
	}
}

// noisy is a GIF frame that does not compress.
func noisy(w, h int) *image.Paletted {
	pal := make(color.Palette, 256)
	for i := range pal {
		pal[i] = color.RGBA{R: uint8(i), G: uint8(255 - i), B: uint8(i * 7), A: 0xff}
	}
	img := image.NewPaletted(image.Rect(0, 0, w, h), pal)
	src := noise(w, h)
	for i := range img.Pix {
		img.Pix[i] = src.Pix[4*i]
	}
	return img
}

// same fails the test when two pictures differ in size or in any pixel.
func same(t *testing.T, got, want *image.RGBA) {
	t.Helper()
	if got.Bounds().Size() != want.Bounds().Size() {
		t.Fatalf("picture is %v, want %v", got.Bounds().Size(), want.Bounds().Size())
	}
	size := want.Bounds().Size()
	for y := range size.Y {
		for x := range size.X {
			g := got.RGBAAt(got.Rect.Min.X+x, got.Rect.Min.Y+y)
			w := want.RGBAAt(want.Rect.Min.X+x, want.Rect.Min.Y+y)
			if g != w {
				t.Fatalf("pixel (%d,%d) = %v, want %v", x, y, g, w)
			}
		}
	}
}

// FuzzDecode checks, for any bytes at all, that nothing panics, that Inspect
// and Decode agree on what is refused, and that a picture which does come
// back is within the limits it was decoded under.
func FuzzDecode(f *testing.F) {
	pic := chart(40, 24)
	f.Add(pngBytes(f, pic))
	f.Add(jpegBytes(f, pic))
	f.Add(gifBytes(f, 40, 24, flat(image.Rect(0, 0, 40, 24), blue()), flat(image.Rect(4, 4, 9, 9), orange())))
	f.Add([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x3e\x80\x00\x00\x3e\x80\x01\x00\x00\x00\x00"))
	f.Add([]byte("GIF89a\xff\xff\xff\xff\x00\x00\x00"))
	f.Add([]byte("\xff\xd8\xff\xc0\x00\x11\x08\x4e\x20\x4e\x20\x03"))
	f.Add([]byte("not an image"))
	f.Add([]byte{})
	limits := termimg.Limits{MaxBytes: 1 << 16, MaxSide: 256, MaxPixels: 1 << 14}
	f.Fuzz(func(t *testing.T, data []byte) {
		info, inspectErr := termimg.Inspect(data, limits)
		if inspectErr == nil && (info.W < 1 || info.H < 1 || info.W > 256 || info.H > 256 || info.W*info.H > 1<<14) {
			t.Fatalf("Inspect accepted %+v, outside the limits", info)
		}
		img, got, err := termimg.Decode(data, limits)
		if inspectErr != nil {
			if img != nil || err == nil || err.Error() != inspectErr.Error() {
				t.Fatalf("Inspect refused with %v, Decode returned %v, %v", inspectErr, img, err)
			}
			return
		}
		if err != nil {
			if img != nil || got != (termimg.Info{}) {
				t.Fatalf("a failed Decode returned %v, %+v", img, got)
			}
			return
		}
		size := img.Bounds().Size()
		if size.X != got.W || size.Y != got.H || got.W < 1 || got.H < 1 || got.W > info.W || got.H > info.H || got.Format != info.Format {
			t.Fatalf("Decode returned a picture %v with info %+v; Inspect said %+v", size, got, info)
		}
		// Whatever it is, it scales and draws.
		rows := termimg.HalfBlocks(termimg.Scale(img, 8, 6))
		if len(rows) < 1 || len(rows) > 3 {
			t.Fatalf("%d rows of half blocks from a picture %v", len(rows), size)
		}
	})
}
