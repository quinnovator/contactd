package stage

import (
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/quinnovator/contactd/internal/tui/canvas"
	"github.com/quinnovator/contactd/internal/tui/sound"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
	"github.com/quinnovator/contactd/internal/tui/theme"
)

// newStage makes a stage of width by height cells for a terminal with every
// colour and a dark background.
func newStage(t *testing.T, width, height int, o Options) *Stage {
	t.Helper()
	o.Theme = theme.New(termcaps.Caps{
		Color: termcaps.ColorTrue, Background: termcaps.BgDark, Glyphs: termcaps.GlyphsUnicode, Width: termcaps.WidthWc,
	}, theme.Options{})
	o.Seed, o.Paper, o.Dark = 1, canvas.Hex(0x121214), true
	s := New(o)
	s.Resize(width, height)
	return s
}

// run lets the stage perform for a while, a frame at a time.
func run(s *Stage, from time.Time, d time.Duration) time.Time {
	for range int(d / (time.Second / 60)) {
		from = from.Add(time.Second / 60)
		s.Tick(from)
	}
	return from
}

// plain is the stage as drawn, without its colours.
func plain(s *Stage) []string {
	rows := s.View()
	for i, row := range rows {
		rows[i] = ansi.Strip(row)
	}
	return rows
}

// box is an exhibit that draws a block of a mark, as large as it is let.
func box(key string, at int, where Anchor, mark string, cols, rows int) Exhibit {
	return Exhibit{Key: key, At: at, Where: where, Draw: func(maxCols, maxRows int) []string {
		out := make([]string, min(rows, maxRows))
		for i := range out {
			out[i] = strings.Repeat(mark, min(cols, maxCols))
		}
		return out
	}}
}

// find is where a mark first stands on the stage: its row and its column.
func find(rows []string, mark string) (row, col int) {
	for i, r := range rows {
		if before, _, found := strings.Cut(r, mark); found {
			return i, ansi.StringWidth(before)
		}
	}
	return -1, -1
}

func TestPlace(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		words string
		want  Anchor
		ok    bool
	}{
		{"top", Top, true},
		{"Bottom", Bottom, true},
		{"left", Left, true},
		{"right", Right, true},
		{"center", Center, true},
		{"centre", Center, true},
		{"middle", Center, true},
		{"top left", TopLeft, true},
		{"top-right", TopRight, true},
		{"left top", TopLeft, true},
		{"bottom_left", BottomLeft, true},
		{"lower right", BottomRight, true},
		{"upper  left", TopLeft, true},
		{"top center", Top, true},
		{"center left", Left, true},
		{"middle right", Right, true},
		{"center center", Center, true},
		{"", Auto, false},
		{"top bottom", Auto, false},
		{"left right", Auto, false},
		{"top left right", Auto, false},
		{"a chart of latency", Auto, false},
		{"north", Auto, false},
	} {
		if got, ok := Place(tc.words); got != tc.want || ok != tc.ok {
			t.Errorf("Place(%q) = %v, %v, want %v, %v", tc.words, got, ok, tc.want, tc.ok)
		}
	}
}

func TestShareNeverTakesMoreThanTheBox(t *testing.T) {
	t.Parallel()
	for w := range 140 {
		for h := range 60 {
			for below := range 3 {
				b := share(w, h, below)
				what := fmt.Sprintf("share(%d, %d, %d) = %+v", w, h, below, b)
				if b.rows > 0 && (b.rows < minFaceRows || b.cols < minFaceCols || b.cols > w || b.cols%2 != 0) {
					t.Fatalf("%s: the face does not fit, or is no face", what)
				}
				used := b.top + b.textH + below
				if b.rows > 0 {
					used += b.rows + gap
				}
				if h >= 1+below && (used > h || b.textH < 1) {
					t.Fatalf("%s: %d rows are used of %d", what, used, h)
				}
				if b.textW < 1 || w >= 1 && b.textW > w || b.textW > maxTextCols || b.textH > maxTextRows {
					t.Fatalf("%s: the words do not fit", what)
				}
			}
		}
	}
	// A roomy box has a large avatar and a handful of lines.
	if b := share(100, 22, 0); b.rows < 12 || b.cols < 30 || b.textH < 4 {
		t.Errorf("share(100, 22, 0) = %+v", b)
	}
	// A box with no room for a face has the words alone.
	if b := share(60, 4, 0); b.rows != 0 || b.textH != 4 {
		t.Errorf("share(60, 4, 0) = %+v", b)
	}
}

