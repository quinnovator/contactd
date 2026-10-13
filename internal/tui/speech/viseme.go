// Package speech turns a stream of text into something that can be spoken: a
// steady cadence, a mouth shape for every sound, and the beats of a sentence.
package speech

import "unicode"

// Viseme is the mouth shape that goes with a sound.
type Viseme struct {
	Open   float64 // how far the jaw drops, 0 to 1
	Spread float64 // lips pursed (-1) through neutral to stretched wide (+1)
}

// Rest is the closed, relaxed mouth between sounds.
//
//nolint:gochecknoglobals // a shape by its name, only read
var Rest = Viseme{}

//nolint:gochecknoglobals // the table of mouth shapes, only read
var (
	consonant = Viseme{Open: 0.28, Spread: 0.25}
	vowels    = [...]Viseme{
		{Open: 1.00, Spread: 0.15},  // a
		{Open: 0.55, Spread: 0.70},  // e
		{Open: 0.32, Spread: 1.00},  // i
		{Open: 0.80, Spread: -0.80}, // o
		{Open: 0.38, Spread: -1.00}, // u
	}
)

// VisemeOf maps a rune to the mouth shape of its most likely sound. It reads
// spelling, not phonetics: enough for lips that move believably with the text.
func VisemeOf(r rune) Viseme {
	if v := vowel(r); v >= 0 {
		return vowels[v]
	}
	switch r = unicode.ToLower(r); {
	case r == 'm', r == 'b', r == 'p':
		return Rest // lips pressed together
	case r == 'f', r == 'v':
		return Viseme{Open: 0.10, Spread: 0.35}
	case unicode.IsLetter(r):
		return consonant
	}
	return Rest
}

// vowel reports which of the five vowel shapes r is sounded with, or -1 if r
// is not a vowel.
func vowel(r rune) int {
	switch unicode.ToLower(r) {
	case 'a':
		return 0
	case 'e':
		return 1
	case 'i', 'y':
		return 2
	case 'o':
		return 3
	case 'u', 'w':
		return 4
	}
	if r > unicode.MaxASCII && unicode.IsLetter(r) || unicode.IsDigit(r) {
		// A script we cannot sound out still deserves a moving mouth: give
		// each rune a stable vowel of its own.
		return int(r) % len(vowels)
	}
	return -1
}
