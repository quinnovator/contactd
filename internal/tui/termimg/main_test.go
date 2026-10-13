package termimg_test

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/quinnovator/contactd/internal/tui/termimg"
	"github.com/quinnovator/contactd/internal/tui/tuitest"
)

func TestMain(m *testing.M) { tuitest.Main(m) }

// The colours of the mock-up's chart.
func ground() color.RGBA { return color.RGBA{R: 0xf4, G: 0xf5, B: 0xf3, A: 0xff} }
func axis() color.RGBA   { return color.RGBA{R: 0x2b, G: 0x31, B: 0x37, A: 0xff} }
func blue() color.RGBA   { return color.RGBA{R: 0x3f, G: 0x6f, B: 0xa8, A: 0xff} }
func orange() color.RGBA { return color.RGBA{R: 0xd9, G: 0x82, B: 0x2b, A: 0xff} }

// mosaic is the chart of Images.dc.html exactly as the mock-up samples it for
// its half-block panel: 44 by 18 pixels, seven pairs of bars on an axis.
func mosaic() *image.RGBA {
	p50 := []int{6, 5, 8, 10, 9, 6, 5}
	p99 := []int{11, 10, 12, 14, 12, 9, 8}
	img := image.NewRGBA(image.Rect(0, 0, 44, 18))
	for y := range 18 {
		for x := range 44 {
			c := ground()
			i, lx := (x-3)/6, (x-3)%6
			switch {
			case y == 16 && x >= 2:
				c = axis()
			case y > 15 || x < 3 || i > 6:
			case lx < 2 && y > 15-p50[i]:
				c = blue()
			case lx >= 2 && lx < 4 && y > 15-p99[i]:
				c = orange()
			}
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

// chart is the same chart drawn w by h pixels, as a tool would save it.
func chart(w, h int) *image.RGBA {
	small := mosaic()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetRGBA(x, y, small.RGBAAt(x*44/w, y*18/h))
		}
	}
	return img
}

// noise is a picture that does not compress: every pixel differs from its
// neighbours. It is the same on every run.
func noise(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	state := uint32(0x2545f491)
	for i := 0; i < len(img.Pix); i += 4 {
		state ^= state << 13
		state ^= state >> 17
		state ^= state << 5
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = uint8(state), uint8(state>>8), uint8(state>>16), 0xff
	}
	return img
}

func pngBytes(tb testing.TB, img image.Image) []byte {
	tb.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		tb.Fatalf("encode png: %v", err)
	}
	return b.Bytes()
}

func jpegBytes(tb testing.TB, img image.Image) []byte {
	tb.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 90}); err != nil {
		tb.Fatalf("encode jpeg: %v", err)
	}
	return b.Bytes()
}

// gifBytes encodes frames as one animation on a canvas w by h.
func gifBytes(tb testing.TB, w, h int, frames ...*image.Paletted) []byte {
	tb.Helper()
	g := &gif.GIF{Config: image.Config{Width: w, Height: h, ColorModel: frames[0].Palette}}
	for _, f := range frames {
		g.Image = append(g.Image, f)
		g.Delay = append(g.Delay, 5)
	}
	var b bytes.Buffer
	if err := gif.EncodeAll(&b, g); err != nil {
		tb.Fatalf("encode gif: %v", err)
	}
	return b.Bytes()
}

// flat is a GIF frame of one colour.
func flat(r image.Rectangle, c color.RGBA) *image.Paletted {
	return image.NewPaletted(r, color.Palette{c, color.RGBA{A: 0xff}})
}

// call is one request a fake server saw.
type call struct{ path, etag string }

// server is a fake of the contact's working directory behind a Fetch. It is
// safe for concurrent use, because a job may run on another goroutine.
type server struct {
	mu    sync.Mutex
	files map[string]termimg.FileData
	errs  map[string]error
	calls []call
}

func newServer() *server {
	return &server{files: make(map[string]termimg.FileData), errs: make(map[string]error)}
}

// put stores a file. Its ETag changes whenever its bytes do.
func (s *server) put(path string, data []byte, modified time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sum := 0
	for _, b := range data {
		sum = sum*31 + int(b)
	}
	etag := `"` + strconv.Itoa(len(data)) + "-" + strconv.Itoa(sum) + `"`
	s.files[path] = termimg.FileData{Data: data, ETag: etag, Modified: modified}
	delete(s.errs, path)
}

// fail makes every fetch of a path return err.
func (s *server) fail(path string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errs[path] = err
}

// fetch is the termimg.Fetch.
func (s *server) fetch(_ context.Context, path, etag string) (termimg.FileData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, call{path: path, etag: etag})
	if err := s.errs[path]; err != nil {
		return termimg.FileData{}, err
	}
	f, ok := s.files[path]
	if !ok {
		return termimg.FileData{}, termimg.ErrNotFound
	}
	if etag != "" && etag == f.ETag {
		return termimg.FileData{ETag: f.ETag, Modified: f.Modified, NotModified: true}, nil
	}
	return f, nil
}

// seen returns the requests so far.
func (s *server) seen() []call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]call(nil), s.calls...)
}
