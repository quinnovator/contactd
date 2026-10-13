package frame

import (
	"strings"

	"github.com/quinnovator/contactd/internal/tui/theme"
)

// paletteHint is what the hint line says while the command palette is open.
const paletteHint = "a line that starts with / is a command and is never sent to the contact"

// HintState is what decides the text of the hint line.
type HintState struct {
	// PaletteOpen says the command palette is open: the line explains what a
	// command is in place of the keys.
	PaletteOpen bool
	// ShiftEnter says the terminal reports shift+enter as a key of its own,
	// so that is the new-line key the line names. Otherwise it names ctrl+j.
	ShiftEnter bool
	// Note is a message that takes the place of the hints, such as
	// "not sent: not connected". NoteTone is the tone it is drawn in, by a
	// name theme.Theme.Style takes; "" is the warn tone.
	Note, NoteTone string
}

// hint is one key hint and how long it stays as the line gets narrower: the
// hint with the lowest rank goes first.
type hint struct {
	text string
	rank int
}

// hints lists the key hints in the order they are drawn. Their ranks drop
// contacts first, then details, the new-line key and send, and leave
// "/ commands" standing.
func hints(shiftEnter bool) []hint {
	newline := "ctrl+j new line"
	if shiftEnter {
		newline = "shift+enter new line"
	}
	return []hint{
		{"enter send", 4},
		{newline, 3},
		{"/ commands", 5},
		{"ctrl+o details", 2},
		{"ctrl+p contacts", 1},
	}
}

// Hints draws the hint line, exactly width cells wide: one space, then the
// text.
//
// Usually the text is the keys, dim: "enter send · ctrl+j new line ·
// / commands · ctrl+o details · ctrl+p contacts". As the width shrinks whole
// hints go, in this order: contacts, details, new line, send. "/ commands" is
// the last one standing, and is truncated where even it does not fit. While
// the palette is open the text is one sentence about commands, truncated to
// the width. A note takes the place of either, in its tone.
//
// A note is drawn as plain text on one line: a line break in it is a space,
// and an escape sequence is not acted on, since its escape character is
// dropped. The only style in the row is the tone.
func Hints(th *theme.Theme, h HintState, width int) string {
	if width <= 0 {
		return ""
	}
	if h.Note != "" {
		tone := h.NoteTone
		if tone == "" {
			tone = toneWarn
		}
		return line(th, tone, h.Note, width)
	}
	if h.PaletteOpen {
		return line(th, toneDim, paletteHint, width)
	}
	all := hints(h.ShiftEnter)
	text := ""
	for cut := range all {
		text = joinHints(th, all, cut)
		if 1+th.Width(text) <= width {
			break
		}
	}
	return line(th, toneDim, text, width)
}

// joinHints joins the hints ranked above cut with the theme's separator.
func joinHints(th *theme.Theme, all []hint, cut int) string {
	var b strings.Builder
	for _, h := range all {
		if h.rank <= cut {
			continue
		}
		if b.Len() > 0 {
			b.WriteString(" " + th.G.Mid + " ")
		}
		b.WriteString(h.text)
	}
	return b.String()
}

// line draws text in a tone after one space, cut or padded to exactly width
// cells. The text is made inert and cut before it is styled, so the only
// style in the row is the tone, and it ends with the text.
func line(th *theme.Theme, tone, text string, width int) string {
	text = th.Truncate(inert(text), width-1)
	if text == "" {
		return th.Pad("", width)
	}
	return th.Pad(" "+th.Style(tone).Render(text), width)
}

// inert makes text safe to put in a row whatever it holds: the escape
// character is dropped, so an escape sequence in the text is drawn as the
// characters that followed it and the terminal acts on none of it. A note
// with a stray style sequence cannot colour the rows under it, and a label
// cannot clear the screen. The theme drops every other control character
// when it cuts the text.
func inert(s string) string {
	return strings.ReplaceAll(s, "\x1b", "")
}
