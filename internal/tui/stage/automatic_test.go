package stage

import (
	"strconv"
	"strings"
	"testing"
)

func TestAutomaticExhibitsShareAndCenterTheStage(t *testing.T) {
	t.Parallel()
	for _, width := range []int{90, 100, 160, 240} {
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			t.Parallel()
			s := newStage(t, width, 42, Options{})
			// A table that fills whatever width it is offered used to take
			// nearly the whole stage and squeeze the speaker underneath it.
			s.Perform(Turn{ID: 1, Text: "API is degraded.", Over: true, Shows: []Exhibit{
				box("status", 0, Auto, "#", 1000, 6),
			}}, true)
			s.View()
			c, b := s.cards[0], s.plan.block
			if !c.on || b.w < 36 || s.plan.text.w < 32 || s.plan.face.h < 12 {
				t.Fatalf("table squeezed the speaker: card %+v, plan %+v", c.box(), s.plan)
			}
			assertSeparated(t, s)
			left, right := min(b.x, c.x), width-max(c.x+c.w, b.x+b.w)
			if left-right < -1 || left-right > 1 {
				t.Fatalf("composition is not centered: margins %d, %d", left, right)
			}
			if s.plan.text.w > maxTextCols {
				t.Fatal("extra width should become margins, not longer lines")
			}
		})
	}
}

func TestAutomaticExhibitsStackWithoutCoveringEachOther(t *testing.T) {
	t.Parallel()
	s := newStage(t, 140, 40, Options{})
	s.Perform(Turn{ID: 1, Text: "Compare the table and the code.", Over: true, Shows: []Exhibit{
		box("table", 0, Auto, "#", 52, 6),
		box("code", 0, Auto, "@", 38, 10),
	}}, true)
	for _, size := range [][2]int{{140, 40}, {60, 50}, {140, 40}} {
		s.Resize(size[0], size[1])
		rows := plain(s)
		a, b := s.cards[0], s.cards[1]
		if !a.on || !b.on || padded(a.box()).meets(b.box()) {
			t.Fatalf("%v: exhibits overlap or disappear: %+v, %+v", size, a.box(), b.box())
		}
		if !strings.Contains(strings.Join(rows, "\n"), "#") || !strings.Contains(strings.Join(rows, "\n"), "@") {
			t.Fatalf("%v: both exhibits should be visible", size)
		}
	}
}

func assertSeparated(t *testing.T, s *Stage) {
	t.Helper()
	for i, c := range s.cards {
		if !c.on {
			continue
		}
		if padded(c.box()).meets(s.plan.block) {
			t.Fatalf("card %+v meets speaker %+v", c.box(), s.plan.block)
		}
		for _, other := range s.cards[i+1:] {
			if other.on && padded(c.box()).meets(other.box()) {
				t.Fatalf("cards touch: %+v, %+v", c.box(), other.box())
			}
		}
	}
}

func TestAutomaticExhibitHeightIsSharedByContent(t *testing.T) {
	t.Parallel()
	s := newStage(t, 140, 24, Options{})
	s.Perform(Turn{ID: 1, Over: true, Shows: []Exhibit{
		box("short", 0, Auto, "#", 40, 3),
		box("long", 0, Auto, "@", 40, 30),
	}}, true)
	s.View()
	a, b := s.cards[0], s.cards[1]
	if a.h != 3 || b.h <= a.h || !a.on || !b.on {
		t.Fatalf("the short exhibit should leave its spare height to the long one: %+v, %+v", a.box(), b.box())
	}
}

func TestToolStatusStaysInsideTheSpeakingBlock(t *testing.T) {
	t.Parallel()
	s := newStage(t, 100, 30, Options{})
	s.SetAround("", []string{strings.Repeat("working ", 12)})
	s.Perform(Turn{ID: 1, Over: true, Shows: []Exhibit{box("table", 0, Auto, "#", 1000, 30)}}, true)
	rows := plain(s)
	row := rows[s.plan.belowY]
	if !strings.Contains(row, "working") || !strings.Contains(row, "#") {
		t.Fatalf("tool status and table should both fit on their row: %q", row)
	}
}
