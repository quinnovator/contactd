package sound

import (
	"encoding/binary"
	"math"
	"testing"

	"github.com/quinnovator/contactd/internal/tui/speech"
)

func chirp() Timbre { return Timbres()[0] }

// play reads the next stretch of sound from s.
func play(s *Synth, seconds float64) []float64 {
	buf := make([]byte, 4*int(seconds*SampleRate))
	_, _ = s.Read(buf) // a synth never fails to sound
	out := make([]float64, len(buf)/4)
	for i := range out {
		out[i] = float64(math.Float32frombits(binary.LittleEndian.Uint32(buf[4*i:])))
	}
	return out
}

// say is the tone for syllable number n, sung on the vowel r.
func say(r rune, n int) Tone {
	return Tone{Mouth: speech.VisemeOf(r), Syllable: speech.Syllable{Count: n}}
}

// babble plays a second and a half of syllables at speaking pace.
func babble(t Timbre) (x []float64) {
	s := NewSynth(t)
	for i := range 20 {
		s.Voice(say(rune("aeiou"[i%5]), i+1))
		x = append(x, play(s, 0.075)...)
	}
	return x
}

// hold returns a stretch of the vowel r held steadily in timbre t, which is
// made to sustain so that there is something steady to measure.
func hold(t Timbre, r rune, semitones float64) []float64 {
	t.Sustain, t.Scoop = 0.3, 0
	s := NewSynth(t)
	s.Voice(Tone{Mouth: speech.VisemeOf(r), Syllable: speech.Syllable{Pitch: semitones}})
	play(s, 0.3)
	return play(s, 0.2)
}

func peak(x []float64) (p float64) {
	for _, v := range x {
		p = max(p, math.Abs(v))
	}
	return p
}

// loudness is the root-mean-square level of x.
func loudness(x []float64) float64 {
	var sum float64
	for _, v := range x {
		sum += v * v
	}
	return math.Sqrt(sum / float64(len(x)))
}

// level is the strength of x at one frequency, in decibels.
func level(x []float64, hz float64) float64 {
	var re, im float64
	for i, v := range x {
		window := 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(len(x)))
		sin, cos := math.Sincos(2 * math.Pi * hz * float64(i) / SampleRate)
		re += v * window * cos
		im += v * window * sin
	}
	return 20 * math.Log10(4*math.Hypot(re, im)/float64(len(x))+1e-12)
}

// pitch finds the fundamental of x, in Hz, as the lag at which it best
// matches itself.
func pitch(x []float64) float64 {
	best, bestLag := 0.0, 0
	for lag := SampleRate / 1200; lag < SampleRate/100; lag++ {
		var sum float64
		for i := lag; i < len(x); i++ {
			sum += x[i] * x[i-lag]
		}
		if sum > best*1.02 { // prefer the shortest lag among near-equals
			best, bestLag = sum, lag
		}
	}
	return SampleRate / float64(bestLag)
}

func TestSilentWhenTheMouthIsShut(t *testing.T) {
	t.Parallel()
	for _, timbre := range Timbres() {
		s := NewSynth(timbre)
		if p := peak(play(s, 0.2)); p != 0 {
			t.Errorf("%s: a new synth should be silent, got peak %g", timbre.Name, p)
		}
		s.Voice(say('a', 1))
		play(s, 0.05)
		s.Voice(Tone{Syllable: speech.Syllable{Count: 1}})
		play(s, 0.3)
		if p := peak(play(s, 0.1)); p > 1e-4 {
			t.Errorf("%s: sound should die away once the mouth closes, got peak %g", timbre.Name, p)
		}
	}
}

func TestEveryTimbreIsAudibleAndNeverClips(t *testing.T) {
	t.Parallel()
	for _, timbre := range Timbres() {
		for _, r := range "aeiou" {
			s := NewSynth(timbre)
			s.Voice(say(r, 1))
			if p := peak(play(s, 0.3)); p < 0.08 || p > volume {
				t.Errorf("%s: %c peaks at %.3f, want between 0.08 and %.1f", timbre.Name, r, p, volume)
			}
		}
	}
}

