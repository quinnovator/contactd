// Package sound gives the avatar a voice: wordless, tuneful babble of the
// kind video-game characters speak in.
//
// It works the way a throat does. A buzzing source sets the pitch, and a
// bank of resonators tuned by the shape of the mouth turns the buzz into a
// vowel. Because the mouth shape driving it is the one on screen, what you
// hear is what you see: open wide for "ah", pursed for "oo", silent when the
// lips close.
package sound

import (
	"encoding/binary"
	"math"
	"sync"

	"github.com/quinnovator/contactd/internal/tui/speech"
)

// SampleRate is the rate, in Hz, at which a Synth produces samples.
const SampleRate = 44100

// Tone is what the voice should be sounding at this moment.
type Tone struct {
	Mouth    speech.Viseme   // sets the vowel, and gates the sound
	Syllable speech.Syllable // sets the note, and when it is struck
}

const (
	volume       = 0.3   // peak output, leaving plenty of headroom
	retune       = 16    // samples between updates of the resonators
	lean         = 1.5   // how much longer a fully stressed syllable rings
	vibratoRate  = 5.5   // Hz
	vibratoDepth = 0.008 // as a fraction of the pitch: about a seventh of a semitone
)

// Time constants, as the fraction of the remaining distance covered each
// sample. Tones arrive only sixty times a second; easing toward them turns
// the steps into curves, and keeps every change free of clicks.
//
//nolint:gochecknoglobals // constants that take a function to work out
var (
	mouthEase  = perSample(0.012)
	pitchGlide = perSample(0.02)
	attackEase = perSample(0.003)
)

func perSample(seconds float64) float64 { return 1 - math.Exp(-1/(seconds*SampleRate)) }

// Synth is the voice. It is an io.Reader of mono 32-bit float samples that
// never ends; Voice changes what it is sounding and Use what it sounds like,
// from any goroutine.
type Synth struct {
	mu     sync.Mutex
	tone   Tone
	timbre Timbre

	// Everything below belongs to the reader.
	Timbre               // the timbre in use
	ceiling      float64 // its low-pass, as a per-sample ease
	open, spread float64 // the mouth, eased
	hz           float64 // the pitch, gliding
	waver        float64 // vibrato: the factor the pitch is bent by just now
	phase        float64 // position in the source's cycle, 0 to 1
	syllable     int
	strike       float64 // jumps to 1 as a syllable begins, then fades...
	fade         float64 // ...at this rate
	accent       float64 // follows strike, with a soft attack
	formants     [2]resonator
	soft         [2]float64 // two stages of low-pass, rounding off the top
	clock        int
	seed         uint32
}

// NewSynth returns a silent voice with the given timbre.
func NewSynth(t Timbre) *Synth { return &Synth{timbre: t, hz: t.Pitch, waver: 1, seed: 1} }

// Use changes the timbre the voice speaks in.
func (s *Synth) Use(t Timbre) {
	s.mu.Lock()
	s.timbre = t
	s.mu.Unlock()
}

// Voice sets the tone to sound from now on.
func (s *Synth) Voice(t Tone) {
	s.mu.Lock()
	s.tone = t
	s.mu.Unlock()
}

// Read fills p with samples: little-endian float32, one channel.
func (s *Synth) Read(p []byte) (int, error) {
	s.mu.Lock()
	tone := s.tone
	s.Timbre = s.timbre
	s.mu.Unlock()

	s.ceiling = perSample(1 / (2 * math.Pi * s.Ceiling))
	hz := s.Pitch * math.Pow(2, tone.Syllable.Pitch/12)
	if tone.Syllable.Count != s.syllable {
		// Strike the new syllable. A voice of separate blips starts each
		// one off its note and slides in; one that sustains glides there
		// from wherever it was.
		s.syllable, s.strike = tone.Syllable.Count, 1
		s.fade = perSample(s.Ring * (1 + lean*tone.Syllable.Stress))
		if s.Sustain == 0 {
			s.hz = hz * math.Pow(2, -s.Scoop/12)
		}
	}
	n := len(p) / 4
	for i := range n {
		binary.LittleEndian.PutUint32(p[4*i:], math.Float32bits(float32(s.sample(tone.Mouth, hz))))
	}
	return 4 * n, nil
}

