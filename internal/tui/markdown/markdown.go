// Package markdown cuts a reply into the parts the client draws in different
// ways: Markdown text, fenced code, and images that are files in the
// contact's own directory.
//
// It reads the reply with goldmark, the parser the renderer uses, so the two
// agree about what is a code fence and what is an image. It draws nothing
// and fetches nothing.
package markdown

import (
	"bytes"
	"strings"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// Kind is what a Part is.
type Kind uint8

const (
	// Text is Markdown source, to be rendered as Markdown.
	Text Kind = iota + 1
	// Code is the content of a fenced code block.
	Code
	// Image is a reference to a file in the contact's own directory.
	Image
)

// Part is one piece of a reply.
type Part struct {
	Kind Kind
	// Text is the Markdown source of a Text part, or the code of a Code
	// part.
	//
	// A Text part is whole lines of the reply, as written. It has no blank
	// line at either end and stops at its last visible character.
	//
	// A Code part is the lines between the fences, without the fences, the
	// fence's indentation or a final line break. An empty fence gives "".
	Text string
	// Lang is the first word after a Code part's opening fence, or "".
	Lang string
	// Alt is an Image part's alternative text, on one line.
	Alt string
	// Path is an Image part's file, as Fetchable returns it.
	Path string
}

// Split cuts a reply at its top-level blocks.
//
// A fenced code block at the top level becomes a Code part. A top-level
// paragraph that holds one fetchable image and nothing else becomes an Image
// part. Everything else is Text, and neighbouring blocks share one part. A
// reply that is empty or blank has no parts.
//
// A fetchable image anywhere else (beside other words, in a list, a quote, a
// heading or a link) stays in its text, and an Image part follows the
// top-level block that holds it. An image in a code fence, in inline code,
// in raw HTML or after an escaped "!" is not an image.
//
// An image is fetchable when Fetchable says so of its destination as
// Markdown reads it: with backslash escapes and character references
// resolved, for an image written out and for one that names a definition.
//
// At most maxImages Image parts are returned; the images after that stay
// text. A block whose place in the source cannot be found also stays text,
// and an image that was waiting for the end of such a block follows the
// text part that holds it.
//
// Nothing of the reply is lost. Each part stands for a run of the source,
// and the runs meet end to end. What the parts leave out is blank lines
// between them, the fence lines of a Code part and the Markdown of an image
// that became a part of its own. Link reference definitions stay in the
// text, so a link in one Text part that is defined in another does not
// resolve when the parts are rendered one at a time.
func Split(src string, maxImages int) []Part {
	pieces := cut(src, maxImages)
	parts := make([]Part, 0, len(pieces))
	for _, p := range pieces {
		if p.Kind != 0 {
			parts = append(parts, p.Part)
		}
	}
	return parts
}

// piece is a part and the run of the source it stands for. A piece with no
// kind is blank lines that no part keeps. An Image part that follows its
// text stands for nothing: its run is empty.
type piece struct {
	Part
	start, end int
}

// cut is Split with the source run of every part. The runs are in order,
// start at 0, end at len(src) and leave no gap.
func cut(src string, maxImages int) []piece {
	doc, at := parse([]byte(src))
	return assemble(src, doc, at, maxImages)
}

// assemble makes the pieces of a parsed reply. A block that at has no place
// for stays text.
func assemble(src string, doc ast.Node, at *marks, maxImages int) []piece {
	c := cutter{src: src, raw: []byte(src), at: at, left: maxImages}
	for n := doc.FirstChild(); n != nil; n = n.NextSibling() {
		c.block(n)
	}
	c.flush(len(src))
	return c.out
}

// cutter walks the top-level blocks of one reply, in order.
type cutter struct {
	src string
	raw []byte
	at  *marks
	// left is how many more Image parts may be made.
	left int
	// cursor is where the text not yet given to a part begins.
	cursor int
	// floor is the furthest the blocks seen so far are known to reach. A
	// block that claims to start before it has not been found.
	floor int
	// waiting holds the images of text blocks whose end has not come yet.
	waiting []Part
	out     []piece
}

// block handles one top-level block.
func (c *cutter) block(n ast.Node) {
	lo, hi, images := survey(n)
	start, found := c.startOf(n, lo)
	if found && len(c.waiting) > 0 {
		// The text before this block ends here, and its images follow it.
		c.flush(start)
	}
	switch b := n.(type) {
	case *ast.FencedCodeBlock:
		if end, ok := c.fenceEnd(b, start, hi); found && ok {
			c.flush(start)
			c.put(Part{Kind: Code, Text: c.code(b), Lang: c.lang(start)}, start, end)
			return
		}
	case *ast.Paragraph:
		if p, ok := c.lone(b, images); ok {
			c.flush(p.start)
			c.put(p.Part, p.start, p.end)
			return
		}
	}
	for _, img := range images {
		if c.left <= 0 {
			break
		}
		if path, ok := Fetchable(destination(img)); ok {
			c.left--
			c.waiting = append(c.waiting, Part{Kind: Image, Alt: altText(img, c.raw), Path: path})
		}
	}
	c.floor = max(c.floor, hi)
}

// startOf returns the offset of the line a block opens on. lo is the first
// byte the block is known to hold, or -1.
func (c *cutter) startOf(n ast.Node, lo int) (int, bool) {
	at, ok := c.at.start[n]
	if !ok {
		return 0, false
	}
	start := lineStart(c.src, at)
	if start < c.floor || (lo >= 0 && start > lo) {
		return 0, false
	}
	return start, true
}

// fenceEnd returns the offset after a fenced block's last line: its closing
// fence, or the end of the reply when it has none.
func (c *cutter) fenceEnd(b *ast.FencedCodeBlock, start, hi int) (int, bool) {
	end := len(c.src)
	if at, closed := c.at.end[b]; closed {
		end = lineEnd(c.src, at)
	} else if b.NextSibling() != nil {
		// An open fence runs to the end of the reply. A block after one
		// means the fence was not read the way it is assumed here.
		return 0, false
	}
	if end <= start || end < hi {
		return 0, false
	}
	return end, true
}

// code returns the lines of a fenced block, joined by "\n".
func (c *cutter) code(b *ast.FencedCodeBlock) string {
	var sb strings.Builder
	lines := b.Lines()
	for i := range lines.Len() {
		if i > 0 {
			sb.WriteByte('\n')
		}
		seg := lines.At(i)
		sb.Write(bytes.TrimRight(seg.Value(c.raw), "\r\n"))
	}
	return sb.String()
}

// lang returns the first word after the opening fence on the line at start.
// It is read from the source: goldmark's own copy drops a one-letter word
// on a last line that has no line break.
func (c *cutter) lang(start int) string {
	fence := strings.TrimLeft(c.src[start:lineEnd(c.src, start+1)], " ")
	if fence == "" {
		return ""
	}
	info := strings.TrimLeft(strings.TrimLeft(fence, fence[:1]), spaces)
	if i := strings.IndexAny(info, spaces); i >= 0 {
		info = info[:i]
	}
	return info
}

// lone reports whether a paragraph is one fetchable image and nothing else,
// and returns the Image part that stands for its lines.
func (c *cutter) lone(p *ast.Paragraph, images []*ast.Image) (piece, bool) {
	if c.left <= 0 || len(images) != 1 || p.ChildCount() != 1 || p.FirstChild() != ast.Node(images[0]) {
		return piece{}, false
	}
	path, ok := Fetchable(destination(images[0]))
	lines := p.Lines()
	if !ok || lines.Len() == 0 {
		return piece{}, false
	}
	first, last := lines.At(0), lines.At(lines.Len()-1)
	start, end := lineStart(c.src, first.Start), lineEnd(c.src, last.Stop)
	if start < c.floor || !blank(c.src[start:first.Start]) || !blank(c.src[last.Stop:end]) {
		return piece{}, false
	}
	c.left--
	return piece{
		Part:  Part{Kind: Image, Alt: altText(images[0], c.raw), Path: path},
		start: start,
		end:   end,
	}, true
}

// put adds a part that stands for src[start:end].
func (c *cutter) put(p Part, start, end int) {
	c.out = append(c.out, piece{Part: p, start: start, end: end})
	c.cursor = end
	c.floor = max(c.floor, end)
}

// flush gives the text before offset to to a Text part, then adds the
// images that were waiting for that text to end.
func (c *cutter) flush(to int) {
	if to > c.cursor {
		c.text(c.cursor, to)
		c.cursor = to
		c.floor = max(c.floor, to)
	}
	for _, p := range c.waiting {
		c.out = append(c.out, piece{Part: p, start: c.cursor, end: c.cursor})
	}
	c.waiting = c.waiting[:0]
}

// text adds src[start:end] as a Text part, less the blank lines at its ends.
func (c *cutter) text(start, end int) {
	run := c.src[start:end]
	rest := strings.TrimLeft(run, blanks)
	if rest == "" {
		c.out = append(c.out, piece{start: start, end: end})
		return
	}
	// The first line keeps its indentation: it may be an indented code
	// block.
	from := lineStart(c.src, end-len(rest))
	to := from + len(strings.TrimRight(c.src[from:end], blanks))
	if from > start {
		c.out = append(c.out, piece{start: start, end: from})
	}
	c.out = append(c.out, piece{Part: Part{Kind: Text, Text: c.src[from:to]}, start: from, end: to})
	if to < end {
		c.out = append(c.out, piece{start: to, end: end})
	}
}

// survey reads one top-level block: the first and last source bytes its
// lines hold (-1 when it holds none) and its images, outermost only, in
// order.
func survey(block ast.Node) (lo, hi int, images []*ast.Image) {
	lo, hi = -1, -1
	note := func(s text.Segment) {
		if lo < 0 || s.Start < lo {
			lo = s.Start
		}
		hi = max(hi, s.Stop)
	}
	// The walker returns only what the callback does, which is no error.
	_ = ast.Walk(block, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		if img, ok := n.(*ast.Image); ok {
			images = append(images, img)
			return ast.WalkSkipChildren, nil
		}
		if n.Type() != ast.TypeBlock {
			return ast.WalkContinue, nil
		}
		lines := n.Lines()
		for i := range lines.Len() {
			note(lines.At(i))
		}
		switch b := n.(type) {
		case *ast.FencedCodeBlock:
			if b.Info != nil {
				note(b.Info.Segment)
			}
		case *ast.HTMLBlock:
			if b.HasClosure() {
				note(b.ClosureLine)
			}
		}
		return ast.WalkContinue, nil
	})
	return lo, hi, images
}

