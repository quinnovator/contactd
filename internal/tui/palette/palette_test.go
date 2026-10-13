package palette_test

import (
	"math"
	"slices"
	"testing"

	"github.com/quinnovator/contactd/internal/tui/commands"
	"github.com/quinnovator/contactd/internal/tui/palette"
)

// step is one thing done to a palette, and whether it is open afterwards.
type step struct {
	// draft is given to Set, unless dismiss is true: then esc is pressed.
	draft   string
	dismiss bool
	open    bool
}

type openCase struct {
	name  string
	steps []step
}

func openCases() []openCase {
	return []openCase{
		{"a slash opens it and a name keeps it open", []step{
			{draft: "", open: false},
			{draft: "/", open: true},
			{draft: "/s", open: true},
			{draft: "/st", open: true},
			{draft: "/ST", open: true},
			{draft: "/no-such-command", open: true},
		}},
		{"a space after the name closes it", []step{
			{draft: "/st", open: true},
			{draft: "/st ", open: false},
			{draft: "/st", open: true},
			{draft: "/statusline edit", open: false},
			{draft: "/st\t", open: false},
			{draft: "/st\u00a0", open: false},
			{draft: "/ x", open: false},
		}},
		{"a message never opens it", []step{
			{draft: "hello", open: false},
			{draft: "a/b", open: false},
			{draft: " /st", open: false},
			{draft: "\n/st", open: false},
			{draft: "//", open: false},
			{draft: "//st", open: false},
		}},
		{"a draft of several lines never opens it", []step{
			{draft: "/st\n", open: false},
			{draft: "/st\nmore", open: false},
			{draft: "/st\r", open: false},
		}},
		{"esc closes it until the draft stops being a command", []step{
			{draft: "/st", open: true},
			{dismiss: true, open: false},
			{draft: "/st", open: false},
			{draft: "/sta", open: false},
			{draft: "/", open: false},
			{draft: "/statusline ", open: false},
			{draft: "/statusline", open: false},
			{draft: "", open: false},
			{draft: "/st", open: true},
		}},
		{"a message reopens it after esc", []step{
			{draft: "/", open: true},
			{dismiss: true, open: false},
			{draft: "//", open: false},
			{draft: "/", open: true},
		}},
		{"a refused draft reopens it after esc", []step{
			{draft: "/st", open: true},
			{dismiss: true, open: false},
			{draft: "/st\nx", open: false},
			{draft: "/st", open: true},
		}},
		{"esc twice is esc once", []step{
			{draft: "/st", open: true},
			{dismiss: true, open: false},
			{dismiss: true, open: false},
			{draft: "", open: false},
			{draft: "/st", open: true},
		}},
		{"esc on a closed palette is not remembered", []step{
			{dismiss: true, open: false},
			{draft: "/st", open: true},
			{draft: "/st x", open: false},
			{dismiss: true, open: false},
			{draft: "/st", open: true},
		}},
	}
}

func TestOpenDismissReopen(t *testing.T) {
	t.Parallel()
	for _, tc := range openCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reg := mockRegistry()
			m := palette.New()
			for i, s := range tc.steps {
				if s.dismiss {
					m.Dismiss()
				} else if got := m.Set(reg, s.draft); got != s.open {
					t.Fatalf("step %d: Set(%q) = %v, want %v", i, s.draft, got, s.open)
				}
				if m.Open() != s.open {
					t.Fatalf("step %d (%+v): Open() = %v", i, s, m.Open())
				}
				// A closed palette asks for no rows, draws none and offers
				// no command; an open one asks for some.
				_, chosen := m.Selected()
				want := m.Want(100)
				lines := m.Lines(mockLook().theme(), 100, 12)
				if !s.open && (want != 0 || lines != nil || chosen) {
					t.Fatalf("step %d (%+v): closed, but Want = %d, %d lines, selected %v", i, s, want, len(lines), chosen)
				}
				if s.open && (want == 0 || len(lines) != 12) {
					t.Fatalf("step %d (%+v): open, but Want = %d and %d lines of 12", i, s, want, len(lines))
				}
			}
		})
	}
}

func TestNoRegistry(t *testing.T) {
	t.Parallel()
	m := palette.New()
	if m.Set(nil, "/st") || m.Open() || m.Want(100) != 0 {
		t.Errorf("a palette with no registry is open")
	}
	m.Move(1)
	m.Dismiss()
	if _, ok := m.Selected(); ok {
		t.Errorf("a palette with no registry has a selection")
	}
	// It was never open, so esc was not remembered.
	if !m.Set(mockRegistry(), "/st") {
		t.Errorf("the palette stays closed once it has a registry")
	}
	if m.Set(nil, "/st") || m.Open() {
		t.Errorf("the palette stays open when its registry is taken away")
	}
}

