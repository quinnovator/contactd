package toml

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Parse reads a file of at most MaxSize bytes. An error is an *Error, and
// names the line that was refused.
func Parse(src []byte) (*Doc, error) {
	if len(src) > MaxSize {
		return nil, &Error{Msg: "the file is larger than 1 MiB"}
	}
	if i := invalidAt(src); i >= 0 {
		return nil, &Error{Line: 1 + bytes.Count(src[:i], []byte{'\n'}), Msg: "the file is not UTF-8"}
	}
	root := &node{kind: nodeTable, kids: make(map[string]*node)}
	p := &parser{
		src:  bytes.TrimPrefix(src, []byte("\uFEFF")),
		line: 1,
		doc:  NewDoc(),
		root: root,
		here: root,
	}
	p.cur = p.doc.Tables[0].e
	for {
		p.blank()
		if p.eof() {
			return p.doc, nil
		}
		var err error
		switch p.src[p.pos] {
		case '\n', '\r', '#':
		case '[':
			err = p.header()
		default:
			err = p.pair()
		}
		if err == nil {
			err = p.endLine()
		}
		if err != nil {
			return nil, err
		}
	}
}

// maxParts is the most names a header may join with dots. The client's own
// files use two; the limit keeps what a hostile file costs in step with its
// size.
const maxParts = 32

// clipAt is how much of a name or a word from the file a message shows.
const clipAt = 48

// clip shortens a name or a word from the file for a message, so that a
// message stays one short line whatever the file holds.
func clip(s string) string { return cut(s, clipAt) }

// quote is clip for a word that may hold anything: what cannot be printed
// is written as an escape, and the escapes count towards the length.
func quote(s string) string {
	q := strconv.Quote(clip(s))
	if len(q) <= 2*clipAt {
		return q
	}
	return cut(q, 2*clipAt) + `"`
}

// cut returns s, or its first bytes up to most and three dots.
func cut(s string, most int) string {
	if len(s) <= most {
		return s
	}
	n := most
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "..."
}

// invalidAt returns where src stops being UTF-8, or -1.
func invalidAt(src []byte) int {
	for i := 0; i < len(src); {
		r, size := utf8.DecodeRune(src[i:])
		if r == utf8.RuneError && size == 1 {
			return i
		}
		i += size
	}
	return -1
}

type parser struct {
	src  []byte
	pos  int
	line int
	doc  *Doc
	// cur holds the keys of the table being read.
	cur *entries
	// root and here check that the file is one TOML allows: root is every
	// name defined so far, here the table being read.
	root, here *node
}

type nodeKind uint8

const (
	// nodeImplicit is a table named only on the way to another: the a of
	// [a.b]. A later [a] may still define it.
	nodeImplicit nodeKind = iota + 1
	// nodeTable is a table with its own header.
	nodeTable
	// nodeArray is an array of tables. Its kids are those of its newest
	// element.
	nodeArray
	// nodeValue is a key with a value.
	nodeValue
)

// node is one name the file has defined, and the names under it.
type node struct {
	kind nodeKind
	line int
	kids map[string]*node
}

func (p *parser) eof() bool { return p.pos >= len(p.src) }

// peek returns the byte n places on, or 0 past the end.
func (p *parser) peek(n int) byte {
	if p.pos+n >= len(p.src) {
		return 0
	}
	return p.src[p.pos+n]
}

func (p *parser) errf(format string, args ...any) error {
	return &Error{Line: p.line, Msg: fmt.Sprintf(format, args...)}
}

// found describes what is at the read position, for a message.
func (p *parser) found() string {
	if p.eof() {
		return "the end of the file"
	}
	if c := p.src[p.pos]; c == '\n' || c == '\r' {
		return "the end of the line"
	}
	r, _ := utf8.DecodeRune(p.src[p.pos:])
	return strconv.QuoteRune(r)
}

// blank skips spaces and tabs.
func (p *parser) blank() {
	for !p.eof() && (p.src[p.pos] == ' ' || p.src[p.pos] == '\t') {
		p.pos++
	}
}

