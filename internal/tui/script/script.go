// Package script reads a reply as something to perform: the words that are
// said, and the things that are shown while they are.
//
// A reply is Markdown. Said aloud its marks are noise, and nobody reads a
// table out a character at a time. So a reply is cut in two. What is prose is
// said: the text, without the marks around it. What is not is shown, at the
// moment the words reach it: a fenced code block, a table, and an image that
// is a file of the contact's own.
//
// A reply arrives a few words at a time and is read again each time, from
// the top. Read never takes back what it said of a shorter reply: what it
// says of a text that only grew begins with what it said before. Where the
// end of the text leaves a question open (a mark that may be half written, a
// link that is not closed yet) Read stops there until more has arrived. So a
// caller can hand the words to a voice as they come, and nothing that was
// said has to be unsaid.
//
// This is not the parser the thread is drawn with, and does not try to be.
// It reads a line at a time and knows the Markdown a model writes; where the
// two disagree a mark is said that should not have been, or the other way
// round. Nothing here draws, fetches or reads a clock.
package script

import (
	"strings"
	"unicode"

	"github.com/quinnovator/contactd/internal/tui/markdown"
)

// Kind is what a Show is.
type Kind uint8

const (
	// Code is a fenced code block.
	Code Kind = iota + 1
	// Table is a Markdown table.
	Table
	// Image is an image that is a file in the contact's own directory.
	Image
)

// Show is one thing a reply shows instead of saying it.
type Show struct {
	Kind Kind
	// At is where in the words it is shown: the number of runes of
	// Script.Text that are said before it.
	At int
	// N counts the shows of this kind in the reply, from 1.
	N int
	// Text is the code of a Code, without its fences, or the lines of a
	// Table as they were written.
	Text string
	// Lang is the first word after a Code's opening fence, or "".
	Lang string
	// Path is an Image's file, as markdown.Fetchable returns it, and Alt its
	// description.
	Path, Alt string
	// Place is an Image's title, trimmed and in lower case: where the reply
	// asks for the picture to be put. It is "" when the reply does not say.
	Place string
}

// Script is a reply as it is performed.
type Script struct {
	// Text is what is said. It has no space at either end. Its lines are
	// the reply's own: a line break where a list item or a heading begins,
	// and an empty line between paragraphs.
	Text string
	// Shows is what is shown, in the order of the reply.
	Shows []Show
}

// The gaps between two words, in rising order: the widest one asked for
// since the last word is the one that is written.
const (
	gapNone uint8 = iota
	gapSpace
	gapLine
	gapPara
)

// The kinds of line.
const (
	lineProse uint8 = iota + 1
	lineBlank
	lineRule
	lineHeading
	lineBullet
	lineOrdered
	lineFence
	lineTable
	lineDefinition
)

const (
	// maxLinkText and maxLinkEnd are how far a link's closing bracket and
	// its closing parenthesis are looked for, in runes. They bound the work
	// a reply of nothing but open brackets can ask for.
	maxLinkText = 256
	maxLinkEnd  = 1280
	// fenceMin is the fewest marks that open a fence.
	fenceMin = 3
)

// Read reads a reply. whole says the reply is finished: what stands at its
// end is then taken as it is, and an open fence is code to the end. While a
// reply is still being written, Read leaves what its last line could yet
// turn into unsaid, and holds a fence or a table back until it has ended.
//
// At most maxImages images are shown; the ones after that are said, by their
// descriptions, as is one that is not a file of the contact's.
//
// For a reply that is a beginning of another, the text Read returns is a
// beginning of the text it returns for the other, whatever whole is for the
// longer one, and its shows are the first of the other's.
//
// The reply is expected to be safe to draw already: Read takes nothing out
// but Markdown's marks.
func Read(src string, whole bool, maxImages int) Script {
	r := reader{left: maxImages}
	for len(src) > 0 {
		line, rest, ended := strings.Cut(src, "\n")
		if !ended && !whole {
			// A carriage return at the end may be half of a line's end.
			r.unfinished(strings.TrimSuffix(line, "\r"))
			return r.script()
		}
		r.line(strings.TrimSuffix(line, "\r"))
		src = rest
	}
	if whole {
		r.endFence()
		r.endTable()
	}
	return r.script()
}

