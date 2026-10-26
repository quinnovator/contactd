package draw

import (
	"bytes"
	"cmp"
	"slices"
	"strings"

	"charm.land/glamour/v2"
	glamouransi "charm.land/glamour/v2/ansi"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/charmbracelet/x/ansi"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"

	"github.com/quinnovator/contactd/internal/tui/markdown"
)

const (
	// tooLong is the line under a reply that was too long to read as
	// Markdown.
	tooLong = "shown as written: this reply is longer than 32 KiB"
	// noBase is a base address that is not one.
	noBase = ":"
)

// reply is what was worked out about one reply, kept so that it is worked
// out once. Cutting a reply into parts costs a parse, and the parser is slow
// on some input.
type reply struct {
	// src is the reply the rest was made from, already safe to draw.
	src   string
	parts []markdown.Part
	// defs is the reply's link definitions, written out so that they can
	// stand in front of any of its Text parts. It is "" where no part can
	// need them: with fewer than two Text parts, or no definition.
	defs string
	// asWritten says the reply could not be cut and is drawn as written.
	asWritten bool
	// load is what the Text parts would cost the Markdown renderer.
	load load
}

// reply draws what an agent said: a reply, or the words before a tool call.
//
// The text is cut into parts at its top-level blocks. Text is rendered as
// Markdown at the width inside the indent. A fenced code block is drawn on
// the code background, hard-wrapped and never word-wrapped. An image that is
// a file of the contact's is asked of the ImageSource. One blank line stands
// between parts.
//
// With ASCII glyphs the text is shown as written, wrapped: the Markdown
// renderer has no ASCII form. So is a reply longer than 32 KiB, whole, with a
// line under it that says so, and the text of one that would take the
// renderer too long.
func (r *Renderer) reply(seq uint64, ts int64, raw string) []string {
	src := trimText(r.clean(raw))
	if src == "" {
		return nil
	}
	// Making text safe can make it longer: a tab becomes four spaces.
	if max(len(raw), len(src)) > maxMarkdown {
		return append(r.written(src), r.indented(toneDim, r.th.Wrap(tooLong, r.inner))...)
	}
	rep := r.parts(seq, src)
	if rep.asWritten {
		return r.written(src)
	}
	r.lookups = 0
	// Text that would take the Markdown renderer too long is shown as
	// written. Its code blocks and images are still drawn as what they are.
	slow := !r.ascii && r.tooHeavy(rep.load)

	var out []string
	codes, images := 0, 0
	for _, p := range rep.parts {
		var block []string
		switch p.Kind {
		case markdown.Text:
			if slow {
				block = r.written(p.Text)
			} else {
				block = r.text(p.Text, rep.defs)
			}
		case markdown.Code:
			codes++
			block = r.code(seq, codes, p)
		case markdown.Image:
			images++
			block = r.image(ImageRef{Seq: seq, N: images, Path: p.Path, Alt: p.Alt, TS: ts})
		}
		if len(block) == 0 {
			continue
		}
		if len(out) > 0 {
			out = append(out, "")
		}
		out = append(out, block...)
	}
	if slow {
		out = append(out, r.indented(toneDim, r.th.Wrap(heavy, r.inner))...)
	}
	return out
}

// parts returns the parts of a reply, from the cache when the reply is the
// one that was cut before. An item with no sequence number is not kept.
func (r *Renderer) parts(seq uint64, src string) *reply {
	if rep := r.cache[seq]; rep != nil && rep.src == src {
		return rep
	}
	r.n.splits++
	rep := cut(src)
	if seq != 0 {
		r.cache[seq] = rep
	}
	return rep
}

// cut reads a reply once. A parser that panics leaves the reply to be drawn
// as written; that is remembered too, so it is not tried on every frame.
func cut(src string) (rep *reply) {
	defer func() {
		if recover() != nil {
			rep = &reply{src: src, asWritten: true}
		}
	}()
	rep = &reply{src: src, parts: markdown.Split(src, maxImages)}
	texts := 0
	for _, p := range rep.parts {
		if p.Kind == markdown.Text {
			texts++
			rep.load.add(weigh(p.Text))
		}
	}
	// A definition is "[label]: destination", so a reply without "]:" has
	// none, and a reply in one piece of text needs no help to find its own.
	if texts > 1 && strings.Contains(src, "]:") {
		rep.defs = definitions(src)
	}
	return rep
}

