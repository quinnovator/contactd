package markdown

import (
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// marks is where the parser met the reply's top-level blocks. goldmark's
// tree keeps the text of a block and not its place: a fence's own lines, and
// so an empty fence, are nowhere in it.
type marks struct {
	// start maps a top-level block to an offset on the line it opened on.
	start map[ast.Node]int
	// end maps a top-level fenced block to the end of its closing fence. A
	// fence that never closed has no entry.
	end map[ast.Node]int
}

// parse reads a reply as CommonMark and notes where each top-level block
// began.
func parse(src []byte) (ast.Node, *marks) {
	m := &marks{start: map[ast.Node]int{}, end: map[ast.Node]int{}}
	defaults := parser.DefaultBlockParsers()
	blocks := make([]util.PrioritizedValue, 0, len(defaults))
	for _, v := range defaults {
		if bp, ok := v.Value.(parser.BlockParser); ok {
			blocks = append(blocks, util.Prioritized(marking{BlockParser: bp, m: m}, v.Priority))
		}
	}
	// A parser is built for each reply because the marks are the reply's
	// own. It is the default parser with every block parser wrapped.
	p := parser.NewParser(
		parser.WithBlockParsers(blocks...),
		parser.WithInlineParsers(parser.DefaultInlineParsers()...),
		parser.WithParagraphTransformers(parser.DefaultParagraphTransformers()...),
	)
	return p.Parse(text.NewReader(src)), m
}

// marking is a block parser that notes where its top-level blocks open and
// where a fenced block closes. It changes nothing about the parse.
type marking struct {
	parser.BlockParser

	m *marks
}

// Open notes the line a top-level block opens on.
func (p marking) Open(parent ast.Node, reader text.Reader, pc parser.Context) (ast.Node, parser.State) {
	if parent.Kind() != ast.KindDocument {
		return p.BlockParser.Open(parent, reader, pc)
	}
	_, line := reader.PeekLine()
	before := pc.LastOpenedBlock().Node
	node, state := p.BlockParser.Open(parent, reader, pc)
	if node == nil {
		return nil, state
	}
	at := line.Start
	if state&parser.RequireParagraph != 0 {
		// A heading underlined with === or --- takes over the paragraph
		// above it, so it starts where that paragraph did.
		if s, ok := p.m.start[before]; ok {
			at = s
		}
	}
	p.m.start[node] = at
	return node, state
}

// Continue notes the closing fence of a top-level fenced block: the only
// line that makes one stop.
func (p marking) Continue(node ast.Node, reader text.Reader, pc parser.Context) parser.State {
	_, line := reader.PeekLine()
	state := p.BlockParser.Continue(node, reader, pc)
	if state&parser.Close != 0 && node.Kind() == ast.KindFencedCodeBlock {
		if _, top := p.m.start[node]; top {
			p.m.end[node] = line.Stop
		}
	}
	return state
}