// reader is one reading of a reply.
type reader struct {
	out strings.Builder
	// said is the runes written to out, and gap what stands before the next.
	said int
	gap  uint8

	shows                 []Show
	codes, tables, images int
	// left is how many more images may be shown.
	left int

	// fence is the code block being read, and table the rows of the table
	// that is.
	fence *fence
	table []string
}

// fence is a fenced code block that has been opened.
type fence struct {
	mark   byte
	n      int
	indent int
	lang   string
	lines  []string
}

func (r *reader) script() Script {
	return Script{Text: r.out.String(), Shows: r.shows}
}

// need asks for a gap before the next word.
func (r *reader) need(gap uint8) { r.gap = max(r.gap, gap) }

// say writes one rune of what is said. Space is never written as it comes:
// it is a gap, and a gap is written when the word after it is.
func (r *reader) say(c rune) {
	if unicode.IsSpace(c) {
		r.need(gapSpace)
		return
	}
	if r.said > 0 {
		switch r.gap {
		case gapSpace:
			r.out.WriteByte(' ')
			r.said++
		case gapLine:
			r.out.WriteByte('\n')
			r.said++
		case gapPara:
			r.out.WriteString("\n\n")
			r.said += 2
		}
	}
	r.gap = gapNone
	r.out.WriteRune(c)
	r.said++
}

// line reads one whole line.
func (r *reader) line(l string) {
	if f := r.fence; f != nil {
		if f.closedBy(l) {
			r.endFence()
		} else {
			f.lines = append(f.lines, l)
		}
		return
	}
	t := unquote(strings.TrimLeft(l, " \t"))
	kind, rest, _ := classify(t, true)
	if kind != lineTable {
		r.endTable()
	}
	switch kind {
	case lineFence:
		r.fence = openFence(l, t)
	case lineTable:
		r.table = append(r.table, t)
	case lineDefinition:
		// A link's definition is not part of what a reply says.
	default:
		r.block(kind, rest, true)
	}
}

// unfinished reads the last line of a reply that is still being written: as
// much of it as cannot turn into anything else.
func (r *reader) unfinished(l string) {
	if r.fence != nil {
		return
	}
	t := unquote(strings.TrimLeft(l, " \t"))
	kind, rest, ok := classify(t, false)
	switch {
	case !ok, kind == lineFence, kind == lineTable, kind == lineDefinition:
		// Held: the line is not known yet, or it is not said whatever
		// follows.
		return
	}
	r.endTable()
	r.block(kind, rest, false)
}

// block says one line of prose of a known kind. whole says the line is.
func (r *reader) block(kind uint8, rest string, whole bool) {
	text := []rune(rest)
	switch kind {
	case lineBlank, lineRule:
		r.need(gapPara)
	case lineHeading:
		r.need(gapPara)
		if r.inline(text, whole) {
			r.need(gapPara)
		}
	case lineBullet:
		r.need(gapLine)
		r.say('•')
		r.need(gapSpace)
		if r.inline(text, whole) {
			r.need(gapLine)
		}
	case lineOrdered:
		// rest begins with the number and its mark.
		r.need(gapLine)
		if r.inline(text, whole) {
			r.need(gapLine)
		}
	default:
		// A line of a paragraph follows the one before it after a space.
		r.need(gapSpace)
		r.inline(text, whole)
	}
}

// unquote takes the marks of a block quote off the front of a line.
func unquote(t string) string {
	for strings.HasPrefix(t, ">") {
		t = strings.TrimLeft(t[1:], " \t")
	}
	return t
}

// classify says what kind of line t is, a line without the space and the
// quote marks in front of it, and returns what of it is said. With whole
// unset the line is still being written, and ok is false while what there is
// of it could yet become a line of another kind.
func classify(t string, whole bool) (kind uint8, rest string, ok bool) {
	if strings.TrimSpace(t) == "" {
		return lineBlank, "", whole
	}
	switch c := t[0]; {
	case c == '|':
		return lineTable, t, true
	case c == '`' || c == '~':
		n := run(t, c)
		if n >= fenceMin && (c == '~' || !strings.Contains(t[n:], "`")) {
			return lineFence, t, true
		}
		// One mark or two may be the start of a fence.
		return lineProse, t, whole || n < len(t)
	case c == '#':
		n := run(t, '#')
		switch {
		case n > 6:
			return lineProse, t, true
		case n == len(t):
			return lineHeading, "", whole
		case t[n] == ' ' || t[n] == '\t':
			return lineHeading, heading(t[n:]), true
		}
		return lineProse, t, true
	case c == '[':
		return definition(t, whole)
	case c >= '0' && c <= '9':
		return ordered(t, whole)
	case c == '-' || c == '*' || c == '_' || c == '=' || c == '+':
		return marked(t, c, whole)
	}
	return lineProse, t, true
}

