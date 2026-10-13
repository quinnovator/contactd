package speech

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Beat marks a moment in a sentence worth reacting to. Beats combine: a
// question mark is both an Ask and a Stop.
type Beat uint8

const (
	Word    Beat = 1 << iota // a new word begins
	Pause                    // a comma, a dash, a line break
	Stop                     // the end of a sentence
	Ask                      // ...that was a question
	Exclaim                  // ...or an exclamation
)

// State says what a Voice is doing.
type State int

const (
	Waiting  State = iota // nothing to say yet, or the stream has gone quiet
	Speaking              // working through the text
	Finished              // the stream has ended and every word is out
)

const (
	breath = 0.13 // seconds of pause after a Pause beat
	rest   = 0.34 // seconds of pause after a Stop beat
	lull   = 0.30 // seconds of silence before an open stream counts as Waiting
	carry  = 2    // runes a vowel's shape is held through
	lift   = 12   // runes over which a sentence's ending bends its pitch

	// skipped is how long ago, in seconds, a rune that was skipped counts as
	// having been spoken.
	skipped = 3600.0
)

// Voice paces text into speech. Text may arrive in any rhythm, a rune at a
// time or a page at once; it is spoken at a steady cadence, pausing at
// punctuation the way a reader would.
type Voice struct {
	Rate float64 // runes per second

	text   []rune
	held   []rune    // whitespace waiting for the word that follows it
	spoken []float64 // spoken[i] is when text[i] was said; its length is the read head
	now    float64
	wait   float64 // time until the next rune is due
	quiet  float64 // time spent with nothing left to say
	closed bool

	syllable Syllable // the one being spoken
	phrase   int      // how many the current sentence has had
}

// NewVoice returns a Voice speaking rate runes per second.
func NewVoice(rate float64) *Voice { return &Voice{Rate: rate} }

// Feed appends text to be spoken, dropping what cannot be: control
// characters, and whitespace with no words on one side of it. Whitespace is
// held back until the word after it arrives, so a message never begins or
// ends with any.
func (v *Voice) Feed(s string) {
	v.text, v.held = feed(v.text, v.held, s)
}

// feed is Feed on a text and the whitespace held back at its end.
func feed(text, held []rune, s string) (grown, kept []rune) {
	for _, r := range s {
		switch {
		case r == '\t':
			r = ' '
		case r == '\n':
		case unicode.IsControl(r), r == utf8.RuneError:
			continue
		}
		if unicode.IsSpace(r) {
			held = append(held, r)
			continue
		}
		if len(text) > 0 {
			text = append(text, held...)
		}
		text, held = append(text, r), held[:0]
	}
	return text, held
}

// Set makes s the whole of what there is to say, for a caller that holds the
// text and not the pieces it arrived in. What has been spoken stays spoken
// as far as s still begins with it; from where the two part, s is spoken
// again. A text that only grew is Feed by another name.
func (v *Voice) Set(s string) {
	text, _ := feed(make([]rune, 0, len(s)), nil, s)
	same := 0
	for same < len(text) && same < len(v.text) && text[same] == v.text[same] {
		same++
	}
	if same < len(v.spoken) {
		v.spoken = v.spoken[:same]
	}
	v.text, v.held = text, v.held[:0]
}

// Close marks the end of the stream: once everything fed has been spoken, the
// voice is Finished.
func (v *Voice) Close() { v.closed = true }

// Open takes Close back: there is more to come after all.
func (v *Voice) Open() { v.closed = false }

// Reset empties the voice for something new to say. Time goes on, and so
// does the count of syllables, which is how a listener tells one from the
// next.
func (v *Voice) Reset() {
	v.text, v.held, v.spoken = v.text[:0], v.held[:0], v.spoken[:0]
	v.wait, v.quiet, v.phrase, v.closed = 0, 0, 0, false
}

// Rewind starts speaking again from the top, keeping the text.
func (v *Voice) Rewind() { v.spoken, v.wait, v.quiet, v.phrase = v.spoken[:0], 0, 0, 0 }

// Skip has the rest said at once, without a sound: every rune there is
// counts as spoken, long enough ago to have dried. It is what a reader does
// who has read ahead of the speaker.
func (v *Voice) Skip() {
	// The times stay in order, which Fresh relies on: everything said so far
	// dries with the rest.
	v.spoken = v.spoken[:0]
	for range v.text {
		v.spoken = append(v.spoken, v.now-skipped)
	}
	v.wait, v.phrase = 0, 0
	v.quiet = lull + 1
}

// Step advances time by dt seconds, speaking any runes that fall due, and
// returns the beats they landed on.
func (v *Voice) Step(dt float64) (beats Beat) {
	v.now += dt
	v.wait -= dt
	for v.wait <= 0 && v.Head() < len(v.text) {
		i := v.Head()
		v.spoken = append(v.spoken, v.now)
		v.intone(i)
		b := v.beat(i)
		beats |= b
		v.wait += 1 / v.Rate
		switch {
		case b&Stop != 0:
			v.wait += rest
			v.phrase = 0
		case b&Pause != 0:
			v.wait += breath
		}
	}
	if v.wait > 0 {
		v.quiet = 0
	} else {
		v.wait = 0 // idle time is not owed back as a burst of speech
		v.quiet += dt
	}
	return beats
}