func TestTheWordsAreSaidAtAPaceAndNeverUnsaid(t *testing.T) {
	t.Parallel()
	s := newStage(t, 100, 22, Options{})
	words := "Two things overlapped. The fix is small, and it holds!"
	s.Perform(Turn{ID: 1, Text: words[:22], Busy: true}, false)
	if s.Said() != "" || !s.Speaking() {
		t.Fatalf("before any time has passed %q is said, speaking %v", s.Said(), s.Speaking())
	}
	now, last := time.Unix(0, 0), ""
	for i := range 40 {
		// The words grow while they are said, as a reply does.
		if i == 5 {
			s.Perform(Turn{ID: 1, Text: words, Over: true}, false)
		}
		now = run(s, now, 100*time.Millisecond)
		said := s.Said()
		if !strings.HasPrefix(said, last) || !strings.HasPrefix(words, said) {
			t.Fatalf("said %q after %q", said, last)
		}
		// What is said is what is drawn under the avatar.
		if said != "" && !strings.Contains(strings.Join(plain(s), "\n"), said[max(len(said)-12, 0):]) {
			t.Fatalf("the stage does not show the last of %q:\n%s", said, strings.Join(plain(s), "\n"))
		}
		last = said
	}
	if last != words || s.Speaking() {
		t.Fatalf("after four seconds %q is said, speaking %v", last, s.Speaking())
	}
	// At 38 characters a second, with a pause at each full stop.
	half := newStage(t, 100, 22, Options{})
	half.Perform(Turn{ID: 1, Text: words, Over: true}, false)
	run(half, time.Unix(0, 0), 500*time.Millisecond)
	if n := len(half.Said()); n < 12 || n > 22 {
		t.Errorf("%d characters are said in half a second: %q", n, half.Said())
	}
}

func TestATurnThatIsNoNewsIsThereAtOnce(t *testing.T) {
	t.Parallel()
	s := newStage(t, 100, 22, Options{})
	s.Perform(Turn{ID: 7, Text: "What the log held.", Over: true, Shows: []Exhibit{box("a", 5, Right, "#", 20, 5)}}, true)
	if s.Said() != "What the log held." || s.Speaking() {
		t.Fatalf("%q is said, speaking %v", s.Said(), s.Speaking())
	}
	rows := plain(s)
	if row, _ := find(rows, "#"); row < 0 {
		t.Errorf("what the turn shows is not on the stage:\n%s", strings.Join(rows, "\n"))
	}
	if !strings.Contains(strings.Join(rows, "\n"), "What the log held.") || strings.Contains(strings.Join(rows, "\n"), cursor) {
		t.Errorf("the words are not all there, or a cursor waits for more:\n%s", strings.Join(rows, "\n"))
	}
	// More of the same turn is news, and is said.
	s.Perform(Turn{ID: 7, Text: "What the log held. And more.", Over: true}, false)
	if s.Said() != "What the log held." || !s.Speaking() {
		t.Errorf("%q is said, speaking %v", s.Said(), s.Speaking())
	}
	// Another turn starts from the top.
	s.Perform(Turn{ID: 9, Text: "Something else."}, false)
	if s.Said() != "" {
		t.Errorf("%q is said of a turn that has just begun", s.Said())
	}
	s.Skip()
	if s.Said() != "Something else." || s.Speaking() {
		t.Errorf("after Skip %q is said, speaking %v", s.Said(), s.Speaking())
	}
}

func TestWordsThatChangeAreSaidAgainFromWhereTheyPart(t *testing.T) {
	t.Parallel()
	s := newStage(t, 100, 22, Options{})
	s.Perform(Turn{ID: 1, Text: "The answer is forty-one, I think."}, false)
	now := run(s, time.Unix(0, 0), 3*time.Second)
	if s.Said() != "The answer is forty-one, I think." {
		t.Fatalf("said %q", s.Said())
	}
	s.Perform(Turn{ID: 1, Text: "The answer is forty-two.", Over: true}, false)
	if s.Said() != "The answer is forty-" {
		t.Fatalf("after the words changed %q is said", s.Said())
	}
	run(s, now, 2*time.Second)
	if s.Said() != "The answer is forty-two." {
		t.Errorf("said %q", s.Said())
	}
}