// definitions returns the link definitions of a reply, one to a line, in a
// form that reads as definitions wherever it stands at the start of a text.
//
// Each part of a reply is rendered alone, and a link written "[text][label]"
// in one part can be defined in another. With these lines in front of it a
// part finds every definition of the reply. They draw nothing themselves: a
// definition is not part of what Markdown shows, and each line is read back
// here to make sure it is taken for one. The first definition of a label
// wins, in the whole reply as in each part.
func definitions(src string) string {
	pc := parser.NewContext()
	goldmark.DefaultParser().Parse(text.NewReader([]byte(src)), parser.WithContext(pc))
	refs := pc.References()
	slices.SortFunc(refs, func(a, b parser.Reference) int {
		return cmp.Compare(util.ToLinkReference(a.Label()), util.ToLinkReference(b.Label()))
	})
	var b strings.Builder
	for _, ref := range refs {
		b.WriteString(definition(ref))
	}
	return b.String()
}

// definition writes one link definition as a line of Markdown, or "" when
// no line reads back as the same definition and nothing else. The title is
// left out: the renderer does not draw it.
func definition(ref parser.Reference) string {
	label, dest := string(ref.Label()), string(ref.Destination())
	// In angle brackets a destination may hold a space and may not hold a
	// bracket; bare, the other way round. One of the two is how it was
	// written.
	for _, line := range []string{"[" + label + "]: <" + dest + ">\n", "[" + label + "]: " + dest + "\n"} {
		if defines(line, ref) {
			return line
		}
	}
	return ""
}

// defines reports whether a line of Markdown is the definition ref and draws
// nothing.
func defines(line string, ref parser.Reference) bool {
	pc := parser.NewContext()
	doc := goldmark.DefaultParser().Parse(text.NewReader([]byte(line)), parser.WithContext(pc))
	got := pc.References()
	if len(got) != 1 || !bytes.Equal(got[0].Destination(), ref.Destination()) ||
		util.ToLinkReference(got[0].Label()) != util.ToLinkReference(ref.Label()) {
		return false
	}
	for n := doc.FirstChild(); n != nil; n = n.NextSibling() {
		if n.Kind() != ast.KindTextBlock || n.Lines().Len() != 0 {
			return false
		}
	}
	return true
}

// text draws one Text part: Markdown, rendered at the width inside the
// indent. The renderer pads every line to the width; the padding goes. What
// is left is wrapped once more by the theme's own measure, which makes every
// line fit and stand alone whatever the renderer counted.
func (r *Renderer) text(src, defs string) []string {
	if r.ascii {
		return r.written(src)
	}
	in := src
	if defs != "" && strings.Contains(src, "[") {
		in = defs + "\n" + src
	}
	in, mark, ok := r.shield(in)
	if !ok {
		return r.written(src)
	}
	out, ok := r.render(&r.prose, r.inner, in)
	if !ok || !r.sound(out) {
		return r.written(src)
	}
	out = unshield(out, mark)
	// The renderer leaves two blank lines in some places; one is enough.
	lines := make([]string, 0, strings.Count(out, "\n")+1)
	for line := range strings.SplitSeq(out, "\n") {
		line = trimRight(line)
		if line == "" && (len(lines) == 0 || lines[len(lines)-1] == "") {
			continue
		}
		lines = append(lines, line)
	}
	if lines = trimBlank(r.quoteHeads(lines)); len(lines) == 0 {
		return nil
	}
	return r.indented("", links(r.th.Wrap(strings.Join(lines, "\n"), r.inner)))
}

// quoteHeads drops the empty row the renderer puts at the head of a quote
// that begins with a list or with another quote: a row of bars and nothing
// else, with nothing above it and more of the quote under it.
func (r *Renderer) quoteHeads(lines []string) []string {
	bar := r.th.G.Sep
	if bar == "" {
		return lines
	}
	out := lines[:0]
	for i, line := range lines {
		if strings.Contains(line, bar) && (len(out) == 0 || out[len(out)-1] == "") && i+1 < len(lines) &&
			r.onlyBars(line) && r.bars(lines[i+1]) >= r.bars(line) && !r.onlyBars(lines[i+1]) {
			continue
		}
		out = append(out, line)
	}
	return out
}

