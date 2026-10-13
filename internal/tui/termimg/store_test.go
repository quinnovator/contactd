package termimg_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/png"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/quinnovator/contactd/internal/tui/termcaps"
	"github.com/quinnovator/contactd/internal/tui/termimg"
)

// caps is a terminal with 8x16 cells that draws images at one level.
func caps(level termcaps.Images, change ...func(*termcaps.Caps)) termcaps.Caps {
	c := termcaps.Caps{Images: level, Glyphs: termcaps.GlyphsUnicode, CellW: 8, CellH: 16}
	for _, f := range change {
		f(&c)
	}
	return c
}

func viaTmux(c *termcaps.Caps) { c.Tmux = true }

func asciiGlyphs(c *termcaps.Caps) { c.Glyphs = termcaps.GlyphsASCII }

func cellSize(w, h int) func(*termcaps.Caps) {
	return func(c *termcaps.Caps) { c.CellW, c.CellH = w, h }
}

// newStore returns a store at a level whose first kitty id is 7.
func newStore(level termcaps.Images, change ...func(*termcaps.Caps)) *termimg.Store {
	s := termimg.NewStore(termimg.StoreOptions{IDBase: 6})
	s.SetCaps(caps(level, change...))
	return s
}

// latency is the reference the mock-up draws: reply 31's only image.
func latency() termimg.Ref {
	return termimg.Ref{Seq: 31, N: 1, Path: "latency.png", Alt: "latency by day", TS: sent().UnixMilli()}
}

// sent is when the test's replies were written; made is when their files
// were, a minute before.
func sent() time.Time { return time.Date(2026, 10, 12, 12, 6, 0, 0, time.UTC) }
func made() time.Time { return sent().Add(-time.Minute) }

// withChart returns a server that has the mock-up's chart, 640 by 360, at
// latency.png, and the file's size as the lines write it.
func withChart(t *testing.T) (*server, string) {
	t.Helper()
	srv := newServer()
	data := pngBytes(t, chart(640, 360))
	srv.put("latency.png", data, made())
	return srv, sizeText(data)
}

// sizeText is a file's size as the lines write it.
func sizeText(data []byte) string {
	line := termimg.TextLine("x", termimg.Info{}, int64(len(data)), false)
	return strings.TrimSuffix(strings.TrimPrefix(line, "[image: x, "), "]")
}

// step runs the next job to its end and returns what Finish said. It fails
// the test when there is no job.
func step(t *testing.T, s *termimg.Store, srv *server) (seq uint64, raw string) {
	t.Helper()
	job, ok := s.Next()
	if !ok {
		t.Fatal("Next has no job")
	}
	return s.Finish(job(t.Context(), srv.fetch))
}

// settle runs jobs until there are none, and returns what each Finish said.
func settle(t *testing.T, s *termimg.Store, srv *server) (seqs []uint64, raws []string) {
	t.Helper()
	for range 2000 {
		job, ok := s.Next()
		if !ok {
			return seqs, raws
		}
		seq, raw := s.Finish(job(t.Context(), srv.fetch))
		seqs, raws = append(seqs, seq), append(raws, raw)
	}
	t.Fatal("the store never runs out of jobs")
	return nil, nil
}

// idle fails the test when the store has a job to hand out.
func idle(t *testing.T, s *termimg.Store) {
	t.Helper()
	if _, ok := s.Next(); ok {
		t.Fatal("Next has a job, want none")
	}
}

// master is the picture the store keeps of the 640x360 chart at 8x16 cells:
// the 44x12 box is 352 by 192 pixels, and 16:9 fits it 341 wide.
func master() *image.RGBA { return termimg.Scale(chart(640, 360), 341, 192) }

// transmitted reads a kitty transmit: the keys of its first command and the
// picture in it.
func transmitted(t *testing.T, raw string) (keys string, pic image.Image) {
	t.Helper()
	cmds := commands(t, raw)
	if len(cmds) == 0 {
		t.Fatal("no kitty command")
	}
	var payload strings.Builder
	for _, c := range cmds {
		payload.WriteString(c.payload)
	}
	file, err := base64.StdEncoding.DecodeString(payload.String())
	if err != nil {
		t.Fatalf("the payload is not base64: %v", err)
	}
	pic, err = png.Decode(bytes.NewReader(file))
	if err != nil {
		t.Fatalf("the payload is not a PNG: %v", err)
	}
	return cmds[0].keys, pic
}

func equalRows(t *testing.T, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("rows differ: got %d rows starting %q, want %d starting %q", len(got), head(strings.Join(got, "")), len(want), head(strings.Join(want, "")))
	}
}

func TestStoreWanted(t *testing.T) {
	t.Parallel()
	srv, _ := withChart(t)
	s := newStore(termcaps.ImagesBlocks)
	idle(t, s)

	got := s.Show(latency(), 44, 12)
	if want := (termimg.Shown{Line: "[image: latency.png]"}); !sameShown(got, want) {
		t.Errorf("first Show = %+v, want %+v", got, want)
	}
	if calls := srv.seen(); len(calls) != 0 {
		t.Errorf("Show fetched: %v", calls)
	}
	job, ok := s.Next()
	if !ok {
		t.Fatal("Next has no job for a wanted image")
	}
	if calls := srv.seen(); len(calls) != 0 {
		t.Errorf("Next fetched: %v", calls)
	}
	// Still loading, however often it is drawn.
	if got := s.Show(latency(), 44, 12); got.Rows != nil || got.Line != "[image: latency.png]" {
		t.Errorf("Show while loading = %+v", got)
	}
	done := job(t.Context(), srv.fetch)
	if got := s.Show(latency(), 44, 12); got.Rows != nil {
		t.Errorf("Show before Finish has rows")
	}
	seq, raw := s.Finish(done)
	if seq != 31 || raw != "" {
		t.Errorf("Finish = %d, %q; want 31 and nothing for the terminal", seq, head(raw))
	}
	if got := s.Show(latency(), 44, 12); len(got.Rows) != 12 {
		t.Errorf("Show after Finish has %d rows, want 12", len(got.Rows))
	}
	idle(t, s)
	if got, want := srv.seen(), []call{{path: "latency.png"}}; !slices.Equal(got, want) {
		t.Errorf("fetches = %v, want %v", got, want)
	}
	if got := s.Stats(); got.Jobs != 1 || got.Encodes != 1 {
		t.Errorf("Stats = %+v, want one job and one encode", got)
	}
}

func sameShown(a, b termimg.Shown) bool {
	return slices.Equal(a.Rows, b.Rows) && (a.Rows == nil) == (b.Rows == nil) &&
		a.Caption == b.Caption && a.Line == b.Line && a.Hint == b.Hint
}

func TestStoreOneJobAtATime(t *testing.T) {
	t.Parallel()
	srv := newServer()
	for _, name := range []string{"a.png", "b.png", "c.png"} {
		srv.put(name, pngBytes(t, chart(64, 36)), made())
	}
	s := newStore(termcaps.ImagesText)
	a := termimg.Ref{Seq: 1, N: 1, Path: "a.png"}
	b := termimg.Ref{Seq: 2, N: 1, Path: "b.png"}
	c := termimg.Ref{Seq: 3, N: 1, Path: "c.png"}
	s.Show(a, 44, 12)
	s.Show(b, 44, 12)
	s.Show(c, 44, 12)

	first, ok := s.Next()
	if !ok {
		t.Fatal("Next has no job")
	}
	for range 3 {
		if _, ok := s.Next(); ok {
			t.Fatal("Next handed out a second job while one is out")
		}
	}
	// A job that has run but is not finished is still out.
	done := first(t.Context(), srv.fetch)
	if _, ok := s.Next(); ok {
		t.Fatal("Next handed out a second job before Finish")
	}
	// The image shown last goes first; showing a again puts it ahead of b.
	s.Show(a, 44, 12)
	if seq, _ := s.Finish(done); seq != 3 {
		t.Errorf("first job was for reply %d, want 3: the image shown last", seq)
	}
	if seq, _ := step(t, s, srv); seq != 1 {
		t.Errorf("second job was for reply %d, want 1", seq)
	}
	if seq, _ := step(t, s, srv); seq != 2 {
		t.Errorf("third job was for reply %d, want 2", seq)
	}
	idle(t, s)
	want := []call{{path: "c.png"}, {path: "a.png"}, {path: "b.png"}}
	if got := srv.seen(); !slices.Equal(got, want) {
		t.Errorf("fetches = %v, want %v", got, want)
	}
	if got := s.Stats().Jobs; got != 3 {
		t.Errorf("Stats.Jobs = %d, want 3", got)
	}
}