func TestAnExhibitIsShownWhenTheWordsReachIt(t *testing.T) {
	t.Parallel()
	s := newStage(t, 100, 22, Options{})
	turn := Turn{ID: 1, Text: "Here is the first thing, and then here is the second one.", Over: true, Shows: []Exhibit{
		box("a", 10, TopLeft, "@", 20, 4),
		box("b", 40, TopRight, "&", 20, 4),
	}}
	s.Perform(turn, false)
	now := time.Unix(0, 0)
	seen := func() (a, b bool) {
		rows := plain(s)
		ra, _ := find(rows, "@")
		rb, _ := find(rows, "&")
		return ra >= 0, rb >= 0
	}
	if a, b := seen(); a || b {
		t.Fatal("something is shown before a word is said")
	}
	for len(s.Said()) < 10 {
		now = run(s, now, 50*time.Millisecond)
	}
	if a, b := seen(); !a || b {
		t.Fatalf("after %q: A shown %v, B shown %v", s.Said(), a, b)
	}
	for len(s.Said()) < 40 {
		now = run(s, now, 50*time.Millisecond)
	}
	if a, b := seen(); !a || !b {
		t.Fatalf("after %q: A shown %v, B shown %v", s.Said(), a, b)
	}

	// What a turn showed stays up for the next one, until that one shows
	// something of its own.
	s.Perform(Turn{ID: 2, Text: "About that picture: it is a fine one.", Over: true, Shows: []Exhibit{box("a", 30, Bottom, "=", 20, 3)}}, false)
	if a, b := seen(); !a || !b {
		t.Fatal("the turn before's exhibits went with it")
	}
	run(s, now, 3*time.Second)
	rows := plain(s)
	if row, _ := find(rows, "="); row < 0 {
		t.Fatalf("the new turn's exhibit is not shown:\n%s", strings.Join(rows, "\n"))
	}
	if a, b := seen(); a || b {
		t.Errorf("the turn before's exhibits stayed up beside the new one:\n%s", strings.Join(rows, "\n"))
	}
	// And is gone by the turn after that.
	s.Perform(Turn{ID: 3, Text: "Something new."}, false)
	s.Perform(Turn{ID: 4, Text: "And newer."}, false)
	if row, _ := find(plain(s), "="); row >= 0 {
		t.Error("an exhibit is still up two turns on")
	}
}

func TestAnExhibitStandsAtItsAnchorAndTheAvatarMakesRoom(t *testing.T) {
	t.Parallel()
	const width, height = 100, 23
	for _, tc := range []struct {
		where    Anchor
		row, col int
	}{
		{TopLeft, 1, 2},
		{Top, 1, 40},
		{TopRight, 1, 78},
		{Left, 10, 2},
		{Center, 10, 40},
		{Right, 10, 78},
		{BottomLeft, 19, 2},
		{Bottom, 19, 40},
		{BottomRight, 19, 78},
		{Auto, 10, 78},
	} {
		s := newStage(t, width, height, Options{})
		s.SetAround("the line above", nil)
		s.Perform(Turn{ID: 1, Text: "Look at this.", Over: true, Shows: []Exhibit{box("a", 0, tc.where, "#", 20, 4)}}, true)
		rows := plain(s)
		if row, col := find(rows, "#"); row != tc.row || col != tc.col {
			t.Errorf("anchor %d: the exhibit is at row %d, column %d, want %d, %d:\n%s", tc.where, row, col, tc.row, tc.col, strings.Join(rows, "\n"))
		}
		if rows[0] != "the line above" {
			t.Errorf("anchor %d: the first row is %q", tc.where, rows[0])
		}
		// The avatar and its words are whole beside it.
		if cols, faceRows := s.Face(); cols < minFaceCols || faceRows < minFaceRows {
			t.Errorf("anchor %d: the avatar is %d by %d cells", tc.where, cols, faceRows)
		}
		if !strings.Contains(strings.Join(rows, "\n"), "Look at this.") {
			t.Errorf("anchor %d: the words are not on the stage:\n%s", tc.where, strings.Join(rows, "\n"))
		}
		for i, row := range rows {
			if strings.Contains(row, "#") && strings.ContainsAny(strings.ReplaceAll(row, "#", ""), "▀▄█▌▐▖▗▘▝") {
				// The avatar may stand beside the exhibit, never under it.
				if at := strings.Index(row, "#"); strings.ContainsAny(row[at:at+20], "▀▄█▌▐▖▗▘▝") {
					t.Errorf("anchor %d: row %d draws the avatar through the exhibit: %q", tc.where, i, row)
				}
			}
		}
	}
	// On a narrow stage an exhibit with no place of its own stands over the
	// avatar, and is cut to leave it its rows.
	s := newStage(t, 50, 20, Options{})
	s.Perform(Turn{ID: 1, Text: "Look.", Over: true, Shows: []Exhibit{box("a", 0, Auto, "#", 50, 30)}}, true)
	rows := plain(s)
	if row, col := find(rows, "#"); row != 0 || col != 0 {
		t.Errorf("the exhibit is at row %d, column %d:\n%s", row, col, strings.Join(rows, "\n"))
	}
	if n := strings.Count(strings.Join(rows, "\n"), strings.Repeat("#", 50)); n != 20-minBlockRows {
		t.Errorf("%d rows of the exhibit are shown, want %d", n, 20-minBlockRows)
	}
	if !strings.Contains(strings.Join(rows, "\n"), "Look.") {
		t.Errorf("the words are not on the stage:\n%s", strings.Join(rows, "\n"))
	}
}