// bars counts the quote bars a rendered row begins with.
func (r *Renderer) bars(line string) int {
	n := 0
	for rest := strings.TrimLeft(ansi.Strip(line), " "); strings.HasPrefix(rest, r.th.G.Sep); n++ {
		rest = strings.TrimLeft(rest[len(r.th.G.Sep):], " ")
	}
	return n
}

// onlyBars reports whether a rendered row draws quote bars and nothing else.
func (r *Renderer) onlyBars(line string) bool {
	return r.bars(line) > 0 && strings.TrimSpace(strings.ReplaceAll(ansi.Strip(line), r.th.G.Sep, "")) == ""
}

// render runs the Markdown renderer kept in *tr, making it first when there
// is none. ok is false when it failed, or panicked: the caller then draws the
// text as written, and the renderer is made again next time.
func (r *Renderer) render(tr **glamour.TermRenderer, wrap int, src string) (out string, ok bool) {
	defer func() {
		if recover() != nil {
			*tr, out, ok = nil, "", false
		}
	}()
	if *tr == nil {
		opts := []glamour.TermRendererOption{
			glamour.WithStyles(r.style()),
			glamour.WithWordWrap(wrap),
			// Keep the line boundaries shown while streaming when the
			// finished reply gains Markdown styling.
			glamour.WithPreservedNewLines(),
			// The renderer resolves a relative address against a base, and
			// with an empty base "notes/a.md" comes out as "/notes/a.md". A
			// base that cannot be parsed makes it leave the address alone.
			glamour.WithBaseURL(noBase),
		}
		if f := r.th.ChromaFormatter(); f != "" {
			opts = append(opts, glamour.WithChromaFormatter(f))
		}
		made, err := glamour.NewTermRenderer(opts...)
		if err != nil {
			return "", false
		}
		*tr = made
	}
	r.n.markdowns++
	out, err := (*tr).Render(src)
	if err != nil {
		*tr = nil
		return "", false
	}
	return out, true
}

// style is the theme's Markdown style, with a block quote set up so that its
// rows fit. The renderer takes one cell off the width for each step of a
// quote's indent and writes the bar once for each step. The theme's bar is two
// cells, a rule and a space, at one step: every row of a quote came out a cell
// too wide, and the renderer then broke it again and left the last word on a
// row with no bar. A margin costs two cells and writes the bar once.
func (r *Renderer) style() glamouransi.StyleConfig {
	style := r.th.Markdown()
	q := &style.BlockQuote
	if q.IndentToken != nil && r.th.Width(*q.IndentToken) == 2 && q.Indent != nil && *q.Indent == 1 && q.Margin == nil {
		q.Indent, q.Margin = new(uint(0)), new(uint(1))
	}
	return style
}

// code draws one fenced code block, or what a mod made of it.
//
// The block is a dim row that names the language, then the code, each row on
// the code background with a cell of space on both sides. It fills 60 cells,
// or the whole width inside the indent where that is less or a line of the
// code needs more. A line longer than that is broken where the row is full,
// never at a space, and the rows after the break keep its colours.
//
// The code is highlighted where the theme names a formatter (with 256
// colours or more, on a background that is known), in the Unicode glyph set,
// when the block names a language the highlighter has.
func (r *Renderer) code(seq uint64, n int, p markdown.Part) []string {
	if lines, ok := r.modded(RenderRef{Seq: seq, N: n, Kind: "code:" + p.Lang, Text: p.Text}, p.Lang, r.inner); ok {
		return r.indented("", r.modLines(lines, r.inner))
	}

	cells := min(codeCells, r.inner)
	room := max(cells-2, 1)
	if cells < r.inner && r.wider(p.Text, room) {
		cells = r.inner
		room = max(cells-2, 1)
	}

	var rows []string
	if !r.ascii && r.th.ChromaFormatter() != "" && r.known(p.Lang) {
		rows = r.highlighted(p, room)
	}
	if rows == nil {
		rows = r.th.Hardwrap(p.Text, room)
	}
	rows = trimBlank(rows)

	out := make([]string, 0, len(rows)+1)
	if lang := r.name(p.Lang); lang != "" {
		out = append(out, r.codeRow(r.tone(toneDim, r.th.Truncate(lang, room)), cells))
	}
	for _, row := range rows {
		out = append(out, r.codeRow(row, cells))
	}
	return out
}

