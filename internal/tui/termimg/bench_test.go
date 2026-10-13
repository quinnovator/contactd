package termimg_test

import (
	"image"
	"testing"

	"github.com/quinnovator/contactd/internal/tui/termimg"
)

// BenchmarkScale is the two scalings an image goes through: a decoded file
// down to its master, in a job, and the master down to a grid of half
// blocks, on the update loop.
func BenchmarkScale(b *testing.B) {
	decoded := image.NewNRGBA(image.Rect(0, 0, 640, 360))
	src := chart(640, 360)
	for y := range 360 {
		for x := range 640 {
			decoded.Set(x, y, src.RGBAAt(x, y))
		}
	}
	master := termimg.Scale(src, 341, 192)
	cases := []struct {
		name string
		src  image.Image
		w, h int
	}{
		{name: "file 640x360 to master 341x192", src: decoded, w: 341, h: 192},
		{name: "master to blocks 42x24", src: master, w: 42, h: 24},
		{name: "master to pixels 336x192", src: master, w: 336, h: 192},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				termimg.Scale(c.src, c.w, c.h)
			}
		})
	}
}

// BenchmarkHalfBlocks encodes a full box, 44 by 12 cells: a chart, whose
// colours run, and noise, where every cell sets both of its colours.
func BenchmarkHalfBlocks(b *testing.B) {
	cases := []struct {
		name string
		img  *image.RGBA
	}{
		{name: "chart", img: chart(44, 24)},
		{name: "noise", img: noise(44, 24)},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				termimg.HalfBlocks(c.img)
			}
		})
	}
}

// BenchmarkKittyTransmit encodes the picture for a grid of 42 by 12 cells of
// 8 by 16 pixels, direct and through tmux. It runs in a job.
func BenchmarkKittyTransmit(b *testing.B) {
	cases := []struct {
		name string
		img  *image.RGBA
		tmux bool
	}{
		{name: "chart", img: chart(336, 192)},
		{name: "noise", img: noise(336, 192)},
		{name: "noise through tmux", img: noise(336, 192), tmux: true},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				raw, err := termimg.KittyTransmit(7, c.img, 42, 12, c.tmux)
				if err != nil {
					b.Fatal(err)
				}
				b.SetBytes(int64(len(raw)))
			}
		})
	}
}