func TestExhibitsThatMeetGiveWayToTheNewest(t *testing.T) {
	t.Parallel()
	s := newStage(t, 100, 23, Options{})
	s.Perform(Turn{ID: 1, Text: "One, two, three.", Over: true, Shows: []Exhibit{
		box("a", 0, Right, "@", 30, 6),
		box("b", 0, Left, "&", 30, 6),
		box("c", 0, Right, "=", 30, 6),
	}}, true)
	rows := plain(s)
	a, _ := find(rows, "@")
	b, _ := find(rows, "&")
	c, _ := find(rows, "=")
	if a >= 0 || c < 0 {
		t.Errorf("A is shown (%v) under C (%v):\n%s", a >= 0, c >= 0, strings.Join(rows, "\n"))
	}
	// Two that leave the avatar no room: the older one goes.
	if b >= 0 && !strings.Contains(strings.Join(rows, "\n"), "One, two, three.") {
		t.Errorf("the words have no room:\n%s", strings.Join(rows, "\n"))
	}
}

func TestAnExhibitIsDrawnAgainOnlyWhenItHasTo(t *testing.T) {
	t.Parallel()
	s := newStage(t, 100, 23, Options{})
	draws := 0
	ex := Exhibit{Key: "a", Where: Right, Draw: func(cols, rows int) []string {
		draws++
		return []string{strings.Repeat("#", min(cols, 10))}
	}}
	turn := Turn{ID: 1, Text: "Look.", Over: true, Shows: []Exhibit{ex}}
	s.Perform(turn, true)
	now := run(s, time.Unix(0, 0), time.Second)
	for range 10 {
		s.View()
	}
	if draws != 1 {
		t.Fatalf("drawn %d times in a second of frames, want once", draws)
	}
	// The same turn again changes nothing; a new Rev does, and so do Refresh
	// and another size.
	s.Perform(turn, false)
	s.View()
	if draws != 1 {
		t.Fatalf("drawn %d times after the same turn was handed in again", draws)
	}
	turn.Shows[0].Rev = 31
	s.Perform(turn, false)
	s.View()
	s.Refresh()
	s.View()
	s.Resize(90, 23)
	s.View()
	if draws != 4 {
		t.Errorf("drawn %d times, want 4", draws)
	}
	run(s, now, time.Second)
}

