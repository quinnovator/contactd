// Package stage is a conversation as a performance: an avatar that says the
// contact's reply aloud, the words under it as they are said, and the things
// the reply shows, each where the reply asked for it.
//
// It stands where a thread of messages would. There is nothing to scroll: the
// stage starts idle and holds the turn in flight, or the last one from this
// visit. What was said before is the server's to keep.
//
// A Stage knows nothing of a conversation. It is handed a Turn, the words of
// which only grow, and Tick advances it: the voice works through the words
// at a speaker's pace, the avatar acts them out, and the synth sounds the
// shape the avatar's mouth is in. An Exhibit is shown when the words reach
// it. View lays all of it out in the rows the stage was given.
//
// Every row View returns is at most the stage's width in cells and switches
// off any style it switches on. The words and the exhibits are the caller's,
// and are expected to be safe to draw.
package stage

import (
	"strings"
	"time"

	"github.com/quinnovator/contactd/internal/tui/avatar"
	"github.com/quinnovator/contactd/internal/tui/canvas"
	"github.com/quinnovator/contactd/internal/tui/motion"
	"github.com/quinnovator/contactd/internal/tui/sound"
	"github.com/quinnovator/contactd/internal/tui/speech"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
	"github.com/quinnovator/contactd/internal/tui/theme"
)

const (
	// The pace the avatar speaks at, in characters a second, and its bounds.
	DefaultRate = 38.0
	MinRate     = 8.0
	MaxRate     = 240.0

	// pleased is how long, in seconds, the avatar looks pleased with what it
	// has said before it is simply there again.
	pleased = 6.0
	// maxBelow is how many of the caller's lines stand over the words.
	maxBelow = 2
)

// Exhibit is one thing a turn shows.
type Exhibit struct {
	// Key names the exhibit among those of its turn. It stays the same for
	// as long as the exhibit is the same thing.
	Key string
	// Rev says which drawing of the exhibit this is. What an exhibit drew
	// under one Rev is drawn again when it comes under another.
	Rev uint64
	// At is when it is shown: the number of runes of the turn's words that
	// are said first.
	At int
	// Where is the place on the stage the turn asked for.
	Where Anchor
	// Draw returns the exhibit's rows for a box of at most cols by rows
	// cells: each row at most cols cells, and at most rows of them. It is
	// asked again after Refresh, and whenever the box changes.
	Draw func(cols, rows int) []string
}

// Turn is what there is to perform: one turn of the conversation.
type Turn struct {
	// ID names the turn. Words handed in under another ID are a new turn,
	// said from the top.
	ID uint64
	// Text is what the contact has said in the turn so far.
	Text string
	// Shows is what the turn shows, in the order of its words.
	Shows []Exhibit
	// Over says nothing more will be said in the turn.
	Over bool
	// Busy says the contact is at work on it: writing, or running a tool.
	Busy bool
}

// Options are what a Stage is made with.
type Options struct {
	// Theme measures text. Nil is the safest one.
	Theme *theme.Theme
	// Rate is the pace of speech, in characters a second. Zero is
	// DefaultRate.
	Rate float64
	// Look is the index into avatar.Palettes of what the avatar wears.
	Look int
	// Grid is how finely the avatar is drawn.
	Grid canvas.Grid
	// Synth is the avatar's voice. Nil is no sound.
	Synth *sound.Synth
	// Muted starts the voice silent.
	Muted bool
	// Seed times the avatar's blinks and glances.
	Seed uint64
	// Paper is the terminal's background and Dark whether it is a dark one.
	Paper canvas.Color
	Dark  bool
}

