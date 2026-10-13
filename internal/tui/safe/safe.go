// Package safe makes text from outside the client safe to draw.
//
// A model, a tool, a mod or a shell command can produce escape sequences.
// Printed as they are they would move the cursor, recolour the screen, set
// the window title, or show another program's image. Nothing here tries to
// parse a sequence: it drops the characters that start or end one, and the
// printable rest is shown as text. A sequence that is cut short is therefore
// as harmless as a whole one.
package safe

import (
	"strings"
	"unicode/utf8"
)

const (
	// tabWidth is how many spaces a tab becomes.
	tabWidth = "    "
	// placeholder is the character kitty replaces with a cell of an image.
	// Text that could print it could show an image another program sent.
	placeholder = 0x10EEEE
	// zwj joins emoji into one picture, and vs16 asks for the emoji form of
	// a character. Terminals that count cells with wcwidth disagree with
	// each other about both.
	zwj  = 0x200D
	vs16 = 0xFE0F
	// lineSep and paraSep are Unicode's own line and paragraph separators.
	// Line treats them as newlines.
	lineSep = 0x2028
	paraSep = 0x2029
	// lineEnds is what Line takes off the end of its input.
	lineEnds = "\n\u2028\u2029"
)

// Clean makes server text safe to draw. CRLF becomes LF, a tab four spaces.
// It drops C0 (except LF), DEL, C1, bidi overrides and isolates (U+202A-202E,
// U+2066-2069) and U+10EEEE. With dropJoiners it also drops U+200D and U+FE0F,
// so grapheme and wcwidth widths agree in most text.
//
// A byte that is not UTF-8 becomes U+FFFD, so the result is always valid
// UTF-8. Text with nothing to change is returned as it is.
func Clean(s string, dropJoiners bool) string {
	i := firstChange(s, dropJoiners)
	if i < 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	b.WriteString(s[:i])
	// Ranging over a string yields U+FFFD for each byte that is not UTF-8,
	// and WriteRune encodes it properly.
	for _, r := range s[i:] {
		switch {
		case r == '\t':
			b.WriteString(tabWidth)
		case dropped(r, dropJoiners):
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Line is for mod and shell output: Clean with dropJoiners, newlines to
// spaces, at most maxRunes runes. With ascii, a rune above 0x7F becomes '?'.
// The result never contains ESC (0x1B) or a C1 byte sequence.
//
// A newline is LF, U+2028 or U+2029; Clean has already turned CRLF into LF.
// Newlines at the very end are dropped, not turned into spaces, so the output
// of a command that prints "main\n" is "main". No space is ever trimmed, so
// that pieces of one line can be joined as they were written.
func Line(s string, maxRunes int, ascii bool) string {
	if maxRunes <= 0 {
		return ""
	}
	var b strings.Builder
	n := 0
	for _, r := range strings.TrimRight(Clean(s, true), lineEnds) {
		if n == maxRunes {
			break
		}
		switch {
		case r == '\n', r == lineSep, r == paraSep:
			r = ' '
		case ascii && r >= utf8.RuneSelf:
			r = '?'
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

// firstChange returns the index of the first byte Clean would not copy as it
// is, or -1 when there is none.
func firstChange(s string, dropJoiners bool) int {
	for i := 0; i < len(s); {
		if c := s[i]; c < utf8.RuneSelf {
			if c != '\n' && (c < 0x20 || c == 0x7F) {
				return i
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if (r == utf8.RuneError && size == 1) || dropped(r, dropJoiners) {
			return i
		}
		i += size
	}
	return -1
}

// dropped reports whether a character is left out. A tab is not asked about:
// it is replaced.
func dropped(r rune, dropJoiners bool) bool {
	switch {
	case r == '\n':
		return false
	case r < 0x20, r == 0x7F:
		// C0 and DEL: ESC, BEL, CR, backspace and the rest.
		return true
	case r >= 0x80 && r <= 0x9F:
		// C1: one-character forms of CSI, OSC, DCS, APC and ST.
		return true
	case r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069:
		// Bidirectional overrides and isolates can make text read as
		// something it is not.
		return true
	case r == placeholder:
		return true
	case dropJoiners && (r == zwj || r == vs16):
		return true
	default:
		return false
	}
}
