package avatar

import (
	"math"

	"github.com/quinnovator/contactd/internal/tui/canvas"
	"github.com/quinnovator/contactd/internal/tui/motion"
)

// Proportions, in canvas units (the canvas's shorter side spans -1 to 1).
const (
	bodyRadius = 0.64 // the rest of the canvas is room for aura, ripples and bounce
	auraReach  = 0.34 // how far the glow extends before it is gone entirely
	orbit      = 0.88 // radius of the ring the thinking-motes travel
	veil       = 0.9  // distance from the centre at which everything starts to fade out
	rippleLife = 0.9  // seconds a voice ripple lasts
)

// The light sits up and to the left, a little toward the viewer.
const lightX, lightY, lightZ = -0.42, -0.56, 0.71

// Paint draws the pose. The picture is a pure function of the pose: every
// pixel is computed from signed distances to the shapes that make up the
// character, which is what lets edges be anti-aliased and motion be
// continuous at a resolution of a few dozen pixels.
func (p Pose) Paint(c *canvas.Canvas) {
	s := newScene(p, c.Pixel())
	c.Shade(s.shade)
}

// scene is a Pose prepared for shading: whatever is constant across a frame,
// worked out once.
type scene struct {
	Pose
	px           float64 // one sample, the anti-aliasing width
	r            float64 // body radius
	cy           float64 // body centre
	cos, sin     float64 // head tilt
	sx, sy       float64 // squash and stretch
	lx, ly       float64 // light direction, in the body's tilted frame
	faceX, faceY float64 // the face slides across the body toward the gaze
	wave, wave2  float64 // phases of the waves running round the outline
	aura         float64 // strength of the glow at the skin
	behind, near []mote  // the thinking-ring, split by which side of the body it is on
}

// mote is one soft point of light in the thinking-ring.
type mote struct{ x, y, r, a float64 }

func newScene(p Pose, px float64) *scene {
	s := &scene{Pose: p, px: px}
	s.r = bodyRadius * (1 + 0.025*p.Energy)
	s.cy = p.Bob
	s.sin, s.cos = math.Sincos(p.Tilt)
	// Stretch conserves volume: taller means narrower.
	s.sy = 1 + p.Squash + 0.03*p.Jaw
	s.sx = 1 / s.sy
	s.lx = lightX*s.cos + lightY*s.sin
	s.ly = -lightX*s.sin + lightY*s.cos
	s.faceX, s.faceY = 0.24*s.r*p.GazeX, 0.2*s.r*p.GazeY
	s.wave, s.wave2 = 1.1*p.Time, -1.7*p.Time
	// The aura brightens with the energy of speech, and breathes a little.
	s.aura = 0.34 + 0.4*p.Energy + 0.04*math.Sin(1.6*p.Time)

	if p.Focus > 0.02 {
		// Three motes circle the body on a tilted ring, each a soft point of
		// light with a few fainter echoes trailing behind it.
		const count, echoes = 3, 4
		const lag = 0.11 // radians between echoes
		sinI, cosI := math.Sincos(-0.32)
		for i := range count {
			for k := range echoes {
				phase := 2.4*p.Time + float64(i)*2*math.Pi/count - float64(k)*lag
				depth, along := math.Sincos(phase)
				ox, oy := orbit*along, 0.3*orbit*depth
				fade := math.Pow(0.45, float64(k))
				m := mote{
					x: ox*cosI - oy*sinI,
					y: s.cy + ox*sinI + oy*cosI,
					r: max(0.05+0.014*depth, 1.3*px) * (1 - 0.15*float64(k)),
					a: p.Focus * fade * (0.85 + 0.15*depth),
				}
				if depth > 0 {
					s.near = append(s.near, m)
				} else {
					s.behind = append(s.behind, m)
				}
			}
		}
	}
	return s
}

// shade computes one pixel, layering back to front.
func (s *scene) shade(x, y float64) canvas.Sample {
	var out canvas.Sample
	pal := &s.Palette

	// Into the body's own frame: recentre, untilt, unstretch.
	dx, dy := x, y-s.cy
	bx := (dx*s.cos + dy*s.sin) / s.sx
	by := (-dx*s.sin + dy*s.cos) / s.sy
	// The outline is a circle with a slow wave running round it, livelier
	// when speaking, so the body reads as something soft.
	angle := math.Atan2(by, bx)
	swell := (0.007 + 0.02*s.Energy) * (0.6*math.Sin(3*angle+s.wave) + 0.4*math.Sin(5*angle+s.wave2))
	edge := s.r * (1 + swell)
	d := math.Hypot(bx, by) - edge // signed distance to the skin

	// The glow the body throws on the air around it, gone entirely by
	// auraReach.
	fall := clamp(1-d/auraReach, 0, 1)
	out = out.Over(pal.Glow, s.aura*fall*fall*fall)
	out = out.Over(pal.Glow, s.ripple(math.Hypot(dx, dy)))
	out = s.ring(out, s.behind, x, y)
	if d < s.px {
		out = out.Over(s.skin(bx/edge, by/edge), cover(d, s.px))
		out = s.face(out, bx-s.faceX, by-s.faceY)
	}
	out = s.ring(out, s.near, x, y)

	// Whatever light reaches the rim of the canvas dissolves before it gets
	// there, so the picture never shows a frame.
	if rim := math.Hypot(x, y); rim > veil {
		out = out.Fade(1 - motion.Smooth((rim-veil)/(1-veil)))
	}
	return out
}

