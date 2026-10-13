package theme

import (
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"

	"github.com/quinnovator/contactd/internal/tui/safe"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
)

const (
	// reset ends every style in force.
	reset = "\x1b[m"
	// reverseOn switches reverse video on, and reverseOff is the parameter
	// that switches it off again.
	reverseOn  = "\x1b[7m"
	reverseOff = "27"
	// backgroundOff is the parameter that puts the terminal's own
	// background back.
	backgroundOff = "49"
	// spreadGap is the least room between the two sides of a row.
	spreadGap = 2
	// tabStop is what a tab is drawn as, in spaces.
	tabStop = "    "
	// joinTries is how often a row is measured and put right after its
	// parts are joined. The first try is right for all but contrived text.
	joinTries = 3
)

// Width returns the number of cells s takes, counted the way the terminal
// counts: a character at a time, as wcwidth does, or a grapheme cluster at a
// time once the terminal has said it does that (Caps.Width). Escape sequences
// take no cells.
//
// Counted by cluster, two texts joined are not always as wide as the two
// apart: a combining character at the start of the second becomes part of the
// last character of the first. So a row is measured after it is put together,
// which is what Pad, Spread, Box, Rule and Bar do.
func (t *Theme) Width(s string) int {
	if t.Caps.Width == termcaps.WidthGrapheme {
		return ansi.StringWidth(s)
	}
	return ansi.StringWidthWc(s)
}

// step splits the first piece off s, which is not empty: an escape sequence,
// or one character of text and the cells it takes. A character is a grapheme
// cluster, found and measured by the function Width measures with, so the
// cells of the pieces add up to the Width of the whole.
func (t *Theme) step(s string) (piece string, cells int, escape bool) {
	switch c := s[0]; {
	case c == 0x1b:
		return s[:escapeLen(s)], 0, true
	case c < 0x20 || c == 0x7f:
		return s[:1], 0, false
	}
	method := ansi.WcWidth
	if t.Caps.Width == termcaps.WidthGrapheme {
		method = ansi.GraphemeWidth
	}
	cluster, cells := ansi.FirstGraphemeCluster(s, method)
	if cluster == "" {
		return s[:1], 0, false
	}
	return cluster, cells, false
}

// escapeLen is the length of the escape sequence s begins with. A sequence
// that is cut short runs to the end of s.
func escapeLen(s string) int {
	if len(s) < 2 {
		return len(s)
	}
	switch s[1] {
	case '[':
		// A control sequence: parameters, then one final byte.
		i := 2
		for i < len(s) && s[i] >= 0x20 && s[i] <= 0x3f {
			i++
		}
		if i < len(s) && s[i] >= 0x40 && s[i] <= 0x7e {
			i++
		}
		return i
	case ']', 'P', '_', '^', 'X':
		// A string sequence: it runs to BEL, or to ESC and a backslash.
		for i := 2; i < len(s); i++ {
			if s[i] == 0x07 {
				return i + 1
			}
			if s[i] == 0x1b {
				if i+1 < len(s) && s[i+1] == '\\' {
					return i + 2
				}
				return i
			}
		}
		return len(s)
	}
	// Any other escape: intermediate bytes, then one final byte.
	i := 1
	for i < len(s) && s[i] >= 0x20 && s[i] <= 0x2f {
		i++
	}
	if i < len(s) && s[i] >= 0x30 && s[i] <= 0x7e {
		i++
	}
	return i
}

// Truncate cuts one line of text to at most w cells. Text that was cut ends
// in G.Ellipsis, which counts towards w; where not even the ellipsis fits,
// the text is cut with nothing after it. Escape sequences are kept, those
// after the cut too, so a style that was switched on is still switched off.
//
// A style that s switches on and never off is still on at the end of the
// result: Truncate and Pad do not close it, Spread and Box do.
//
// The result fits whatever s holds, and it is one line. A tab becomes four
// spaces and a newline one; any other control character is dropped, since a
// terminal would act on it and not draw it. A byte that is not UTF-8 becomes
// U+FFFD ("?" with ASCII glyphs). Text with a broken escape sequence in it,
// which cannot be measured, is drawn without any of its escape sequences.
func (t *Theme) Truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = t.tidy(s)
	if out, ok := t.cut(s, w); ok {
		return out
	}
	if out, ok := t.cut(plain(s), w); ok {
		return out
	}
	return ""
}