// newline consumes a line ending, if one is next.
func (p *parser) newline() bool {
	switch {
	case p.peek(0) == '\n':
		p.pos++
	case p.peek(0) == '\r' && p.peek(1) == '\n':
		p.pos += 2
	default:
		return false
	}
	p.line++
	return true
}

// comment skips a comment, if one is next, up to the line ending.
func (p *parser) comment() error {
	if p.peek(0) != '#' {
		return nil
	}
	for !p.eof() {
		c := p.src[p.pos]
		if c == '\n' || c == '\r' && p.peek(1) == '\n' {
			return nil
		}
		if isControl(c) {
			return p.errf("control character in a comment")
		}
		p.pos++
	}
	return nil
}

// endLine consumes what may follow a header or a value: spaces, a comment,
// and the line ending.
func (p *parser) endLine() error {
	p.blank()
	if err := p.comment(); err != nil {
		return err
	}
	if p.eof() || p.newline() {
		return nil
	}
	if p.peek(0) == '\r' {
		return p.errf("a carriage return must be followed by a line feed")
	}
	return p.errf("expected the end of the line, found %s", p.found())
}

// filler skips everything that may sit between the items of an array:
// spaces, line endings and comments.
func (p *parser) filler() error {
	for {
		p.blank()
		if err := p.comment(); err != nil {
			return err
		}
		if !p.newline() {
			return nil
		}
	}
}

// key reads a bare key.
func (p *parser) key() (string, error) {
	if c := p.peek(0); c == '"' || c == '\'' {
		return "", p.errf("quoted keys are not supported")
	}
	start := p.pos
	for !p.eof() && isBareByte(p.src[p.pos]) {
		p.pos++
	}
	if p.pos == start {
		return "", p.errf("expected a key, found %s", p.found())
	}
	return string(p.src[start:p.pos]), nil
}

// header reads [a], [a.b] or [[a]] and makes it the table being read.
func (p *parser) header() error {
	p.pos++
	array := p.peek(0) == '['
	if array {
		p.pos++
	}
	var parts []string
	for {
		p.blank()
		if len(parts) == 0 && p.peek(0) == ']' {
			return p.errf("expected a table name")
		}
		part, err := p.key()
		if err != nil {
			return err
		}
		parts = append(parts, part)
		if len(parts) > maxParts {
			return p.errf("a table name may have at most %d parts", maxParts)
		}
		p.blank()
		if p.peek(0) != '.' {
			break
		}
		p.pos++
	}
	name := strings.Join(parts, ".")
	if p.peek(0) != ']' {
		return p.errf("expected ] after the table name, found %s", p.found())
	}
	p.pos++
	if array {
		if p.peek(0) != ']' {
			return p.errf("expected ]] to close [[%s", clip(name))
		}
		p.pos++
	}
	n, err := p.define(parts, array)
	if err != nil {
		return err
	}
	p.doc.Tables = append(p.doc.Tables, Table{Name: name, Array: array, Line: p.line, e: &entries{}})
	p.cur, p.here = p.doc.Tables[len(p.doc.Tables)-1].e, n
	return nil
}

// define records a header's name and refuses one TOML does not allow: a
// table defined twice, or a name that is already something else.
func (p *parser) define(parts []string, array bool) (*node, error) {
	n := p.root
	// name is the header as far as part i, for a message.
	name := func(i int) string { return clip(strings.Join(parts[:i+1], ".")) }
	for i, part := range parts {
		last := i == len(parts)-1
		kid := n.kids[part]
		if kid == nil {
			kid = &node{kind: nodeImplicit, line: p.line, kids: make(map[string]*node)}
			if last {
				kid.kind = nodeTable
				if array {
					kid.kind = nodeArray
				}
			}
			n.kids[part] = kid
			n = kid
			continue
		}
		switch kid.kind {
		case nodeValue:
			return nil, p.errf("%s is already a key, set on line %d", name(i), kid.line)
		case nodeImplicit:
			if last && array {
				return nil, p.errf("%s is already a table, named on line %d", name(i), kid.line)
			}
			if last {
				kid.kind, kid.line = nodeTable, p.line
			}
		case nodeTable:
			if last && array {
				return nil, p.errf("%s is already a table, defined on line %d", name(i), kid.line)
			}
			if last {
				return nil, p.errf("table %s is defined twice (first on line %d)", name(i), kid.line)
			}
		case nodeArray:
			if last && !array {
				return nil, p.errf("%s is already an array of tables, begun on line %d", name(i), kid.line)
			}
			if last {
				kid.kids = make(map[string]*node)
			}
		}
		n = kid
	}
	return n, nil
}