// heading is the words of a heading: what follows its marks, without the
// marks that may close it. Those stand apart from the words, so "C#" keeps
// its own.
func heading(rest string) string {
	rest = strings.TrimRight(rest, " \t")
	bare := strings.TrimRight(rest, "#")
	if bare == "" || bare[len(bare)-1] == ' ' || bare[len(bare)-1] == '\t' {
		return bare
	}
	return rest
}

// marked reads a line that begins with a mark a rule or a list item could
// begin with.
func marked(t string, c byte, whole bool) (kind uint8, rest string, ok bool) {
	// A rule is three or more of one mark and nothing else but space.
	marks, only := 0, true
	for i := range len(t) {
		switch t[i] {
		case c:
			marks++
		case ' ', '\t':
		default:
			only = false
		}
	}
	if c != '+' && only {
		if marks >= fenceMin {
			return lineRule, "", whole
		}
		if !whole {
			return lineProse, t, false
		}
	}
	bullet := c == '-' || c == '*' || c == '+'
	switch {
	case bullet && len(t) == 1:
		return lineBullet, "", whole
	case bullet && (t[1] == ' ' || t[1] == '\t'):
		return lineBullet, t[2:], true
	}
	return lineProse, t, true
}

// ordered reads a line that begins with a digit: an item of a numbered list
// when a full stop or a parenthesis and a space follow the number.
func ordered(t string, whole bool) (kind uint8, rest string, ok bool) {
	n := 0
	for n < len(t) && t[n] >= '0' && t[n] <= '9' {
		n++
	}
	switch {
	case n > 9:
		return lineProse, t, true
	case n == len(t):
		return lineProse, t, whole
	case t[n] != '.' && t[n] != ')':
		return lineProse, t, true
	case n+1 == len(t):
		return lineProse, t, whole
	case t[n+1] == ' ' || t[n+1] == '\t':
		return lineOrdered, t, true
	}
	return lineProse, t, true
}

// definition reads a line that begins with a bracket. It is a link's
// definition when a colon follows the closing bracket, then one word, and
// after that nothing but a title in quotes. Whether that is so is known when
// the line has ended, and until then it is held as one.
func definition(t string, whole bool) (kind uint8, rest string, ok bool) {
	end := strings.IndexByte(t, ']')
	switch {
	case end < 0, end+1 == len(t):
		return lineProse, t, whole
	case end < 2 || t[end+1] != ':':
		return lineProse, t, true
	case !whole:
		return lineDefinition, "", true
	}
	_, title, _ := strings.Cut(strings.TrimSpace(t[end+2:]), " ")
	if title = strings.TrimSpace(title); title == "" || strings.ContainsRune(`"'(`, rune(title[0])) {
		return lineDefinition, "", true
	}
	return lineProse, t, true
}

// run counts the marks c that t begins with.
func run(t string, c byte) int {
	n := 0
	for n < len(t) && t[n] == c {
		n++
	}
	return n
}

// openFence starts a code block at the line l, which is t behind its
// indentation.
func openFence(l, t string) *fence {
	f := &fence{mark: t[0], n: run(t, t[0]), indent: len(l) - len(t)}
	if words := strings.Fields(t[f.n:]); len(words) > 0 {
		f.lang = words[0]
	}
	return f
}

// closedBy reports whether a line ends the code block: at least as many of
// the fence's mark as opened it, and nothing else.
func (f *fence) closedBy(l string) bool {
	t := strings.TrimLeft(l, " \t")
	n := run(t, f.mark)
	return n >= f.n && strings.TrimSpace(t[n:]) == ""
}

// endFence shows the code block that was being read, if there is one.
func (r *reader) endFence() {
	f := r.fence
	if f == nil {
		return
	}
	r.fence = nil
	for i, l := range f.lines {
		// The code stands as far in as its fence does.
		cut := 0
		for cut < f.indent && cut < len(l) && (l[cut] == ' ' || l[cut] == '\t') {
			cut++
		}
		f.lines[i] = l[cut:]
	}
	r.codes++
	r.show(Show{Kind: Code, N: r.codes, Text: strings.Join(f.lines, "\n"), Lang: f.lang})
}

