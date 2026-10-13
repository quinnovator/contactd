package frame

import (
	"strconv"

	"github.com/quinnovator/contactd/internal/tui/theme"
)

// The widths at which the header says less.
const (
	// wideFrom is the width from which the header names the provider and
	// shows the date range.
	wideFrom = 80
	// narrowUnder is the width under which the header is the label and a
	// bare percentage.
	narrowUnder = 40
)

// The tones this package draws in, by the names theme.Theme.Style takes.
const (
	toneDim  = "dim"
	toneWarn = "warn"
)

// HeaderInfo is what the header shows about the open contact.
type HeaderInfo struct {
	// Label is the contact's name. Provider and Model are its route. Range
	// is the dates of the lines in view, already written out: "7 Oct – 12
	// Oct".
	Label, Provider, Model, Range string
	// HasCall says the contact has made a model call, so CachePct means
	// something.
	HasCall bool
	// CachePct is the share of the last call's input that came from cache,
	// in percent.
	CachePct int
}

// Header draws the header row, exactly width cells wide.
//
// On the left is the label in bold, then two spaces and the route, dim:
// "provider/model" from 80 columns, the model alone under 80, and nothing
// under 40. On the right, dim, is the date range from 80 columns, "cache 82%"
// from 40 to 79, and "82%" under 40. The date range is shown whenever there is
// one; the cache share is not shown before the first model call.
//
// The right side gives way first: when the two do not fit two cells apart it
// is left out whole, and then the left is truncated.
//
// Every piece is drawn as plain text on one line: a line break in it is a
// space, other control characters are dropped, and an escape sequence is not
// acted on, since its escape character is dropped. The only styles in the row
// are the bold of the label and the dim of the rest.
func Header(th *theme.Theme, h HeaderInfo, width int) string {
	if width <= 0 {
		return ""
	}
	// Each piece is cut to the width before it is styled: nothing wider can
	// be shown, and the cut makes it one line.
	left := ""
	if label := th.Truncate(inert(h.Label), width); label != "" {
		left = th.Title().Render(label)
	}
	if r := th.Truncate(inert(routeAt(h, width)), width); r != "" {
		if left != "" {
			r = "  " + r
		}
		left += th.Style(toneDim).Render(r)
	}
	right := ""
	if s := th.Truncate(inert(headerRight(h, width)), width); s != "" {
		right = th.Style(toneDim).Render(s)
	}
	return th.Spread(left, right, width)
}

// routeAt is how the header names the model at a width.
func routeAt(h HeaderInfo, width int) string {
	switch {
	case width < narrowUnder:
		return ""
	case width < wideFrom || h.Provider == "":
		return h.Model
	case h.Model == "":
		return h.Provider
	}
	return h.Provider + "/" + h.Model
}

// headerRight is the right side of the header at a width, before it is
// styled.
func headerRight(h HeaderInfo, width int) string {
	if width >= wideFrom {
		return h.Range
	}
	if !h.HasCall {
		return ""
	}
	pct := strconv.Itoa(min(max(h.CachePct, 0), 100)) + "%"
	if width < narrowUnder {
		return pct
	}
	return "cache " + pct
}