// destination returns the destination an image names. The tree holds it as
// written, so its backslash escapes and character references are resolved
// here, the way a renderer resolves them: "a\_b.png" names a_b.png, and
// "https&colon;//host" is a URL with a scheme, which Fetchable then refuses.
func destination(img *ast.Image) string {
	d := util.UnescapePunctuations(img.Destination)
	d = util.ResolveNumericReferences(d)
	d = util.ResolveEntityNames(d)
	return string(d)
}

// altText returns an image's alternative text: the words of its
// description without their Markdown, on one line.
func altText(img *ast.Image, src []byte) string {
	var sb strings.Builder
	writeText(&sb, img, src)
	return strings.Join(strings.Fields(sb.String()), " ")
}

func writeText(sb *strings.Builder, n ast.Node, src []byte) {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch t := c.(type) {
		case *ast.String:
			sb.Write(t.Value)
		case *ast.Text:
			v := t.Segment.Value(src)
			if !t.IsRaw() {
				v = util.ResolveEntityNames(util.ResolveNumericReferences(util.UnescapePunctuations(v)))
			}
			sb.Write(v)
			if t.SoftLineBreak() || t.HardLineBreak() {
				sb.WriteByte(' ')
			}
		case *ast.AutoLink:
			sb.Write(t.Label(src))
		default:
			writeText(sb, c, src)
		}
	}
}

// blanks are the bytes a blank line is made of.
const blanks = " \t\r\n"

// spaces are the bytes that end a word after a fence, as goldmark has them.
const spaces = " \t\r\n\v\f"

// blank reports whether s holds nothing to see.
func blank(s string) bool {
	return strings.Trim(s, blanks) == ""
}

// lineStart returns the offset of the first byte of the line that holds
// offset at.
func lineStart(src string, at int) int {
	return strings.LastIndexByte(src[:at], '\n') + 1
}

// lineEnd returns the offset after the line that ends at or after offset
// at, its line break included. An offset just after a line break is that
// line's end.
func lineEnd(src string, at int) int {
	if at > 0 && src[at-1] == '\n' {
		return at
	}
	i := strings.IndexByte(src[at:], '\n')
	if i < 0 {
		return len(src)
	}
	return at + i + 1
}
