package motion

import (
	"math"
	"testing"
	"time"
)

func TestClockTracksRealTime(t *testing.T) {
	t.Parallel()
	var c Clock
	now := time.Unix(0, 0)
	if c.Tick(now) != 0 {
		t.Error("the first tick has nothing to measure from")
	}
	// Frames arriving at an awkward rate still add up to FPS steps a second.
	steps := 0
	for range 144 {
		now = now.Add(time.Second / 144)
		steps += c.Tick(now)
	}
	if steps < FPS-1 || steps > FPS {
		t.Errorf("%d steps in one second, want %d", steps, FPS)
	}
	if got := c.Tick(now.Add(time.Hour)); got > FPS/2 {
		t.Errorf("a long stall produced %d steps; it should be dropped, not replayed", got)
	}
}

func TestSpringSettlesOnItsTarget(t *testing.T) {
	t.Parallel()
	s := NewSpring(12, 0.4)
	s.Target = 1
	s.Kick(5)
	peak := 0.0
	for range 3 * FPS {
		s.Step()
		peak = max(peak, s.Pos)
	}
	if peak <= 1 {
		t.Error("an underdamped spring should overshoot")
	}
	if math.Abs(s.Pos-1) > 1e-3 || math.Abs(s.Vel) > 1e-3 {
		t.Errorf("spring did not settle: pos %v, vel %v", s.Pos, s.Vel)
	}
}
