package canvas

import "image/color"

// Color is an sRGB colour with components in [0, 1]. Working in floats keeps
// gradients and repeated blends free of 8-bit banding; quantisation happens
// once, when a frame is encoded.
type Color struct{ R, G, B float64 }

// White is the colour of catchlights and specular highlights.
//
//nolint:gochecknoglobals // a colour by its name, only read
var White = Color{1, 1, 1}

// Hex builds a Color from a 0xRRGGBB literal.
func Hex(rgb uint32) Color {
	return Color{
		R: float64(rgb>>16&0xff) / 255,
		G: float64(rgb>>8&0xff) / 255,
		B: float64(rgb&0xff) / 255,
	}
}

// FromColor converts any standard-library colour, ignoring its alpha.
func FromColor(c color.Color) Color {
	r, g, b, _ := c.RGBA()
	return Color{float64(r) / 0xffff, float64(g) / 0xffff, float64(b) / 0xffff}
}

// Mix interpolates from c to o as t goes from 0 to 1.
func (c Color) Mix(o Color, t float64) Color {
	return Color{c.R + (o.R-c.R)*t, c.G + (o.G-c.G)*t, c.B + (o.B-c.B)*t}
}

// RGBA implements color.Color, so a Color can be handed straight to Lip Gloss.
func (c Color) RGBA() (r, g, b, a uint32) {
	return uint32(quantise(c.R)) * 0x101, uint32(quantise(c.G)) * 0x101, uint32(quantise(c.B)) * 0x101, 0xffff
}

// pack quantises c to 0xRRGGBB.
func (c Color) pack() int32 {
	return int32(quantise(c.R))<<16 | int32(quantise(c.G))<<8 | int32(quantise(c.B))
}

func quantise(v float64) uint8 { return uint8(min(max(v, 0), 1)*255 + 0.5) }

// Sample is what a shader returns for one pixel: a colour and how much of the
// pixel it covers. The colour is premultiplied by coverage, which turns
// layering into a single multiply-add. The zero Sample is fully transparent.
type Sample struct {
	Color
	A float64
}

// Over paints c on top of s, covering the fraction a of the pixel.
func (s Sample) Over(c Color, a float64) Sample {
	a = min(max(a, 0), 1)
	k := 1 - a
	return Sample{Color{c.R*a + s.R*k, c.G*a + s.G*k, c.B*a + s.B*k}, a + s.A*k}
}

// Fade thins the sample to the fraction k of its coverage.
func (s Sample) Fade(k float64) Sample {
	return Sample{Color{s.R * k, s.G * k, s.B * k}, s.A * k}
}

// mean is the average of two samples.
func mean(a, b Sample) Sample {
	return Sample{Color{(a.R + b.R) / 2, (a.G + b.G) / 2, (a.B + b.B) / 2}, (a.A + b.A) / 2}
}

// on resolves the sample against an opaque backdrop.
func (s Sample) on(bg Color) Color {
	k := 1 - s.A
	return Color{s.R + bg.R*k, s.G + bg.G*k, s.B + bg.B*k}
}