// Stage is the avatar, its voice and what it shows. It is not safe for use
// from several goroutines.
type Stage struct {
	th     *theme.Theme
	voice  *speech.Voice
	avatar *avatar.Avatar
	synth  *sound.Synth
	muted  bool
	clock  motion.Clock
	canvas *canvas.Canvas
	grid   canvas.Grid
	// elapsed is the seconds of animation so far, and rested those since the
	// voice last finished.
	elapsed, rested float64

	paper canvas.Color
	dark  bool

	turn  Turn
	begun bool
	// cards are the exhibits on the stage, in the order they were shown, and
	// look the one the avatar has yet to glance at.
	cards []*card
	look  *card

	// above is the line over everything, and below the lines between the
	// avatar and its words. Both are the caller's, drawn and fitted.
	above string
	below []string

	width, height int
	plan          plan
	// stale says the plan has to be made again.
	stale bool
	page  page
}

// New makes a Stage with nothing to say.
func New(o Options) *Stage {
	th := o.Theme
	if th == nil {
		th = theme.New(termcaps.Caps{}, theme.Options{})
	}
	look := min(max(o.Look, 0), len(avatar.Palettes())-1)
	rate := o.Rate
	if rate == 0 {
		rate = DefaultRate
	}
	return &Stage{
		th:     th,
		voice:  speech.NewVoice(min(max(rate, MinRate), MaxRate)),
		avatar: avatar.New(avatar.Palettes()[look], o.Seed),
		synth:  o.Synth,
		muted:  o.Muted,
		canvas: canvas.New(1, 1, o.Grid),
		grid:   o.Grid,
		paper:  o.Paper,
		dark:   o.Dark,
		width:  1, height: 1,
		stale: true,
	}
}

// Perform hands the stage the turn to perform, as it is now. The words of a
// turn that was handed in before are said from where the voice has got to;
// a new turn is said from the top. said marks a turn that is not news: its
// words are there at once, without a sound.
//
// What the turn before showed stays up until this one shows something of its
// own: a picture is still there for the question about it and the answer.
// What was shown earlier than that is taken down.
func (s *Stage) Perform(t Turn, said bool) {
	if !s.begun || t.ID != s.turn.ID {
		last := s.turn.ID
		kept := s.cards[:0]
		for _, c := range s.cards {
			if s.begun && c.turn == last {
				kept = append(kept, c)
			}
		}
		s.cards, s.look = kept, nil
		s.voice.Reset()
		s.begun, s.rested, s.stale = true, 0, true
	}
	s.turn = t
	s.voice.Set(t.Text)
	if t.Over {
		s.voice.Close()
	} else {
		s.voice.Open()
	}
	if said {
		s.voice.Skip()
	}
	s.page.reset()
	s.reveal()
}

// Skip has the rest of the turn said at once: the words are all there, and
// the avatar stops talking.
func (s *Stage) Skip() {
	s.voice.Skip()
	s.reveal()
}

// Speaking reports whether there are words the voice has not said yet.
func (s *Stage) Speaking() bool { return s.voice.Head() < len(s.voice.Text()) }

// Tick advances the performance to the time now.
func (s *Stage) Tick(now time.Time) {
	for range s.clock.Tick(now) {
		s.step()
	}
}

// step advances the performance by one step of the simulation. The voice
// leads: it decides which runes are said, the avatar acts them out, and the
// synth sounds the shape the avatar's mouth ends up in.
func (s *Stage) step() {
	s.elapsed += motion.Dt
	beats := s.voice.Step(motion.Dt)
	state := s.voice.State()
	if state == speech.Finished {
		s.rested += motion.Dt
	} else {
		s.rested = 0
	}
	s.avatar.Step(s.mood(state), s.voice.Mouth(), beats)
	s.sound(s.avatar.Mouth())
	s.reveal()
}

// mood is the avatar's frame of mind: lost in thought while the contact is
// at work and has nothing to say, pleased for a moment once it has said its
// piece, and otherwise simply there.
func (s *Stage) mood(state speech.State) avatar.Mood {
	switch state {
	case speech.Speaking:
		return avatar.Speaking
	case speech.Waiting:
		if s.turn.Busy {
			return avatar.Thinking
		}
	case speech.Finished:
		if s.rested < pleased && len(s.voice.Text()) > 0 {
			return avatar.Content
		}
	}
	return avatar.Idle
}

