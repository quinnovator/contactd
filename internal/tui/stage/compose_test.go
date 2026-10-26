package stage

import (
	"strconv"
	"strings"
	"testing"

	"github.com/quinnovator/contactd/internal/tui/canvas"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
	"github.com/quinnovator/contactd/internal/tui/theme"
)

func TestContentWidthChangesTheArrangement(t *testing.T) {
	t.Parallel()
	for _, preferred := range []int{30, 100} {
		t.Run(strconv.Itoa(preferred), func(t *testing.T) {
			t.Parallel()
			s := newStage(t, 120, 40, Options{})
			ex := box("content", 0, Auto, "#", preferred, 8)
			ex.MinCols = preferred // this content cannot wrap meaningfully
			s.Perform(Turn{ID: 1, Text: "Here is the result.", Over: true, Shows: []Exhibit{ex}}, true)
			s.View()
			c := s.cards[0]
			if !c.on || c.w != preferred || c.h != 8 {
				t.Fatalf("content should fit without loss: %+v", c.box())
			}
			vertical := c.y+c.h <= s.plan.block.y || s.plan.block.y+s.plan.block.h <= c.y
			if vertical != (preferred == 100) {
				t.Fatalf("width %d did not select the appropriate orientation: card %+v, speaker %+v", preferred, c.box(), s.plan.block)
			}
			assertSeparated(t, s)
		})
	}
}

func TestAllAnchorsCanMixWithAutomaticContent(t *testing.T) {
	t.Parallel()
	for anchor := TopLeft; anchor <= BottomRight; anchor++ {
		s := newStage(t, 160, 48, Options{})
		s.Perform(Turn{ID: 1, Text: "Compare these.", Over: true, Shows: []Exhibit{
			box("anchored", 0, anchor, "#", 30, 8),
			box("table", 0, Auto, "@", 42, 6),
			box("code", 0, Auto, "%", 32, 10),
		}}, true)
		s.View()
		assertSeparated(t, s)
		for _, c := range s.cards {
			if !c.on {
				t.Fatalf("anchor %d: an exhibit disappeared despite ample space", anchor)
			}
		}
	}
}

func TestPriorityAndOverflowAreExplicit(t *testing.T) {
	t.Parallel()
	s := newStage(t, 60, 20, Options{})
	exhibits := make([]Exhibit, 0, 24)
	for i := range 24 {
		ex := box(strconv.Itoa(i), 0, Auto, "#", 44, 12)
		if i == 0 {
			ex.Priority = 3
		}
		exhibits = append(exhibits, ex)
	}
	s.Perform(Turn{ID: 1, Text: "Inspect the important result.", Over: true, Shows: exhibits}, true)
	rows := plain(s)
	if !s.cards[0].on {
		t.Fatal("the high priority artifact was dropped")
	}
	if !strings.Contains(rows[len(rows)-1], "hidden") || !strings.Contains(rows[len(rows)-1], "ctrl+o") {
		t.Fatalf("missing overflow and inspection hint: %q", rows[len(rows)-1])
	}
	assertSeparated(t, s)
}

func TestLayoutBoundsAndSpacingAcrossResizes(t *testing.T) {
	t.Parallel()
	s := newStage(t, 160, 48, Options{})
	s.Perform(Turn{ID: 1, Text: strings.Repeat("Some words. ", 12), Over: true, Shows: []Exhibit{
		box("a", 0, Left, "@", 44, 10),
		box("b", 0, Auto, "#", 62, 8),
		box("c", 0, BottomRight, "%", 24, 16),
		box("d", 0, Auto, "+", 24, 4),
	}}, true)
	for _, size := range [][2]int{{160, 48}, {80, 30}, {32, 16}, {12, 8}, {1, 1}, {1, 0}, {240, 60}, {160, 48}} {
		s.Resize(size[0], size[1])
		s.View()
		assertSeparated(t, s)
		for _, c := range s.cards {
			if c.on && !contains(rect{w: size[0], h: size[1]}, c.box()) {
				t.Fatalf("%v: out of bounds: %+v", size, c.box())
			}
		}
	}
}