// Switching voice should change the character of the sound, not its volume.
func TestTimbresAreAboutAsLoudAsEachOther(t *testing.T) {
	t.Parallel()
	house := loudness(babble(chirp()))
	for _, timbre := range Timbres() {
		if db := 20 * math.Log10(loudness(babble(timbre))/house); math.Abs(db) > 2 {
			t.Errorf("%s is %+.1f dB against %s, want within 2", timbre.Name, db, chirp().Name)
		}
	}
}

func TestPitchFollowsTheTone(t *testing.T) {
	t.Parallel()
	for _, timbre := range Timbres() {
		for _, semitones := range []float64{0, 12, -5} {
			want := timbre.Pitch * math.Pow(2, semitones/12)
			if got := pitch(hold(timbre, 'a', semitones)); math.Abs(got/want-1) > 0.03 {
				t.Errorf("%s: at %+.0f semitones the note is %.0f Hz, want %.0f", timbre.Name, semitones, got, want)
			}
		}
	}
}

// What makes vowels differ is where their energy sits. An open "ah" is
// strong around 1 kHz; a spread "ee" keeps a peak near 1.8 kHz that a pursed
// "oo" lacks.
func TestMouthShapeColoursTheVowel(t *testing.T) {
	t.Parallel()
	sound := map[rune][]float64{}
	for _, r := range "aiu" {
		sound[r] = hold(chirp(), r, 0)
	}
	low, high := 4*chirp().Pitch, 7*chirp().Pitch // the harmonics nearest 1 and 1.8 kHz
	if a, i := level(sound['a'], low), level(sound['i'], low); a < i+10 {
		t.Errorf("around 1 kHz, \"ah\" (%.0f dB) should be much stronger than \"ee\" (%.0f dB)", a, i)
	}
	if i, u := level(sound['i'], high), level(sound['u'], high); i < u+10 {
		t.Errorf("around 1.8 kHz, \"ee\" (%.0f dB) should be much stronger than \"oo\" (%.0f dB)", i, u)
	}
}

// A voice has almost nothing above a few kilohertz; a synth that does
// sounds like a buzzer. Measured against the strongest harmonic, everything
// from 3 kHz up should be far down. The 8-bit voice is exempt: buzzing is
// its job.
func TestNoHarshTop(t *testing.T) {
	t.Parallel()
	for _, timbre := range Timbres() {
		if timbre.Wave == Square {
			continue
		}
		for _, r := range "aeiou" {
			x := hold(timbre, r, 0)
			strongest, top := -200.0, -200.0
			for k := 1.0; k*timbre.Pitch < 6000; k++ {
				l := level(x, k*timbre.Pitch)
				strongest = max(strongest, l)
				if k*timbre.Pitch >= 3000 {
					top = max(top, l)
				}
			}
			if top > strongest-30 {
				t.Errorf("%s, %c: harmonics above 3 kHz are only %.0f dB below the strongest, want at least 30", timbre.Name, r, strongest-top)
			}
		}
	}
}

// Tones change abruptly, sixty times a second, and the voice can be swapped
// mid-word. The sound must not jump: a step between neighbouring samples
// bigger than the waveform itself makes is heard as a click.
func TestNoClicks(t *testing.T) {
	t.Parallel()
	for _, timbre := range Timbres() {
		s := NewSynth(timbre)
		x := make([]float64, 0, 2*SampleRate)
		for i := range 120 {
			tone := Tone{Syllable: speech.Syllable{Count: i / 3, Pitch: float64(i%4*5 - 6)}}
			if i%10 < 7 {
				tone.Mouth = speech.VisemeOf(rune("aiou"[i%4]))
			}
			if i == 60 {
				s.Use(Timbres()[(i/60)%len(Timbres())])
			}
			s.Voice(tone)
			x = append(x, play(s, 1.0/60)...)
		}
		// A square wave is all jumps; allow it the height of its own edges.
		limit := 0.15
		if timbre.Wave == Square {
			limit = 0.3
		}
		for i := 1; i < len(x); i++ {
			if jump := math.Abs(x[i] - x[i-1]); jump > limit {
				t.Fatalf("%s: a jump of %.3f at sample %d would click", timbre.Name, jump, i)
			}
		}
		if peak(x) < 0.08 {
			t.Errorf("%s: the test tones should have been audible", timbre.Name)
		}
	}
}