func TestStoreReady(t *testing.T) {
	t.Parallel()
	t.Run("text", func(t *testing.T) {
		t.Parallel()
		srv, size := withChart(t)
		s := newStore(termcaps.ImagesText)
		s.Show(latency(), 44, 12)
		seqs, raws := settle(t, s, srv)
		if !slices.Equal(seqs, []uint64{31}) || !slices.Equal(raws, []string{""}) {
			t.Errorf("Finish said %v, %q; want reply 31 and nothing for the terminal", seqs, raws)
		}
		want := termimg.Shown{
			Line: "[image: latency.png, 640×360, " + size + "]",
			Hint: "/image save 31 ./latency.png",
		}
		if got := s.Show(latency(), 44, 12); !sameShown(got, want) {
			t.Errorf("Show = %+v, want %+v", got, want)
		}
		if got := s.Stats().Encodes; got != 0 {
			t.Errorf("a text line cost %d encodes", got)
		}
	})
	t.Run("blocks", func(t *testing.T) {
		t.Parallel()
		srv, size := withChart(t)
		s := newStore(termcaps.ImagesBlocks)
		s.Show(latency(), 44, 12)
		seqs, raws := settle(t, s, srv)
		if !slices.Equal(seqs, []uint64{31}) || !slices.Equal(raws, []string{""}) {
			t.Errorf("Finish said %v, %q; want reply 31 and nothing for the terminal", seqs, raws)
		}
		got := s.Show(latency(), 44, 12)
		// 42 by 12 cells, two pixels to a cell, from the master.
		equalRows(t, got.Rows, termimg.HalfBlocks(termimg.Scale(master(), 42, 24)))
		for i, row := range got.Rows {
			if w := ansi.StringWidth(row); w != 42 {
				t.Errorf("row %d is %d cells wide, want 42", i, w)
			}
		}
		if want := "latency.png · 640×360 · " + size + " · /image save 31"; got.Caption != want {
			t.Errorf("caption = %q, want %q", got.Caption, want)
		}
		if got.Line != "" || got.Hint != "" {
			t.Errorf("a picture came with a text line %q and hint %q", got.Line, got.Hint)
		}
	})
	t.Run("pixels", func(t *testing.T) {
		t.Parallel()
		srv, size := withChart(t)
		s := newStore(termcaps.ImagesPixels)
		s.Show(latency(), 44, 12)
		seqs, raws := settle(t, s, srv)
		if !slices.Equal(seqs, []uint64{31}) || len(raws) != 1 {
			t.Fatalf("Finish said %v with %d results; want reply 31 once", seqs, len(raws))
		}
		// 42 by 12 cells of 8 by 16 pixels, from the master.
		want, err := termimg.KittyTransmit(7, termimg.Scale(master(), 336, 192), 42, 12, false)
		if err != nil {
			t.Fatalf("KittyTransmit: %v", err)
		}
		if raws[0] != want {
			t.Errorf("the transmit is not the master scaled to the grid under id 7: starts %q", head(raws[0]))
		}
		got := s.Show(latency(), 44, 12)
		equalRows(t, got.Rows, termimg.KittyPlaceholders(7, 42, 12))
		if want := "latency.png · 640×360 · " + size + " · /image save 31"; got.Caption != want {
			t.Errorf("caption = %q, want %q", got.Caption, want)
		}
		for _, row := range got.Rows {
			if strings.Contains(row, "\x1b_") {
				t.Errorf("a row for the view holds a graphics command")
			}
		}
		idle(t, s)
		if got := s.Stats(); got.Jobs != 1 || got.Encodes != 1 {
			t.Errorf("Stats = %+v, want one job and one encode", got)
		}
	})
}

func TestStoreFailures(t *testing.T) {
	t.Parallel()
	whole := pngBytes(t, chart(640, 360))
	cases := []struct {
		name   string
		limits termimg.Limits
		setup  func(srv *server)
		fetch  termimg.Fetch // instead of the server's
		ctx    func(t *testing.T) context.Context
		want   string
	}{
		{name: "no such file", setup: func(*server) {}, want: "[image: latency.png, not found]"},
		{
			name:  "not found, wrapped",
			setup: func(srv *server) { srv.fail("latency.png", fmt.Errorf("GET latency.png: %w", termimg.ErrNotFound)) },
			want:  "[image: latency.png, not found]",
		},
		{
			name:  "the server would not send it whole",
			setup: func(srv *server) { srv.fail("latency.png", termimg.ErrTooLarge) },
			want:  "[image: latency.png, too large]",
		},
		{
			name:   "a picture over the limits",
			limits: termimg.Limits{MaxSide: 500},
			setup:  func(srv *server) { srv.put("latency.png", whole, made()) },
			want:   "[image: latency.png, too large]",
		},
		{
			name:   "a file over the limits",
			limits: termimg.Limits{MaxBytes: 100},
			setup:  func(srv *server) { srv.put("latency.png", whole, made()) },
			want:   "[image: latency.png, too large]",
		},
		{
			name:  "text with an image's name",
			setup: func(srv *server) { srv.put("latency.png", []byte("day,p50,p99\nMon,0.8,3.1\n"), made()) },
			want:  "[image: latency.png, not an image]",
		},
		{
			name:  "an SVG",
			setup: func(srv *server) { srv.put("latency.png", []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), made()) },
			want:  "[image: latency.png, not an image]",
		},
		{
			name:  "the connection failed",
			setup: func(srv *server) { srv.fail("latency.png", errors.New("connection refused")) },
			want:  "[image: latency.png, could not be read; /reload to retry]",
		},
		{
			name:  "a file half written",
			setup: func(srv *server) { srv.put("latency.png", whole[:len(whole)/2], made()) },
			want:  "[image: latency.png, could not be read; /reload to retry]",
		},
		{
			name:  "a file just created",
			setup: func(srv *server) { srv.put("latency.png", nil, made()) },
			want:  "[image: latency.png, could not be read; /reload to retry]",
		},
		{
			name: "not modified, to a request that named no version",
			fetch: func(context.Context, string, string) (termimg.FileData, error) {
				return termimg.FileData{NotModified: true}, nil
			},
			want: "[image: latency.png, could not be read; /reload to retry]",
		},
		{
			name:  "a fetch that panics",
			fetch: func(context.Context, string, string) (termimg.FileData, error) { panic("boom") },
			want:  "[image: latency.png, could not be read; /reload to retry]",
		},
		{
			name: "a cancelled context",
			fetch: func(ctx context.Context, _, _ string) (termimg.FileData, error) {
				return termimg.FileData{}, ctx.Err()
			},
			ctx: func(t *testing.T) context.Context {
				t.Helper()
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				return ctx
			},
			want: "[image: latency.png, could not be read; /reload to retry]",
		},
		{
			// The fake server answers whatever the context says, so the
			// file arrives whole and it is the decoding that is stopped.
			name:  "cancelled once the file is here",
			setup: func(srv *server) { srv.put("latency.png", whole, made()) },
			ctx: func(t *testing.T) context.Context {
				t.Helper()
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				return ctx
			},
			want: "[image: latency.png, could not be read; /reload to retry]",
		},
		{name: "no fetch function", want: "[image: latency.png, could not be read; /reload to retry]"},
	}
	for _, c := range cases {
		for _, level := range []termcaps.Images{termcaps.ImagesText, termcaps.ImagesBlocks, termcaps.ImagesPixels} {
			t.Run(c.name+"/"+level.String(), func(t *testing.T) {
				t.Parallel()
				srv := newServer()
				fetch := c.fetch
				if c.setup != nil {
					c.setup(srv)
					fetch = srv.fetch
				}
				ctx := t.Context()
				if c.ctx != nil {
					ctx = c.ctx(t)
				}
				s := termimg.NewStore(termimg.StoreOptions{Limits: c.limits})
				s.SetCaps(caps(level))
				s.Show(latency(), 44, 12)
				job, ok := s.Next()
				if !ok {
					t.Fatal("Next has no job")
				}
				seq, raw := s.Finish(job(ctx, fetch))
				if seq != 31 || raw != "" {
					t.Errorf("Finish = %d, %q; want 31 and nothing for the terminal", seq, head(raw))
				}
				for range 2 {
					if got := s.Show(latency(), 44, 12); !sameShown(got, termimg.Shown{Line: c.want}) {
						t.Errorf("Show = %+v, want only the line %q", got, c.want)
					}
					// A failure is not tried again by being drawn.
					idle(t, s)
				}
				if got := s.DeleteAll(); got != "" {
					t.Errorf("a failed image left something to delete: %q", got)
				}
			})
		}
	}
}

func TestStoreRetry(t *testing.T) {
	t.Parallel()
	whole := pngBytes(t, chart(640, 360))
	srv := newServer()
	srv.put("latency.png", whole[:len(whole)-40], made()) // the tool is still writing it
	srv.put("other.png", whole, made())
	s := newStore(termcaps.ImagesBlocks)
	other := termimg.Ref{Seq: 32, N: 1, Path: "other.png"}
	s.Show(latency(), 44, 12)
	s.Show(other, 44, 12)
	settle(t, s, srv)
	const failed = "[image: latency.png, could not be read; /reload to retry]"
	if got := s.Show(latency(), 44, 12); got.Line != failed {
		t.Fatalf("Show = %+v, want the line %q", got, failed)
	}

	// Retry with the file as it was fails the same way, once.
	s.Retry()
	if seqs, _ := settle(t, s, srv); !slices.Equal(seqs, []uint64{31}) {
		t.Errorf("Retry ran jobs for %v, want only the failed reply 31", seqs)
	}
	if got := s.Show(latency(), 44, 12); got.Line != failed {
		t.Errorf("Show = %+v, want the line %q", got, failed)
	}

	// The file is whole now.
	srv.put("latency.png", whole, made())
	idle(t, s)
	s.Retry()
	if got := s.Show(latency(), 44, 12); !sameShown(got, termimg.Shown{Line: "[image: latency.png]"}) {
		t.Errorf("Show after Retry = %+v, want the loading line", got)
	}
	if seqs, _ := settle(t, s, srv); !slices.Equal(seqs, []uint64{31}) {
		t.Errorf("Retry ran jobs for %v, want only reply 31", seqs)
	}
	if got := s.Show(latency(), 44, 12); len(got.Rows) != 12 {
		t.Errorf("Show after the retry has %d rows, want 12", len(got.Rows))
	}
	// Nothing has failed: Retry is no work, and the image that was fine
	// was fetched once.
	s.Retry()
	idle(t, s)
	fetched := 0
	for _, c := range srv.seen() {
		if c.path == "other.png" {
			fetched++
		}
	}
	if fetched != 1 {
		t.Errorf("the image that never failed was fetched %d times", fetched)
	}
}

