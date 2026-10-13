package palette_test

import (
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"github.com/quinnovator/contactd/internal/tui/commands"
	"github.com/quinnovator/contactd/internal/tui/palette"
	"github.com/quinnovator/contactd/internal/tui/theme"
)

// plainModel is what a palette must do, written the plain way: it reads the
// registry again at every step and remembers the chosen command by name.
type plainModel struct {
	open, dismissed bool
	reg             *commands.Registry
	word, chosen    string
	matches         []commands.Spec
}

func (p *plainModel) set(reg *commands.Registry, draft string) {
	line := commands.Parse(draft)
	command := line.Kind == commands.LineCommand
	if !command {
		p.dismissed = false
	}
	if reg == nil || !command || p.dismissed || strings.IndexFunc(draft, unicode.IsSpace) >= 0 {
		p.open = false
		return
	}
	matches := reg.Match(line.Name)
	// The chosen command stays chosen only on a palette that was open on this
	// word and this registry, and only while it still matches.
	keep := p.open && reg == p.reg && line.Name == p.word &&
		slices.ContainsFunc(matches, func(s commands.Spec) bool { return s.Name == p.chosen })
	if !keep {
		// A new list starts on the command the word names in full, or on the
		// first match.
		p.chosen = ""
		if len(matches) > 0 {
			p.chosen = matches[0].Name
		}
		if slices.ContainsFunc(matches, func(s commands.Spec) bool { return s.Name == line.Name }) {
			p.chosen = line.Name
		}
	}
	p.open, p.reg, p.word, p.matches = true, reg, line.Name, matches
}

func (p *plainModel) dismiss() {
	if p.open {
		p.open, p.dismissed = false, true
	}
}

// at returns where the chosen command is among the matches.
func (p *plainModel) at() int {
	return slices.IndexFunc(p.matches, func(s commands.Spec) bool { return s.Name == p.chosen })
}

func (p *plainModel) move(delta int) {
	if !p.open || len(p.matches) == 0 {
		return
	}
	i := p.at()
	for range min(max(delta, -delta), len(p.matches)) {
		if delta > 0 && i < len(p.matches)-1 {
			i++
		}
		if delta < 0 && i > 0 {
			i--
		}
	}
	p.chosen = p.matches[i].Name
}

// script plays random steps on a palette and on the plain model, and compares
// the two after each.
type script struct {
	t     *testing.T
	rng   *rand.Rand
	th    *theme.Theme
	regs  []*commands.Registry
	reg   *commands.Registry
	draft string
	m     *palette.Model
	want  plainModel
}

// drafts are what the script types. Most of them are commands.
func drafts() []string {
	return []string{
		"", "/", "/s", "/st", "/ST", "/St", "/sta", "/stat", "/status", "/STATUS", "/stats", "/statusline", "/o", "/zzz", "/tus", "/l", "/lo",
		"/st ", "/st x", "/statusline edit", "/st\t", "//", "//st", "hello", " /st", "/st\nmore", "/st\r",
	}
}

func (s *script) step() {
	switch s.rng.IntN(10) {
	case 0, 1, 2:
		s.draft = drafts()[s.rng.IntN(len(drafts()))]
	case 3:
		s.m.Dismiss()
		s.want.dismiss()
		return
	case 4, 5:
		delta := []int{-100, -9, -3, -1, -1, 0, 1, 1, 2, 4, 9, 100}[s.rng.IntN(12)]
		s.m.Move(delta)
		s.want.move(delta)
		return
	case 6:
		// A mod comes, changes or goes. The model is told the draft again,
		// as the chat's model must after any change to the registry.
		if s.reg == nil {
			return
		}
		switch s.rng.IntN(4) {
		case 0:
			s.reg.SetMod("clock", []commands.Spec{{Name: "start", Summary: "start a timer"}, {Name: "lap"}})
		case 1:
			s.reg.RemoveMod("clock")
		case 2:
			s.reg.RemoveMod("cache")
		default:
			s.reg.SetMod("cache", []commands.Spec{{Name: "stats", Summary: "token use and cache share by day"}})
		}
	case 7:
		s.reg = s.regs[s.rng.IntN(len(s.regs))]
	default:
		s.draw()
		return
	}
	got := s.m.Set(s.reg, s.draft)
	s.want.set(s.reg, s.draft)
	if got != s.want.open {
		s.t.Fatalf("Set(%q) = %v, want %v", s.draft, got, s.want.open)
	}
}