// endTable shows the table that was being read, if there is one. Rows that
// are no table, for want of the row of dashes under their first, are said
// like any other lines.
func (r *reader) endTable() {
	rows := r.table
	if len(rows) == 0 {
		return
	}
	r.table = nil
	if len(rows) >= 2 && dashes(rows[1]) {
		r.tables++
		r.show(Show{Kind: Table, N: r.tables, Text: strings.Join(rows, "\n")})
		return
	}
	for _, row := range rows {
		r.need(gapLine)
		r.inline([]rune(row), true)
	}
	r.need(gapLine)
}

// dashes reports whether a row is the one that makes the rows around it a
// table: bars, dashes, colons and space, with a dash among them.
func dashes(row string) bool {
	return strings.Contains(row, "-") && strings.Trim(row, "|-: \t") == ""
}

// show adds something that is shown where the words have got to. A block
// stands apart from the words on both sides of it; an image stands in its
// sentence, which goes on.
func (r *reader) show(s Show) {
	s.At = r.said
	r.shows = append(r.shows, s)
	if s.Kind != Image {
		r.need(gapPara)
	}
}

// inline says the prose of one line without its marks. whole says the line
// has been written to its end. While it has not, inline stops where what
// follows could change how the text before it is read, and reports false.
func (r *reader) inline(s []rune, whole bool) bool {
	for i := 0; i < len(s); {
		c := s[i]
		switch c {
		case '\\':
			if i+1 >= len(s) {
				if !whole {
					return false
				}
				r.say(c)
				i++
				continue
			}
			// A backslash takes a mark's meaning away, and is not said.
			if unicode.IsPunct(s[i+1]) || unicode.IsSymbol(s[i+1]) {
				r.say(s[i+1])
				i += 2
				continue
			}
			r.say(c)
			i++
		case '`':
			n := marks(s, i)
			if i+n >= len(s) && !whole {
				return false
			}
			end, atEnd := closing(s, i+n, n)
			if (end < 0 || atEnd) && !whole {
				// The marks that close the span are not there yet, or the
				// ones that are may be the start of a longer run.
				return false
			}
			if end < 0 {
				// Marks that open nothing are not said either.
				i += n
				continue
			}
			// What stands between them is said as it is.
			for _, x := range s[i+n : end] {
				r.say(x)
			}
			i = end + n
		case '!', '[':
			at := i
			if c == '!' {
				if i+1 >= len(s) && !whole {
					return false
				}
				if i+1 >= len(s) || s[i+1] != '[' {
					r.say(c)
					i++
					continue
				}
				at = i + 1
			}
			l, found, open := link(s, at)
			switch {
			case open && !whole:
				return false
			case !found:
				r.say(c)
				i++
			case c == '!':
				r.image(l)
				i = l.end
			default:
				r.inline(l.text, true)
				i = l.end
			}
		case '*', '_', '~':
			n := marks(s, i)
			if i+n >= len(s) && !whole {
				return false
			}
			if spoken(c, n, around(s, i-1), around(s, i+n)) {
				for range n {
					r.say(c)
				}
			}
			i += n
		case '<':
			end := i + 1
			for end < len(s) && s[end] != '>' && s[end] != '<' && !unicode.IsSpace(s[end]) {
				end++
			}
			if end >= len(s) && !whole {
				return false
			}
			// An address in angle brackets is said without them.
			if end < len(s) && s[end] == '>' && address(s[i+1:end]) {
				for _, x := range s[i+1 : end] {
					r.say(x)
				}
				i = end + 1
				continue
			}
			r.say(c)
			i++
		default:
			r.say(c)
			i++
		}
	}
	return true
}

// marks counts the runes like s[i] that stand at i.
func marks(s []rune, i int) int {
	n := 1
	for i+n < len(s) && s[i+n] == s[i] {
		n++
	}
	return n
}

// closing finds the backticks that end a code span opened by n of them: the
// first run of exactly n from i on, or -1. atEnd says that run is the last
// thing on the line.
func closing(s []rune, i, n int) (at int, atEnd bool) {
	for i < len(s) {
		if s[i] != '`' {
			i++
			continue
		}
		k := marks(s, i)
		if k == n {
			return i, i+k == len(s)
		}
		i += k
	}
	return -1, false
}

