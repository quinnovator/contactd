package commands

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// LineKind is what a draft turned out to be.
type LineKind uint8

const (
	// LineMessage is text to send to the contact.
	LineMessage LineKind = iota + 1
	// LineCommand is a command to run here. It is never sent.
	LineCommand
	// LineRefused is a draft of several lines that starts with one slash.
	// It is neither run nor sent: it reads as a command and cannot be one.
	LineRefused
)

// Line is a draft as Parse read it. Kind says which fields are set.
type Line struct {
	Kind LineKind
	// Name is the command's name, without the slash and in lower case. It
	// is empty when the draft is a slash alone or a slash and a space.
	Name string
	// Args is Rest split on spaces. "Double quotes" keep the spaces inside
	// them and are dropped; a quote left open runs to the end.
	Args []string
	// Rest is everything after the name, trimmed.
	Rest string
	// Message is what to send, for a LineMessage.
	Message string
}

// Parse reads a draft from the input box. Trailing white space is dropped
// first, and nothing else is.
//
// A draft that starts with "//" is a message: the draft without its first
// slash. One line that starts with "/" is a command. Several lines that
// start with one "/" are refused. Anything else is a message, as written.
func Parse(draft string) Line {
	draft = strings.TrimRightFunc(draft, unicode.IsSpace)
	switch {
	case strings.HasPrefix(draft, "//"):
		return Line{Kind: LineMessage, Message: draft[1:]}
	case !strings.HasPrefix(draft, "/"):
		return Line{Kind: LineMessage, Message: draft}
	case strings.ContainsAny(draft, "\n\r"):
		return Line{Kind: LineRefused}
	}
	name, rest, _ := cutWord(draft[1:])
	rest = strings.TrimSpace(rest)
	return Line{Kind: LineCommand, Name: strings.ToLower(name), Args: split(rest), Rest: rest}
}

// Complete returns the draft that selects a command and waits for its
// arguments: "/name ".
func Complete(s Spec) string { return "/" + s.Name + " " }

// Ghost returns what to draw dim after the cursor while a command is being
// typed, given the command it would run.
//
// While the name is being typed that is the rest of the name, two spaces and
// the usage. Once the whole name and a space are typed it is the usage
// alone. It is empty once an argument has been started, when the draft is
// not a one-line command, and when the typed word is not the start of the
// command's name.
func Ghost(draft string, s Spec) string {
	if !strings.HasPrefix(draft, "/") || strings.HasPrefix(draft, "//") || strings.ContainsAny(draft, "\n\r") {
		return ""
	}
	word, after, spaced := cutWord(draft[1:])
	word = strings.ToLower(word)
	if spaced {
		if word != s.Name || strings.TrimSpace(after) != "" {
			return ""
		}
		return s.Usage
	}
	rest, ok := strings.CutPrefix(s.Name, word)
	switch {
	case !ok:
		return ""
	case s.Usage == "":
		return rest
	default:
		return rest + "  " + s.Usage
	}
}

// cutWord cuts s at its first white space. found says there was some.
func cutWord(s string) (word, after string, found bool) {
	i := strings.IndexFunc(s, unicode.IsSpace)
	if i < 0 {
		return s, "", false
	}
	return s[:i], s[i:], true
}

// split cuts a command's arguments at white space, keeping what is inside
// double quotes together.
func split(s string) []string {
	var (
		args   []string
		arg    strings.Builder
		open   bool // an argument has begun
		quoted bool
	)
	for i, size := 0, 0; i < len(s); i += size {
		var c rune
		c, size = utf8.DecodeRuneInString(s[i:])
		switch {
		case c == '"':
			// A pair of quotes with nothing between them is an empty argument.
			quoted, open = !quoted, true
		case unicode.IsSpace(c) && !quoted:
			if open {
				args = append(args, arg.String())
				arg.Reset()
				open = false
			}
		default:
			// The bytes as written, so that a byte that is not UTF-8 survives.
			arg.WriteString(s[i : i+size])
			open = true
		}
	}
	if open {
		args = append(args, arg.String())
	}
	return args
}