// Text is everything fed so far. The first Head runes have been spoken.
func (v *Voice) Text() []rune { return v.text }

// Head is the number of runes spoken.
func (v *Voice) Head() int { return len(v.spoken) }

// Age is how long ago, in seconds, rune i was spoken.
func (v *Voice) Age(i int) float64 { return v.now - v.spoken[i] }

// Fresh returns the index of the first rune spoken within the last window
// seconds. Runes from there to Head are the ones still wet on the page.
func (v *Voice) Fresh(window float64) int {
	return sort.SearchFloat64s(v.spoken, v.now-window)
}

// Mouth is the shape the mouth should be making right now. Vowels carry: an
// open shape is held through the consonants that follow it, as it is in real
// speech, until the lips close or the word ends. In the gaps the mouth rests.
func (v *Voice) Mouth() Viseme {
	n := v.Head()
	if n == 0 || v.Age(n-1) > 1.5/v.Rate {
		return Rest
	}
	shape := VisemeOf(v.text[n-1])
	for i := n - 2; i >= max(n-1-carry, 0) && shape != Rest; i-- {
		prev := VisemeOf(v.text[i])
		if prev == Rest {
			break
		}
		if prev.Open > shape.Open {
			shape = prev
		}
	}
	return shape
}

// Syllable describes one syllable of speech.
type Syllable struct {
	Count  int     // goes up by one as each syllable begins
	Pitch  float64 // semitones above the speaker's resting note
	Stress float64 // 0 for a syllable said in passing, up to 1 for one a sentence lands on
}

// Syllable is the syllable being spoken, or the last one that was.
func (v *Voice) Syllable() Syllable { return v.syllable }

// State reports what the voice is doing. Brief gaps while a stream catches up
// still count as Speaking, so the state does not flicker.
func (v *Voice) State() State {
	switch {
	case v.Head() < len(v.text) || v.wait > 0:
		return Speaking
	case v.closed:
		return Finished
	case v.Head() == 0 || v.quiet > lull:
		return Waiting
	}
	return Speaking
}

// The notes syllables are sung on: a pentatonic scale, in which any sequence
// makes a passable tune.
//
//nolint:gochecknoglobals // a table, only read
var scale = [...]float64{-3, 0, 2, 4, 7, 9}

// endings is how far each closing mark bends the pitch of the words that
// lead up to it, in semitones.
//
//nolint:gochecknoglobals // a table, only read
var endings = map[rune]float64{'?': 8, '!': 4, '.': -2, '…': -2}

// intone starts a new syllable if the rune at i opens one, a vowel not
// following another, and gives it a place in the melody of the sentence.
func (v *Voice) intone(i int) {
	if vowel(v.text[i]) < 0 || i > 0 && vowel(v.text[i-1]) >= 0 {
		return
	}
	// The note comes from the syllable's own letters, so a word always
	// sounds the same.
	key := int(v.text[i])
	if i > 0 {
		key += 3 * int(v.text[i-1])
	}
	s := Syllable{Count: v.syllable.Count + 1, Pitch: scale[key%len(scale)]}
	// Sentences drift downward as they go...
	s.Pitch -= min(0.4*float64(v.phrase), 4)
	// ...until the approach to the end, which rises to a question and
	// falls to a full stop. The last syllable before a break is the one the
	// phrase lands on: leant on a little at a comma, fully at the end.
	last := true
	for j := i + 1; j < len(v.text) && j <= i+lift; j++ {
		r := v.text[j]
		switch {
		case vowel(r) >= 0 && vowel(v.text[j-1]) < 0:
			last = false // another syllable comes first
		case !v.boundary(j + 1):
		case last && s.Stress == 0 && strings.ContainsRune(",;:", r):
			s.Stress = 0.5
		}
		if bend, ok := endings[r]; ok && v.boundary(j+1) {
			s.Pitch += bend * (1 - float64(j-i)/lift)
			if last {
				s.Stress = 1
			}
			break
		}
	}
	v.syllable = s
	v.phrase++
}

// beat classifies the rune at i by its role in the sentence.
func (v *Voice) beat(i int) Beat {
	r := v.text[i]
	switch {
	case isWord(r):
		if i == 0 || !isWord(v.text[i-1]) && !strings.ContainsRune("'’", v.text[i-1]) {
			return Word
		}
	case r == '\n', r == '—', r == '–':
		return Pause
	case strings.ContainsRune(",;:", r) && v.boundary(i+1):
		return Pause
	case strings.ContainsRune(".!?…", r) && v.boundary(i+1):
		switch r {
		case '?':
			return Stop | Ask
		case '!':
			return Stop | Exclaim
		}
		return Stop
	}
	return 0
}

// boundary reports whether a phrase can end just before index i, which keeps
// "3.14" and "example.com" from being read as two sentences.
func (v *Voice) boundary(i int) bool {
	return i >= len(v.text) || unicode.IsSpace(v.text[i]) || strings.ContainsRune(`"')]”’`, v.text[i])
}

func isWord(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }
