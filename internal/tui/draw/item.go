package draw

import (
	"strings"
	"time"

	"github.com/quinnovator/contactd/internal/transcript"
	"github.com/quinnovator/contactd/internal/tui/mods/wire"
)

// Item draws one entry of the conversation as a block of lines.
//
// A message from here is the prompt glyph and the text, bold in the accent
// tone.
//
// A reply is cut at its top-level blocks and drawn part by part, two cells
// in, with a blank line between parts. Text is rendered as Markdown. A fenced
// code block is drawn on the code background, 60 cells of it or the width,
// broken where a row is full and never at a space. An image that is a file
// of the contact's is asked of the ImageSource, and its description is not
// drawn. With ASCII glyphs the text is shown as written, wrapped. A reply
// longer than 32 KiB is shown as written whole, with a dim line under it that
// says so: the Markdown parser is slow on some input. So is the text of a
// reply that is nested deeper than the row has room for, or that holds more
// headings, quotes, list items or unnamed code blocks than the renderer lays
// out in a moment; its code blocks and images are still drawn.
//
// A character reference in a reply ("&#27;") is read only where it names a
// character that is safe to draw. Any other stays as it was written.
//
// A tool call is what the agent said first, drawn as a reply, then the tool
// glyph and the tool's name, with the code it runs dim under it. A tool's
// output hangs from the elbow glyph. Both are folded until the Renderer is
// expanded, and the fold counts rows of the screen, not lines of the text.
// Under 40 columns a folded call shows no code.
//
// A mod's lines stand in for a code block or for a tool's output once the
// RenderSource has them.
//
// A reply that is blank draws nothing, so a block can be empty.
func (r *Renderer) Item(it *transcript.Item) []string {
	if it == nil {
		return nil
	}
	var out []string
	switch it.Kind {
	case transcript.User:
		mark := r.th.G.Prompt
		if it.Task {
			mark = r.th.G.Task // a sub-agent's task, set by its parent
		}
		out = r.marked(mark, r.th.Style(toneAccent), trimText(r.clean(it.Text)))
	case transcript.Agent:
		out = r.reply(it.Seq, it.TS, it.Text)
		if it.Interrupted {
			out = append(out, strings.Repeat(" ", indent)+r.tone(toneDim, r.th.G.Stopped+" interrupted"))
		}
	case transcript.ToolCall:
		out = r.call(it)
	case transcript.ToolResult:
		out = r.result(it)
	case transcript.Notice:
		out = r.marked(r.th.G.Notice, r.th.Style(toneWarn), trimText(r.clean(it.Text)))
	case transcript.Checkpoint:
		out = []string{r.th.Rule(r.w, "earlier conversation folded into a summary")}
		if r.o.Expanded {
			out = append(out, r.indented(toneDim, trimBlank(r.th.Wrap(r.clean(it.Text), r.inner)))...)
		}
	case transcript.Failure:
		out = r.marked(r.th.G.Fail, r.th.Style(toneBad), "no reply: "+failureText(trimText(r.clean(it.Text))))
	}
	return r.fit(out)
}

// call draws a tool call: what the agent said first, the tool, and the code.
// Under 40 columns a folded call shows the tool alone.
func (r *Renderer) call(it *transcript.Item) []string {
	out := r.reply(it.Seq, it.TS, it.Lead)
	if len(out) > 0 {
		out = append(out, "")
	}
	out = append(out, r.tone(toneTool, r.th.G.Tool+" "+r.toolName(it.Tool)))

	code := trimText(r.clean(it.Text))
	if code == "" {
		return out
	}
	const codeIndent = 2 * indent
	limit := foldedCode
	if r.w < narrow {
		limit = 0
	}
	pad := strings.Repeat(" ", codeIndent)
	rows, hidden := r.folded(code, r.w-codeIndent, limit)
	for _, row := range rows {
		if row == "" {
			out = append(out, "")
			continue
		}
		out = append(out, pad+r.tone(toneDim, row))
	}
	if hidden > 0 && limit > 0 {
		out = append(out, pad+r.more(hidden, r.w-codeIndent))
	}
	return out
}

