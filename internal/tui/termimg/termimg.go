// Package termimg shows an image from a reply in a terminal.
//
// There are three ways to draw one, and the terminal's capabilities choose
// between them: the picture itself through the kitty graphics protocol, a
// mosaic of half-block characters, or one line of text that names the file.
//
// The bytes come from a contact's working directory, so a model chose them.
// They are never handed to the terminal. A file is measured before it is
// decoded (Inspect), decoded under limits (Decode), scaled down to a small
// master picture (Scale), and only that picture is encoded again for the
// screen. A file that claims to be huge is refused from its header alone.
//
// A Store ties the steps together for a chat view. It answers at once with a
// line of text, hands out the slow work as one Job at a time for the caller to
// run off its update loop, and gives back the rows to draw when the job is
// done. Nothing in this package does I/O: a Job is handed the function that
// fetches a file.
package termimg

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
)

// The formats Inspect and Decode read.
const (
	formatPNG  = "png"
	formatJPEG = "jpeg"
	formatGIF  = "gif"
)

// Limits bound what Inspect and Decode accept. A field that is zero or less
// takes its value from DefaultLimits.
type Limits struct {
	// MaxBytes is the largest file, in bytes.
	MaxBytes int64
	// MaxSide is the largest width or height, in pixels.
	MaxSide int
	// MaxPixels is the largest width times height.
	MaxPixels int
}

// DefaultLimits returns 8 MiB, 8192 pixels a side and 12,000,000 pixels.
//
// The pixels bound what a decode can allocate, whatever the file's size,
// because a decoder allocates from the header's claim before it reads the
// body. Twelve million pixels are 46 MiB as an 8-bit picture. The standard
// decoders need more for some files while they work: about 90 MiB for a PNG
// with 16 bits a channel, and up to about 230 MiB for a progressive JPEG,
// which keeps every coefficient until its last scan.
func DefaultLimits() Limits {
	return Limits{MaxBytes: 8 << 20, MaxSide: 8192, MaxPixels: 12_000_000}
}

// filled returns l with each unset field replaced by its default.
func (l Limits) filled() Limits {
	d := DefaultLimits()
	if l.MaxBytes <= 0 {
		l.MaxBytes = d.MaxBytes
	}
	if l.MaxSide <= 0 {
		l.MaxSide = d.MaxSide
	}
	if l.MaxPixels <= 0 {
		l.MaxPixels = d.MaxPixels
	}
	return l
}

// Info is what a file's header says about its picture.
type Info struct {
	// Format is "png", "jpeg" or "gif".
	Format string
	// W and H are the picture's width and height in pixels.
	W, H int
}

var (
	// ErrFormat means the bytes are not a PNG, a JPEG or a GIF.
	ErrFormat = errors.New("not a PNG, JPEG or GIF image")
	// ErrTooLarge means the file or its picture is over a limit. A Fetch
	// returns it for a file the server would not send whole.
	ErrTooLarge = errors.New("image is too large")
	// ErrNotFound is what a Fetch returns when the file does not exist.
	ErrNotFound = errors.New("image not found")
)

