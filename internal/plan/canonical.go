package plan

import (
	"strconv"
	"unicode/utf8"

	"github.com/cespare/xxhash/v2"

	"github.com/quinnovator/contactd/internal/core"
)

// Canonical bytes are JSON with keys in sorted order, no insignificant
// whitespace and one fixed string escaping. They are written by hand, field
// by field, so that no map iteration or encoder option can change them.

// Canonical returns the canonical bytes of a message. Adapters do not send
// these bytes; they exist to be hashed and to be pinned by golden tests.
func Canonical(m *core.Message) []byte { return appendMessage(nil, m) }

// CanonicalTools returns the canonical bytes of a tool list, in declaration
// order.
func CanonicalTools(tools []core.ToolDef) []byte { return appendTools(nil, tools) }

func appendMessage(b []byte, m *core.Message) []byte {
	b = append(b, '{')
	if r := m.Reasoning; r != nil {
		// The payload is opaque and can be large; its hash stands in for it.
		b = append(b, `"reasoning":{"data":"`...)
		b = strconv.AppendUint(b, xxhash.Sum64(r.Data), 16)
		b = append(b, `","route":`...)
		b = appendString(b, r.Route)
		b = append(b, `},`...)
	}
	b = append(b, `"role":`...)
	b = appendString(b, m.Role.String())
	if m.Text != "" {
		b = append(b, `,"text":`...)
		b = appendString(b, m.Text)
	}
	if c := m.ToolCall; c != nil {
		b = append(b, `,"tool_call":{`...)
		if len(c.Args) > 0 {
			b = append(b, `"args":`...)
			b = append(b, c.Args...)
			b = append(b, ',')
		}
		b = append(b, `"code":`...)
		b = appendString(b, c.Code)
		b = append(b, `,"id":`...)
		b = appendString(b, c.ID)
		b = append(b, `,"name":`...)
		b = appendString(b, c.Name)
		if c.Sibling > 0 {
			b = append(b, `,"sibling":`...)
			b = strconv.AppendInt(b, int64(c.Sibling), 10)
		}
		b = append(b, '}')
	}
	if r := m.ToolResult; r != nil {
		b = append(b, `,"tool_result":{"id":`...)
		b = appendString(b, r.ID)
		b = append(b, `,"is_error":`...)
		b = strconv.AppendBool(b, r.IsError)
		b = append(b, `,"output":`...)
		b = appendString(b, r.Output)
		b = append(b, '}')
	}
	return append(b, '}')
}

func appendTools(b []byte, tools []core.ToolDef) []byte {
	b = append(b, '[')
	for i := range tools {
		if i > 0 {
			b = append(b, ',')
		}
		t := &tools[i]
		b = append(b, `{"description":`...)
		b = appendString(b, t.Description)
		b = append(b, `,"env":{"base":`...)
		b = appendString(b, t.Env.Base)
		b = append(b, `,"network":`...)
		b = strconv.AppendBool(b, t.Env.Network)
		b = append(b, `,"packages":[`...)
		for j, p := range t.Env.Packages {
			if j > 0 {
				b = append(b, ',')
			}
			b = appendString(b, p)
		}
		b = append(b, `],"tier":`...)
		b = strconv.AppendUint(b, uint64(t.Env.Tier), 10)
		b = append(b, '}')
		if len(t.Input) > 0 {
			b = append(b, `,"input":`...)
			b = append(b, t.Input...)
		}
		b = append(b, `,"name":`...)
		b = appendString(b, t.Name)
		b = append(b, '}')
	}
	return append(b, ']')
}

const hexDigits = "0123456789abcdef"

// appendString writes s as a JSON string. Only the characters JSON requires
// are escaped; everything else, including non-ASCII, passes through. Invalid
// UTF-8 becomes U+FFFD so the output is always valid.
func appendString(b []byte, s string) []byte {
	b = append(b, '"')
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			switch {
			case c == '"' || c == '\\':
				b = append(b, '\\', c)
			case c == '\n':
				b = append(b, '\\', 'n')
			case c == '\r':
				b = append(b, '\\', 'r')
			case c == '\t':
				b = append(b, '\\', 't')
			case c < 0x20:
				b = append(b, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0xf])
			default:
				b = append(b, c)
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b = append(b, "�"...)
		} else {
			b = append(b, s[i:i+size]...)
		}
		i += size
	}
	return append(b, '"')
}