func TestStoreLevelChange(t *testing.T) {
	t.Parallel()
	srv, size := withChart(t)
	s := newStore(termcaps.ImagesText)
	s.Show(latency(), 44, 12)
	settle(t, s, srv)
	text := termimg.Shown{Line: "[image: latency.png, 640×360, " + size + "]", Hint: "/image save 31 ./latency.png"}
	caption := "latency.png · 640×360 · " + size + " · /image save 31"

	// The same caps again: nothing to do.
	if changed, raw := s.SetCaps(caps(termcaps.ImagesText)); changed != nil || raw != "" {
		t.Errorf("SetCaps with the same caps = %v, %q; want nothing", changed, raw)
	}
	// Fields the store does not draw from change nothing either.
	other := caps(termcaps.ImagesText)
	other.Color, other.Background, other.Terminal, other.Settled = termcaps.ColorTrue, termcaps.BgDark, "ghostty", true
	if changed, raw := s.SetCaps(other); changed != nil || raw != "" {
		t.Errorf("SetCaps with another colour depth = %v, %q; want nothing", changed, raw)
	}

	// Text to blocks: drawn at once from the master.
	changed, raw := s.SetCaps(caps(termcaps.ImagesBlocks))
	if !slices.Equal(changed, []uint64{31}) || raw != "" {
		t.Errorf("SetCaps(blocks) = %v, %q; want reply 31 and nothing for the terminal", changed, raw)
	}
	blocks := termimg.HalfBlocks(termimg.Scale(master(), 42, 24))
	got := s.Show(latency(), 44, 12)
	equalRows(t, got.Rows, blocks)
	if got.Caption != caption {
		t.Errorf("caption = %q, want %q", got.Caption, caption)
	}
	idle(t, s)

	// Blocks to pixels: the text form until the picture has been sent.
	changed, raw = s.SetCaps(caps(termcaps.ImagesPixels))
	if !slices.Equal(changed, []uint64{31}) || raw != "" {
		t.Errorf("SetCaps(pixels) = %v, %q; want reply 31 and nothing for the terminal", changed, raw)
	}
	if got := s.Show(latency(), 44, 12); !sameShown(got, text) {
		t.Errorf("Show before the transmit = %+v, want %+v", got, text)
	}
	seq, raw := step(t, s, srv)
	keys, _ := transmitted(t, raw)
	if seq != 31 || keys != "a=T,U=1,q=2,f=100,t=d,i=7,c=42,r=12,m=0" && keys != "a=T,U=1,q=2,f=100,t=d,i=7,c=42,r=12,m=1" {
		t.Errorf("Finish = %d with keys %q; want reply 31 sent as id 7 on 42x12", seq, keys)
	}
	equalRows(t, s.Show(latency(), 44, 12).Rows, termimg.KittyPlaceholders(7, 42, 12))
	idle(t, s)

	// Pixels to blocks: the terminal is told to forget the picture.
	changed, raw = s.SetCaps(caps(termcaps.ImagesBlocks))
	if !slices.Equal(changed, []uint64{31}) || raw != termimg.KittyDelete(7, false) {
		t.Errorf("SetCaps(blocks) = %v, %q; want reply 31 and the delete of id 7", changed, raw)
	}
	equalRows(t, s.Show(latency(), 44, 12).Rows, blocks)

	// A new cell shape gives a new grid: 8x20 cells make the box 352 by
	// 240 pixels, which 16:9 fills across, 9 rows deep.
	changed, raw = s.SetCaps(caps(termcaps.ImagesBlocks, cellSize(8, 20)))
	if !slices.Equal(changed, []uint64{31}) || raw != "" {
		t.Errorf("SetCaps(a new cell) = %v, %q; want reply 31 and nothing for the terminal", changed, raw)
	}
	equalRows(t, s.Show(latency(), 44, 12).Rows, termimg.HalfBlocks(termimg.Scale(master(), 44, 18)))

	// Blocks to text.
	changed, _ = s.SetCaps(caps(termcaps.ImagesText))
	if !slices.Equal(changed, []uint64{31}) {
		t.Errorf("SetCaps(text) = %v, want reply 31", changed)
	}
	if got := s.Show(latency(), 44, 12); !sameShown(got, text) {
		t.Errorf("Show at text = %+v, want %+v", got, text)
	}
	// ASCII glyphs: the same line without the times sign.
	changed, _ = s.SetCaps(caps(termcaps.ImagesText, asciiGlyphs))
	if !slices.Equal(changed, []uint64{31}) {
		t.Errorf("SetCaps(ascii) = %v, want reply 31", changed)
	}
	if got, want := s.Show(latency(), 44, 12).Line, "[image: latency.png, 640x360, "+size+"]"; got != want {
		t.Errorf("line with ASCII glyphs = %q, want %q", got, want)
	}
	// A level the store does not know is text.
	if changed, raw := s.SetCaps(termcaps.Caps{Glyphs: termcaps.GlyphsASCII, CellW: 8, CellH: 16}); changed != nil || raw != "" {
		t.Errorf("SetCaps with no level = %v, %q; want it to count as text", changed, raw)
	}

	// Through all of it the file was fetched once.
	idle(t, s)
	if got, want := srv.seen(), []call{{path: "latency.png"}}; !slices.Equal(got, want) {
		t.Errorf("fetches = %v, want %v", got, want)
	}
}

func TestStoreSetCapsNamesEveryReply(t *testing.T) {
	t.Parallel()
	srv, _ := withChart(t)
	s := newStore(termcaps.ImagesBlocks)
	second := latency()
	second.N = 2
	s.Show(termimg.Ref{Seq: 40, N: 1, Path: "latency.png"}, 44, 12)
	s.Show(latency(), 44, 12)
	s.Show(second, 44, 12)
	s.Show(termimg.Ref{Seq: 35, N: 1, Path: "missing.png"}, 44, 12)
	s.Show(termimg.Ref{Seq: 12, N: 1, Path: "latency.png"}, 44, 12)
	settle(t, s, srv)
	loading := termimg.Ref{Seq: 50, N: 1, Path: "latency.png"}
	s.Show(loading, 44, 12)

	// A level change redraws the replies with a picture, each once, in
	// order. The failed one and the loading one are lines that stay.
	changed, _ := s.SetCaps(caps(termcaps.ImagesText))
	if want := []uint64{12, 31, 40}; !slices.Equal(changed, want) {
		t.Errorf("changed = %v, want %v", changed, want)
	}
	// A glyph change redraws them all.
	changed, _ = s.SetCaps(caps(termcaps.ImagesText, asciiGlyphs))
	if want := []uint64{12, 31, 35, 40, 50}; !slices.Equal(changed, want) {
		t.Errorf("changed = %v, want %v", changed, want)
	}
}

func TestStoreResize(t *testing.T) {
	t.Parallel()
	t.Run("blocks", func(t *testing.T) {
		t.Parallel()
		srv, _ := withChart(t)
		s := newStore(termcaps.ImagesBlocks)
		s.Show(latency(), 44, 12)
		settle(t, s, srv)
		first := s.Show(latency(), 44, 12).Rows

		// The same box, as on a scroll or a key press: the same rows,
		// and nothing encoded.
		before := s.Stats()
		for range 5 {
			again := s.Show(latency(), 44, 12).Rows
			if len(again) != len(first) || &again[0] != &first[0] {
				t.Fatal("Show with the same box made new rows")
			}
		}
		if got := s.Stats(); got != before {
			t.Errorf("Stats went from %+v to %+v on redraws", before, got)
		}
		idle(t, s)

		// A box 30 cells wide: 30 by 8, encoded from the master at once.
		narrow := s.Show(latency(), 30, 12)
		equalRows(t, narrow.Rows, termimg.HalfBlocks(termimg.Scale(master(), 30, 16)))
		if got := s.Stats().Encodes - before.Encodes; got != 1 {
			t.Errorf("a new box cost %d encodes, want 1", got)
		}
		// A box only 5 rows deep.
		equalRows(t, s.Show(latency(), 44, 5).Rows, termimg.HalfBlocks(termimg.Scale(master(), 17, 10)))
		// A box larger than the largest is cut to 44 by 12.
		equalRows(t, s.Show(latency(), 200, 60).Rows, first)
		// A box of nothing is one cell.
		if got := s.Show(latency(), 0, -1).Rows; len(got) != 1 || ansi.StringWidth(got[0]) != 1 {
			t.Errorf("a box of nothing gave %q, want one cell", got)
		}
		idle(t, s)
		if got, want := srv.seen(), []call{{path: "latency.png"}}; !slices.Equal(got, want) {
			t.Errorf("fetches = %v, want %v", got, want)
		}
	})
	t.Run("pixels", func(t *testing.T) {
		t.Parallel()
		srv, size := withChart(t)
		s := newStore(termcaps.ImagesPixels)
		s.Show(latency(), 44, 12)
		settle(t, s, srv)

		// The same box: no job, so nothing is sent again.
		before := s.Stats()
		for range 5 {
			equalRows(t, s.Show(latency(), 44, 12).Rows, termimg.KittyPlaceholders(7, 42, 12))
			idle(t, s)
		}
		if got := s.Stats(); got != before {
			t.Errorf("Stats went from %+v to %+v on redraws", before, got)
		}

		// A narrower box needs a new grid in the terminal. Until it is
		// there the image is its text form.
		text := termimg.Shown{Line: "[image: latency.png, 640×360, " + size + "]", Hint: "/image save 31 ./latency.png"}
		for range 3 {
			if got := s.Show(latency(), 30, 12); !sameShown(got, text) {
				t.Errorf("Show before the new transmit = %+v, want %+v", got, text)
			}
		}
		seq, raw := step(t, s, srv)
		want, err := termimg.KittyTransmit(7, termimg.Scale(master(), 240, 128), 30, 8, false)
		if err != nil {
			t.Fatalf("KittyTransmit: %v", err)
		}
		if seq != 31 || raw != want {
			t.Errorf("Finish = %d, %q; want reply 31 sent again as id 7 on 30x8", seq, head(raw))
		}
		equalRows(t, s.Show(latency(), 30, 12).Rows, termimg.KittyPlaceholders(7, 30, 8))
		// Drawing it three times while the job was out asked for one job.
		idle(t, s)
		if got := s.Stats(); got.Jobs != before.Jobs+1 || got.Encodes != before.Encodes+1 {
			t.Errorf("Stats went from %+v to %+v, want one more job and encode", before, got)
		}
		if got, want := srv.seen(), []call{{path: "latency.png"}}; !slices.Equal(got, want) {
			t.Errorf("fetches = %v, want %v", got, want)
		}
	})
	t.Run("pixels, a box that comes back", func(t *testing.T) {
		t.Parallel()
		srv, _ := withChart(t)
		s := newStore(termcaps.ImagesPixels)
		s.Show(latency(), 44, 12)
		settle(t, s, srv)
		before := s.Stats()

		// The window is narrow for a frame and wide again before any job
		// ran: the terminal still holds the wide grid, so nothing is sent.
		if got := s.Show(latency(), 30, 12); got.Rows != nil {
			t.Errorf("Show in the narrow box has rows the terminal was never sent")
		}
		equalRows(t, s.Show(latency(), 44, 12).Rows, termimg.KittyPlaceholders(7, 42, 12))
		idle(t, s)
		if got := s.Stats(); got != before {
			t.Errorf("Stats went from %+v to %+v", before, got)
		}
	})
	t.Run("pixels, resized again while the job is out", func(t *testing.T) {
		t.Parallel()
		srv, _ := withChart(t)
		s := newStore(termcaps.ImagesPixels)
		s.Show(latency(), 44, 12)
		settle(t, s, srv)

		s.Show(latency(), 30, 12)
		job, ok := s.Next()
		if !ok {
			t.Fatal("Next has no job for the new box")
		}
		s.Show(latency(), 20, 12) // the window shrank again
		seq, raw := s.Finish(job(t.Context(), srv.fetch))
		if seq != 31 || raw != "" {
			t.Errorf("Finish = %d, %q; want reply 31 and the transmit for the old box dropped", seq, head(raw))
		}
		if got := s.Show(latency(), 20, 12); got.Rows != nil {
			t.Errorf("Show has rows for a grid the terminal was never sent")
		}
		_, raw = step(t, s, srv)
		if keys, _ := transmitted(t, raw); !strings.HasPrefix(keys, "a=T,U=1,q=2,f=100,t=d,i=7,c=20,r=5,") {
			t.Errorf("the next transmit has keys %q, want the grid 20x5", keys)
		}
		equalRows(t, s.Show(latency(), 20, 12).Rows, termimg.KittyPlaceholders(7, 20, 5))
		idle(t, s)
	})
}

func TestStoreCacheBound(t *testing.T) {
	t.Parallel()
	// A master of the chart is 341 by 192 pixels: 261,888 bytes. Two fit
	// in 600,000 and three do not.
	const limit = 600_000
	srv := newServer()
	refs := make([]termimg.Ref, 3)
	for i, name := range []string{"a.png", "b.png", "c.png"} {
		srv.put(name, pngBytes(t, chart(640, 360)), made())
		refs[i] = termimg.Ref{Seq: uint64(10 + i), N: 1, Path: name}
	}
	s := termimg.NewStore(termimg.StoreOptions{CacheBytes: limit})
	s.SetCaps(caps(termcaps.ImagesBlocks))
	for i, ref := range refs {
		s.Show(ref, 44, 12)
		settle(t, s, srv)
		if got := s.CacheBytes(); got > limit {
			t.Fatalf("after %d images the cache holds %d bytes, over its %d", i+1, got, limit)
		}
	}
	if got, want := s.CacheBytes(), 2*341*192*4; got != want || s.CacheLen() != 2 {
		t.Errorf("the cache holds %d masters in %d bytes, want 2 in %d", s.CacheLen(), got, want)
	}
	// All three are still drawn: their rows do not need the master.
	for _, ref := range refs {
		if got := s.Show(ref, 44, 12); len(got.Rows) != 12 {
			t.Errorf("%s has %d rows, want 12", ref.Path, len(got.Rows))
		}
	}
	idle(t, s)

	// A new box needs the masters. b and c have theirs; a's was dropped
	// first, so a is fetched again and shown as text meanwhile.
	for _, ref := range refs[1:] {
		if got := s.Show(ref, 30, 12); len(got.Rows) != 8 {
			t.Errorf("%s has %d rows in the new box, want 8", ref.Path, len(got.Rows))
		}
	}
	idle(t, s)
	got := s.Show(refs[0], 30, 12)
	if got.Rows != nil || !strings.HasPrefix(got.Line, "[image: a.png, 640×360, ") || got.Hint != "/image save 10 ./a.png" {
		t.Errorf("Show of the image whose master was dropped = %+v, want its text form", got)
	}
	if seqs, _ := settle(t, s, srv); !slices.Equal(seqs, []uint64{10}) {
		t.Errorf("jobs ran for %v, want reply 10", seqs)
	}
	if got := s.Show(refs[0], 30, 12); len(got.Rows) != 8 {
		t.Errorf("a.png has %d rows after the second fetch, want 8", len(got.Rows))
	}
	want := []call{{path: "a.png"}, {path: "b.png"}, {path: "c.png"}, {path: "a.png"}}
	if got := srv.seen(); !slices.Equal(got, want) {
		t.Errorf("fetches = %v, want %v", got, want)
	}
	if got := s.CacheBytes(); got > limit || s.CacheLen() != 2 {
		t.Errorf("the cache holds %d masters in %d bytes, want 2 within %d", s.CacheLen(), got, limit)
	}
}

func TestStoreCacheTooSmallForOne(t *testing.T) {
	t.Parallel()
	for _, level := range []termcaps.Images{termcaps.ImagesBlocks, termcaps.ImagesPixels} {
		t.Run(level.String(), func(t *testing.T) {
			t.Parallel()
			srv, _ := withChart(t)
			s := termimg.NewStore(termimg.StoreOptions{CacheBytes: 1000})
			s.SetCaps(caps(level))
			s.Show(latency(), 44, 12)
			settle(t, s, srv)
			if s.CacheBytes() != 0 || s.CacheLen() != 0 {
				t.Errorf("the cache holds %d bytes, want none: the master is larger than all of it", s.CacheBytes())
			}
			// The image is drawn all the same, and stays drawn.
			for range 3 {
				if got := s.Show(latency(), 44, 12); len(got.Rows) != 12 {
					t.Fatalf("Show has %d rows, want 12", len(got.Rows))
				}
				idle(t, s)
			}
			// A new box costs a fetch, and then holds.
			s.Show(latency(), 30, 12)
			settle(t, s, srv)
			if got := s.Show(latency(), 30, 12); len(got.Rows) != 8 {
				t.Errorf("Show in the new box has %d rows, want 8", len(got.Rows))
			}
			idle(t, s)
			if got := len(srv.seen()); got != 2 {
				t.Errorf("%d fetches, want 2", got)
			}
		})
	}
}

func TestStoreConditionalFetch(t *testing.T) {
	t.Parallel()
	srv, size := withChart(t)
	s := newStore(termcaps.ImagesBlocks)
	s.Show(latency(), 44, 12)
	settle(t, s, srv)
	etag := srv.files["latency.png"].ETag

	// A later reply names the same file. The store asks whether it is
	// still the version it has, and is told yes: no bytes, no decoding.
	later := termimg.Ref{Seq: 40, N: 1, Path: "latency.png", TS: sent().Add(time.Hour).UnixMilli()}
	s.Show(later, 44, 12)
	if seqs, _ := settle(t, s, srv); !slices.Equal(seqs, []uint64{40}) {
		t.Errorf("jobs ran for %v, want reply 40", seqs)
	}
	got := s.Show(later, 44, 12)
	equalRows(t, got.Rows, termimg.HalfBlocks(termimg.Scale(master(), 42, 24)))
	if want := "latency.png · 640×360 · " + size + " · /image save 40"; got.Caption != want {
		t.Errorf("caption = %q, want %q", got.Caption, want)
	}
	if got, want := srv.seen(), []call{{path: "latency.png"}, {path: "latency.png", etag: etag}}; !slices.Equal(got, want) {
		t.Errorf("fetches = %v, want %v", got, want)
	}
	if s.CacheLen() != 1 {
		t.Errorf("the cache holds %d masters of one file, want 1", s.CacheLen())
	}

	// The tool overwrites the file, and a third reply names it.
	changed := pngBytes(t, chart(320, 200))
	srv.put("latency.png", changed, sent().Add(2*time.Hour))
	third := termimg.Ref{Seq: 50, N: 1, Path: "latency.png", TS: sent().Add(3 * time.Hour).UnixMilli()}
	s.Show(third, 44, 12)
	settle(t, s, srv)
	if got, want := s.Show(third, 44, 12).Caption, "latency.png · 320×200 · "+sizeText(changed)+" · /image save 50"; got != want {
		t.Errorf("caption of the new version = %q, want %q", got, want)
	}
	if got := srv.seen(); len(got) != 3 || got[2] != (call{path: "latency.png", etag: etag}) {
		t.Errorf("fetches = %v, want a third that asks about the first version", got)
	}
	// The earlier replies keep the picture they were drawn with.
	if got, want := s.Show(latency(), 44, 12).Caption, "latency.png · 640×360 · "+size+" · /image save 31"; got != want {
		t.Errorf("caption of the first reply = %q, want %q", got, want)
	}
	idle(t, s)
}

func TestStoreChangedSince(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		modified time.Time
		ts       int64
		want     bool
	}{
		{name: "written before the reply", modified: made(), ts: sent().UnixMilli()},
		{name: "written in the same instant", modified: sent(), ts: sent().UnixMilli()},
		{name: "written after the reply", modified: sent().Add(time.Second), ts: sent().UnixMilli(), want: true},
		{name: "the server gave no time", ts: sent().UnixMilli()},
		{name: "the reply has no time", modified: sent().Add(time.Hour)},
	}
	for _, c := range cases {
		for _, level := range []termcaps.Images{termcaps.ImagesBlocks, termcaps.ImagesPixels} {
			t.Run(c.name+"/"+level.String(), func(t *testing.T) {
				t.Parallel()
				srv := newServer()
				data := pngBytes(t, chart(640, 360))
				srv.put("latency.png", data, c.modified)
				s := newStore(level)
				ref := latency()
				ref.TS = c.ts
				s.Show(ref, 44, 12)
				settle(t, s, srv)
				want := "latency.png · 640×360 · " + sizeText(data) + " · /image save 31"
				if c.want {
					want += " · changed since"
				}
				if got := s.Show(ref, 44, 12).Caption; got != want {
					t.Errorf("caption = %q, want %q", got, want)
				}
			})
		}
	}
}