// known reports whether the highlighter has a language. A block that names
// none, or one it does not have, is drawn plain: left to itself the
// highlighter then tries every language it has on the code, which takes
// milliseconds a block. The answer for a name is kept.
func (r *Renderer) known(lang string) bool {
	if lang == "" {
		return false
	}
	known, ok := r.langs[lang]
	if !ok {
		// A name the highlighter does not have costs it a millisecond to
		// say so. A reply chooses its names, so only so many new ones are
		// looked up for one reply; the rest are taken as unknown this time.
		if r.lookups >= maxLookups {
			return false
		}
		r.lookups++
		if len(r.langs) >= maxLangs {
			clear(r.langs)
		}
		known = lexers.Get(lang) != nil
		r.langs[lang] = known
	}
	return known
}

// wider reports whether a line of text takes more than room cells.
func (r *Renderer) wider(s string, room int) bool {
	for line := range strings.SplitSeq(s, "\n") {
		// A cell is at least a byte, so a short line needs no measuring.
		if len(line) > room && r.th.Width(line) > room {
			return true
		}
	}
	return false
}

// highlighted returns the rows of a code block in the colours of its
// language, or nil when the renderer failed. The block is rendered alone and
// unwrapped, then broken by the theme, which carries the colours in force
// over each break.
func (r *Renderer) highlighted(p markdown.Part, room int) []string {
	// A fence of tildes may be followed by any word, and one longer than
	// every run of tildes in the code cannot be closed by the code.
	run, longest := 0, 0
	for i := range len(p.Text) {
		if p.Text[i] != '~' {
			run = 0
			continue
		}
		run++
		longest = max(longest, run)
	}
	fence := strings.Repeat("~", max(longest+1, 3))
	// The space keeps a language that begins with a tilde out of the fence.
	out, ok := r.render(&r.block, 0, fence+" "+p.Lang+"\n"+p.Text+"\n"+fence+"\n")
	if !ok || !r.sound(out) {
		return nil
	}
	return r.th.Hardwrap(strings.Trim(out, "\n"), room)
}

// codeRow draws one row of a code block: inside the indent, a cell in from
// the edge of the background. Where there is no background a blank row is
// empty.
func (r *Renderer) codeRow(row string, cells int) string {
	filled := r.th.CodeFill(" "+row, cells)
	if blank(filled) && !strings.Contains(filled, "\x1b") {
		return ""
	}
	return strings.Repeat(" ", indent) + filled
}

// image draws one image of a reply: the picture and its caption, or the line
// that stands for it and how to save it. The description is not drawn.
func (r *Renderer) image(ref ImageRef) []string {
	var s Shown
	if r.o.Images != nil {
		s = r.o.Images.Image(ref, r.cols, r.rows)
	}
	return r.imageShown(ref, s)
}

func (r *Renderer) imageShown(ref ImageRef, s Shown) []string {
	pad := strings.Repeat(" ", indent)
	if len(s.Rows) > 0 {
		out := make([]string, 0, len(s.Rows)+1)
		for _, row := range s.Rows {
			out = append(out, pad+row)
		}
		return append(out, r.aside(toneDim, s.Caption)...)
	}
	if s.Line == "" {
		s.Line = "[image: " + r.fileName(ref.Path) + "]"
	}
	return append(r.aside("", s.Line), r.aside(toneDim, s.Hint)...)
}

// aside draws one line of text about an image: made safe, wrapped, inside
// the indent. No text is no line.
func (r *Renderer) aside(tone, s string) []string {
	if s = r.note(s); s == "" {
		return nil
	}
	return r.indented(tone, r.th.Wrap(s, r.inner))
}