func TestMeasurementDoesNotRenderCandidates(t *testing.T) {
	t.Parallel()
	s := newStage(t, 160, 48, Options{})
	draws := 0
	ex := box("a", 0, Auto, "#", 48, 10)
	draw := ex.Draw
	ex.Draw = func(w, h int) []string { draws++; return draw(w, h) }
	s.Perform(Turn{ID: 1, Text: "Look.", Over: true, Shows: []Exhibit{ex}}, true)
	s.View()
	if draws != 1 {
		t.Fatalf("rendered %d candidates, want one final rendering", draws)
	}
	before := s.cards[0].box()
	for range 100 {
		s.Perform(Turn{ID: 1, Text: "Look.", Over: true, Shows: []Exhibit{ex}}, true)
		s.View()
	}
	if draws != 1 || before != s.cards[0].box() {
		t.Fatal("unchanged content was redrawn or moved")
	}
}

func TestImageFitsRatherThanLosingRows(t *testing.T) {
	t.Parallel()
	s := newStage(t, 100, 28, Options{})
	ex := box("portrait", 0, Auto, "#", 24, 24)
	ex.PreferredCols, ex.MinCols, ex.Scalable = 24, 8, true
	ex.Measure = func(w, h int) Size { side := min(w, h, 24); return Size{side, side} }
	ex.Draw = func(w, h int) []string {
		size := ex.Measure(w, h)
		return strings.Split(strings.TrimSuffix(strings.Repeat(strings.Repeat("#", size.Cols)+"\n", size.Rows), "\n"), "\n")
	}
	s.Perform(Turn{ID: 1, Text: "The whole portrait.", Over: true, Shows: []Exhibit{ex}}, true)
	s.View()
	if c := s.cards[0]; !c.on || c.w != c.h || c.w < 8 {
		t.Fatalf("image was cropped instead of scaled: %+v", c.box())
	}
	if s.plan.overflow != "" {
		t.Fatal(s.plan.overflow)
	}
}

func TestDenseGridSharesHeightWithoutClipping(t *testing.T) {
	t.Parallel()
	s := newStage(t, 160, 48, Options{})
	exhibits := make([]Exhibit, 0, 8)
	for i := range 8 {
		exhibits = append(exhibits, box(strconv.Itoa(i), 0, Auto, "#", 23, i+3))
	}
	s.Perform(Turn{ID: 1, Text: "Several results need to share the stage.", Over: true, Shows: exhibits}, true)
	s.View()
	assertSeparated(t, s)
	for i, c := range s.cards {
		if !c.on || c.h != i+3 {
			t.Fatalf("item %d lost content despite room in the grid: %+v", i, c.box())
		}
	}
	if s.plan.overflow != "" {
		t.Fatal(s.plan.overflow)
	}
}

func TestCustomRendererOverflowRemainsVisibleAfterRelayout(t *testing.T) {
	t.Parallel()
	s := newStage(t, 100, 28, Options{})
	ex := box("custom", 0, Auto, "#", 30, 4)
	// A mod can draw more rows than the plain source measurement predicts.
	ex.Draw = func(_, _ int) []string { return strings.Split(strings.Repeat("custom output\n", 50), "\n") }
	s.Perform(Turn{ID: 1, Text: "Result.", Over: true, Shows: []Exhibit{ex}}, true)
	for range 2 {
		s.stale = true
		view := strings.Join(plain(s), "\n")
		if !strings.Contains(view, "1 shortened") || !strings.Contains(view, "more (ctrl+o)") {
			t.Fatalf("custom rendering was silently clipped:\n%s", view)
		}
		assertSeparated(t, s)
	}
}

func BenchmarkContentLayout(b *testing.B) {
	s := New(Options{Theme: theme.New(termcaps.Caps{}, theme.Options{}), Paper: canvas.Hex(0x121214)})
	s.Resize(180, 50)
	exhibits := make([]Exhibit, 0, 8)
	for i := range 8 {
		exhibits = append(exhibits, box(strconv.Itoa(i), 0, Auto, "#", 24+i*4, 5+i))
	}
	s.Perform(Turn{ID: 1, Text: "Results.", Over: true, Shows: exhibits}, true)
	b.ReportAllocs()
	for b.Loop() {
		s.stale = true
		s.arrange()
	}
}
