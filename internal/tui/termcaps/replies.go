package termcaps

import "strings"

// Replies is what the terminal has answered so far. It is comparable.
type Replies struct {
	// Deadline means the time for answers is over.
	Deadline bool
	// Background is the answer to the background-colour question, or zero.
	Background Background
	// Version is the terminal's name and version in lower case. It never
	// starts with "tmux".
	Version string
	// TmuxSeen means a version reply started with "tmux".
	TmuxSeen bool
	// Kitty and KittyOuter are the kitty graphics answers: OK for query id
	// 31 (asked directly) and id 32 (asked through tmux passthrough).
	Kitty, KittyOuter bool
	// DA1 counts device-attribute replies. Terminals answer that question
	// whatever else they leave unanswered, and answer in order, so a reply
	// to it means the questions before it have been answered or never will
	// be.
	DA1 int
	// CellW and CellH are the pixels per cell the terminal reported, or zero.
	CellW, CellH int
	// Graphemes means mode 2027 was recognised.
	Graphemes bool
	// KeyFlags are the keyboard enhancements the terminal reported.
	KeyFlags int
}

// ReplyKind tags what a Reply carries.
type ReplyKind uint8

const (
	// ReplyBackground is the terminal's background colour (OSC 11).
	ReplyBackground ReplyKind = iota + 1
	// ReplyVersion is the terminal's name and version (XTVERSION).
	ReplyVersion
	// ReplyKitty answers a kitty graphics query.
	ReplyKitty
	// ReplyDA1 is the primary device attributes.
	ReplyDA1
	// ReplyCell is the size of a cell in pixels (CSI 16 t).
	ReplyCell
	// ReplyMode2027 reports the grapheme-cluster mode.
	ReplyMode2027
	// ReplyKeyboard reports keyboard enhancements.
	ReplyKeyboard
	// ReplyDeadline is not from the terminal: the time for answers is over.
	ReplyDeadline
)

// Reply is one answer from the terminal. Kind says which fields are set.
type Reply struct {
	Kind ReplyKind
	// Dark, with ReplyBackground, says the background is dark.
	Dark bool
	// Text, with ReplyVersion, is the name and version.
	Text string
	// ID, with ReplyKitty, is the query's id: 31 or 32.
	ID int
	// OK, with ReplyKitty, says the payload was "OK"; with ReplyMode2027,
	// that the mode was recognised.
	OK bool
	// W and H, with ReplyCell, are the cell's width and height in pixels.
	// The terminal's own reply gives the height first.
	W, H int
	// Flags, with ReplyKeyboard, are the enhancements in force.
	Flags int
}

const (
	// kittyDirect and kittyOuter are the ids of the two kitty graphics
	// queries: the one sent straight to the terminal and the one sent
	// through tmux passthrough.
	kittyDirect = 31
	kittyOuter  = 32
	// maxName is the longest terminal name kept.
	maxName = 64
	// maxCell is the largest cell side believed, in pixels.
	maxCell = 4096
)

// Fold adds one reply. After Deadline it ignores ReplyVersion, ReplyKitty,
// ReplyDA1 and ReplyCell; ReplyBackground, ReplyMode2027 and ReplyKeyboard are
// always folded, because Bubble Tea produces them on its own schedule.
//
// A reply of an unknown kind, a kitty reply with another id, an empty name
// and a cell size that makes no sense change nothing.
func Fold(r Replies, in Reply) Replies {
	late := r.Deadline
	switch in.Kind {
	case ReplyBackground:
		r.Background = BgLight
		if in.Dark {
			r.Background = BgDark
		}
	case ReplyMode2027:
		r.Graphemes = in.OK
	case ReplyKeyboard:
		r.KeyFlags = in.Flags
	case ReplyDeadline:
		r.Deadline = true
	case ReplyVersion:
		name := terminalName(in.Text)
		switch {
		case late || name == "":
		case strings.HasPrefix(name, "tmux"):
			r.TmuxSeen = true
		default:
			r.Version = name
		}
	case ReplyKitty:
		switch {
		case late:
		case in.ID == kittyDirect:
			r.Kitty = in.OK
		case in.ID == kittyOuter:
			r.KittyOuter = in.OK
		}
	case ReplyDA1:
		if !late {
			r.DA1++
		}
	case ReplyCell:
		if !late && in.W > 0 && in.H > 0 && in.W <= maxCell && in.H <= maxCell {
			r.CellW, r.CellH = in.W, in.H
		}
	}
	return r
}

// terminalName makes a version reply fit to keep and to show: lower case,
// printable ASCII only, trimmed, and not longer than maxName. The terminal
// wrote it, so it is not trusted to be a name.
func terminalName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || r > 0x7E {
			continue
		}
		if r >= 'A' && r <= 'Z' {
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	name := strings.TrimSpace(b.String())
	if len(name) > maxName {
		name = strings.TrimSpace(name[:maxName])
	}
	return name
}
