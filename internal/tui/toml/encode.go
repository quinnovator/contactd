package toml

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
)

// Encode writes the document as a file. The same document always gives the
// same bytes: tables and keys in their order, one key to a line, a blank
// line before each header. Comments are not kept, because a Doc holds none.
//
// Parse reads the result back as an equal document, unless the document was
// built with names that clash (Parse refuses those) or the result is over
// MaxSize.
func (d *Doc) Encode() []byte {
	var b bytes.Buffer
	for i := range d.Tables {
		t := &d.Tables[i]
		if i > 0 {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			if t.Array {
				b.WriteString("[[" + t.Name + "]]\n")
			} else {
				b.WriteString("[" + t.Name + "]\n")
			}
		}
		if t.e == nil {
			continue
		}
		for _, key := range t.e.keys {
			b.WriteString(key)
			b.WriteString(" = ")
			writeValue(&b, t.e.vals[key])
			b.WriteByte('\n')
		}
	}
	return b.Bytes()
}

func writeValue(b *bytes.Buffer, v value) {
	switch v.kind {
	case kindString:
		writeString(b, v.s)
	case kindInt:
		b.WriteString(strconv.FormatInt(v.i, 10))
	case kindBool:
		b.WriteString(strconv.FormatBool(v.b))
	case kindStrings:
		b.WriteByte('[')
		for i, s := range v.list {
			if i > 0 {
				b.WriteString(", ")
			}
			writeString(b, s)
		}
		b.WriteByte(']')
	}
}

// writeString writes s in quotes. A string with a backslash or a double
// quote is written literally where that is possible, so that a pattern
// stays readable: '\d+' and not "\\d+".
func writeString(b *bytes.Buffer, s string) {
	if strings.ContainsAny(s, `\"`) && !strings.ContainsFunc(s, func(r rune) bool { return r == '\'' || hidden(r) }) {
		b.WriteByte('\'')
		b.WriteString(s)
		b.WriteByte('\'')
		return
	}
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\t':
			b.WriteString(`\t`)
		case hidden(r):
			fmt.Fprintf(b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
}

// hidden reports whether r is a control character, C0 or C1. One is never
// written as itself: a file may be shown on a terminal.
func hidden(r rune) bool { return r < 0x20 || r >= 0x7F && r <= 0x9F }
