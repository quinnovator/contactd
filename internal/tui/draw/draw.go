// Package draw turns a conversation into the lines the terminal client shows:
// one block of lines for each item of the transcript, for a message still on
// its way, for the reply being written, and for the rules between days.
//
// A Renderer holds no conversation. It is made for one width, one theme and
// one expansion state, and is handed an item at a time. Every line it returns
// stands alone: it is at most the width in cells, and any style or link it
// switches on it also switches off, so a line can be drawn without the lines
// around it.
//
// All text from the server is made safe before it is drawn. A model or a tool
// can write escape sequences; none of them reaches a line.
package draw

import (
	"time"

	"charm.land/glamour/v2"

	"github.com/quinnovator/contactd/internal/tui/mods/wire"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
	"github.com/quinnovator/contactd/internal/tui/theme"
)

const (
	// indent is how far a reply, and everything that belongs to one, stands
	// in from the left edge.
	indent = 2
	// codeCells is the width of a code block where the terminal has room and
	// no line of the code needs more.
	codeCells = 60
	// foldedCode and foldedOutput are the screen rows of a tool call's code
	// and of a tool's output that are shown before ctrl+o.
	foldedCode   = 6
	foldedOutput = 4
	// narrow is the width under which a folded tool call shows no code.
	narrow = 40
	// maxMarkdown is the longest reply, in bytes, that is read as Markdown.
	// The parser is quadratic on some input, so a longer one is drawn as
	// written.
	maxMarkdown = 32 << 10
	// maxImages is how many images of one reply are shown.
	maxImages = 4
	// defaultCols and defaultRows are the image box when none is given.
	defaultCols = 44
	defaultRows = 12
	// maxModLines and maxSpanRunes bound what a mod or a command prints, as
	// the mod contract does.
	maxModLines  = 200
	maxSpanRunes = 512
	// maxNameRunes bounds a tool's name, a code block's language and a file
	// name.
	maxNameRunes = 80
	// maxLangs is how many language names are remembered. A reply chooses
	// them, so there is a bound.
	maxLangs = 256
)

// The tone names used here. They are the ones theme.Style takes.
const (
	toneDim    = "dim"
	toneAccent = "accent"
	toneTool   = "tool"
	toneWarn   = "warn"
	toneBad    = "bad"
)

// ImageRef names one image of a reply. Seq is the sequence number of the item
// that holds it, and N counts the item's images from 1. Path is the file in
// the contact's own directory, Alt the image's description, and TS the time
// of the item in unix ms.
type ImageRef struct {
	Seq       uint64
	N         int
	Path, Alt string
	TS        int64
}

// Shown is what there is to draw of an image. It has the same fields as
// termimg.Shown; draw declares its own so that it does not import termimg.
//
// Rows are the picture, one string for each row of cells, and Caption goes
// under them. Where there is no picture, Line names the image and Hint says
// how to save it. None of them is indented or dimmed: draw does both.
type Shown struct {
	Rows                []string
	Caption, Line, Hint string
}

// ImageSource has the images of a conversation. Image answers at once: with
// the text form while it has nothing else. draw asks again every time it
// draws the item, so a source that later has more only needs the item drawn
// again.
type ImageSource interface {
	Image(ref ImageRef, maxCols, maxRows int) Shown
}

// RenderRef names one thing a mod may draw. Kind is "code:<language>" for a
// code block of a reply, with the language as the reply wrote it, and
// "tool:<name>" for a tool's output. Seq is the item's sequence number. N
// counts the code blocks of the item from 1, and is 1 for a tool's output.
// Text is the code, or the output, already made safe to draw.
type RenderRef struct {
	Seq        uint64
	N          int
	Kind, Text string
}

// RenderSource has what mods drew.
type RenderSource interface {
	// Rendered returns a mod's lines once it has answered. Until then ok is
	// false and core draws the block; the source redraws it later. width is
	// the cells there are for a line.
	Rendered(ref RenderRef, width int) (lines []wire.Line, ok bool)
}

// Options are what a Renderer is made from.
type Options struct {
	// Theme is the look. Nil is the safest one: ASCII and no colour.
	Theme *theme.Theme
	// Width is the cells in a row, at least 1.
	Width int
	// Expanded shows what is otherwise folded: all of a tool call's code and
	// of a tool's output, and a checkpoint's summary.
	Expanded bool
	// Location is the time zone days are told in. Nil is the local one.
	Location *time.Location
	// Images has the images. Nil: an image is one line of text.
	Images ImageSource
	// Mods has what mods drew. Nil: there are no mod renderers.
	Mods RenderSource
	// MaxCols and MaxRows are the image box, in cells. Zero is 44 by 12.
	MaxCols, MaxRows int
}