// around is the rune at i, or a space where the line ends.
func around(s []rune, i int) rune {
	if i < 0 || i >= len(s) {
		return ' '
	}
	return s[i]
}

// spoken reports whether a run of n marks c is said: it is when it stands
// where it cannot be emphasis. A mark between spaces is itself, and so is an
// underscore inside a word, one star inside a word, and one tilde anywhere.
func spoken(c rune, n int, before, after rune) bool {
	apart := unicode.IsSpace(before) && unicode.IsSpace(after)
	inside := word(before) && word(after)
	switch c {
	case '*':
		return apart || n == 1 && inside
	case '_':
		return apart || inside
	default:
		return n == 1
	}
}

func word(c rune) bool { return unicode.IsLetter(c) || unicode.IsDigit(c) }

// address reports whether what stands in angle brackets is an address and
// not a tag.
func address(s []rune) bool {
	t := string(s)
	return strings.HasPrefix(t, "http://") || strings.HasPrefix(t, "https://") || strings.HasPrefix(t, "mailto:")
}

// linked is a link or an image as it was written: "[text](dest "title")" or
// "[text][label]".
type linked struct {
	text        []rune
	dest, title string
	// end is the index after the link.
	end int
}

// link reads the link whose opening bracket is s[i]. open says the line
// ended before the link could be told from text that only looks like one.
func link(s []rune, i int) (l linked, found, open bool) {
	stop, ok, ran := bracket(s, i, maxLinkText)
	if !ok {
		return l, false, ran
	}
	l.text = s[i+1 : stop]
	next := stop + 1
	if next >= len(s) {
		return l, false, true
	}
	switch s[next] {
	case '[':
		end, ok, ran := bracket(s, next, maxLinkText)
		if !ok {
			return l, false, ran
		}
		l.end = end + 1
		return l, true, false
	case '(':
		return l.target(s, next+1)
	}
	return l, false, false
}

// bracket finds the bracket that closes the one at s[i], within limit runes.
// ran says the line ended first.
func bracket(s []rune, i, limit int) (end int, found, ran bool) {
	depth := 0
	for j := i; j < len(s); j++ {
		if j-i > limit {
			return 0, false, false
		}
		switch s[j] {
		case '\\':
			j++
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return j, true, false
			}
		}
	}
	return 0, false, true
}

// target reads what follows a link's opening parenthesis: the destination,
// a title in quotes, and the closing parenthesis.
func (l linked) target(s []rune, i int) (out linked, found, open bool) {
	limit := min(len(s), i+maxLinkEnd)
	ran := limit == len(s)
	skip := func() {
		for i < limit && unicode.IsSpace(s[i]) {
			i++
		}
	}
	skip()
	from := i
	if i < limit && s[i] == '<' {
		for i < limit && s[i] != '>' {
			i++
		}
		if i >= limit {
			return l, false, ran
		}
		l.dest = string(s[from+1 : i])
		i++
	} else {
		depth := 0
		for i < limit && !unicode.IsSpace(s[i]) {
			if s[i] == '(' {
				depth++
			}
			if s[i] == ')' {
				if depth == 0 {
					break
				}
				depth--
			}
			i++
		}
		l.dest = string(s[from:i])
	}
	skip()
	if i < limit && (s[i] == '"' || s[i] == '\'') {
		quote := s[i]
		from = i + 1
		for i++; i < limit && s[i] != quote; i++ {
			if s[i] == '\\' {
				i++
			}
		}
		if i >= limit {
			return l, false, ran
		}
		l.title = string(s[from:i])
		i++
		skip()
	}
	if i >= limit {
		return l, false, ran
	}
	if s[i] != ')' {
		return l, false, false
	}
	l.end = i + 1
	return l, true, false
}

// image shows an image, when it is a file of the contact's and one more may
// be shown, and says its description otherwise.
func (r *reader) image(l linked) {
	path, ok := markdown.Fetchable(l.dest)
	if !ok || r.left <= 0 {
		r.inline(l.text, true)
		return
	}
	r.left--
	r.images++
	r.show(Show{
		Kind: Image, N: r.images, Path: path,
		Alt:   strings.Join(strings.Fields(string(l.text)), " "),
		Place: strings.ToLower(strings.Join(strings.Fields(l.title), " ")),
	})
}