// Inspect reads a file's header and checks it against the limits. It never
// decodes the picture, so its cost does not depend on what the header claims.
//
// The format is told from the first bytes, not from a name. Anything but PNG,
// JPEG and GIF is ErrFormat. A file over MaxBytes, or a picture over MaxSide
// or MaxPixels, is ErrTooLarge. A file of a known format whose header cannot
// be read, and an empty file, give some other error: the file may be half
// written.
func Inspect(data []byte, l Limits) (Info, error) {
	l = l.filled()
	if int64(len(data)) > l.MaxBytes {
		return Info{}, fmt.Errorf("%w: %d bytes, the limit is %d", ErrTooLarge, len(data), l.MaxBytes)
	}
	if len(data) == 0 {
		return Info{}, errors.New("termimg: the file is empty")
	}
	format := sniff(data)
	var (
		cfg image.Config
		err error
	)
	switch format {
	case formatPNG:
		cfg, err = png.DecodeConfig(bytes.NewReader(data))
	case formatJPEG:
		cfg, err = jpeg.DecodeConfig(bytes.NewReader(data))
	case formatGIF:
		cfg, err = gif.DecodeConfig(bytes.NewReader(data))
	default:
		return Info{}, ErrFormat
	}
	if err != nil {
		return Info{}, fmt.Errorf("termimg: read the %s header: %w", format, err)
	}
	info := Info{Format: format, W: cfg.Width, H: cfg.Height}
	if info.W <= 0 || info.H <= 0 {
		return Info{}, fmt.Errorf("termimg: the %s header gives an empty picture", format)
	}
	if info.W > l.MaxSide || info.H > l.MaxSide || int64(info.W)*int64(info.H) > int64(l.MaxPixels) {
		return Info{}, fmt.Errorf("%w: %dx%d pixels", ErrTooLarge, info.W, info.H)
	}
	return info, nil
}

// Decode decodes a file's picture. It calls Inspect first and decodes only
// what Inspect accepted. Of a GIF it decodes the first frame and reads no
// further, so an animation costs one frame.
//
// A failure after Inspect is neither ErrFormat nor ErrTooLarge: the file has
// the right header and a body that cannot be read, as one still being
// written has.
//
// The Info describes the picture that is returned. That differs from
// Inspect's only for a GIF whose first frame is smaller than its canvas.
//
// The limits bound the memory a decode takes, not its time: a small
// progressive JPEG can hold thousands of scans, each a pass over the whole
// picture. A Job therefore decodes under a deadline; Decode itself runs to
// the end.
func Decode(data []byte, l Limits) (image.Image, Info, error) {
	return decode(context.Background(), data, l)
}

// decode is Decode that gives up when ctx ends. The decoders cannot be
// stopped from outside, so the file is handed to them through a reader that
// fails once ctx is done: they stop at their next read, which is never more
// than a few hundred bytes of file away.
func decode(ctx context.Context, data []byte, l Limits) (image.Image, Info, error) {
	info, err := Inspect(data, l)
	if err != nil {
		return nil, Info{}, err
	}
	var img image.Image
	file := &fileReader{ctx: ctx, data: data}
	switch info.Format {
	case formatPNG:
		img, err = png.Decode(file)
	case formatJPEG:
		img, err = jpeg.Decode(file)
	case formatGIF:
		img, err = gif.Decode(file)
	default:
		return nil, Info{}, ErrFormat
	}
	if err != nil {
		return nil, Info{}, fmt.Errorf("termimg: decode the %s: %w", info.Format, err)
	}
	b := img.Bounds()
	if b.Empty() || b.Dx() > info.W || b.Dy() > info.H {
		return nil, Info{}, fmt.Errorf("termimg: the %s decoded to %dx%d, its header said %dx%d",
			info.Format, b.Dx(), b.Dy(), info.W, info.H)
	}
	info.W, info.H = b.Dx(), b.Dy()
	return img, info, nil
}

// readChunk is the most bytes a decoder is given at a time, so that it comes
// back to ask, and can be stopped, often.
const readChunk = 512

// fileReader hands a file's bytes to a decoder, a little at a time, for as
// long as its context lasts.
type fileReader struct {
	ctx  context.Context //nolint:containedctx // the reader lives for one decode, inside the call that owns the context
	data []byte
}

// Read implements io.Reader.
func (f *fileReader) Read(p []byte) (int, error) {
	if err := f.ctx.Err(); err != nil {
		return 0, fmt.Errorf("termimg: decoding stopped: %w", err)
	}
	if len(f.data) == 0 {
		return 0, io.EOF
	}
	n := copy(p[:min(len(p), readChunk)], f.data)
	f.data = f.data[n:]
	return n, nil
}

// sniff names a file's format from its first bytes, or returns "".
func sniff(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return formatPNG
	case bytes.HasPrefix(data, []byte("\xff\xd8\xff")):
		return formatJPEG
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
		return formatGIF
	}
	return ""
}
