// Package avatar is a small animated character that speaks.
//
// It is split the way a puppet is. Avatar is the puppeteer: it holds a spring
// for every moving part and decides, from mood and speech, where each should
// be heading. Pose is the puppet at one instant, plain numbers with no
// behaviour. Paint draws a Pose and knows nothing about time.
package avatar

import (
	"math"
	"math/rand/v2"

	"github.com/quinnovator/contactd/internal/tui/canvas"
	"github.com/quinnovator/contactd/internal/tui/motion"
	"github.com/quinnovator/contactd/internal/tui/speech"
)

// Mood is the avatar's frame of mind, which colours everything it does.
type Mood int

const (
	Idle     Mood = iota // present and attentive
	Thinking             // waiting for the words to come
	Speaking
	Content // finished, and pleased about it
)

// Avatar animates a face. Call Step once per simulation step and Draw
// whenever a frame is needed.
type Avatar struct {
	t    float64
	mood Mood

	gazeX, gazeY motion.Spring // where the eyes point, each in [-1, 1]
	lids         motion.Spring // 1 open, 0 shut
	awe          motion.Spring // eyes widening in surprise
	jaw, spread  motion.Spring // the mouth, following the current viseme
	smile        motion.Spring
	tilt         motion.Spring // head tilt, radians
	bob          motion.Spring // vertical nod
	squash       motion.Spring // stretch tall (+) or squash flat (-)
	focus        motion.Spring // 1 while lost in thought
	energy       float64       // how loudly it is speaking, smoothed

	blinkUntil, nextBlink float64
	nextGlance            float64
	glanceX, glanceY      float64
	watchX, watchY        float64 // something outside worth looking at...
	watchUntil            float64 // ...until it stops moving
	lean                  float64 // which way the next quizzical tilt goes
	ripples               []float64
	// chance times the blinks and the glances. It is the avatar's own, so
	// that two avatars started alike move alike.
	chance *rand.Rand

	palette, wearing Palette
}

// New returns an avatar wearing the given palette. seed sets when it blinks
// and where it glances: the same seed gives the same performance.
func New(p Palette, seed uint64) *Avatar {
	a := &Avatar{
		//nolint:gosec // it times a blink; nothing is kept secret with it
		chance:  rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)),
		gazeX:   motion.NewSpring(17, 0.75),
		gazeY:   motion.NewSpring(17, 0.75),
		lids:    motion.NewSpring(60, 1),
		awe:     motion.NewSpring(11, 0.45),
		jaw:     motion.NewSpring(40, 0.75),
		spread:  motion.NewSpring(28, 0.9),
		smile:   motion.NewSpring(7, 0.6),
		tilt:    motion.NewSpring(7, 0.4),
		bob:     motion.NewSpring(12, 0.4),
		squash:  motion.NewSpring(15, 0.28),
		focus:   motion.NewSpring(6, 1),
		lean:    1,
		palette: p,
		wearing: p,
	}
	a.lids.Set(1)
	a.nextBlink = 1.5
	return a
}

// Step advances the animation by motion.Dt. The mouth shape and beats come
// from the voice; everything else the avatar does on its own.
func (a *Avatar) Step(mood Mood, mouth speech.Viseme, beats speech.Beat) {
	a.t += motion.Dt
	a.feel(mood)
	a.react(beats)
	a.articulate(mouth)
	a.blink()
	a.look()
	for _, s := range []*motion.Spring{
		&a.gazeX, &a.gazeY, &a.lids, &a.awe, &a.jaw, &a.spread,
		&a.smile, &a.tilt, &a.bob, &a.squash, &a.focus,
	} {
		s.Step()
	}
	a.palette = a.palette.Mix(a.wearing, 0.07)
}

// LookAt draws the avatar's eyes to a point in canvas coordinates. It keeps
// watching for a moment after the last call, then loses interest.
func (a *Avatar) LookAt(x, y float64) {
	a.watchX, a.watchY = clamp(x, -1, 1), clamp(y, -1, 1)
	a.watchUntil = a.t + 2.5
}

// Wear cross-fades the avatar into a new palette.
func (a *Avatar) Wear(p Palette) { a.wearing = p }

// Palette is the palette as currently blended, mid-fade included.
func (a *Avatar) Palette() Palette { return a.palette }

// Mouth is the shape the mouth is actually in, springs and all: where the
// voice asked it to be, a moment late and a little smoother.
func (a *Avatar) Mouth() speech.Viseme {
	return speech.Viseme{Open: motion.Clamp01(a.jaw.Pos), Spread: a.spread.Pos}
}

// Draw paints the current pose.
func (a *Avatar) Draw(c *canvas.Canvas) { a.Pose().Paint(c) }

// feel settles into a mood, with a flourish on the way in.
func (a *Avatar) feel(mood Mood) {
	if mood != a.mood {
		switch mood {
		case Speaking:
			a.awe.Kick(2) // perk up
		case Content:
			a.squash.Kick(2.2) // a pleased little bounce
			a.blinkUntil = a.t + 0.12
		case Idle, Thinking:
			// These come on quietly.
		}
		a.mood = mood
		a.nextGlance = a.t
	}
	a.smile.Target = [...]float64{Idle: 0.3, Thinking: 0, Speaking: 0.35, Content: 1}[mood]
	a.focus.Target = 0
	if mood == Thinking {
		a.focus.Target = 1
	}
}