// A blip is over quickly even if the mouth stays open, so that syllables
// 75 ms apart are heard as separate sounds with quiet between.
func TestBlipsAreSeparate(t *testing.T) {
	t.Parallel()
	for _, timbre := range Timbres() {
		if timbre.Sustain > 0 {
			continue
		}
		s := NewSynth(timbre)
		s.Voice(say('a', 1))
		first := loudness(play(s, 0.03))
		play(s, 0.03+2*timbre.Ring)
		between := loudness(play(s, 0.015))
		s.Voice(say('a', 2))
		second := loudness(play(s, 0.03))
		if between > first/5 || between > second/5 {
			t.Errorf("%s: syllables should be separated by near-silence: %.4f, then %.4f, then %.4f", timbre.Name, first, between, second)
		}
	}
}

func TestStressedSyllablesRingLonger(t *testing.T) {
	t.Parallel()
	tail := func(stress float64) float64 {
		s := NewSynth(chirp())
		s.Voice(Tone{Mouth: speech.VisemeOf('a'), Syllable: speech.Syllable{Count: 1, Stress: stress}})
		play(s, 0.08)
		return loudness(play(s, 0.03))
	}
	if passing, landed := tail(0), tail(1); landed < 4*passing {
		t.Errorf("80 ms in, a stressed syllable should still be sounding: %.4f against %.4f in passing", landed, passing)
	}
}

// A blip starts off its note and slides onto it: from below for most
// voices, from above for one whose Scoop is negative.
func TestBlipsSlideIntoTheirNote(t *testing.T) {
	t.Parallel()
	for _, timbre := range Timbres() {
		if timbre.Sustain > 0 || timbre.Scoop == 0 {
			continue
		}
		s := NewSynth(timbre)
		s.Voice(say('a', 1))
		play(s, 0.002)
		start := s.hz
		play(s, 0.1)
		if below := start < timbre.Pitch; below != (timbre.Scoop > 0) || math.Abs(start/timbre.Pitch-1) < 0.05 {
			t.Errorf("%s: with a scoop of %+.1f, the blip started at %.0f Hz for a note of %.0f", timbre.Name, timbre.Scoop, start, timbre.Pitch)
		}
		if math.Abs(s.hz/timbre.Pitch-1) > 0.02 {
			t.Errorf("%s: the blip should settle on %.0f Hz, got %.0f", timbre.Name, timbre.Pitch, s.hz)
		}
	}
}

func TestSustainingVoicesHoldTheNote(t *testing.T) {
	t.Parallel()
	for _, timbre := range Timbres() {
		if timbre.Sustain == 0 {
			continue
		}
		s := NewSynth(timbre)
		s.Voice(say('a', 1))
		play(s, 0.5)
		settled := peak(play(s, 0.05))
		if settled < 0.03 {
			t.Errorf("%s: a held vowel should keep sounding, got peak %.3f", timbre.Name, settled)
		}
		s.Voice(say('a', 2))
		if struck := peak(play(s, 0.05)); struck < 1.5*settled {
			t.Errorf("%s: a new syllable should land louder than a held one: %.3f against %.3f", timbre.Name, struck, settled)
		}
	}
}

func TestReadFillsWholeSamples(t *testing.T) {
	t.Parallel()
	if n, err := NewSynth(chirp()).Read(make([]byte, 10)); n != 8 || err != nil {
		t.Errorf("Read into 10 bytes = %d, %v; want 8, nil", n, err)
	}
}

func BenchmarkSecondOfSound(b *testing.B) {
	s := NewSynth(chirp())
	s.Voice(say('a', 1))
	buf := make([]byte, 4*SampleRate)
	for b.Loop() {
		_, _ = s.Read(buf) // a synth never fails to sound
	}
}