func TestStoreNumbersAndNames(t *testing.T) {
	t.Parallel()
	srv := newServer()
	data := pngBytes(t, chart(64, 36))
	size := sizeText(data)
	cases := []struct {
		name    string
		ref     termimg.Ref
		line    string
		hint    string
		caption string
		ascii   string // the caption with ASCII glyphs
	}{
		{
			name:    "the first image of a reply",
			ref:     termimg.Ref{Seq: 31, N: 1, Path: "latency.png"},
			line:    "[image: latency.png, 64×36, " + size + "]",
			hint:    "/image save 31 ./latency.png",
			caption: "latency.png · 64×36 · " + size + " · /image save 31",
			ascii:   "latency.png | 64x36 | " + size + " | /image save 31",
		},
		{
			name:    "the second",
			ref:     termimg.Ref{Seq: 31, N: 2, Path: "p99.png"},
			line:    "[image: p99.png, 64×36, " + size + "]",
			hint:    "/image save 31.2 ./p99.png",
			caption: "p99.png · 64×36 · " + size + " · /image save 31.2",
			ascii:   "p99.png | 64x36 | " + size + " | /image save 31.2",
		},
		{
			name:    "a file in a directory is named by its last part",
			ref:     termimg.Ref{Seq: 7, N: 1, Path: "out/plots/latency.png"},
			line:    "[image: latency.png, 64×36, " + size + "]",
			hint:    "/image save 7 ./latency.png",
			caption: "latency.png · 64×36 · " + size + " · /image save 7",
			ascii:   "latency.png | 64x36 | " + size + " | /image save 7",
		},
		{
			name:    "a name with a space is quoted for the command line",
			ref:     termimg.Ref{Seq: 8, N: 3, Path: "p99 by day.png"},
			line:    "[image: p99 by day.png, 64×36, " + size + "]",
			hint:    `/image save 8.3 "./p99 by day.png"`,
			caption: "p99 by day.png · 64×36 · " + size + " · /image save 8.3",
			ascii:   "p99 by day.png | 64x36 | " + size + " | /image save 8.3",
		},
		{
			name:    "an image counted from zero is the first",
			ref:     termimg.Ref{Seq: 9, Path: "zero.png"},
			line:    "[image: zero.png, 64×36, " + size + "]",
			hint:    "/image save 9 ./zero.png",
			caption: "zero.png · 64×36 · " + size + " · /image save 9",
			ascii:   "zero.png | 64x36 | " + size + " | /image save 9",
		},
	}
	for _, c := range cases {
		srv.put(c.ref.Path, data, made())
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s := newStore(termcaps.ImagesText)
			s.Show(c.ref, 44, 12)
			settle(t, s, srv)
			if got := s.Show(c.ref, 44, 12); got.Line != c.line || got.Hint != c.hint {
				t.Errorf("text = %q and %q, want %q and %q", got.Line, got.Hint, c.line, c.hint)
			}
			s.SetCaps(caps(termcaps.ImagesBlocks))
			if got := s.Show(c.ref, 44, 12).Caption; got != c.caption {
				t.Errorf("caption = %q, want %q", got, c.caption)
			}
			s.SetCaps(caps(termcaps.ImagesBlocks, asciiGlyphs))
			if got := s.Show(c.ref, 44, 12).Caption; got != c.ascii {
				t.Errorf("caption with ASCII glyphs = %q, want %q", got, c.ascii)
			}
		})
	}
}

func TestStoreLookup(t *testing.T) {
	t.Parallel()
	s := newStore(termcaps.ImagesText)
	if _, ok := s.Lookup(31, 1); ok {
		t.Error("Lookup found an image in an empty store")
	}
	first := latency()
	second := termimg.Ref{Seq: 31, N: 2, Path: "plots/p99.png", Alt: "p99", TS: 5}
	s.Show(first, 44, 12)
	s.Show(second, 44, 12)
	cases := []struct {
		seq  uint64
		n    int
		want termimg.Ref
		ok   bool
	}{
		{seq: 31, n: 1, want: first, ok: true},
		{seq: 31, n: 2, want: second, ok: true},
		{seq: 31, n: 0, want: first, ok: true}, // "/image save 31" names the first
		{seq: 31, n: 3},
		{seq: 32, n: 1},
		{seq: 0, n: 1},
	}
	for _, c := range cases {
		got, ok := s.Lookup(c.seq, c.n)
		if got != c.want || ok != c.ok {
			t.Errorf("Lookup(%d, %d) = %+v, %v; want %+v, %v", c.seq, c.n, got, ok, c.want, c.ok)
		}
	}
	// Found before it is fetched, and still found when it has failed.
	srv := newServer()
	settle(t, s, srv)
	if got, ok := s.Lookup(31, 2); !ok || got != second {
		t.Errorf("Lookup of a failed image = %+v, %v; want %+v", got, ok, second)
	}
}

func TestStoreDeleteAll(t *testing.T) {
	t.Parallel()
	for _, tmux := range []bool{false, true} {
		t.Run("tmux="+strconv.FormatBool(tmux), func(t *testing.T) {
			t.Parallel()
			srv, _ := withChart(t)
			s := termimg.NewStore(termimg.StoreOptions{IDBase: 6})
			pixels := caps(termcaps.ImagesPixels)
			pixels.Tmux = tmux
			s.SetCaps(pixels)
			if got := s.DeleteAll(); got != "" {
				t.Errorf("DeleteAll with nothing sent = %q", got)
			}
			second := termimg.Ref{Seq: 32, N: 1, Path: "latency.png"}
			s.Show(latency(), 44, 12)
			settle(t, s, srv)
			s.Show(second, 44, 12)
			_, raws := settle(t, s, srv)
			if tmux && !strings.HasPrefix(raws[0], "\x1bPtmux;") {
				t.Errorf("the transmit is not wrapped for tmux: %q", head(raws[0]))
			}

			want := termimg.KittyDelete(7, tmux) + termimg.KittyDelete(8, tmux)
			if got := s.DeleteAll(); got != want {
				t.Errorf("DeleteAll = %q, want %q", got, want)
			}
			if got := s.DeleteAll(); got != "" {
				t.Errorf("a second DeleteAll = %q, want nothing", got)
			}

			// The terminal has nothing now, so an image shown again is
			// sent again, without a fetch.
			fetches := len(srv.seen())
			if got := s.Show(latency(), 44, 12); got.Rows != nil {
				t.Errorf("Show after DeleteAll has rows for a picture the terminal dropped")
			}
			seq, raw := step(t, s, srv)
			if tmux {
				raw = unwrap(t, raw)
			}
			keys, _ := transmitted(t, raw)
			if seq != 31 || !strings.Contains(keys, ",i=9,c=42,r=12,") {
				t.Errorf("Finish = %d with keys %q; want reply 31 sent under the next id, 9", seq, keys)
			}
			equalRows(t, s.Show(latency(), 44, 12).Rows, termimg.KittyPlaceholders(9, 42, 12))
			if got := len(srv.seen()); got != fetches {
				t.Errorf("sending again fetched %d more times", got-fetches)
			}
			if got, want := s.DeleteAll(), termimg.KittyDelete(9, tmux); got != want {
				t.Errorf("DeleteAll = %q, want %q", got, want)
			}
		})
	}
	t.Run("nothing to delete at blocks", func(t *testing.T) {
		t.Parallel()
		srv, _ := withChart(t)
		s := newStore(termcaps.ImagesBlocks)
		s.Show(latency(), 44, 12)
		settle(t, s, srv)
		if got := s.DeleteAll(); got != "" {
			t.Errorf("DeleteAll = %q, want nothing", got)
		}
		if got := s.Show(latency(), 44, 12); len(got.Rows) != 12 {
			t.Errorf("Show after DeleteAll has %d rows, want 12", len(got.Rows))
		}
	})
	t.Run("while a job is out", func(t *testing.T) {
		t.Parallel()
		srv, _ := withChart(t)
		s := newStore(termcaps.ImagesPixels)
		s.Show(latency(), 44, 12)
		job, ok := s.Next()
		if !ok {
			t.Fatal("Next has no job")
		}
		// The terminal has been sent nothing yet, so there is nothing to
		// delete, and the transmit that then arrives is not passed on.
		if got := s.DeleteAll(); got != "" {
			t.Errorf("DeleteAll = %q, want nothing", got)
		}
		seq, raw := s.Finish(job(t.Context(), srv.fetch))
		if seq != 31 || raw != "" {
			t.Errorf("Finish = %d, %q; want reply 31 and no transmit", seq, head(raw))
		}
		if got := s.DeleteAll(); got != "" {
			t.Errorf("DeleteAll = %q, want nothing: the terminal was sent nothing", got)
		}
	})
}

func TestStoreIDEviction(t *testing.T) {
	t.Parallel()
	srv := newServer()
	// 32 by 32 pixels is 4 by 2 cells: the picture covers its box.
	srv.put("dot.png", pngBytes(t, chart(32, 32)), made())
	s := termimg.NewStore(termimg.StoreOptions{})
	s.SetCaps(caps(termcaps.ImagesPixels))
	ref := func(i int) termimg.Ref { return termimg.Ref{Seq: uint64(i), N: 1, Path: "dot.png"} }
	// 255 images take the 255 ids, 1 to 255 in order.
	for i := 1; i <= 255; i++ {
		s.Show(ref(i), 4, 2)
		_, raws := settle(t, s, srv)
		if keys, _ := transmitted(t, raws[0]); !strings.Contains(keys, ",i="+strconv.Itoa(i)+",") {
			t.Fatalf("image %d was sent with keys %q", i, keys)
		}
	}
	if got := s.Stale(); got != nil {
		t.Errorf("Stale = %v while every image has its own id, want none", got)
	}
	// Image 1 is drawn again, so image 2 is the one shown longest ago.
	equalRows(t, s.Show(ref(1), 4, 2).Rows, termimg.KittyPlaceholders(1, 4, 2))

	// One more image takes image 2's id. The terminal is told to drop
	// the old picture before it is sent the new one.
	s.Show(ref(256), 4, 2)
	seqs, raws := settle(t, s, srv)
	del := termimg.KittyDelete(2, false)
	if !slices.Equal(seqs, []uint64{256}) || !strings.HasPrefix(raws[0], del) {
		t.Fatalf("Finish = %v, %q; want reply 256 and bytes that start with the delete of id 2", seqs, head(raws[0]))
	}
	if keys, _ := transmitted(t, strings.TrimPrefix(raws[0], del)); !strings.Contains(keys, ",i=2,") {
		t.Errorf("the new image was sent with keys %q, want id 2", keys)
	}
	equalRows(t, s.Show(ref(256), 4, 2).Rows, termimg.KittyPlaceholders(2, 4, 2))
	equalRows(t, s.Show(ref(1), 4, 2).Rows, termimg.KittyPlaceholders(1, 4, 2))
	// The rows reply 2 was drawn with are now reply 256's picture, cell for
	// cell, so the store names reply 2 as one to draw again, once.
	if got := s.Stale(); !slices.Equal(got, []uint64{2}) {
		t.Errorf("Stale = %v, want reply 2: its cells show another picture", got)
	}
	if got := s.Stale(); got != nil {
		t.Errorf("a second Stale = %v, want none", got)
	}

	// Image 2 lost its id. Shown again it is text until it has been sent
	// under another: image 3's, now the oldest.
	fetches := len(srv.seen())
	if got := s.Show(ref(2), 4, 2); got.Rows != nil || got.Hint != "/image save 2 ./dot.png" {
		t.Errorf("Show of the image that lost its id = %+v, want its text form", got)
	}
	seqs, raws = settle(t, s, srv)
	del = termimg.KittyDelete(3, false)
	if !slices.Equal(seqs, []uint64{2}) || !strings.HasPrefix(raws[0], del) {
		t.Fatalf("Finish = %v, %q; want reply 2 and bytes that start with the delete of id 3", seqs, head(raws[0]))
	}
	equalRows(t, s.Show(ref(2), 4, 2).Rows, termimg.KittyPlaceholders(3, 4, 2))
	if got := s.Stale(); !slices.Equal(got, []uint64{3}) {
		t.Errorf("Stale = %v, want reply 3", got)
	}
	if got := len(srv.seen()); got != fetches {
		t.Errorf("sending image 2 again fetched %d more times", got-fetches)
	}
	if got := strings.Count(s.DeleteAll(), "\x1b_G"); got != 255 {
		t.Errorf("DeleteAll deletes %d pictures, want 255", got)
	}
}

// TestStoreFailureGivesTheIDBack checks an image that was drawn and then
// fails: the terminal is told to drop its picture, and its id is free.
func TestStoreFailureGivesTheIDBack(t *testing.T) {
	t.Parallel()
	srv, _ := withChart(t)
	// A cache too small for the master, so a new box means a new fetch.
	s := termimg.NewStore(termimg.StoreOptions{CacheBytes: 1000, IDBase: 6})
	s.SetCaps(caps(termcaps.ImagesPixels))
	s.Show(latency(), 44, 12)
	settle(t, s, srv)
	equalRows(t, s.Show(latency(), 44, 12).Rows, termimg.KittyPlaceholders(7, 42, 12))

	srv.fail("latency.png", termimg.ErrNotFound) // the tool deleted it
	s.Show(latency(), 30, 12)
	seq, raw := step(t, s, srv)
	if seq != 31 || raw != termimg.KittyDelete(7, false) {
		t.Errorf("Finish = %d, %q; want reply 31 and the delete of id 7", seq, raw)
	}
	if got := s.Show(latency(), 30, 12); !sameShown(got, termimg.Shown{Line: "[image: latency.png, not found]"}) {
		t.Errorf("Show = %+v, want the not-found line", got)
	}
	if got := s.DeleteAll(); got != "" {
		t.Errorf("DeleteAll = %q, want nothing: the picture is already dropped", got)
	}
}

func TestStoreStaleResults(t *testing.T) {
	t.Parallel()
	srv, _ := withChart(t)
	srv.put("other.png", pngBytes(t, chart(320, 200)), made())
	s := newStore(termcaps.ImagesPixels)

	// The zero Done is no result, with or without a job out.
	if seq, raw := s.Finish(termimg.Done{}); seq != 0 || raw != "" {
		t.Errorf("Finish(Done{}) = %d, %q; want nothing", seq, raw)
	}
	s.Show(latency(), 44, 12)
	job, ok := s.Next()
	if !ok {
		t.Fatal("Next has no job")
	}
	if seq, raw := s.Finish(termimg.Done{}); seq != 0 || raw != "" {
		t.Errorf("Finish(Done{}) = %d, %q; want nothing", seq, raw)
	}
	if _, ok := s.Next(); ok {
		t.Fatal("the zero Done counted as the job's result")
	}

	// A result handed in twice counts once, and a result from another
	// store, as one arrives after the user has moved to another contact,
	// not at all.
	done := job(t.Context(), srv.fetch)
	other := newStore(termcaps.ImagesPixels)
	other.Show(latency(), 44, 12)
	if _, ok := other.Next(); !ok {
		t.Fatal("the other store has no job")
	}
	if seq, raw := other.Finish(done); seq != 0 || raw != "" {
		t.Errorf("another store's Finish = %d, %q; want nothing", seq, head(raw))
	}
	if _, ok := other.Next(); ok {
		t.Error("another store's result counted as this store's job")
	}
	if seq, raw := s.Finish(done); seq != 31 || raw == "" {
		t.Errorf("Finish = %d, %q; want reply 31 and its transmit", seq, head(raw))
	}
	if seq, raw := s.Finish(done); seq != 0 || raw != "" {
		t.Errorf("the same result again = %d, %q; want nothing", seq, head(raw))
	}

	// The reply is replaced by one that names another file while the job
	// for the old file is out: its result is dropped, the new file fetched.
	s = newStore(termcaps.ImagesPixels)
	s.Show(latency(), 44, 12)
	job, _ = s.Next()
	edited := latency()
	edited.Path = "other.png"
	if got := s.Show(edited, 44, 12); got.Line != "[image: other.png]" {
		t.Errorf("Show of the new file = %+v, want its loading line", got)
	}
	if seq, raw := s.Finish(job(t.Context(), srv.fetch)); seq != 0 || raw != "" {
		t.Errorf("Finish for the old file = %d, %q; want nothing", seq, head(raw))
	}
	seq, raw := step(t, s, srv)
	keys, pic := transmitted(t, raw)
	if seq != 31 || !strings.Contains(keys, ",i=7,c=38,r=12,") || pic.Bounds().Dx() != 304 {
		t.Errorf("Finish = %d with keys %q and a picture %v; want reply 31 with the 320x200 file on 38x12", seq, keys, pic.Bounds())
	}
	if got, ok := s.Lookup(31, 1); !ok || got != edited {
		t.Errorf("Lookup = %+v, %v; want the new reference", got, ok)
	}
	idle(t, s)
}

func TestStoreSetCapsWhileJobOut(t *testing.T) {
	t.Parallel()
	srv, _ := withChart(t)
	s := newStore(termcaps.ImagesPixels)
	s.Show(latency(), 44, 12)
	job, ok := s.Next()
	if !ok {
		t.Fatal("Next has no job")
	}
	// tmux turns out to be in between. Nothing was sent yet, so there is
	// nothing to delete.
	changed, raw := s.SetCaps(caps(termcaps.ImagesPixels, viaTmux))
	if changed != nil || raw != "" {
		t.Errorf("SetCaps = %v, %q; want no reply to redraw yet and nothing for the terminal", changed, raw)
	}
	// The job's transmit was made the old way and is dropped. Its
	// picture is kept.
	seq, raw := s.Finish(job(t.Context(), srv.fetch))
	if seq != 31 || raw != "" {
		t.Errorf("Finish = %d, %q; want reply 31 and no transmit", seq, head(raw))
	}
	if got := s.Show(latency(), 44, 12); got.Rows != nil || got.Hint == "" {
		t.Errorf("Show = %+v, want the text form until the picture is sent through tmux", got)
	}
	seq, raw = step(t, s, srv)
	want, err := termimg.KittyTransmit(8, termimg.Scale(master(), 336, 192), 42, 12, true)
	if err != nil {
		t.Fatalf("KittyTransmit: %v", err)
	}
	if seq != 31 || raw != want {
		t.Errorf("Finish = %d, %q; want reply 31 sent through tmux under id 8", seq, head(raw))
	}
	equalRows(t, s.Show(latency(), 44, 12).Rows, termimg.KittyPlaceholders(8, 42, 12))
	idle(t, s)
	if got := len(srv.seen()); got != 1 {
		t.Errorf("%d fetches, want 1", got)
	}
}

// TestStoreJobOffTheLoop runs jobs on another goroutine while the store is
// used, as a client does. The race detector is the judge.
func TestStoreJobOffTheLoop(t *testing.T) {
	t.Parallel()
	srv, _ := withChart(t)
	srv.put("other.png", pngBytes(t, chart(320, 200)), made())
	s := newStore(termcaps.ImagesPixels)
	refs := []termimg.Ref{latency(), {Seq: 32, N: 1, Path: "other.png"}, {Seq: 33, N: 1, Path: "latency.png"}}
	results := make(chan termimg.Done)
	for round := range 60 {
		for _, ref := range refs {
			s.Show(ref, 44, 12)
		}
		job, ok := s.Next()
		if !ok {
			if round < 20 {
				t.Fatalf("no job in round %d, while the terminal was still changing", round)
			}
			break
		}
		go func() { results <- job(t.Context(), srv.fetch) }()
		// The loop goes on drawing while the job runs, and for the first
		// rounds the terminal changes its mind as well.
		for _, ref := range refs {
			s.Show(ref, 30, 12)
			s.Show(ref, 44, 12)
		}
		if round < 20 {
			s.SetCaps(caps(termcaps.ImagesBlocks))
			s.SetCaps(caps(termcaps.ImagesPixels))
		}
		s.Lookup(31, 1)
		s.Finish(<-results)
	}
	for _, ref := range refs {
		if got := s.Show(ref, 44, 12); len(got.Rows) != 12 {
			t.Errorf("reply %d has %d rows once things settled, want 12", ref.Seq, len(got.Rows))
		}
	}
	idle(t, s)
}