// sample computes the next sample, on the way to the given mouth and pitch.
func (s *Synth) sample(mouth speech.Viseme, hz float64) float64 {
	s.open += (mouth.Open - s.open) * mouthEase
	s.spread += (mouth.Spread - s.spread) * mouthEase
	s.hz += (hz - s.hz) * pitchGlide
	s.accent += (s.strike - s.accent) * attackEase
	s.strike -= s.strike * s.fade

	if s.clock%retune == 0 {
		s.shape()
	}
	s.clock++

	// The source is a plain wave, rich in harmonics for the resonators to
	// pick from, with a little breath mixed in.
	step := s.hz * s.waver / SampleRate
	if s.phase += step; s.phase >= 1 {
		s.phase--
	}
	buzz := s.Wave.at(s.phase, step) + 0.03*s.breath()

	// In a voice the resonators do most of the talking: the vowel is far
	// louder than the note it is sung on, and only a trace of the bare note
	// stays underneath for body. A less vocal timbre lets more of the raw
	// wave straight through.
	out := (1-s.Vowel)*buzz + 0.04*math.Sin(2*math.Pi*s.phase)
	for i := range s.formants {
		out += s.Vowel * s.formants[i].filter(buzz)
	}
	// Voices have very little above a couple of kilohertz. Leaving the
	// wave's upper harmonics in is what makes a synth sound like a buzzer.
	s.soft[0] += (out - s.soft[0]) * s.ceiling
	s.soft[1] += (s.soft[0] - s.soft[1]) * s.ceiling

	// The mouth gates the sound: nothing through closed lips, full voice
	// once they are well parted. Within that, each syllable is struck and
	// then dies away, which is what separates it from the next.
	loud := smooth(s.open/0.2) * (s.Sustain + (1.2-s.Sustain)*s.accent)
	return volume * math.Tanh(s.Gain*loud*s.soft[1])
}

// shape tunes the resonators to the mouth, as the tongue and lips tune the
// throat. Opening the jaw raises the first formant; spreading the lips raises
// the second and pursing them lowers it. Those two between them are what
// make one vowel sound different from another, and a larger throat lowers
// both. It also advances the vibrato,
// which like the mouth moves slowly enough to update only now and then.
func (s *Synth) shape() {
	s.formants[0].tune((360+700*s.open)/s.Throat, 6, 2)
	s.formants[1].tune((1400+500*s.spread)/s.Throat, 6, 2)
	s.waver = 1 + vibratoDepth*math.Sin(2*math.Pi*vibratoRate*float64(s.clock)/SampleRate)
}

// breath is white noise from a xorshift generator, in [-1, 1].
func (s *Synth) breath() float64 {
	s.seed ^= s.seed << 13
	s.seed ^= s.seed >> 17
	s.seed ^= s.seed << 5
	return float64(s.seed)/(1<<31) - 1
}

// resonator is a two-pole band-pass filter: it rings at one frequency and
// lets little else through.
type resonator struct {
	b, a1, a2      float64
	x1, x2, y1, y2 float64
}

// tune centres the resonator on hz. A higher q rings longer and more
// narrowly; gain scales its output.
func (r *resonator) tune(hz, q, gain float64) {
	sin, cos := math.Sincos(2 * math.Pi * hz / SampleRate)
	alpha := sin / (2 * q)
	r.b = gain * alpha / (1 + alpha)
	r.a1 = -2 * cos / (1 + alpha)
	r.a2 = (1 - alpha) / (1 + alpha)
}

func (r *resonator) filter(x float64) float64 {
	y := r.b*(x-r.x2) - r.a1*r.y1 - r.a2*r.y2
	r.x2, r.x1 = r.x1, x
	r.y2, r.y1 = r.y1, y
	return y
}

func smooth(t float64) float64 {
	t = min(max(t, 0), 1)
	return t * t * (3 - 2*t)
}
