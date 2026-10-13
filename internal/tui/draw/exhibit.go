package draw

import (
	"strings"
	"time"

	"github.com/quinnovator/contactd/internal/transcript"
	"github.com/quinnovator/contactd/internal/tui/markdown"
)

// What a conversation shown by an avatar draws apart from the reply it is
// part of: a code block, a table and an image stand on the stage by
// themselves, and the turn in flight is one line under the avatar. Each is
// drawn here the way the thread draws it, so the two views of a conversation
// agree, and what a mod draws is drawn in both.

// MaxImages is how many images of one reply are shown.
const MaxImages = maxImages

// Code draws one fenced code block of a reply, as the reply itself is drawn
// with it. seq is the reply's sequence number and n counts its code blocks
// from 1; a mod that draws the language draws this.
func (r *Renderer) Code(seq uint64, n int, lang, code string) []string {
	r.lookups = 0
	return r.fit(r.code(seq, n, markdown.Part{Kind: markdown.Code, Text: r.clean(code), Lang: lang}))
}

// Markdown draws a piece of a reply's Markdown by itself: a table, say.
func (r *Renderer) Markdown(src string) []string {
	src = trimText(r.clean(src))
	if src == "" || len(src) > maxMarkdown {
		return r.fit(r.written(src))
	}
	return r.fit(r.text(src, ""))
}

// Image draws one image of a reply: the picture and its caption, or the line
// that stands for it.
func (r *Renderer) Image(ref ImageRef) []string {
	return r.fit(r.image(ref))
}

// Clip cuts drawn lines to at most rows of them. Where some are left out,
// the last row says how many, and names the key that shows the turn whole.
func (r *Renderer) Clip(lines []string, rows int) []string {
	if len(lines) <= rows {
		return lines
	}
	if rows <= 1 {
		return lines[:max(rows, 0)]
	}
	out := append(make([]string, 0, rows), lines[:rows-1]...)
	return append(out, strings.Repeat(" ", indent)+r.more(len(lines)-rows+1, r.inner))
}

// Doing says in one line what the turn in flight is busy with: the tool it
// has begun to call, or the one that is running and for how long. It is ""
// while the contact is only writing.
func (r *Renderer) Doing(l transcript.Live, running time.Duration) string {
	var line string
	switch {
	case l.Running != "":
		line = r.tone(toneTool, r.th.G.Tool+" "+r.toolName(l.Running)) +
			r.tone(toneDim, "  running"+r.th.G.Ellipsis+" "+seconds(running))
	case l.Starting != "":
		line = r.tone(toneTool, r.th.G.Tool+" "+r.toolName(l.Starting)) + r.tone(toneDim, " "+r.th.G.Ellipsis)
	default:
		return ""
	}
	return r.row(line)
}

// row fits one drawn line to the width.
func (r *Renderer) row(line string) string {
	lines := append(make([]string, 0, 1), line)
	return strings.Join(r.fit(lines), "")
}

// Stopped is the line that says a reply was interrupted.
func (r *Renderer) Stopped() string {
	return r.row(r.tone(toneDim, r.th.G.Stopped+" interrupted"))
}