// react punctuates speech with the body.
func (a *Avatar) react(b speech.Beat) {
	if b&speech.Word != 0 {
		a.ripple(0.45)
	}
	if b&speech.Pause != 0 {
		a.bob.Kick(0.35)
	}
	if b&speech.Stop != 0 {
		a.bob.Kick(0.6) // a nod to close the sentence
		if a.chance.Float64() < 0.5 {
			a.blinkUntil = a.t + 0.1
		}
	}
	if b&speech.Ask != 0 {
		a.lean = -a.lean
		a.tilt.Kick(2.2 * a.lean) // head cocked, alternating sides
		a.awe.Kick(4)
	}
	if b&speech.Exclaim != 0 {
		a.squash.Kick(2.6)
		a.awe.Kick(2.5)
		a.ripple(0.15)
	}
	for len(a.ripples) > 0 && a.t-a.ripples[0] > rippleLife {
		a.ripples = a.ripples[1:]
	}
}

// ripple sends a ring of sound out from the body, unless the last one left
// less than gap seconds ago.
func (a *Avatar) ripple(gap float64) {
	if n := len(a.ripples); n == 0 || a.t-a.ripples[n-1] > gap {
		a.ripples = append(a.ripples, a.t)
	}
}

// articulate shapes the mouth, and tracks how energetic the speech is with a
// follower that rises fast and falls slowly, like a level meter.
func (a *Avatar) articulate(mouth speech.Viseme) {
	a.jaw.Target, a.spread.Target = mouth.Open, mouth.Spread
	level := motion.Clamp01(a.jaw.Pos * 1.5)
	rate := 0.03
	if level > a.energy {
		rate = 0.25
	}
	a.energy += (level - a.energy) * rate
}

// blink closes the eyes for an instant every few seconds, and now and then
// twice in a row.
func (a *Avatar) blink() {
	if a.t >= a.nextBlink {
		a.blinkUntil = a.t + 0.1
		a.nextBlink = a.t + 2 + 4*a.chance.Float64()
		if a.chance.Float64() < 0.15 {
			a.nextBlink = a.t + 0.3
		}
	}
	a.lids.Target = 1
	if a.t < a.blinkUntil {
		a.lids.Target = 0
	}
}

// look moves the eyes the way eyes move: in quick hops between points of
// interest, with a dwell on each.
func (a *Avatar) look() {
	if a.t >= a.nextGlance {
		switch {
		case a.mood == Thinking:
			// Up and off to one side, switching sides as it ponders.
			side := 1.0
			if a.glanceX > 0 {
				side = -1
			}
			a.glanceX, a.glanceY = side*a.between(0.35, 0.75), -a.between(0.45, 0.8)
			a.nextGlance = a.t + a.between(0.9, 1.8)
		case a.mood == Speaking && a.chance.Float64() < 0.3:
			// A glance down at its own words.
			a.glanceX, a.glanceY = a.between(-0.3, 0.3), a.between(0.5, 0.7)
			a.nextGlance = a.t + a.between(0.5, 0.9)
		default:
			// Eye contact, near enough.
			a.glanceX, a.glanceY = a.between(-0.14, 0.14), a.between(-0.1, 0.08)
			a.nextGlance = a.t + a.between(1.2, 3)
		}
	}
	a.gazeX.Target, a.gazeY.Target = a.glanceX, a.glanceY
	if a.t < a.watchUntil {
		a.gazeX.Target, a.gazeY.Target = a.watchX, a.watchY
	}
}

// Pose is the avatar at one instant: everything Paint needs, nothing more.
type Pose struct {
	Time          float64
	GazeX, GazeY  float64
	Lids, Awe     float64
	Jaw, Spread   float64
	Smile         float64
	Tilt, Bob     float64
	Squash        float64
	Focus, Energy float64
	Ripples       []float64 // age of each voice ripple, in seconds
	Palette       Palette
}

// Pose samples the springs and layers on the motion that never stops:
// breathing, and a sway that grows with the energy of speech.
func (a *Avatar) Pose() Pose {
	p := Pose{
		Time:    a.t,
		GazeX:   a.gazeX.Pos,
		GazeY:   a.gazeY.Pos,
		Lids:    motion.Clamp01(a.lids.Pos),
		Awe:     a.awe.Pos,
		Jaw:     motion.Clamp01(a.jaw.Pos),
		Spread:  a.spread.Pos,
		Smile:   a.smile.Pos,
		Tilt:    a.tilt.Pos + 0.03*motion.Wander(0.5*a.t, 1) + 0.05*a.energy*motion.Wander(2.3*a.t, 4),
		Bob:     a.bob.Pos + 0.014*math.Sin(1.6*a.t) + 0.02*a.energy*motion.Wander(3.1*a.t, 7),
		Squash:  a.squash.Pos + 0.012*math.Sin(1.6*a.t+1),
		Focus:   motion.Clamp01(a.focus.Pos),
		Energy:  a.energy,
		Palette: a.palette,
	}
	for _, born := range a.ripples {
		p.Ripples = append(p.Ripples, a.t-born)
	}
	return p
}

func (a *Avatar) between(lo, hi float64) float64 { return lo + (hi-lo)*a.chance.Float64() }

func clamp(v, lo, hi float64) float64 { return min(max(v, lo), hi) }
