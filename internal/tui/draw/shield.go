package draw

import (
	"html"
	"strings"
)

// The Markdown renderer reads character references. It turns "&#27;" into
// the escape character, "&#x202e;" into a bidi override and "&#x10eeee;" into
// the character a terminal draws another program's image with. A reply is
// made safe before it is rendered, so a reference is a way around that: the
// text holds no escape character, and the renderer writes one.
//
// The renderer also reads a reference that has no semicolon, lets the digits
// of one overflow, and in some places joins pieces of the text before it
// looks, so that one can be written in two halves. So nothing is left to it
// but what is known to be harmless: shield keeps an ampersand only where it
// begins a whole reference, ended by its semicolon, to characters that are
// safe to draw. Every other ampersand is swapped for a stand-in, a character
// the text does not hold, which the renderer has no reason to touch. unshield
// puts the ampersand back in what the renderer made, so the text reads as it
// was written.

const (
	// standInFirst and standInLast are the characters a stand-in is chosen
	// from: a plane of private-use characters, four bytes each, of which a
	// reply cannot hold them all.
	standInFirst = rune(0xf0000)
	standInLast  = rune(0xffffd)
	// standInLead is the first byte of every one of them.
	standInLead = 0xf3
)

// shield returns Markdown that is safe to hand to the renderer, and the
// stand-in that was put in the place of ampersands. The stand-in is "" when
// the text has no ampersand; then the text is returned as it is. ok is false
// when no stand-in could be found, and the text is not to be rendered.
func (r *Renderer) shield(src string) (out, mark string, ok bool) {
	i := strings.IndexByte(src, '&')
	if i < 0 {
		return src, "", true
	}
	if mark = standIn(src); mark == "" {
		return "", "", false
	}
	var b strings.Builder
	b.Grow(len(src) + 2*len(mark))
	for ; i >= 0; i = strings.IndexByte(src, '&') {
		b.WriteString(src[:i])
		ref := src[i : i+reference(src[i:])]
		if r.harmless(ref, mark) {
			b.WriteString(ref)
		} else {
			b.WriteString(mark)
			b.WriteString(ref[1:])
		}
		src = src[i+len(ref):]
	}
	b.WriteString(src)
	return b.String(), mark, true
}

// unshield puts the ampersands back where their stand-in is.
func unshield(s, mark string) string {
	if mark == "" {
		return s
	}
	return strings.ReplaceAll(s, mark, "&")
}

// standIn returns a character that s does not hold, or "" when it holds
// every one there is to choose from.
func standIn(s string) string {
	if strings.IndexByte(s, standInLead) < 0 {
		return string(standInFirst)
	}
	used := make(map[rune]bool)
	for _, c := range s {
		if c >= standInFirst && c <= standInLast {
			used[c] = true
		}
	}
	for c := standInFirst; c <= standInLast; c++ {
		if !used[c] {
			return string(c)
		}
	}
	return ""
}

// reference returns the length of what could be read as one character
// reference at the start of s, which begins with an ampersand: the ampersand,
// then a number sign and digits or a name, then a semicolon when there is
// one. It takes in at least everything the renderer would, and never a second
// ampersand.
func reference(s string) int {
	j := 1
	if j < len(s) && s[j] == '#' {
		j++
		if j < len(s) && (s[j] == 'x' || s[j] == 'X') {
			j++
		}
		// The digits of a decimal number are digits of a hexadecimal one too.
		for j < len(s) && (isDigit(s[j]) || isHexLetter(s[j])) {
			j++
		}
	} else {
		for j < len(s) && (isDigit(s[j]) || isLetter(s[j])) {
			j++
		}
	}
	if j < len(s) && s[j] == ';' {
		j++
	}
	return j
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isLetter(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }

func isHexLetter(c byte) bool { return (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') }

// harmless reports whether ref may be left for the renderer to read: it is a
// reference, its semicolon ends it, and what it stands for is drawn as it is.
// Without the semicolon it is not a reference in Markdown, and what follows
// it could become part of it.
func (r *Renderer) harmless(ref, mark string) bool {
	if !strings.HasSuffix(ref, ";") {
		return false
	}
	// This is the function the renderer reads references with.
	to := html.UnescapeString(ref)
	return to != ref && r.drawn(to) && !strings.Contains(to, mark)
}

// sound reports whether what a renderer made holds nothing a terminal would
// act on but what the client writes itself: a style and a hyperlink. It is
// the second lock on the door shield closes. What fails is not drawn.
func (r *Renderer) sound(out string) bool {
	for {
		i := strings.IndexByte(out, 0x1b)
		if i < 0 {
			break
		}
		n := r.sequence(out[i:])
		if n == 0 || !r.drawn(out[:i]) {
			r.n.unsound++
			return false
		}
		out = out[i+n:]
	}
	if !r.drawn(out) {
		r.n.unsound++
		return false
	}
	return true
}

// drawn reports whether text is only characters that are drawn: making it
// safe would change nothing.
func (r *Renderer) drawn(s string) bool {
	return r.clean(s) == s
}

// sequence returns the length of the style or hyperlink sequence that s
// begins with, or 0 when it begins with anything else.
func (r *Renderer) sequence(s string) int {
	if strings.HasPrefix(s, linkStart) {
		at, size := stringEnd(s[len(linkStart):])
		if at < 0 || !r.drawn(s[len(linkStart):len(linkStart)+at]) {
			return 0
		}
		return len(linkStart) + at + size
	}
	if end := strings.IndexByte(s, 'm'); end >= 0 && strings.HasPrefix(s, "\x1b[") && isStyle(s[:end+1]) {
		return end + 1
	}
	return 0
}