// At every size, in every state, View returns exactly the stage's rows and
// none is wider than the stage, by either way of counting cells.
func TestViewFits(t *testing.T) {
	t.Parallel()
	words := "p99 peaked on Thursday, 日本 too 👍🏽. A paragraph that is long enough to wrap in any caption there is room for, and then some more of it.\n\nAnd a second one."
	for _, width := range []int{140, 100, 72, 60, 40, 26, 12, 5, 1} {
		for _, height := range []int{40, 23, 16, 9, 8, 7, 4, 2, 1, 0} {
			for _, where := range []Anchor{Auto, TopLeft, Top, Left, Center, Right, Bottom, BottomRight} {
				s := newStage(t, width, height, Options{})
				s.SetAround("❯ the question that was asked", []string{"⏺ python  running… 2.1s"})
				s.Perform(Turn{ID: 1, Text: words, Busy: true, Shows: []Exhibit{
					box("a", 5, where, "#", 44, 12),
					box("b", 30, Auto, "界", 60, 30),
				}}, false)
				now := time.Unix(0, 0)
				for range 8 {
					now = run(s, now, 400*time.Millisecond)
					rows := s.View()
					if len(rows) != height {
						t.Fatalf("%dx%d, anchor %d: %d rows", width, height, where, len(rows))
					}
					for i, row := range rows {
						if w := max(ansi.StringWidth(row), ansi.StringWidthWc(row)); w > width {
							t.Fatalf("%dx%d, anchor %d: row %d is %d cells wide: %q", width, height, where, i, w, ansi.Strip(row))
						}
						// The last sequence of a row switches everything off.
						if at := strings.LastIndex(row, "\x1b"); at >= 0 && !strings.HasPrefix(row[at:], reset) {
							t.Fatalf("%dx%d, anchor %d: row %d leaves a style on: %q", width, height, where, i, row)
						}
					}
				}
			}
		}
	}
}

// level is how loud a synth is right now: the largest of its next samples.
func level(s *sound.Synth) float64 {
	buf := make([]byte, 4*4096)
	if _, err := s.Read(buf); err != nil {
		return math.NaN()
	}
	loud := 0.0
	for i := 0; i < len(buf); i += 4 {
		loud = max(loud, math.Abs(float64(math.Float32frombits(binary.LittleEndian.Uint32(buf[i:])))))
	}
	return loud
}

func TestTheVoiceSoundsUnlessItIsMuted(t *testing.T) {
	t.Parallel()
	words := "Ooh, a wide open aaah and an eee, over and over again."
	heard := func(o Options, then func(*Stage)) float64 {
		synth := sound.NewSynth(sound.Timbres()[0])
		o.Synth = synth
		s := newStage(t, 100, 22, o)
		s.Perform(Turn{ID: 1, Text: words}, false)
		now, loud := time.Unix(0, 0), 0.0
		for range 30 {
			now = run(s, now, 30*time.Millisecond)
			if then != nil {
				then(s)
			}
			loud = max(loud, level(synth))
		}
		return loud
	}
	if loud := heard(Options{}, nil); loud < 0.02 {
		t.Errorf("a speaking avatar peaks at %.4f", loud)
	}
	if loud := heard(Options{Muted: true}, nil); loud > 0.001 {
		t.Errorf("a muted avatar peaks at %.4f", loud)
	}
	if loud := heard(Options{}, func(s *Stage) { s.SetMuted(true) }); loud > 0.02 {
		t.Errorf("an avatar that was muted peaks at %.4f", loud)
	}
	if loud := heard(Options{}, func(s *Stage) { s.Hush() }); loud > 0.02 {
		t.Errorf("a hushed avatar peaks at %.4f", loud)
	}
}

func TestSettingsStayInBounds(t *testing.T) {
	t.Parallel()
	s := newStage(t, 100, 22, Options{Rate: 1000, Look: 99})
	if got := s.SetRate(1); got != MinRate {
		t.Errorf("SetRate(1) = %v", got)
	}
	if got := s.SetRate(1e9); got != MaxRate {
		t.Errorf("SetRate(1e9) = %v", got)
	}
	s.Wear(-1)
	s.Wear(99)
	s.Wear(1)
	s.SetGrid(canvas.Octants)
	s.SetPaper(canvas.Hex(0xffffff), false)
	s.Glance(5, -5)
	s.Perform(Turn{ID: 1, Text: "Still here."}, true)
	if rows := s.View(); len(rows) != 22 {
		t.Errorf("%d rows", len(rows))
	}
}

func BenchmarkFrame(b *testing.B) {
	s := New(Options{Seed: 1, Paper: canvas.Hex(0x121214), Dark: true, Theme: theme.New(termcaps.Caps{
		Color: termcaps.ColorTrue, Background: termcaps.BgDark, Glyphs: termcaps.GlyphsUnicode, Width: termcaps.WidthWc,
	}, theme.Options{})})
	s.Resize(100, 23)
	s.Perform(Turn{ID: 1, Text: strings.Repeat("A reply that goes on for as long as the benchmark does. ", 400)}, false)
	now := time.Unix(0, 0)
	for b.Loop() {
		now = now.Add(time.Second / 60)
		s.Tick(now)
		s.View()
	}
}