// check compares what the palette says with the plain model.
func (s *script) check() {
	if s.m.Open() != s.want.open {
		s.t.Fatalf("draft %q: Open() = %v, want %v", s.draft, s.m.Open(), s.want.open)
	}
	got, ok := s.m.Selected()
	if !s.want.open || len(s.want.matches) == 0 {
		if ok {
			s.t.Fatalf("draft %q: selected %q, want none", s.draft, got.Name)
		}
		return
	}
	if !ok || got != s.want.matches[s.want.at()] {
		s.t.Fatalf("draft %q: selected %q (%v), want %q", s.draft, got.Name, ok, s.want.chosen)
	}
}

// draw asks for the rows at a random size and checks what must hold of them.
func (s *script) draw() {
	width := []int{100, 80, 60, 59, 40, 39, 30}[s.rng.IntN(7)]
	rows := 1 + s.rng.IntN(14)
	lines := s.m.Lines(s.th, width, rows)
	if !s.want.open {
		if lines != nil || s.m.Want(width) != 0 {
			s.t.Fatalf("draft %q: a closed palette draws %d lines and wants %d rows", s.draft, len(lines), s.m.Want(width))
		}
		return
	}
	listed := min(max(len(s.want.matches), 1), 8)
	want := listed
	if width >= 40 {
		want += 4
	}
	if got := s.m.Want(width); got != want {
		s.t.Fatalf("draft %q at %d columns: Want = %d, want %d", s.draft, width, got, want)
	}
	if len(lines) != rows {
		s.t.Fatalf("draft %q: %d lines for %d rows", s.draft, len(lines), rows)
	}
	for i, line := range lines {
		if got := s.th.Width(line); got != width {
			s.t.Fatalf("draft %q at %dx%d: line %d is %d cells: %q", s.draft, width, rows, i, got, line)
		}
	}
	text := strings.Join(plain(lines), "\n")
	if len(s.want.matches) == 0 {
		if !strings.Contains(text, "no command matches") {
			s.t.Fatalf("draft %q at %dx%d: nothing matches and nothing says so:\n%s", s.draft, width, rows, text)
		}
		return
	}
	// The rows are a run of the matches, as many as there is room for, and
	// the chosen command is one of them and the only one pointed at.
	if width >= 40 && rows-4 >= min(listed, 3) {
		listed = min(listed, rows-4)
	} else {
		listed = min(listed, rows)
	}
	all := specNames(s.want.matches)
	got := names(lines)
	if len(got) == 0 {
		s.t.Fatalf("draft %q at %dx%d: no command is drawn:\n%s", s.draft, width, rows, text)
	}
	first := slices.Index(all, got[0])
	if len(got) != listed || first < 0 || first+listed > len(all) || !slices.Equal(got, all[first:first+listed]) {
		s.t.Fatalf("draft %q at %dx%d: rows %v, want %d in a row of %v", s.draft, width, rows, got, listed, all)
	}
	if on := pointed(s.th, lines); !slices.Equal(on, []string{s.want.chosen}) {
		s.t.Fatalf("draft %q at %dx%d: the pointer is on %v, want %s:\n%s", s.draft, width, rows, on, s.want.chosen, text)
	}
}

// TestAgainstPlainModel plays random scripts of every operation: drafts, esc,
// moves, mods that come and go, another registry, and draws at any size.
func TestAgainstPlainModel(t *testing.T) {
	t.Parallel()
	for seed := range uint64(200) {
		t.Run(strconv.FormatUint(seed, 10), func(t *testing.T) {
			t.Parallel()
			s := &script{
				t:    t,
				rng:  rand.New(rand.NewPCG(seed, 5)),
				th:   mockLook().theme(),
				regs: []*commands.Registry{mockRegistry(), commands.NewRegistry(commands.Core()[:6]), nil},
				m:    palette.New(),
			}
			s.reg = s.regs[0]
			for range 400 {
				s.step()
				s.check()
			}
		})
	}
}