// cut is Truncate for text that can be measured. ok is false when the result
// does not fit after all, because the cells of its pieces did not add up.
func (t *Theme) cut(s string, w int) (out string, ok bool) {
	if t.Width(s) <= w {
		return s, true
	}
	tail := t.G.Ellipsis
	tw := t.Width(tail)
	if tw > w {
		tail, tw = "", 0
	}
	limit := w - tw

	var b strings.Builder
	used, cut := 0, false
	for rest := s; rest != ""; {
		piece, cells, escape := t.step(rest)
		rest = rest[len(piece):]
		switch {
		case escape:
			b.WriteString(piece)
		case cut:
		case used+cells > limit:
			b.WriteString(tail)
			cut = true
		default:
			b.WriteString(piece)
			used += cells
		}
	}
	out = b.String()
	return out, t.Width(out) <= w
}

// tidy returns s as text one row can hold: valid UTF-8, with no control
// character left for the terminal to act on. Width counts a tab, a newline or
// a backspace as no cells, and a terminal moves its cursor for each, so a row
// that kept one would not end where it was measured to end.
//
// A tab becomes four spaces and a newline one space. Every other control
// character goes: C0, DEL, and C1, which some terminals take for the start of
// an escape sequence. Escape sequences are kept. Inside one, only the ESC and
// the BEL that end it are kept of the control characters.
func (t *Theme) tidy(s string) string {
	if tidied(s) {
		return s
	}
	s = t.valid(s)
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		switch c := s[i]; {
		case c == 0x1b:
			n := escapeLen(s[i:])
			writeEscape(&b, s[i:i+n])
			i += n
		case c == '\t':
			b.WriteString(tabStop)
			i++
		case c == '\n':
			b.WriteByte(' ')
			i++
		case c < 0x20 || c == 0x7f:
			i++
		case c == 0xc2 && i+1 < len(s) && s[i+1] < 0xa0:
			i += 2 // U+0080 to U+009F
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// tidied reports whether tidy would return s as it is.
func tidied(s string) bool {
	for i := range len(s) {
		switch c := s[i]; {
		case c < 0x20 && c != 0x1b, c == 0x7f:
			return false
		case c == 0xc2 && i+1 < len(s) && s[i+1] < 0xa0:
			return false
		}
	}
	return utf8.ValidString(s)
}

// writeEscape writes one escape sequence, which is valid UTF-8, without the
// control characters inside it: all but the ESC it starts with, and the BEL
// or the ESC and backslash it ends with.
func writeEscape(b *strings.Builder, seq string) {
	for i := 0; i < len(seq); i++ {
		switch c := seq[i]; {
		case i == 0:
			b.WriteByte(c)
		case c == 0x07 && i == len(seq)-1, c == 0x1b && i == len(seq)-2:
			b.WriteByte(c)
		case c < 0x20 || c == 0x7f:
		case c == 0xc2 && i+1 < len(seq) && seq[i+1] < 0xa0:
			i++ // U+0080 to U+009F
		default:
			b.WriteByte(c)
		}
	}
}

// valid returns s as UTF-8: a run of bytes that is not becomes U+FFFD, or "?"
// with ASCII glyphs, where nothing but ASCII is added to a row. A stray byte
// cannot be measured, because what it is depends on the bytes after it.
func (t *Theme) valid(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	if t.Caps.Glyphs != termcaps.GlyphsUnicode {
		return strings.ToValidUTF8(s, "?")
	}
	return strings.ToValidUTF8(s, string(utf8.RuneError))
}

// plain is what is drawn of text whose escape sequences cannot be trusted:
// its characters, on one line, and nothing else.
func plain(s string) string {
	return strings.ReplaceAll(safe.Clean(ansi.Strip(s), false), "\n", " ")
}

// Pad truncates or pads one line of text to exactly w cells. The padding is
// spaces on the right. The text is made one line as Truncate makes it, and
// like Truncate, Pad draws text that cannot be measured without its escape
// sequences, so the result is w cells wide whatever s holds.
func (t *Theme) Pad(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if out, ok := t.pad(t.Truncate(s, w), w); ok {
		return out
	}
	if out, ok := t.pad(t.Truncate(plain(t.tidy(s)), w), w); ok {
		return out
	}
	return strings.Repeat(" ", w)
}

// pad adds spaces to s, which is at most w cells wide, and measures the
// result. One pass is enough unless the text ends in a character that takes
// the space after it into its own cluster. ok is false when no number of
// spaces makes the row w cells wide: the text ends in half an escape
// sequence, which swallows them.
func (t *Theme) pad(s string, w int) (out string, ok bool) {
	for range joinTries {
		short := w - t.Width(s)
		if short == 0 {
			return s, true
		}
		if short < 0 {
			return s, false
		}
		s += strings.Repeat(" ", short)
	}
	return s, t.Width(s) == w
}

// Wrap breaks text into lines of at most w cells. It breaks at spaces, and
// inside a word only when the word is longer than a line. A newline in s
// starts a new line, and the spaces a line begins with are kept, so text that
// was indented stays indented. No text is lost but the spaces at a break, and
// a character wider than the whole width, which is cut.
//
// A style in force where a line breaks is switched off at the end of that
// line and on again at the start of the next, so each line can be drawn alone.
// A tab is four spaces, and other control characters are dropped as Truncate
// drops them. A width under 1 is taken as 1.
func (t *Theme) Wrap(s string, w int) []string {
	w = max(w, 1)
	var out []string
	for line := range strings.SplitSeq(s, "\n") {
		out = t.wrapLine(out, t.tidy(line), w)
	}
	return t.finish(out, w)
}

// wrapLine wraps one line of the source and appends the result to out.
//
// The line being built is kept in two parts: head is what is settled, and
// word is the word still being read. When the word no longer fits it moves to
// the next line, unless head is nothing but the line's indentation: then the
// word is as long as a line and is broken where it stands.
func (t *Theme) wrapLine(out []string, s string, w int) []string {
	var head, word strings.Builder
	headW, wordW := 0, 0
	text := false  // head holds more than indentation
	broke := false // the line was broken at a space: the spaces after it go too
	for s != "" {
		seq, cells, escape := t.step(s)
		s = s[len(seq):]
		switch {
		case escape:
			word.WriteString(seq)
		case seq == " ":
			if broke {
				continue
			}
			head.WriteString(word.String())
			headW, text = headW+wordW, text || wordW > 0
			word.Reset()
			wordW = 0
			switch {
			case headW < w:
				head.WriteByte(' ')
				headW++
			case text:
				out = append(out, strings.TrimRight(head.String(), " "))
				head.Reset()
				headW, text, broke = 0, false, true
			}
		default:
			broke = false
			if headW+wordW+cells > w {
				switch {
				case text:
					out = append(out, strings.TrimRight(head.String(), " "))
					head.Reset()
					headW, text = 0, false
					if wordW > 0 && wordW+cells > w {
						out = append(out, word.String())
						word.Reset()
						wordW = 0
					}
				case wordW > 0:
					out = append(out, head.String()+word.String())
					head.Reset()
					word.Reset()
					headW, wordW = 0, 0
				default:
					// Indentation as wide as the line leaves no room
					// for the text, so the indentation goes.
					head.Reset()
					headW = 0
				}
			}
			word.WriteString(seq)
			wordW += cells
		}
	}
	rest := head.String() + word.String()
	if broke && len(out) > 0 {
		// Nothing followed the last break but escape sequences, which
		// belong to the line that was broken.
		out[len(out)-1] += rest
		return out
	}
	return append(out, rest)
}

// Hardwrap breaks text into lines of at most w cells, wherever the line is
// full: in the middle of a word, and never at a space in preference. It is
// for code and for a tool's output, where every space counts. A newline in s
// starts a new line. Styles are carried across breaks as Wrap carries them,
// a tab is four spaces, other control characters are dropped, and a width
// under 1 is taken as 1.
func (t *Theme) Hardwrap(s string, w int) []string {
	w = max(w, 1)
	var out []string
	for line := range strings.SplitSeq(s, "\n") {
		line = t.tidy(line)
		var b strings.Builder
		used := 0
		for line != "" {
			seq, cells, _ := t.step(line)
			line = line[len(seq):]
			if used > 0 && used+cells > w {
				out = append(out, b.String())
				b.Reset()
				used = 0
			}
			b.WriteString(seq)
			used += cells
		}
		out = append(out, b.String())
	}
	return t.finish(out, w)
}

// finish makes wrapped lines safe to draw one at a time. A line is wider than
// w only when one character is, in a column narrower than the character; such
// a line is cut. Then every style left on at the end of a line is switched off
// there and on again at the start of the next.
func (t *Theme) finish(lines []string, w int) []string {
	var open []string
	for i, line := range lines {
		if t.Width(line) > w {
			line = t.Truncate(line, w)
		}
		if len(open) > 0 || strings.Contains(line, "\x1b[") {
			before := strings.Join(open, "")
			open = styleAfter(open, line)
			line = before + line
			if len(open) > 0 {
				line += reset
			}
		}
		lines[i] = line
	}
	return lines
}

// Spread puts left at the left edge of a row of exactly w cells and right at
// its right edge, at least two cells apart. When both do not fit, right gives
// way: it is left out whole. Then left is truncated.
//
// Each side is one line, as Truncate makes it. A style that a side switches
// on and never off ends with that side, so it colours neither the gap nor
// what follows the row.
func (t *Theme) Spread(left, right string, w int) string {
	if w <= 0 {
		return ""
	}
	left, right = closed(t.tidy(left)), closed(t.tidy(right))
	lw, rw := t.Width(left), t.Width(right)
	gap := 0
	if lw > 0 {
		gap = spreadGap
	}
	if rw > 0 && lw+gap+rw <= w {
		// The row is measured after it is joined: see Width.
		between := w - lw - rw
		for range joinTries {
			row := left + strings.Repeat(" ", between) + right
			off := w - t.Width(row)
			if off == 0 {
				return row
			}
			if between += off; between < gap {
				break
			}
		}
	}
	return t.Pad(left, w)
}

// nextCSI finds the first control sequence in s. It returns the text before
// it, the sequence, and the text after it; found is false when s holds no
// whole sequence.
func nextCSI(s string) (before, seq, after string, found bool) {
	i := strings.Index(s, "\x1b[")
	if i < 0 {
		return s, "", "", false
	}
	j := i + 2
	for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
		j++
	}
	if j >= len(s) {
		return s, "", "", false
	}
	return s[:i], s[i : j+1], s[j+1:], true
}

// sgrParams returns the parameters of a style sequence: "1;38;5;80" for
// "\x1b[1;38;5;80m". ok is false for any other sequence.
func sgrParams(seq string) (params string, ok bool) {
	if len(seq) < 3 || !strings.HasPrefix(seq, "\x1b[") || seq[len(seq)-1] != 'm' {
		return "", false
	}
	params = seq[2 : len(seq)-1]
	for i := range len(params) {
		if c := params[i]; (c < '0' || c > '9') && c != ';' && c != ':' {
			return "", false
		}
	}
	return params, true
}

// sgrEnds reports whether a style sequence with these parameters ends what
// an earlier one began: it resets every style, or it holds the parameter off.
// The numbers that follow 38, 48 and 58 are a colour, not parameters, so the
// 0 in "38;5;0" is not a reset.
func sgrEnds(params, off string) bool {
	ps := strings.Split(params, ";")
	for i := range ps {
		ps[i] = strings.TrimLeft(ps[i], "0") // "00" is 0, and 0 is the default
	}
	for i := 0; i < len(ps); i++ {
		switch ps[i] {
		case "", off:
			return true
		case "38", "48", "58":
			if i+1 < len(ps) {
				switch ps[i+1] {
				case "5":
					i += 2
				case "2":
					i += 4
				}
			}
		}
	}
	return false
}

// styleAfter returns the style sequences in force after line, given those in
// force before it. A reset empties the list; anything else is added to it.
func styleAfter(open []string, line string) []string {
	for {
		_, seq, after, found := nextCSI(line)
		if !found {
			return open
		}
		line = after
		params, ok := sgrParams(seq)
		if !ok {
			continue
		}
		if sgrEnds(params, "") {
			open = nil
			if strings.Trim(params, "0") == "" {
				continue
			}
		}
		open = append(open, seq)
	}
}

// closed returns s with every style it leaves on switched off at its end.
func closed(s string) string {
	if !strings.Contains(s, "\x1b[") || len(styleAfter(nil, s)) == 0 {
		return s
	}
	return s + reset
}

// fill draws one line of text, padded to exactly w cells, with a style under
// all of it: on is the sequence that switches the style on, and off the
// parameter that would switch it off. The text may be styled already. Every
// time a sequence in it ends the style, the style is switched on again, so
// the fill has no holes.
func (t *Theme) fill(s string, w int, on, off string) string {
	if w <= 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(on)
	rest := t.Pad(s, w)
	for {
		before, seq, after, found := nextCSI(rest)
		b.WriteString(before)
		if !found {
			break
		}
		b.WriteString(seq)
		if params, ok := sgrParams(seq); ok && sgrEnds(params, off) {
			b.WriteString(on)
		}
		rest = after
	}
	b.WriteString(reset)
	return b.String()
}
