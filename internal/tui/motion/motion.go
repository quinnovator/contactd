// Package motion holds the timing primitives every animation is built from: a
// fixed-step clock, damped springs, and a little organic noise.
//
// Nothing here is keyframed. Each animated quantity is a spring chasing a
// target, so motion can be redirected at any instant without a visible seam.
package motion

import (
	"math"
	"time"

	"github.com/charmbracelet/harmonica"
)

// The simulation advances in fixed steps, whatever rate frames arrive at.
const (
	FPS = 60
	Dt  = 1.0 / FPS

	step   = time.Second / FPS
	maxLag = 250 * time.Millisecond // after a stall, drop time rather than fast-forward
)

// Clock turns wall-clock frame times into whole simulation steps, carrying
// the remainder forward so the simulation never drifts from real time.
type Clock struct {
	last time.Time
	debt time.Duration
}

// Tick reports how many steps are due at time now.
func (c *Clock) Tick(now time.Time) int {
	if !c.last.IsZero() {
		c.debt += min(now.Sub(c.last), maxLag)
	}
	c.last = now
	steps := int(c.debt / step)
	c.debt -= time.Duration(steps) * step
	return steps
}

// Spring is a damped harmonic oscillator pulling Pos toward Target.
type Spring struct {
	Pos, Vel, Target float64
	sim              harmonica.Spring
}

// NewSpring returns a spring at rest at zero. Frequency is its stiffness in
// radians per second; damping is the damping ratio, where 1 settles as fast
// as possible without overshoot and smaller values bounce.
func NewSpring(frequency, damping float64) Spring {
	return Spring{sim: harmonica.NewSpring(Dt, frequency, damping)}
}

// Step advances the spring by Dt.
func (s *Spring) Step() { s.Pos, s.Vel = s.sim.Update(s.Pos, s.Vel, s.Target) }

// Kick adds an impulse, leaving the spring to swing out and settle back.
func (s *Spring) Kick(impulse float64) { s.Vel += impulse }

// Set moves the spring to v and brings it to rest there.
func (s *Spring) Set(v float64) { s.Pos, s.Vel, s.Target = v, 0, v }

// Wander returns smooth, aperiodic drift in [-1, 1]: three sines whose rates
// share no common period. Different seeds give independent channels.
func Wander(t, seed float64) float64 {
	return (math.Sin(t+seed) + 0.6*math.Sin(1.618*t+2.7*seed) + 0.3*math.Sin(2.879*t+5.1*seed)) / 1.9
}

// Clamp01 limits v to [0, 1].
func Clamp01(v float64) float64 { return min(max(v, 0), 1) }

// Smooth eases t from 0 to 1 with zero slope at both ends.
func Smooth(t float64) float64 {
	t = Clamp01(t)
	return t * t * (3 - 2*t)
}