// ripple is the coverage of the voice ripples at a distance from the centre:
// rings that expand, soften and fade.
func (s *scene) ripple(dist float64) float64 {
	var a float64
	for _, age := range s.Ripples {
		life := age / rippleLife
		radius := s.r + 0.02 + 0.36*age
		width := 0.025 + 0.07*age
		a += 0.2 * (1 - life) * (1 - life) * glow(dist-radius, width)
	}
	return a
}

// ring paints the motes on one side of the body.
func (s *scene) ring(out canvas.Sample, motes []mote, x, y float64) canvas.Sample {
	for _, m := range motes {
		// Squared distance, in mote radii: this loop runs for every sample.
		dx, dy := (x-m.x)/m.r, (y-m.y)/m.r
		if d2 := dx*dx + dy*dy; d2 < glowReach*glowReach {
			out = out.Over(s.Palette.Light, m.a*math.Exp(-d2))
		}
	}
	return out
}

// skin lights the body as a sphere with surface normal (nx, ny): diffuse
// shading wrapped well past the terminator, a glowing rim, and one soft
// highlight.
func (s *scene) skin(nx, ny float64) canvas.Color {
	pal := &s.Palette
	nz := math.Sqrt(max(0, 1-nx*nx-ny*ny))
	lit := 0.5 + 0.5*(nx*s.lx+ny*s.ly+nz*lightZ)
	c := pal.skin(lit * lit)
	rim := (1 - nz) * (1 - nz) * (1 - nz)
	c = c.Mix(pal.Glow, 0.5*rim)
	hx, hy := nx-0.9*s.lx, ny-0.9*s.ly
	return c.Mix(canvas.White, 0.38*glow(math.Hypot(hx, hy), 0.19))
}

// face paints the features at (x, y) in the face's frame. The face is
// mirror-symmetric, so both eyes and both cheeks are drawn by folding x.
func (s *scene) face(out canvas.Sample, x, y float64) canvas.Sample {
	r, pal := s.r, &s.Palette
	ax := math.Abs(x)

	// Cheeks, flushing as the smile grows.
	blush := (0.14 + 0.3*max(s.Smile, 0)) * glow(math.Hypot(ax-0.47*r, y-0.12*r), 0.13*r)
	out = out.Over(pal.Blush, blush)

	// Eyes are upright capsules that flatten to a line when the lids close.
	// Joy pushes the cheeks up under them, bending each into an arch.
	joy := clamp(2*s.Smile-1, 0, 1)
	ex, ey := ax-0.31*r, y+0.12*r
	w := 0.11 * r * (1 + 0.3*(1-s.Lids) + 0.35*joy)
	h := max(0.22*r*s.Lids*(1+0.3*s.Awe), 0.7*s.px)
	if eye := cover(capsule(ex, ey, w, h), s.px); eye > 0 {
		cheek := 0.2 * r
		eye *= cover(cheek-math.Hypot(ex, ey-h-cheek+1.4*h*joy), s.px)
		out = out.Over(pal.Ink, eye)
		// A catchlight toward the lamp, on the same side of both eyes. It
		// goes out as the eye closes.
		gx := math.Copysign(ex, x) + 0.2*w
		glint := cover(math.Hypot(gx, ey+0.45*h)-0.36*w, s.px)
		out = out.Over(canvas.White, 0.85*eye*glint*s.Lids*(1-joy))
	}

	return s.mouth(out, x, y-0.30*r)
}

// mouth paints the mouth at (x, y) relative to its centre: a curve whose ends
// lift with the smile, opened along its length by the jaw.
func (s *scene) mouth(out canvas.Sample, x, y float64) canvas.Sample {
	r, pal := s.r, &s.Palette
	w := r * (0.21 + 0.07*s.Spread + 0.05*s.Smile*(1-s.Jaw))
	if math.Abs(x) > w+s.px {
		return out
	}
	u := x / w
	arch := max(0, 1-u*u) // 1 at the centre of the mouth, 0 at its corners
	gape := 0.34 * r * s.Jaw * math.Sqrt(arch)
	lip := max(0.03*r, 0.7*s.px)
	centre := 0.13 * r * s.Smile * (arch - 0.5)
	top := centre - lip - 0.3*gape
	bottom := centre + lip + gape
	d := max(top-y, y-bottom, math.Abs(x)-w)
	inside := cover(d, s.px)
	if inside == 0 {
		return out
	}
	// A closed mouth is an ink line; opening reveals the darker inside, and
	// then the tongue resting on its floor.
	out = out.Over(pal.Ink.Mix(pal.Maw, clamp(4*s.Jaw, 0, 1)), inside)
	tw, th := 0.62*w, 0.6*gape
	tongue := (math.Hypot(x/tw, (y-bottom)/max(th, 1e-6)) - 1) * min(tw, th)
	return out.Over(pal.Tongue, inside*cover(tongue, s.px)*clamp(3*s.Jaw-0.6, 0, 1))
}

// cover converts a signed distance (negative inside) to the fraction of a
// pixel covered: a one-pixel ramp centred on the edge.
func cover(d, px float64) float64 { return clamp(0.5-d/px, 0, 1) }

// capsule is the signed distance from (x, y) to a box of half-size w×h with
// fully rounded ends.
func capsule(x, y, w, h float64) float64 {
	r := min(w, h)
	return math.Hypot(max(math.Abs(x)-w+r, 0), max(math.Abs(y)-h+r, 0)) - r
}

// glowReach is how many sizes out a glow extends before it is too faint to
// change a pixel.
const glowReach = 2.5

// glow is a soft falloff with distance d from a light of the given size: a
// Gaussian, cut off at glowReach.
func glow(d, size float64) float64 {
	if d = d / size; d > glowReach || d < -glowReach {
		return 0
	}
	return math.Exp(-d * d)
}
