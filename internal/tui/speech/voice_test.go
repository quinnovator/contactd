package speech

import (
	"slices"
	"testing"
)

const dt = 1.0 / 60

// speak steps v for the given number of seconds and returns every beat heard.
func speak(v *Voice, seconds float64) (beats Beat) {
	for range int(seconds/dt + 0.5) {
		beats |= v.Step(dt)
	}
	return beats
}

func TestVoiceKeepsASteadyPace(t *testing.T) {
	t.Parallel()
	v := NewVoice(30)
	v.Feed("abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyz")
	speak(v, 1)
	if got := v.Head(); got < 29 || got > 31 {
		t.Errorf("spoke %d runes in a second at 30/s", got)
	}
}

func TestVoicePausesBetweenSentences(t *testing.T) {
	t.Parallel()
	plain, stopped := NewVoice(30), NewVoice(30)
	plain.Feed("one two three four five")
	stopped.Feed("one. two. three. four.")
	speak(plain, 0.5)
	speak(stopped, 0.5)
	if stopped.Head() >= plain.Head() {
		t.Errorf("sentences did not slow the voice: %d runes with stops, %d without", stopped.Head(), plain.Head())
	}
}

func TestVoiceDoesNotRushAfterSilence(t *testing.T) {
	t.Parallel()
	v := NewVoice(30)
	speak(v, 5) // nothing to say for a long while
	v.Feed("hello there, how are you today")
	v.Step(dt)
	if got := v.Head(); got != 1 {
		t.Errorf("spoke %d runes in the first step after a silence, want 1", got)
	}
}

func TestVoiceState(t *testing.T) {
	t.Parallel()
	v := NewVoice(60)
	if v.State() != Waiting {
		t.Error("a voice with nothing to say should be Waiting")
	}
	v.Feed("hi")
	speak(v, 0.1)
	if v.State() != Speaking {
		t.Error("a brief gap in the stream should still count as Speaking")
	}
	speak(v, 1)
	if v.State() != Waiting {
		t.Error("a long gap in an open stream should be Waiting")
	}
	v.Close()
	if v.State() != Finished {
		t.Error("a closed stream with everything spoken should be Finished")
	}
	v.Rewind()
	if v.Head() != 0 || v.State() != Speaking {
		t.Error("a rewound voice should start over")
	}
}

func TestVoiceTrimsWhitespaceAndControls(t *testing.T) {
	t.Parallel()
	v := NewVoice(30)
	for _, chunk := range []string{"\n  hel", "lo\tthere", "\x1b\r\n", "\n", "friend. \n"} {
		v.Feed(chunk)
	}
	if got, want := string(v.Text()), "hello there\n\nfriend."; got != want {
		t.Errorf("text = %q, want %q", got, want)
	}
}

func TestBeats(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		text string
		want Beat
	}{
		{"hello", Word},
		{"don't", Word}, // an apostrophe does not start a new word
		{"yes, no", Word | Pause},
		{"1,000", Word}, // a comma inside a number is not a pause
		{"Done.", Word | Stop},
		{"3.14", Word}, // nor is a decimal point a full stop
		{"Really?", Word | Stop | Ask},
		{`"Wow!" she`, Word | Stop | Exclaim},
		{"well…", Word | Stop},
		{"a\nb", Word | Pause},
	} {
		v := NewVoice(1000)
		v.Feed(tc.text)
		if got := speak(v, 2); got != tc.want {
			t.Errorf("beats of %q = %05b, want %05b", tc.text, got, tc.want)
		}
	}
}

func TestMouthCarriesVowels(t *testing.T) {
	t.Parallel()
	v := NewVoice(10)
	v.Feed("at m")
	shapes := map[int]Viseme{}
	for v.Head() < len(v.Text()) {
		v.Step(dt)
		shapes[v.Head()] = v.Mouth()
	}
	if shapes[1] != VisemeOf('a') {
		t.Errorf("mouth on 'a' = %+v", shapes[1])
	}
	if shapes[2] != VisemeOf('a') {
		t.Errorf("the vowel should carry through the consonant after it, got %+v", shapes[2])
	}
	if shapes[3] != Rest || shapes[4] != Rest {
		t.Errorf("the mouth should close for a space and for 'm', got %+v and %+v", shapes[3], shapes[4])
	}
	speak(v, 1)
	if v.Mouth() != Rest {
		t.Error("the mouth should rest once the words run out")
	}
}

// spoken speaks text through and returns each syllable as it begins.
func spoken(text string) (all []Syllable) {
	v := NewVoice(1000)
	v.Feed(text)
	for v.Head() < len(v.Text()) {
		v.Step(1.0 / 1000)
		if s := v.Syllable(); s.Count != len(all) {
			all = append(all, s)
		}
	}
	return all
}

// syllables is the pitch of each syllable of text.
func syllables(text string) (pitches []float64) {
	for _, s := range spoken(text) {
		pitches = append(pitches, s.Pitch)
	}
	return pitches
}

func TestPhrasesLandOnTheirLastSyllable(t *testing.T) {
	t.Parallel()
	got := make([]float64, 0, 8)
	for _, s := range spoken("well, so it was you. banana!") {
		got = append(got, s.Stress)
	}
	//              well  so it was you  ba na na
	want := []float64{0.5, 0, 0, 0, 1, 0, 0, 1}
	if !slices.Equal(got, want) {
		t.Errorf("stress = %v, want %v", got, want)
	}
}

func TestSyllables(t *testing.T) {
	t.Parallel()
	for text, want := range map[string]int{
		"banana":    3,
		"you":       1, // neighbouring vowels share a syllable
		"hello you": 3,
		"rhythm":    1, // y stands in for a vowel
		"mmm, pfft": 0,
		"42":        1,
	} {
		if got := len(syllables(text)); got != want {
			t.Errorf("%q has %d syllables, want %d", text, got, want)
		}
	}
}

func TestMelody(t *testing.T) {
	t.Parallel()
	last := func(p []float64) float64 { return p[len(p)-1] }
	told, asked := syllables("so it was you."), syllables("so it was you?")
	if last(asked) < last(told)+6 {
		t.Errorf("a question should end higher than a statement: %.1f against %.1f", last(asked), last(told))
	}
	if told[0] != asked[0] {
		t.Errorf("far from the end, the same words should sound the same: %.1f against %.1f", told[0], asked[0])
	}
	a := syllables("la la la la la la la la la la la la la la la la")
	if a[10] > a[0]-3 {
		t.Errorf("a long sentence should drift downward: from %.1f to %.1f", a[0], a[10])
	}
	b := syllables("la la la la la la la la la la la la la la la la. la la")
	if got := b[len(b)-2]; got != a[0] {
		t.Errorf("a new sentence should start afresh at %.1f, got %.1f", a[0], got)
	}
}