// sound has the synth sound a mouth, unless the voice is muted.
func (s *Stage) sound(mouth speech.Viseme) {
	if s.synth == nil {
		return
	}
	tone := sound.Tone{Syllable: s.voice.Syllable()}
	if !s.muted {
		tone.Mouth = mouth
	}
	s.synth.Voice(tone)
}

// Hush stops the sound until the next Tick. It is for a stage that leaves
// the screen in the middle of a word.
func (s *Stage) Hush() { s.sound(speech.Rest) }

// reveal puts on the stage the exhibits the words have reached, and takes
// down the ones the turn no longer has.
func (s *Stage) reveal() {
	head := s.voice.Head()
	kept := s.cards[:0]
	for _, c := range s.cards {
		if c.turn == s.turn.ID {
			i := s.exhibit(c.ex.Key)
			if i < 0 || s.turn.Shows[i].At > head {
				s.stale = true
				continue
			}
			if s.turn.Shows[i].Rev != c.ex.Rev {
				c.drawn, s.stale = false, true
			}
			c.ex = s.turn.Shows[i]
		}
		kept = append(kept, c)
	}
	s.cards = kept
	for _, ex := range s.turn.Shows {
		if ex.At > head || s.card(ex.Key) != nil {
			continue
		}
		// The first thing a turn shows takes down what the turn before
		// left up.
		if !s.showing() {
			s.cards = s.cards[:0]
		}
		c := &card{ex: ex, turn: s.turn.ID}
		s.cards, s.look, s.stale = append(s.cards, c), c, true
	}
}

// exhibit finds one of the turn's exhibits by its key, or -1.
func (s *Stage) exhibit(key string) int {
	for i := range s.turn.Shows {
		if s.turn.Shows[i].Key == key {
			return i
		}
	}
	return -1
}

// card finds the card of one of the turn's exhibits on the stage.
func (s *Stage) card(key string) *card {
	for _, c := range s.cards {
		if c.turn == s.turn.ID && c.ex.Key == key {
			return c
		}
	}
	return nil
}

// showing reports whether the turn has something of its own on the stage.
func (s *Stage) showing() bool {
	for _, c := range s.cards {
		if c.turn == s.turn.ID {
			return true
		}
	}
	return false
}

// Resize gives the stage its size, in cells.
func (s *Stage) Resize(width, height int) {
	width, height = max(width, 1), max(height, 0)
	if width == s.width && height == s.height {
		return
	}
	s.width, s.height, s.stale = width, height, true
	s.Refresh()
}

// Refresh has every exhibit drawn again: what one draws has changed.
func (s *Stage) Refresh() {
	for _, c := range s.cards {
		c.drawn = false
	}
	s.stale = true
}

// SetTheme measures text with another theme from now on.
func (s *Stage) SetTheme(th *theme.Theme) {
	s.th = th
	s.page.reset()
	s.Refresh()
}

// SetPaper tells the stage the terminal's background.
func (s *Stage) SetPaper(paper canvas.Color, dark bool) { s.paper, s.dark = paper, dark }

// SetAround gives the stage the line over everything, and the lines that
// stand between the avatar and its words: at most two, each drawn and no
// wider than the stage.
func (s *Stage) SetAround(above string, below []string) {
	below = below[:min(len(below), maxBelow)]
	if above == s.above && len(below) == len(s.below) {
		same := true
		for i := range below {
			same = same && below[i] == s.below[i]
		}
		if same {
			return
		}
	}
	// The plan follows the number of lines, not what they say.
	if (above == "") != (s.above == "") || len(below) != len(s.below) {
		s.stale = true
	}
	s.above, s.below = above, append(s.below[:0], below...)
}

// Wear cross-fades the avatar into another of avatar.Palettes.
func (s *Stage) Wear(look int) {
	if look >= 0 && look < len(avatar.Palettes()) {
		s.avatar.Wear(avatar.Palettes()[look])
	}
}

// SetRate sets the pace of speech, in characters a second, within its
// bounds, and returns what it is now.
func (s *Stage) SetRate(rate float64) float64 {
	s.voice.Rate = min(max(rate, MinRate), MaxRate)
	return s.voice.Rate
}

// SetMuted silences the voice, or lets it sound again.
func (s *Stage) SetMuted(muted bool) { s.muted = muted }

// SetGrid changes how finely the avatar is drawn.
func (s *Stage) SetGrid(g canvas.Grid) {
	if g != s.grid {
		s.grid, s.stale = g, true
	}
}

// Glance draws the avatar's eyes toward a point for a moment: x and y each
// from -1 to 1, from the left and the top of where the avatar stands.
func (s *Stage) Glance(x, y float64) { s.avatar.LookAt(x, y) }

// Face is the size of the avatar's picture as the stage is laid out now, in
// cells. It is zero by zero where the stage has no room for one.
func (s *Stage) Face() (cols, rows int) {
	s.arrange()
	return s.plan.face.w, s.plan.face.h
}

// Said is the words of the turn that have been said so far.
func (s *Stage) Said() string { return string(s.voice.Text()[:s.voice.Head()]) }

// View draws the stage: exactly as many rows as it is high.
func (s *Stage) View() []string {
	s.arrange()
	p := &s.plan
	rows := make([][]piece, s.height)
	put := func(y int, pc piece) {
		if y >= 0 && y < len(rows) && pc.s != "" {
			rows[y] = append(rows[y], pc)
		}
	}
	if p.above {
		put(0, piece{x: 0, w: s.th.Width(s.above), s: s.above})
	}
	for _, c := range s.cards {
		if !c.on {
			continue
		}
		for i, row := range c.rows[:c.h] {
			put(c.y+i, piece{x: c.x, w: s.th.Width(row), s: closed(row)})
		}
	}
	if p.face.h > 0 {
		s.avatar.Draw(s.canvas)
		for i, row := range strings.Split(s.canvas.Render(s.paper), "\n") {
			put(p.face.y+i, piece{x: p.face.x, w: p.face.w, s: closed(row)})
		}
	}
	for i, row := range s.caption(p.text.w, p.text.h) {
		put(p.text.y+i, piece{x: p.text.x, w: row.w, s: row.s})
	}
	for i, line := range s.below[:p.below] {
		w := s.th.Width(line)
		put(p.belowY+i, piece{x: p.block.x + max(p.block.w-w, 0)/2, w: w, s: line})
	}
	out := make([]string, s.height)
	for y, pieces := range rows {
		out[y] = join(pieces, s.width)
	}
	return out
}

// closed is a row that leaves no style on behind it.
func closed(row string) string {
	if strings.Contains(row, "\x1b") && !strings.HasSuffix(row, reset) {
		return row + reset
	}
	return row
}

// reset switches every style off.
const reset = "\x1b[m"

// piece is a run of cells on one row of the stage.
type piece struct {
	x, w int
	s    string
}

// join writes the pieces of one row side by side, each where it stands. A
// piece that would begin under the one before it, or end past the stage's
// edge, is left out: a row is never wider than the stage.
func join(pieces []piece, width int) string {
	if len(pieces) == 0 {
		return ""
	}
	// A row has a handful of pieces: the simplest sort does.
	for i := 1; i < len(pieces); i++ {
		for j := i; j > 0 && pieces[j].x < pieces[j-1].x; j-- {
			pieces[j], pieces[j-1] = pieces[j-1], pieces[j]
		}
	}
	var b strings.Builder
	at := 0
	for _, p := range pieces {
		if p.x < at || p.x+p.w > width {
			continue
		}
		b.WriteString(strings.Repeat(" ", p.x-at))
		b.WriteString(p.s)
		at = p.x + p.w
	}
	return b.String()
}