func TestZeroValue(t *testing.T) {
	t.Parallel()
	var m palette.Model
	if m.Open() || m.Want(100) != 0 || m.Lines(mockLook().theme(), 100, 5) != nil {
		t.Errorf("the zero Model is not a closed palette")
	}
	if !m.Set(mockRegistry(), "/") {
		t.Errorf("the zero Model does not open")
	}
}

func TestMatches(t *testing.T) {
	t.Parallel()
	tests := []struct {
		draft string
		want  []string
		// chosen is the command a new list starts on.
		chosen string
	}{
		{"/st", []string{"statusline", "status", "stop", "standup", "stats"}, "statusline"},
		{"/ST", []string{"statusline", "status", "stop", "standup", "stats"}, "statusline"},
		{"/statusl", []string{"statusline"}, "statusline"},
		{"/tus", []string{"statusline", "status"}, "statusline"},
		// The word is a whole name: that command is chosen, though another
		// that starts with the word is listed before it.
		{"/status", []string{"statusline", "status"}, "status"},
		{"/STATUS", []string{"statusline", "status"}, "status"},
		{"/statusline", []string{"statusline"}, "statusline"},
		{"/stats", []string{"stats"}, "stats"},
		{"/zzz", nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.draft, func(t *testing.T) {
			t.Parallel()
			m := opened(t, mockRegistry(), tc.draft)
			if got := specNames(m.Matches()); !slices.Equal(got, tc.want) {
				t.Errorf("matches = %v, want %v", got, tc.want)
			}
			got, ok := m.Selected()
			if ok != (len(tc.want) > 0) {
				t.Fatalf("Selected() ok = %v with matches %v", ok, tc.want)
			}
			if ok && got.Name != tc.chosen {
				t.Errorf("Selected() = %q, want %q", got.Name, tc.chosen)
			}
		})
	}

	t.Run("a slash alone lists every command", func(t *testing.T) {
		t.Parallel()
		reg := mockRegistry()
		m := opened(t, reg, "/")
		if got, want := specNames(m.Matches()), specNames(reg.Match("")); !slices.Equal(got, want) || len(got) != reg.Len() {
			t.Errorf("matches = %v, want all %d: %v", got, reg.Len(), want)
		}
	})
}

func TestSelectedIsWholeSpec(t *testing.T) {
	t.Parallel()
	reg := mockRegistry()
	m := opened(t, reg, "/op")
	got, ok := m.Selected()
	want, _ := reg.Lookup("open")
	if !ok || got != want || !got.NeedsArg || got.Usage != "<contact>" {
		t.Errorf("Selected() = %+v, %v; want %+v", got, ok, want)
	}
	// What the model completes to, and what it draws after the cursor.
	if commands.Complete(got) != "/open " || commands.Ghost("/op", got) != "en  <contact>" {
		t.Errorf("complete %q, ghost %q", commands.Complete(got), commands.Ghost("/op", got))
	}
}

func TestMoveClamps(t *testing.T) {
	t.Parallel()
	// "/st" matches statusline, status, stop, standup, stats.
	tests := []struct {
		name  string
		moves []int
		want  string
	}{
		{"nothing moved", nil, "statusline"},
		{"down one", []int{1}, "status"},
		{"down two, up one", []int{1, 1, -1}, "status"},
		{"up from the first stays", []int{-1}, "statusline"},
		{"up twice from the first, then down", []int{-1, -1, 1}, "status"},
		{"down to the last", []int{4}, "stats"},
		{"down past the last stays", []int{1, 1, 1, 1, 1, 1, 1}, "stats"},
		{"past the last, then up one", []int{9, -1}, "standup"},
		{"a long way down", []int{1000}, "stats"},
		{"a long way up", []int{3, -1000}, "statusline"},
		{"the largest step there is", []int{2, math.MaxInt}, "stats"},
		{"the smallest step there is", []int{2, math.MinInt}, "statusline"},
		{"no step", []int{2, 0}, "stop"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := opened(t, mockRegistry(), "/st")
			for _, d := range tc.moves {
				m.Move(d)
			}
			if got, ok := m.Selected(); !ok || got.Name != tc.want {
				t.Errorf("after %v: selected %q (%v), want %q", tc.moves, got.Name, ok, tc.want)
			}
		})
	}

	t.Run("nothing to move among", func(t *testing.T) {
		t.Parallel()
		m := opened(t, mockRegistry(), "/zzz")
		m.Move(1)
		m.Move(-3)
		if got, ok := m.Selected(); ok {
			t.Errorf("selected %q where nothing matches", got.Name)
		}
	})

	t.Run("a closed palette does not move", func(t *testing.T) {
		t.Parallel()
		reg := mockRegistry()
		m := opened(t, reg, "/st")
		m.Set(reg, "/st x")
		m.Move(3)
		m.Set(reg, "/st")
		if got, _ := m.Selected(); got.Name != "statusline" {
			t.Errorf("selected %q after moving a closed palette", got.Name)
		}
	})
}

// sameList reports whether two filtered lists are one list: Match returns a
// new slice every time it is called, so one slice means it was not called.
func sameList(a, b []commands.Spec) bool {
	return len(a) > 0 && len(a) == len(b) && &a[0] == &b[0]
}

func TestRefilter(t *testing.T) {
	t.Parallel()

	t.Run("the same word is not filtered again", func(t *testing.T) {
		t.Parallel()
		reg := mockRegistry()
		m := opened(t, reg, "/st")
		first := m.Matches()
		m.Move(2)
		for _, draft := range []string{"/st", "/St", "/ST", "/st"} {
			m.Set(reg, draft)
			if !sameList(first, m.Matches()) {
				t.Fatalf("Set(%q) filtered again", draft)
			}
			if got, _ := m.Selected(); got.Name != "stop" {
				t.Fatalf("Set(%q) moved the selection to %q", draft, got.Name)
			}
		}
	})

	t.Run("a new word is filtered and starts at the first match", func(t *testing.T) {
		t.Parallel()
		reg := mockRegistry()
		m := opened(t, reg, "/st")
		first := m.Matches()
		m.Move(2)
		m.Set(reg, "/sta")
		if sameList(first, m.Matches()) {
			t.Fatalf("a new word was not filtered")
		}
		if got := specNames(m.Matches()); !slices.Equal(got, []string{"statusline", "status", "standup", "stats"}) {
			t.Errorf("matches = %v", got)
		}
		if got, _ := m.Selected(); got.Name != "statusline" {
			t.Errorf("selected %q after a new word, want the first match", got.Name)
		}
		// Back to the old word: filtered again, and again from the top.
		m.Move(3)
		m.Set(reg, "/st")
		if got, _ := m.Selected(); got.Name != "statusline" || len(m.Matches()) != 5 {
			t.Errorf("selected %q of %d after going back a letter", got.Name, len(m.Matches()))
		}
	})

	t.Run("closing and opening on the same word does not filter", func(t *testing.T) {
		t.Parallel()
		reg := mockRegistry()
		m := opened(t, reg, "/st")
		first := m.Matches()
		m.Move(3)
		m.Set(reg, "/st ")
		m.Set(reg, "/st")
		if !sameList(first, m.Matches()) {
			t.Errorf("filtered again on the same word")
		}
		if got, _ := m.Selected(); got.Name != "statusline" {
			t.Errorf("selected %q when opened again, want the first match", got.Name)
		}
	})

	t.Run("a dismissed palette does not filter", func(t *testing.T) {
		t.Parallel()
		reg := mockRegistry()
		m := opened(t, reg, "/st")
		first := m.Matches()
		m.Dismiss()
		for _, draft := range []string{"/sta", "/stat", "/statusline"} {
			m.Set(reg, draft)
			if !sameList(first, m.Matches()) {
				t.Fatalf("Set(%q) filtered while dismissed", draft)
			}
		}
		m.Set(reg, "")
		m.Set(reg, "/stat")
		if got := specNames(m.Matches()); !slices.Equal(got, []string{"statusline", "status", "stats"}) {
			t.Errorf("matches = %v once opened again", got)
		}
	})

	t.Run("a changed registry is filtered and keeps the selection", func(t *testing.T) {
		t.Parallel()
		reg := mockRegistry()
		m := opened(t, reg, "/st")
		m.Move(4) // stats, the last
		first := m.Matches()

		// Setting a mod to what it already has changes nothing.
		reg.SetMod("standup", []commands.Spec{{Name: "standup", Summary: "summarize yesterday for the team"}})
		m.Set(reg, "/st")
		if !sameList(first, m.Matches()) {
			t.Fatalf("filtered again though the registry did not change")
		}

		reg.SetMod("clock", []commands.Spec{{Name: "start", Summary: "start a timer"}})
		m.Set(reg, "/st")
		if got := specNames(m.Matches()); !slices.Equal(got, []string{"statusline", "status", "stop", "standup", "start", "stats"}) {
			t.Fatalf("matches = %v after a mod added a command", got)
		}
		if got, _ := m.Selected(); got.Name != "stats" {
			t.Errorf("selected %q after the registry grew, want it kept on stats", got.Name)
		}

		// The chosen command goes away: back to the first.
		reg.RemoveMod("cache")
		m.Set(reg, "/st")
		if got, _ := m.Selected(); got.Name != "statusline" || len(m.Matches()) != 5 {
			t.Errorf("selected %q of %d after its mod was removed", got.Name, len(m.Matches()))
		}
	})

	t.Run("a registry that changed while closed starts at the first match", func(t *testing.T) {
		t.Parallel()
		reg := mockRegistry()
		m := opened(t, reg, "/st")
		m.Move(4)
		m.Set(reg, "/st ")
		reg.SetMod("clock", []commands.Spec{{Name: "start"}})
		m.Set(reg, "/st")
		if got, _ := m.Selected(); got.Name != "statusline" || len(m.Matches()) != 6 {
			t.Errorf("selected %q of %d", got.Name, len(m.Matches()))
		}
	})

	t.Run("a whole name is chosen again when the palette opens again", func(t *testing.T) {
		t.Parallel()
		reg := mockRegistry()
		m := opened(t, reg, "/status")
		first := m.Matches()
		m.Move(-1)
		if got, _ := m.Selected(); got.Name != "statusline" {
			t.Fatalf("selected %q after moving up, want statusline", got.Name)
		}
		// The same word again leaves the selection where it was moved to.
		m.Set(reg, "/Status")
		if got, _ := m.Selected(); got.Name != "statusline" {
			t.Errorf("selected %q after the same word, want statusline still", got.Name)
		}
		m.Set(reg, "/status ")
		m.Set(reg, "/status")
		if got, _ := m.Selected(); got.Name != "status" || !sameList(first, m.Matches()) {
			t.Errorf("selected %q when opened again, want status and the same list", got.Name)
		}
		// A longer word and back: filtered again, and on the whole name again.
		m.Move(-1)
		m.Set(reg, "/statusl")
		m.Set(reg, "/status")
		if got, _ := m.Selected(); got.Name != "status" {
			t.Errorf("selected %q after going back a letter, want status", got.Name)
		}
		// The chosen command goes away while the palette is open: the whole
		// name is chosen, not the first match.
		reg.SetMod("clock", []commands.Spec{{Name: "status-of-clock"}})
		m.Set(reg, "/status")
		m.Move(1)
		if got, _ := m.Selected(); got.Name != "status-of-clock" {
			t.Fatalf("selected %q, want the mod's command", got.Name)
		}
		reg.RemoveMod("clock")
		m.Set(reg, "/status")
		if got, _ := m.Selected(); got.Name != "status" {
			t.Errorf("selected %q after the chosen command went away, want status", got.Name)
		}
	})

	t.Run("a new first match while closed is the one chosen", func(t *testing.T) {
		t.Parallel()
		// "/lo" matches log first. A command that sorts before it arrives
		// while the palette is closed: the first match is chosen, not the
		// command that was first before.
		reg := mockRegistry()
		m := opened(t, reg, "/lo")
		if got, _ := m.Selected(); got.Name != "log" {
			t.Fatalf("selected %q, want log", got.Name)
		}
		m.Set(reg, "/lo ")
		reg.SetMod("door", []commands.Spec{{Name: "lock"}})
		m.Set(reg, "/lo")
		if got, _ := m.Selected(); got.Name != "lock" {
			t.Errorf("selected %q, want the new first match lock", got.Name)
		}
	})

	t.Run("another registry is filtered", func(t *testing.T) {
		t.Parallel()
		// Both registries are at revision 0: the palette must not take one
		// for the other.
		a, b := commands.NewRegistry(commands.Core()), commands.NewRegistry(commands.Core()[:3])
		if a.Rev() != b.Rev() {
			t.Fatalf("the registries differ in revision: %d, %d", a.Rev(), b.Rev())
		}
		m := opened(t, a, "/")
		m.Move(5)
		m.Set(b, "/")
		if got := specNames(m.Matches()); !slices.Equal(got, []string{"statusline", "status", "stop"}) {
			t.Errorf("matches = %v from the second registry", got)
		}
		if got, _ := m.Selected(); got.Name != "statusline" {
			t.Errorf("selected %q in the second registry", got.Name)
		}
	})
}

func TestWant(t *testing.T) {
	t.Parallel()
	tests := []struct {
		draft string
		width int
		want  int
	}{
		// Five matches; from 40 columns the box, its title and the keys.
		{"/st", 100, 9},
		{"/st", 60, 9},
		{"/st", 40, 9},
		{"/st", 39, 5},
		{"/st", 30, 5},
		{"/st", 1, 5},
		{"/st", 0, 5},
		// Never more than eight commands at once.
		{"/", 100, 12},
		{"/", 39, 8},
		{"/statusl", 100, 5},
		{"/statusl", 39, 1},
		// Nothing matches: one row says so.
		{"/zzz", 100, 5},
		{"/zzz", 39, 1},
	}
	for _, tc := range tests {
		m := opened(t, mockRegistry(), tc.draft)
		if got := m.Want(tc.width); got != tc.want {
			t.Errorf("%q at %d columns: Want = %d, want %d", tc.draft, tc.width, got, tc.want)
		}
	}
}
