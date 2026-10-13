package draw

import "strings"

// The Markdown renderer is slow in ways the size of a reply does not bound.
// It lays a quote or a list out once more for every level it is nested in,
// and when the levels leave no width at all it breaks every character onto a
// row of its own: 250 bytes of ">" took it half a minute. A heading, a quote
// and a list item each cost it far more than a line of text does, and a code
// block that names no language it knows costs milliseconds, since the
// highlighter then tries every language it has.
//
// So before a reply is rendered its lines are weighed, without parsing them:
// how deep a line could be nested, and how much work the lines add up to. A
// reply that is too deep for the width, or too much work, has its text shown
// as written. The count errs on the heavy side: every line that could open a
// quote, a list or a code block is taken to.

const (
	// maxNest is the deepest nesting of quotes and lists that is rendered,
	// where the row is wide enough for it.
	maxNest = 12
	// maxWork is the most work a reply may be and still be rendered. The
	// unit is what a line of a paragraph costs, a few microseconds.
	maxWork = 50000
	// The work of one line of each kind, measured against a line of text.
	workLine    = 1
	workItem    = 10
	workHeading = 60
	workQuote   = 60
	workTable   = 20
	workFence   = 20
	// workPlain is a code block the highlighter has to guess the language
	// of.
	workPlain = 500
	// maxFences is how many fences inside the text of a reply have their
	// language looked at. The rest count as having none.
	maxFences = 256
	// maxLookups is how many language names that were never seen before are
	// looked up for one reply. A name the highlighter does not have costs it
	// a millisecond to say so, and a reply chooses the names.
	maxLookups = 16
	// codeIndent is the indentation that makes a line code.
	codeIndent = 4
	// heavy is the line under a reply whose text was too much to render.
	heavy = "shown as written: this reply would take too long to lay out"
)

// load is what the text of a reply would cost the Markdown renderer.
type load struct {
	// depth is the deepest a line could be nested in quotes and lists.
	depth int
	// work is the cost of the lines, without their code blocks.
	work int
	// fences are the languages named by the fences inside the text, and
	// plain the code blocks that name none: indented ones, and fences past
	// the number that is kept.
	fences []string
	plain  int
}

// add adds the load of another piece of text.
func (l *load) add(o load) {
	l.depth = max(l.depth, o.depth)
	l.work += o.work
	l.plain += o.plain
	l.fences = append(l.fences, o.fences...)
	if extra := len(l.fences) - maxFences; extra > 0 {
		l.fences, l.plain = l.fences[:maxFences], l.plain+extra
	}
}

// weigh tells the load of a piece of Markdown from its lines.
func weigh(src string) load {
	var l load
	// blank says the line before drew nothing, and code that the last line
	// that drew something was indented as code is.
	blank, code := true, false
	for line := range strings.SplitSeq(src, "\n") {
		quotes, items, spaces, indent, rest := markers(line)
		if quotes+items > 0 {
			l.depth = max(l.depth, quotes+items+spaces/2)
		}
		l.work += workLine + workItem*items + workQuote*quotes*(quotes+1)/2
		if rest == "" {
			blank = true
			continue
		}
		switch {
		case strings.HasPrefix(rest, "```"), strings.HasPrefix(rest, "~~~"):
			// Any fence could be the one that opens a block.
			l.work += workFence
			if len(l.fences) < maxFences {
				l.fences = append(l.fences, fenceLanguage(rest))
			} else {
				l.plain++
			}
		case indent >= codeIndent:
			// The first line of an indented code block. A line that is
			// indented under a list item looks the same and is counted too.
			if blank && !code {
				l.plain++
			}
		case rest[0] == '#', strings.Trim(rest, "=") == "", strings.Trim(rest, "-") == "":
			l.work += workHeading
		case strings.Contains(rest, "|") && strings.Contains(rest, "-") && strings.Trim(rest, "|-: ") == "":
			l.work += workTable
		}
		blank, code = false, indent >= codeIndent
	}
	return l
}

// markers reads what a line begins with: the quote marks and list markers
// that could each open a level of nesting, the spaces before the last of
// them, the spaces after it, and the rest of the line. A list is nested by
// indenting it, two spaces a level at the least, so the spaces count towards
// how deep a line could be.
func markers(line string) (quotes, items, spaces, indent int, rest string) {
	i := 0
	for i < len(line) {
		c, n := line[i], 0
		switch {
		case c == ' ':
			indent++
			i++
			continue
		case c == '>':
			quotes, n = quotes+1, 1
		case c == '-' || c == '+' || c == '*' || c == ':':
			n = 1
		case isDigit(c):
			for n = 1; i+n < len(line) && isDigit(line[i+n]); n++ {
			}
			if i+n == len(line) || (line[i+n] != '.' && line[i+n] != ')') {
				return quotes, items, spaces, indent, line[i:]
			}
			n++
		default:
			return quotes, items, spaces, indent, line[i:]
		}
		if c != '>' {
			// A list marker is one only before a space or the line's end.
			if i+n < len(line) && line[i+n] != ' ' {
				return quotes, items, spaces, indent, line[i:]
			}
			items++
		}
		spaces, indent = spaces+indent, 0
		i += n
	}
	return quotes, items, spaces, indent, ""
}

// fenceLanguage is the first word after the marks of a code fence.
func fenceLanguage(rest string) string {
	word, _, _ := strings.Cut(strings.TrimLeft(strings.TrimLeft(rest, string(rest[0])), " "), " ")
	return word
}

// tooHeavy reports whether the text of a reply is too much for the Markdown
// renderer at this width and with this theme.
func (r *Renderer) tooHeavy(l load) bool {
	if l.depth > r.nest || l.work > maxWork {
		return true
	}
	if r.ascii || r.th.ChromaFormatter() == "" {
		return false // code is not highlighted: no language is guessed
	}
	work := l.work + workPlain*l.plain
	for _, lang := range l.fences {
		if work > maxWork {
			break
		}
		if !r.known(lang) {
			work += workPlain
		}
	}
	return work > maxWork
}