// result draws a tool's output under the elbow glyph, or what a mod made of
// it.
func (r *Renderer) result(it *transcript.Item) []string {
	lead := strings.Repeat(" ", indent) + r.th.G.Elbow + " "
	pad := strings.Repeat(" ", r.th.Width(lead))
	room := r.w - len(pad)
	tone := toneDim
	if it.IsError {
		tone = toneBad
	}
	text := trimText(r.clean(it.Text))

	if lines, ok := r.modded(RenderRef{Seq: it.Seq, N: 1, Kind: "tool:" + it.Tool, Text: text}, it.Tool, room); ok {
		out := r.modLines(lines, room)
		for i, line := range out {
			switch {
			case i == 0:
				out[i] = r.tone(tone, lead) + line
			case line != "":
				out[i] = pad + line
			}
		}
		return out
	}

	if text == "" {
		return []string{r.tone(tone, lead+"(no output)")}
	}
	rows, hidden := r.folded(text, room, foldedOutput)
	out := make([]string, 0, len(rows)+1)
	for i, row := range rows {
		switch {
		case i == 0:
			out = append(out, r.tone(tone, lead+row))
		case row == "":
			out = append(out, "")
		default:
			out = append(out, pad+r.tone(tone, row))
		}
	}
	if hidden > 0 {
		out = append(out, pad+r.more(hidden, room))
	}
	return out
}

// modded asks the mods for their drawing of one thing. A thing with no name
// to match a renderer by has none, and a mod that drew no lines has not
// drawn it: core does.
func (r *Renderer) modded(ref RenderRef, name string, width int) ([]wire.Line, bool) {
	if r.o.Mods == nil || name == "" {
		return nil, false
	}
	lines, ok := r.o.Mods.Rendered(ref, max(width, 1))
	return lines, ok && len(lines) > 0
}

// toolName is a tool's name as it is shown.
func (r *Renderer) toolName(name string) string {
	if name = r.name(name); name != "" {
		return name
	}
	return "tool"
}

// failureText says what a turn's failure reason means, where it is one of
// the runtime's own short codes.
func failureText(reason string) string {
	if reason == "step_limit" {
		return "the agent used every step it is allowed in one turn"
	}
	return reason
}

// Pending draws a message that was sent and is not yet acknowledged.
func (r *Renderer) Pending(text string) []string {
	out := r.marked(r.th.G.Prompt, r.th.Pending(), trimText(r.clean(text)))
	note := strings.Repeat(" ", indent) + "sending" + r.th.G.Ellipsis
	if last := len(out) - 1; r.th.Width(out[last])+r.th.Width(note) <= r.w {
		out[last] += r.tone(toneDim, note)
	} else {
		out = append(out, r.tone(toneDim, note))
	}
	return r.fit(out)
}

// Day draws the rule that separates one day's conversation from the next. ts
// is a time on that day, in unix ms.
func (r *Renderer) Day(ts int64) []string {
	return []string{r.th.Rule(r.w, time.UnixMilli(ts).In(r.loc).Format("Mon 2 Jan 2006"))}
}

// Beginning draws the rule above the first thing ever said.
func (r *Renderer) Beginning() []string {
	return []string{r.th.Rule(r.w, "the beginning")}
}

// Local draws what a command printed: a dim title, then the lines inside the
// indent, wrapped. A title of "" is left out. Every span is made safe on the
// way, whoever wrote it.
func (r *Renderer) Local(title string, lines []wire.Line) []string {
	out := make([]string, 0, len(lines)+1)
	if title = r.name(title); title != "" {
		out = append(out, r.tone(toneDim, r.th.Truncate(title, r.w)))
	}
	for _, line := range lines {
		out = append(out, r.indented("", r.th.Wrap(r.spans(line), r.inner))...)
	}
	return r.fit(out)
}
