package avatar

import "github.com/quinnovator/contactd/internal/tui/canvas"

// Palette is the set of colours an avatar is painted with.
type Palette struct {
	Name              string
	Light, Core, Deep canvas.Color // the body, from its lit side round to shadow
	Glow              canvas.Color // aura and rim light
	Ink               canvas.Color // eyes, and the line of a closed mouth
	Maw, Tongue       canvas.Color // inside an open one
	Blush             canvas.Color
}

// Palettes returns the built-in looks, in the order they are listed. The
// slice is the caller's own.
func Palettes() []Palette {
	return []Palette{
		{
			Name:  "ember",
			Light: canvas.Hex(0xFFC79E), Core: canvas.Hex(0xE5805A), Deep: canvas.Hex(0x9E3F2C),
			Glow: canvas.Hex(0xFF8757), Ink: canvas.Hex(0x2A1310),
			Maw: canvas.Hex(0x5C1A1C), Tongue: canvas.Hex(0xF4727C), Blush: canvas.Hex(0xFF5F6D),
		},
		{
			Name:  "tide",
			Light: canvas.Hex(0xB4F5EA), Core: canvas.Hex(0x3DB5C6), Deep: canvas.Hex(0x1A4F86),
			Glow: canvas.Hex(0x45D4E4), Ink: canvas.Hex(0x061C2B),
			Maw: canvas.Hex(0x0B2A4A), Tongue: canvas.Hex(0xF78DA7), Blush: canvas.Hex(0xB99CFF),
		},
		{
			Name:  "orchid",
			Light: canvas.Hex(0xF6CCFF), Core: canvas.Hex(0xAE7BEA), Deep: canvas.Hex(0x53349E),
			Glow: canvas.Hex(0xC18BFF), Ink: canvas.Hex(0x1B0E30),
			Maw: canvas.Hex(0x35125A), Tongue: canvas.Hex(0xFF8FB3), Blush: canvas.Hex(0xFF86CF),
		},
		{
			Name:  "moss",
			Light: canvas.Hex(0xE4F7A8), Core: canvas.Hex(0x7FBF5A), Deep: canvas.Hex(0x2C6B45),
			Glow: canvas.Hex(0xA6E36A), Ink: canvas.Hex(0x10230F),
			Maw: canvas.Hex(0x1C3D22), Tongue: canvas.Hex(0xF58A8A), Blush: canvas.Hex(0xFFC46B),
		},
	}
}

// Mix blends every colour from p toward q, for cross-fading between looks.
func (p Palette) Mix(q Palette, t float64) Palette {
	return Palette{
		Name:   q.Name,
		Light:  p.Light.Mix(q.Light, t),
		Core:   p.Core.Mix(q.Core, t),
		Deep:   p.Deep.Mix(q.Deep, t),
		Glow:   p.Glow.Mix(q.Glow, t),
		Ink:    p.Ink.Mix(q.Ink, t),
		Maw:    p.Maw.Mix(q.Maw, t),
		Tongue: p.Tongue.Mix(q.Tongue, t),
		Blush:  p.Blush.Mix(q.Blush, t),
	}
}

// skin shades the body for a surface facing the light by lit, from 0 (turned
// away) to 1 (facing it). Most of the body sits near Core; Light is kept for
// the part that faces the lamp squarely.
func (p *Palette) skin(lit float64) canvas.Color {
	const knee = 0.6
	if lit < knee {
		return p.Deep.Mix(p.Core, lit/knee)
	}
	return p.Core.Mix(p.Light, (lit-knee)/(1-knee))
}