// pair reads key = value into the table being read.
func (p *parser) pair() error {
	line := p.line
	key, err := p.key()
	if err != nil {
		return err
	}
	p.blank()
	switch p.peek(0) {
	case '=':
		p.pos++
	case '.':
		return p.errf("dotted keys are not supported")
	default:
		return p.errf("expected = after %s, found %s", clip(key), p.found())
	}
	if old := p.here.kids[key]; old != nil {
		if old.kind == nodeValue {
			return p.errf("key %s is set twice (first on line %d)", clip(key), old.line)
		}
		return p.errf("%s is already a table, named on line %d", clip(key), old.line)
	}
	p.blank()
	val, err := p.value()
	if err != nil {
		return err
	}
	val.line = line
	p.here.kids[key] = &node{kind: nodeValue, line: line}
	p.cur.put(key, val)
	return nil
}

func (p *parser) value() (value, error) {
	switch c := p.peek(0); {
	case p.eof(), c == '\n', c == '\r', c == '#':
		return value{}, p.errf("expected a value after =")
	case c == '"', c == '\'':
		s, err := p.str()
		return value{kind: kindString, s: s}, err
	case c == '[':
		list, err := p.array()
		return value{kind: kindStrings, list: list}, err
	case c == '{':
		return value{}, p.errf("inline tables are not supported")
	default:
		return p.scalar()
	}
}

// str reads a basic or a literal string, whichever the quote opens.
func (p *parser) str() (string, error) {
	quote := p.src[p.pos]
	if p.peek(1) == quote && p.peek(2) == quote {
		return "", p.errf("multi-line strings are not supported")
	}
	p.pos++
	var b strings.Builder
	for {
		c := p.peek(0)
		switch {
		case p.eof(), c == '\n', c == '\r':
			return "", p.errf("the string is not closed")
		case c == quote:
			p.pos++
			return b.String(), nil
		case isControl(c):
			return "", p.errf(`control character in a string; in double quotes it is written \u%04X`, c)
		case c == '\\' && quote == '"':
			r, err := p.escape()
			if err != nil {
				return "", err
			}
			b.WriteRune(r)
		default:
			b.WriteByte(c)
			p.pos++
		}
	}
}

// escape reads one backslash escape of a basic string.
func (p *parser) escape() (rune, error) {
	p.pos++
	c := p.peek(0)
	switch {
	case p.eof(), c == '\n', c == '\r':
		return 0, p.errf("the string is not closed")
	case c == '"', c == '\\':
		p.pos++
		return rune(c), nil
	case c == 'n':
		p.pos++
		return '\n', nil
	case c == 't':
		p.pos++
		return '\t', nil
	case c == 'u':
		return p.unicode()
	default:
		// What follows the backslash is the file's own text, and may be a
		// control character: it is shown only if it can be printed.
		r, _ := utf8.DecodeRune(p.src[p.pos:])
		what := `\` + string(r)
		if !strconv.IsPrint(r) {
			what = "of " + strconv.QuoteRune(r)
		}
		return 0, p.errf(`the escape %s is not supported; there are \" \\ \n \t and \uXXXX, `+
			`or write the string in single quotes`, what)
	}
}

