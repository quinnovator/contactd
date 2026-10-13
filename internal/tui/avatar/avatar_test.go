package avatar

import (
	"math"
	"testing"

	"github.com/quinnovator/contactd/internal/tui/canvas"
	"github.com/quinnovator/contactd/internal/tui/motion"
	"github.com/quinnovator/contactd/internal/tui/speech"
)

// run steps the avatar for the given number of seconds, calling watch (if
// non-nil) on every pose along the way.
func run(a *Avatar, seconds float64, mood Mood, mouth speech.Viseme, watch func(Pose)) {
	for range int(seconds * motion.FPS) {
		a.Step(mood, mouth, 0)
		if watch != nil {
			watch(a.Pose())
		}
	}
}

func TestBlinks(t *testing.T) {
	t.Parallel()
	a := New(Palettes()[0], 1)
	shut, open := false, false
	run(a, 10, Idle, speech.Rest, func(p Pose) {
		shut = shut || p.Lids < 0.15
		open = open || p.Lids > 0.95
	})
	if !shut || !open {
		t.Errorf("in ten seconds the eyes should both close and open (shut=%v, open=%v)", shut, open)
	}
}

func TestMouthFollowsTheVoice(t *testing.T) {
	t.Parallel()
	a := New(Palettes()[0], 1)
	run(a, 0.3, Speaking, speech.VisemeOf('a'), nil)
	if p := a.Pose(); p.Jaw < 0.9 || p.Energy < 0.5 {
		t.Errorf("held on a wide vowel: jaw %.2f, energy %.2f", p.Jaw, p.Energy)
	}
	run(a, 0.3, Speaking, speech.Rest, nil)
	if p := a.Pose(); p.Jaw > 0.05 {
		t.Errorf("at rest the jaw should close, got %.2f", p.Jaw)
	}
}

func TestMoodsShowOnTheFace(t *testing.T) {
	t.Parallel()
	a := New(Palettes()[0], 1)
	run(a, 3, Thinking, speech.Rest, nil)
	if p := a.Pose(); p.Focus < 0.9 || p.GazeY > -0.3 {
		t.Errorf("thinking should look up, lost in thought: focus %.2f, gaze y %.2f", p.Focus, p.GazeY)
	}
	run(a, 3, Content, speech.Rest, nil)
	if p := a.Pose(); p.Smile < 0.9 || p.Focus > 0.1 {
		t.Errorf("content should smile: smile %.2f, focus %.2f", p.Smile, p.Focus)
	}
}

func TestLookAtHoldsThenLetsGo(t *testing.T) {
	t.Parallel()
	a := New(Palettes()[0], 1)
	a.LookAt(1, 1)
	run(a, 1, Idle, speech.Rest, nil)
	if p := a.Pose(); p.GazeX < 0.9 || p.GazeY < 0.9 {
		t.Errorf("gaze should reach the pointer, got (%.2f, %.2f)", p.GazeX, p.GazeY)
	}
	run(a, 4, Idle, speech.Rest, nil)
	if p := a.Pose(); p.GazeX > 0.3 || p.GazeY > 0.3 {
		t.Errorf("gaze should drift back once the pointer is still, got (%.2f, %.2f)", p.GazeX, p.GazeY)
	}
}

// However it is provoked, the avatar must stay in one piece.
func TestStaysComposed(t *testing.T) {
	t.Parallel()
	a := New(Palettes()[0], 1)
	every := speech.Word | speech.Pause | speech.Stop | speech.Ask | speech.Exclaim
	for i := range 20 * motion.FPS {
		var beats speech.Beat
		if i%7 == 0 {
			beats = every
		}
		a.Step(Speaking, speech.VisemeOf(rune("aeioum "[i%7])), beats)
		p := a.Pose()
		for name, v := range map[string]float64{"tilt": p.Tilt, "bob": p.Bob, "squash": p.Squash, "awe": p.Awe} {
			if math.IsNaN(v) || math.Abs(v) > 0.8 {
				t.Fatalf("step %d: %s = %v", i, name, v)
			}
		}
		if len(p.Ripples) > 8 {
			t.Fatalf("step %d: %d ripples alive; old ones should expire", i, len(p.Ripples))
		}
	}
}

// The canvas has no frame: even at its most exuberant, the avatar's light has
// died away by the time it reaches the outermost pixels.
func TestPaintFadesOutBeforeTheEdge(t *testing.T) {
	t.Parallel()
	p := Pose{Lids: 1, Jaw: 1, Energy: 1, Focus: 1, Smile: 1, Squash: 0.15, Ripples: []float64{0.2, 0.5, 0.8}, Palette: Palettes()[0]}
	c := canvas.New(60, 24, canvas.Octants)
	s := newScene(p, c.Pixel())
	edge := 1 - c.Pixel()/2 // the centre of the last row of pixels
	for x := -1.2; x <= 1.2; x += 0.01 {
		for _, y := range []float64{-edge, edge} {
			if a := s.shade(x, y).A; a > 1.0/255 {
				t.Fatalf("coverage %.4f at (%.2f, %.2f), on the edge of the canvas", a, x, y)
			}
		}
	}
	if a := s.shade(0, 0).A; a != 1 {
		t.Errorf("the middle of the canvas should be solid, got coverage %.2f", a)
	}
}

func BenchmarkFrame(b *testing.B) {
	p := Pose{Time: 1, Lids: 1, Smile: 0.3, Jaw: 0.6, Energy: 0.6, Focus: 1, Ripples: []float64{0.1, 0.4}, Palette: Palettes()[0]}
	c := canvas.New(78, 31, canvas.Octants)
	for b.Loop() {
		p.Paint(c)
		_ = c.Render(canvas.Color{})
	}
}