func TestStoreHostileName(t *testing.T) {
	t.Parallel()
	// A path that tries to set the window title, colour the screen, and
	// print cells that show another program's picture.
	const path = "plots/a\x1b]0;owned\x07\x1b[41m\U0010EEEE\u0305\u0305\u202Egnp.png"
	srv := newServer()
	srv.put(path, pngBytes(t, chart(64, 36)), made())
	ref := termimg.Ref{Seq: 31, N: 1, Path: path, Alt: "\x1b[2J\U0010EEEE"}
	missing := termimg.Ref{Seq: 32, N: 1, Path: "\x1b[2J\U0010EEEE.png"}
	for _, level := range []termcaps.Images{termcaps.ImagesText, termcaps.ImagesBlocks, termcaps.ImagesPixels} {
		t.Run(level.String(), func(t *testing.T) {
			t.Parallel()
			s := newStore(level)
			before := []termimg.Shown{s.Show(ref, 44, 12), s.Show(missing, 44, 12)}
			settle(t, s, srv)
			after := []termimg.Shown{s.Show(ref, 44, 12), s.Show(missing, 44, 12)}
			if after[0].Line == before[0].Line && after[0].Rows == nil {
				t.Errorf("the image never loaded: %+v", after[0])
			}
			for _, sh := range slices.Concat(before, after) {
				for _, text := range []string{sh.Line, sh.Caption, sh.Hint} {
					for _, r := range text {
						if r < 0x20 || r == 0x7f || r == '\U0010EEEE' || r == '\u202E' {
							t.Errorf("%q holds %U", text, r)
						}
					}
				}
			}
			// The path itself is fetched as it was written.
			if got := srv.seen(); !slices.Contains(got, call{path: path}) {
				t.Errorf("fetches = %q, want the path unchanged", got)
			}
		})
	}
}

// TestStoreSmallPicture checks that a picture smaller than its box is never
// made larger, by the client or by the terminal: it is sent its own pixels,
// for the cells it covers at its own size.
func TestStoreSmallPicture(t *testing.T) {
	t.Parallel()
	src := chart(16, 16)
	srv := newServer()
	srv.put("dot.png", pngBytes(t, src), made())
	ref := termimg.Ref{Seq: 5, N: 1, Path: "dot.png"}

	s := newStore(termcaps.ImagesPixels)
	s.Show(ref, 44, 12)
	_, raws := settle(t, s, srv)
	keys, pic := transmitted(t, raws[0])
	if !strings.Contains(keys, ",c=2,r=1,") {
		t.Errorf("keys = %q, want the 16x16 square on its own 2x1 cells", keys)
	}
	same(t, termimg.Scale(pic, 16, 16), src)
	equalRows(t, s.Show(ref, 44, 12).Rows, termimg.KittyPlaceholders(7, 2, 1))

	// As blocks a pixel of it can be no smaller than half a cell. It is
	// drawn with each pixel once, 16 cells by 8, and not as two cells of
	// four colours, nor stretched over the box.
	s.SetCaps(caps(termcaps.ImagesBlocks))
	rows := s.Show(ref, 44, 12).Rows
	equalRows(t, rows, termimg.HalfBlocks(src))
	if len(rows) != 8 {
		t.Errorf("as blocks the icon is %d rows, want 8: one pixel to a half cell", len(rows))
	}
	// The cell's size in pixels does not change that: a block is a block.
	s.SetCaps(caps(termcaps.ImagesBlocks, cellSize(16, 32)))
	equalRows(t, s.Show(ref, 44, 12).Rows, termimg.HalfBlocks(src))
	// A box smaller than the icon still holds it.
	equalRows(t, s.Show(ref, 10, 3).Rows, termimg.HalfBlocks(termimg.Scale(src, 6, 6)))
	idle(t, s)

	// Cells with twice the pixels: the same picture covers one cell.
	s.SetCaps(caps(termcaps.ImagesPixels, cellSize(16, 32)))
	s.Show(ref, 44, 12)
	_, raws = settle(t, s, srv)
	keys, pic = transmitted(t, raws[len(raws)-1])
	if !strings.Contains(keys, ",c=1,r=1,") {
		t.Errorf("keys = %q at 16x32 cells, want one cell", keys)
	}
	same(t, termimg.Scale(pic, 16, 16), src)
}

// TestStoreSmallPictureKeepsItsShape is pictures smaller than the box whose
// sizes are not whole cells. As pixels each is sent no larger than it is, on
// a grid of its own shape: the side that was rounded down sets the other. As
// blocks each gets the cells that show its pixels once, or the box.
func TestStoreSmallPictureKeepsItsShape(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		w, h         int
		cellW, cellH int
		keys         string
		sent         image.Point // the picture sent as pixels
		blocks       image.Point // the picture drawn as blocks: cells by half cells
	}{
		// Three cells by one and a half: 3x1 would draw it half as wide
		// again as it is tall.
		{name: "an icon of 24 pixels", w: 24, h: 24, cellW: 8, cellH: 16, keys: ",c=2,r=1,", sent: image.Pt(16, 16), blocks: image.Pt(24, 24)},
		{name: "a thumbnail", w: 100, h: 100, cellW: 8, cellH: 16, keys: ",c=12,r=6,", sent: image.Pt(96, 96), blocks: image.Pt(24, 24)},
		{name: "a banner", w: 200, h: 50, cellW: 9, cellH: 18, keys: ",c=16,r=2,", sent: image.Pt(144, 36), blocks: image.Pt(44, 10)},
		{name: "cells that are not one by two", w: 40, h: 40, cellW: 11, cellH: 23, keys: ",c=2,r=1,", sent: image.Pt(22, 23), blocks: image.Pt(25, 24)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			src := chart(c.w, c.h)
			srv := newServer()
			srv.put("small.png", pngBytes(t, src), made())
			ref := termimg.Ref{Seq: 5, N: 1, Path: "small.png"}

			s := newStore(termcaps.ImagesPixels, cellSize(c.cellW, c.cellH))
			s.Show(ref, 44, 12)
			_, raws := settle(t, s, srv)
			keys, pic := transmitted(t, raws[0])
			if !strings.Contains(keys, c.keys) {
				t.Errorf("keys = %q, want %q in them", keys, c.keys)
			}
			if got := pic.Bounds().Size(); got != c.sent || got.X > c.w || got.Y > c.h {
				t.Errorf("the picture sent is %v, want %v", got, c.sent)
			}

			s.SetCaps(caps(termcaps.ImagesBlocks, cellSize(c.cellW, c.cellH)))
			rows := s.Show(ref, 44, 12).Rows
			equalRows(t, rows, termimg.HalfBlocks(termimg.Scale(src, c.blocks.X, c.blocks.Y)))
			if len(rows) != c.blocks.Y/2 {
				t.Errorf("as blocks it is %d rows, want %d", len(rows), c.blocks.Y/2)
			}
			idle(t, s)
		})
	}
}

// TestStoreMasterSize checks what the store keeps of a large picture: the
// largest box at the cell size, and never more than 1024 pixels a side.
func TestStoreMasterSize(t *testing.T) {
	t.Parallel()
	srv := newServer()
	srv.put("big.png", pngBytes(t, chart(1200, 900)), made())
	ref := termimg.Ref{Seq: 5, N: 1, Path: "big.png"}
	cases := []struct {
		name         string
		cellW, cellH int
		keys         string
		w, h         int // the picture sent
		bytes        int // the master kept
	}{
		// The box is 352 by 192 pixels; 4:3 fits it 256 wide.
		{name: "8x16 cells", cellW: 8, cellH: 16, keys: ",c=32,r=12,", w: 256, h: 192, bytes: 256 * 192 * 4},
		// Twice the pixels: 704 by 384.
		{name: "16x32 cells", cellW: 16, cellH: 32, keys: ",c=32,r=12,", w: 512, h: 384, bytes: 512 * 384 * 4},
		// A cell so large that the box would be 1760 by 960, more than the
		// picture has: the grid is the 11 whole rows the picture covers and
		// the 29 columns that keep its shape. The master stops at 1024
		// wide, and the grid's 1160 pixels are not made up.
		{name: "40x80 cells", cellW: 40, cellH: 80, keys: ",c=29,r=11,", w: 1024, h: 768, bytes: 1024 * 768 * 4},
		// No cell size is the default one.
		{name: "no cell size", keys: ",c=32,r=12,", w: 256, h: 192, bytes: 256 * 192 * 4},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s := termimg.NewStore(termimg.StoreOptions{})
			s.SetCaps(termcaps.Caps{Images: termcaps.ImagesPixels, Glyphs: termcaps.GlyphsUnicode, CellW: c.cellW, CellH: c.cellH})
			s.Show(ref, 44, 12)
			_, raws := settle(t, s, srv)
			keys, pic := transmitted(t, raws[0])
			if !strings.Contains(keys, c.keys) {
				t.Errorf("keys = %q, want %q in them", keys, c.keys)
			}
			if got := pic.Bounds().Size(); got != image.Pt(c.w, c.h) {
				t.Errorf("the picture sent is %v, want %dx%d", got, c.w, c.h)
			}
			if got := s.CacheBytes(); got != c.bytes {
				t.Errorf("the master is %d bytes, want %d", got, c.bytes)
			}
		})
	}
}

