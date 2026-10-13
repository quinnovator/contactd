package sound

import "math"

// Timbre is a voice the synth can speak in: the same melody and the same
// mouth, heard through a different throat.
type Timbre struct {
	Name    string
	Pitch   float64 // resting note, in Hz
	Wave    Wave    // the raw buzz the voice is carved from
	Vowel   float64 // how much the mouth colours the sound: 1 is all vowel, 0 the bare wave
	Throat  float64 // size of the creature: above 1 is larger, with deeper vowels
	Ceiling float64 // Hz above which the sound is rounded away
	Ring    float64 // seconds a syllable rings before dying away
	Sustain float64 // how loud the voice stays between syllables; 0 makes each a separate blip
	Scoop   float64 // semitones below its note a syllable starts; negative starts above and falls
	Gain    float64 // evens out loudness between timbres
}

// Timbres returns the built-in voices, in the order they are listed. The
// slice is the caller's own.
func Timbres() []Timbre {
	return []Timbre{
		// The house voice: vowel resonances matched to recorded Animalese, each
		// syllable a short blip that chirps up into its note.
		{Name: "chirp", Pitch: 255, Wave: Saw, Vowel: 1, Throat: 1, Ceiling: 1900, Ring: 0.022, Scoop: 2, Gain: 1.8},
		// Something very small, talking fast.
		{Name: "pip", Pitch: 350, Wave: Saw, Vowel: 1, Throat: 0.8, Ceiling: 2300, Ring: 0.016, Scoop: 3, Gain: 2.2},
		// A soft hum whose syllables run together.
		{Name: "mellow", Pitch: 210, Wave: Triangle, Vowel: 0.7, Throat: 1.1, Ceiling: 1500, Ring: 0.05, Sustain: 0.45, Scoop: 1, Gain: 1.1},
		// Plain square-wave beeps, as a text box in an 8-bit game would make.
		{Name: "retro", Pitch: 300, Wave: Square, Vowel: 0.25, Throat: 1, Ceiling: 3200, Ring: 0.02, Gain: 0.62},
		// Something large and unhurried, its syllables falling instead of rising.
		{Name: "deep", Pitch: 150, Wave: Saw, Vowel: 1, Throat: 1.3, Ceiling: 1500, Ring: 0.035, Scoop: -1.5, Gain: 1.4},
	}
}

// Wave is the shape of the source's cycle.
type Wave int

const (
	Saw      Wave = iota // every harmonic: bright, the most voice-like once filtered
	Square               // odd harmonics only: hollow, the sound of old game consoles
	Triangle             // odd harmonics, fading fast: close to a pure tone
)

// at is the wave's value at a phase from 0 to 1, advancing step per sample.
// The sudden edges of the saw and square are rounded off just enough to keep
// them from aliasing into a harsh, unmusical ring.
func (w Wave) at(phase, step float64) float64 {
	switch w {
	case Square:
		half := math.Mod(phase+0.5, 1)
		return math.Copysign(1, 0.5-phase) + blep(phase, step) - blep(half, step)
	case Triangle:
		return 1 - 4*math.Abs(phase-0.5)
	case Saw:
	}
	return 2*phase - 1 - blep(phase, step)
}

// blep is the correction that rounds off an instantaneous drop of 2 at phase
// zero (seen from phase t, advancing dt per sample).
func blep(t, dt float64) float64 {
	switch {
	case t < dt:
		t /= dt
		return t + t - t*t - 1
	case t > 1-dt:
		t = (t - 1) / dt
		return t*t + t + t + 1
	}
	return 0
}