// counts are what a Renderer has done, for its tests.
type counts struct {
	// splits is the replies cut into parts, markdowns the calls to the
	// Markdown renderer, and liveWraps the source lines of the reply in
	// flight that were wrapped. unsound is the renderings that were thrown
	// away because they held something a terminal would act on.
	splits, markdowns, liveWraps, unsound int
}

// Renderer draws transcript items for one width, theme and expansion state.
// It is not safe for use from several goroutines.
type Renderer struct {
	o  Options
	th *theme.Theme
	// w is the width and inner what is left of it inside the indent.
	w, inner int
	// nest is how deep quotes and lists may be nested and still be rendered:
	// every level takes two cells, and one with no cell left is what the
	// Markdown renderer cannot lay out.
	nest int
	// cols and rows are the image box.
	cols, rows int
	loc        *time.Location
	// ascii says the glyph set is ASCII: replies are then shown as written.
	ascii bool
	// joiners says server text loses its joiners, so that the two ways of
	// counting cells agree about it.
	joiners bool

	// cache holds the parts of the replies drawn so far, by sequence number.
	cache map[uint64]*reply
	// langs says, for each language a code block named, whether the
	// highlighter knows it.
	langs map[string]bool
	// prose renders the text of a reply and block one fenced block alone.
	// Both are made when first needed.
	prose, block *glamour.TermRenderer
	// lookups is how many language names were looked up for the reply being
	// drawn.
	lookups int
	live    liveText
	n       counts
}

// New makes a Renderer. One is needed for each width, theme and expansion
// state, and one for each conversation: replies are kept by their sequence
// numbers. With makes the next from the last and keeps what was worked out.
func New(o Options) *Renderer {
	th := o.Theme
	if th == nil {
		th = theme.New(termcaps.Caps{}, theme.Options{})
	}
	loc := o.Location
	if loc == nil {
		loc = time.Local
	}
	w := max(o.Width, 1)
	inner := max(w-indent, 1)
	cols, rows := o.MaxCols, o.MaxRows
	if cols <= 0 {
		cols = defaultCols
	}
	if rows <= 0 {
		rows = defaultRows
	}
	return &Renderer{
		o:       o,
		th:      th,
		w:       w,
		inner:   inner,
		nest:    min(maxNest, (inner-1)/2),
		cols:    min(cols, inner),
		rows:    rows,
		loc:     loc,
		ascii:   th.Caps.Glyphs != termcaps.GlyphsUnicode,
		joiners: th.Caps.Width != termcaps.WidthGrapheme,
		cache:   make(map[uint64]*reply),
		langs:   make(map[string]bool),
	}
}

// With makes a Renderer for other options that keeps the parts of every
// reply this one drew, so that a resize, a change of theme or ctrl+o does not
// read every reply in view again. The two share what is kept, so Forget on
// either drops it for both; the old one is meant to be let go.
func (r *Renderer) With(o Options) *Renderer {
	next := New(o)
	next.cache, next.langs = r.cache, r.langs
	return next
}

// Forget drops the cached parts of one item, by its sequence number. The
// next time the item is drawn its reply is read again.
func (r *Renderer) Forget(seq uint64) {
	delete(r.cache, seq)
}

// DateRange names the days from first to last, two times in unix ms: "7 Oct
// – 12 Oct", or "12 Oct" when both are on one day. dash is the glyph between
// them. A time of zero is no time: with one the other stands alone, and with
// both the result is "". Days in different years carry their year. A nil
// loc is the local time zone.
func DateRange(first, last int64, loc *time.Location, dash string) string {
	if loc == nil {
		loc = time.Local
	}
	switch {
	case first == 0 && last == 0:
		return ""
	case first == 0:
		first = last
	case last == 0:
		last = first
	}
	if last < first {
		first, last = last, first
	}
	a, b := time.UnixMilli(first).In(loc), time.UnixMilli(last).In(loc)
	layout := "2 Jan"
	if a.Year() != b.Year() {
		layout = "2 Jan 2006"
	}
	if a.Year() == b.Year() && a.YearDay() == b.YearDay() {
		return a.Format(layout)
	}
	return a.Format(layout) + " " + dash + " " + b.Format(layout)
}