// TestStoreRowsBound checks that a long scroll through history does not
// leave every image's rows behind: only the most recent keep theirs, and the
// rest are encoded again when they come back into view.
func TestStoreRowsBound(t *testing.T) {
	t.Parallel()
	srv := newServer()
	srv.put("dot.png", pngBytes(t, chart(16, 16)), made())
	s := newStore(termcaps.ImagesBlocks)
	ref := func(i int) termimg.Ref { return termimg.Ref{Seq: uint64(i), N: 1, Path: "dot.png"} }
	const images = 300
	for i := 1; i <= images; i++ {
		s.Show(ref(i), 44, 12)
		settle(t, s, srv)
	}
	before := s.Stats().Encodes
	if before != images {
		t.Fatalf("%d encodes for %d images", before, images)
	}
	// The newest are still held: drawing them encodes nothing.
	for i := images; i > images-100; i-- {
		s.Show(ref(i), 44, 12)
	}
	if got := s.Stats().Encodes; got != before {
		t.Errorf("drawing the newest 100 again cost %d encodes", got-before)
	}
	// The oldest were let go, and come back from the master: 16 by 16
	// pixels as blocks is 8 rows.
	if got := s.Show(ref(1), 44, 12); len(got.Rows) != 8 {
		t.Errorf("the oldest image has %d rows, want 8", len(got.Rows))
	}
	if got := s.Stats().Encodes; got != before+1 {
		t.Errorf("drawing the oldest again cost %d encodes, want 1", got-before)
	}
	idle(t, s)
}

// TestStoreIDsFollowShowOrder is a fast scroll through more images than there
// are ids: every one is shown before any job has run. The jobs of the images
// scrolled past run last, and must not take the ids of the ones shown after
// them, which are the ones on screen.
func TestStoreIDsFollowShowOrder(t *testing.T) {
	t.Parallel()
	srv := newServer()
	// 32 by 32 pixels is 4 by 2 cells: the picture covers its box.
	srv.put("dot.png", pngBytes(t, chart(32, 32)), made())
	s := termimg.NewStore(termimg.StoreOptions{})
	s.SetCaps(caps(termcaps.ImagesPixels))
	ref := func(i int) termimg.Ref { return termimg.Ref{Seq: uint64(i), N: 1, Path: "dot.png"} }
	const images = 300
	for i := 1; i <= images; i++ {
		s.Show(ref(i), 4, 2)
	}
	// The newest goes first and is drawn.
	if seq, raw := step(t, s, srv); seq != images || raw == "" {
		t.Fatalf("the first job was for reply %d with %d bytes, want reply %d and its transmit", seq, len(raw), images)
	}
	first := slices.Clone(s.Show(ref(images), 4, 2).Rows)
	if first == nil {
		t.Fatal("the newest image has no rows after its job")
	}
	seqs, raws := settle(t, s, srv)
	if len(seqs) != images-1 {
		t.Fatalf("%d more jobs, want one for each of the other %d images", len(seqs), images-1)
	}
	// 255 pictures were sent, to the 255 images shown last. The 45 shown
	// before them were read and not sent, and nothing was deleted.
	for i, raw := range raws {
		if wantSent := i < 254; (raw != "") != wantSent {
			t.Fatalf("the job for reply %d returned %d bytes; sent should be %v", seqs[i], len(raw), wantSent)
		}
		if strings.Contains(raw, "a=d") {
			t.Fatalf("the job for reply %d deleted a picture", seqs[i])
		}
	}
	if got := s.Stats(); got.Jobs != images || got.Encodes != 255 {
		t.Errorf("Stats = %+v, want %d jobs and 255 encodes", got, images)
	}
	if got := s.Stale(); got != nil {
		t.Errorf("Stale = %v, want none: no image that was drawn lost its id", got)
	}
	// The image on screen still has its picture, under the id it was
	// drawn with.
	before := s.Stats()
	equalRows(t, s.Show(ref(images), 4, 2).Rows, first)
	idle(t, s)
	if got := s.Stats(); got != before {
		t.Errorf("Stats went from %+v to %+v on redraws", before, got)
	}

	// An image that got no id is its text form. Shown again it is the newest,
	// so it takes the id of the one shown longest ago, reply 46, without a
	// fetch.
	fetches := len(srv.seen())
	if got := s.Show(ref(1), 4, 2); got.Rows != nil || got.Hint != "/image save 1 ./dot.png" {
		t.Errorf("Show of an image that was never sent = %+v, want its text form", got)
	}
	seq, raw := step(t, s, srv)
	if del := termimg.KittyDelete(255, false); seq != 1 || !strings.HasPrefix(raw, del) {
		t.Fatalf("Finish = %d, %q; want reply 1 and bytes that start with the delete of id 255", seq, head(raw))
	}
	equalRows(t, s.Show(ref(1), 4, 2).Rows, termimg.KittyPlaceholders(255, 4, 2))
	if got := s.Stale(); !slices.Equal(got, []uint64{46}) {
		t.Errorf("Stale = %v, want reply 46", got)
	}
	if got := len(srv.seen()); got != fetches {
		t.Errorf("sending reply 1 fetched %d more times", got-fetches)
	}
	idle(t, s)
}

// TestStoreCellGrows is an image loaded before the terminal has said how
// large its cells are, as the first ones of a chat are. Its master was made
// for the default cell. When the cells turn out larger, the picture is sent
// from the file's own pixels, not from the small master stretched.
func TestStoreCellGrows(t *testing.T) {
	t.Parallel()
	noCell := termcaps.Caps{Images: termcaps.ImagesBlocks, Glyphs: termcaps.GlyphsUnicode}
	sentPicture := func(t *testing.T, raws []string) (string, image.Point) {
		t.Helper()
		if len(raws) != 1 {
			t.Fatalf("%d jobs, want one", len(raws))
		}
		keys, pic := transmitted(t, raws[0])
		return keys, pic.Bounds().Size()
	}
	t.Run("pixels", func(t *testing.T) {
		t.Parallel()
		srv, _ := withChart(t)
		s := termimg.NewStore(termimg.StoreOptions{IDBase: 6})
		s.SetCaps(noCell)
		s.Show(latency(), 44, 12)
		settle(t, s, srv)
		if got, want := s.CacheBytes(), 341*192*4; got != want {
			t.Fatalf("the first master is %d bytes, want %d: 341 by 192", got, want)
		}

		// The terminal answers: pixels, and cells of 16 by 32. The box is
		// now 704 by 384 pixels, more than the 640 by 360 file has, so the
		// picture keeps to the 11 whole rows it covers and the 39 columns
		// that keep its shape.
		changed, _ := s.SetCaps(caps(termcaps.ImagesPixels, cellSize(16, 32)))
		if !slices.Equal(changed, []uint64{31}) {
			t.Errorf("SetCaps = %v, want reply 31", changed)
		}
		s.Show(latency(), 44, 12)
		_, raws := settle(t, s, srv)
		keys, size := sentPicture(t, raws)
		if !strings.Contains(keys, ",c=39,r=11,") || size != image.Pt(624, 352) {
			t.Errorf("sent %v with keys %q, want the file's pixels, 624x352, on 39x11", size, keys)
		}
		if got, want := srv.seen(), []call{{path: "latency.png"}, {path: "latency.png"}}; !slices.Equal(got, want) {
			t.Errorf("fetches = %v, want a second one for the whole file", got)
		}
		if got, want := s.CacheBytes(), 640*360*4; got != want || s.CacheLen() != 1 {
			t.Errorf("the cache holds %d masters in %d bytes, want the new one alone in %d", s.CacheLen(), got, want)
		}
		equalRows(t, s.Show(latency(), 44, 12).Rows, termimg.KittyPlaceholders(7, 39, 11))

		// Smaller cells, and the same larger ones again: the master has
		// all the pixels either needs, so nothing more is fetched.
		s.SetCaps(caps(termcaps.ImagesPixels))
		s.Show(latency(), 44, 12)
		_, raws = settle(t, s, srv)
		if _, size := sentPicture(t, raws); size != image.Pt(336, 192) {
			t.Errorf("sent %v at 8x16 cells, want 336x192", size)
		}
		s.SetCaps(caps(termcaps.ImagesPixels, cellSize(16, 32)))
		s.Show(latency(), 44, 12)
		_, raws = settle(t, s, srv)
		if _, size := sentPicture(t, raws); size != image.Pt(624, 352) {
			t.Errorf("sent %v at 16x32 cells, want 624x352", size)
		}
		if got := len(srv.seen()); got != 2 {
			t.Errorf("%d fetches, want 2", got)
		}
	})
	t.Run("pixels, and the file is gone", func(t *testing.T) {
		t.Parallel()
		srv, _ := withChart(t)
		s := termimg.NewStore(termimg.StoreOptions{IDBase: 6})
		s.SetCaps(noCell)
		s.Show(latency(), 44, 12)
		settle(t, s, srv)
		srv.fail("latency.png", termimg.ErrNotFound)

		// The picture it has is still the picture: sent as it is.
		s.SetCaps(caps(termcaps.ImagesPixels, cellSize(16, 32)))
		s.Show(latency(), 44, 12)
		seqs, raws := settle(t, s, srv)
		keys, size := sentPicture(t, raws)
		if !slices.Equal(seqs, []uint64{31}) || !strings.Contains(keys, ",c=39,r=11,") || size != image.Pt(341, 192) {
			t.Errorf("Finish = %v, sent %v with keys %q; want reply 31 and the 341x192 master on 39x11", seqs, size, keys)
		}
		equalRows(t, s.Show(latency(), 44, 12).Rows, termimg.KittyPlaceholders(7, 39, 11))
		idle(t, s)
		if got := len(srv.seen()); got != 2 {
			t.Errorf("%d fetches, want 2: one try for the larger picture", got)
		}
	})
	t.Run("blocks need no more pixels", func(t *testing.T) {
		t.Parallel()
		srv, _ := withChart(t)
		s := termimg.NewStore(termimg.StoreOptions{})
		s.SetCaps(noCell)
		s.Show(latency(), 44, 12)
		settle(t, s, srv)
		s.SetCaps(caps(termcaps.ImagesBlocks, cellSize(16, 32)))
		// A block is a block whatever the cell's pixels: the chart fills
		// the box as it did at 8 by 16.
		equalRows(t, s.Show(latency(), 44, 12).Rows, termimg.HalfBlocks(termimg.Scale(master(), 42, 24)))
		idle(t, s)
		if got := len(srv.seen()); got != 1 {
			t.Errorf("%d fetches, want 1", got)
		}
	})
}