// unicode reads the four hex digits of a \uXXXX escape.
func (p *parser) unicode() (rune, error) {
	digits := p.src[p.pos+1 : min(p.pos+5, len(p.src))]
	if len(digits) != 4 || bytes.ContainsFunc(digits, notHex) {
		return 0, p.errf(`\u must be followed by four hex digits`)
	}
	n, err := strconv.ParseUint(string(digits), 16, 16)
	if err != nil {
		return 0, p.errf(`\u must be followed by four hex digits`)
	}
	if n >= 0xD800 && n <= 0xDFFF {
		return 0, p.errf(`\u%s is half of a surrogate pair, not a character`, digits)
	}
	p.pos += 5
	return rune(n), nil
}

func notHex(r rune) bool {
	return (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F')
}

// array reads an array of strings.
func (p *parser) array() ([]string, error) {
	open := &Error{Line: p.line, Msg: "the array is not closed"}
	p.pos++
	list := []string{}
	for {
		if err := p.filler(); err != nil {
			return nil, err
		}
		switch c := p.peek(0); {
		case p.eof():
			return nil, open
		case c == ']':
			p.pos++
			return list, nil
		case c == ',':
			return nil, p.errf("expected a string before the comma")
		case c != '"' && c != '\'':
			return nil, p.errf("an array may hold only strings")
		}
		s, err := p.str()
		if err != nil {
			return nil, err
		}
		list = append(list, s)
		if err := p.filler(); err != nil {
			return nil, err
		}
		switch c := p.peek(0); {
		case p.eof():
			return nil, open
		case c == ',':
			p.pos++
		case c != ']':
			return nil, p.errf("expected , or ] in the array, found %s", p.found())
		}
	}
}

// scalar reads a value that is not a string or an array: an integer or a
// boolean, or one of the kinds this package refuses.
func (p *parser) scalar() (value, error) {
	start := p.pos
	for !p.eof() && !strings.ContainsRune(" \t\r\n#", rune(p.src[p.pos])) {
		p.pos++
	}
	tok := string(p.src[start:p.pos])
	digits := strings.TrimLeft(tok, "+-")
	switch {
	case tok == "true", tok == "false":
		return value{kind: kindBool, b: tok == "true"}, nil
	case isDate(tok):
		return value{}, p.errf("dates and times are not supported")
	case strings.HasPrefix(digits, "0x"), strings.HasPrefix(digits, "0o"), strings.HasPrefix(digits, "0b"):
		return value{}, p.errf("only decimal integers are supported")
	case isFloat(tok):
		return value{}, p.errf("floats are not supported")
	case len(tok)-len(digits) > 1, digits == "", strings.Trim(digits, "0123456789_") != "":
		return value{}, p.errf("%s is not a value; a string needs quotes", quote(tok))
	case len(digits) > 1 && digits[0] == '0':
		return value{}, p.errf("an integer may not start with 0")
	case !isDigit(digits[0]), !isDigit(digits[len(digits)-1]), strings.Contains(digits, "__"):
		return value{}, p.errf("an underscore in an integer must sit between two digits")
	}
	n, err := strconv.ParseInt(strings.ReplaceAll(tok, "_", ""), 10, 64)
	if err != nil {
		return value{}, p.errf("the integer does not fit in 64 bits")
	}
	return value{kind: kindInt, i: n}, nil
}

// isDate reports whether tok begins like a date (1979-05-27) or a time
// (07:32:00).
func isDate(tok string) bool {
	n := 0
	for n < len(tok) && isDigit(tok[n]) {
		n++
	}
	if n == len(tok) {
		return false
	}
	return n == 4 && tok[n] == '-' || n == 2 && tok[n] == ':'
}

// isFloat reports whether tok is written like a float: digits with a
// fraction or an exponent, or inf or nan.
func isFloat(tok string) bool {
	if len(tok) > 0 && (tok[0] == '+' || tok[0] == '-') {
		tok = tok[1:]
	}
	if tok == "inf" || tok == "nan" {
		return true
	}
	return tok != "" && isDigit(tok[0]) &&
		strings.Trim(tok, "0123456789_.eE+-") == "" && strings.ContainsAny(tok, ".eE")
}
