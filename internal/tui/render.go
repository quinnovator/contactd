package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/quinnovator/contactd/internal/transcript"
)

// How much of a tool call and of its output is shown before ctrl+o.
const (
	foldedCodeLines   = 6
	foldedOutputLines = 4
)

// styles are the few looks the interface has. They use the terminal's own
// sixteen colours, so they follow whatever theme the terminal has.
type styles struct {
	you      lipgloss.Style // the mark and text of a message from here
	pending  lipgloss.Style
	dim      lipgloss.Style
	tool     lipgloss.Style
	bad      lipgloss.Style
	notice   lipgloss.Style
	title    lipgloss.Style
	selected lipgloss.Style
	warn     lipgloss.Style
}

func newStyles() styles {
	return styles{
		you:      lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6")),
		pending:  lipgloss.NewStyle().Faint(true).Italic(true),
		dim:      lipgloss.NewStyle().Faint(true),
		tool:     lipgloss.NewStyle().Foreground(lipgloss.Color("4")),
		bad:      lipgloss.NewStyle().Foreground(lipgloss.Color("1")),
		notice:   lipgloss.NewStyle().Foreground(lipgloss.Color("3")),
		title:    lipgloss.NewStyle().Bold(true),
		selected: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6")),
		warn:     lipgloss.NewStyle().Foreground(lipgloss.Color("3")),
	}
}

// markdown renders an agent's reply for a given width.
type markdown func(text string, width int) string

// glamourMarkdown renders replies as Markdown. Renderers are kept per width,
// since building one is the slow part.
func glamourMarkdown(dark bool) markdown {
	style := "light"
	if dark {
		style = "dark"
	}
	renderers := make(map[int]*glamour.TermRenderer)
	return func(text string, width int) string {
		r := renderers[width]
		if r == nil {
			var err error
			if r, err = glamour.NewTermRenderer(glamour.WithStandardStyle(style), glamour.WithWordWrap(width)); err != nil {
				return plainMarkdown(text, width)
			}
			renderers[width] = r
		}
		out, err := r.Render(text)
		if err != nil {
			return plainMarkdown(text, width)
		}
		return trimBlankLines(out)
	}
}

// plainMarkdown shows a reply as written, wrapped and indented like the
// rendered form. It is what a reply looks like while it is still arriving,
// when re-rendering Markdown on every fragment would make the text jump.
func plainMarkdown(text string, width int) string {
	return indent(wrap(text, width-2), "  ")
}

// clean makes text from the server safe to put on a terminal. A tool's
// output, or a model, can contain escape sequences; printed as they are they
// would move the cursor, recolour the screen or set the window title.
func clean(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n':
			return r
		case r == '\t':
			return ' '
		case unicode.IsControl(r):
			return -1
		// Bidirectional overrides can make text read as something it is not.
		case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
			return -1
		default:
			return r
		}
	}, s)
}

func wrap(s string, width int) string {
	return ansi.Wrap(s, max(width, 8), "")
}

func indent(s, prefix string) string {
	return prefix + strings.ReplaceAll(s, "\n", "\n"+prefix)
}

func trimBlankLines(s string) string {
	lines := strings.Split(s, "\n")
	for len(lines) > 0 && strings.TrimSpace(ansi.Strip(lines[0])) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(ansi.Strip(lines[len(lines)-1])) == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

// fold keeps the first limit lines of s and says how many it left out.
func fold(s string, limit int, expanded bool) (shown string, hidden int) {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if expanded || len(lines) <= limit {
		return strings.Join(lines, "\n"), 0
	}
	return strings.Join(lines[:limit], "\n"), len(lines) - limit
}

// renderer draws transcript items. It holds no conversation state.
type renderer struct {
	st       styles
	md       markdown
	width    int
	expanded bool
	loc      *time.Location
}

// item draws one entry of the conversation as a block of lines.
func (r *renderer) item(it *transcript.Item) string {
	text := clean(it.Text)
	switch it.Kind {
	case transcript.User:
		mark := "❯ "
		if it.Task {
			mark = "⇢ " // a sub-agent's task, set by its parent
		}
		return r.st.you.Render(mark) + r.st.you.Render(indentRest(wrap(text, r.width-2), "  "))
	case transcript.Agent:
		out := r.md(text, r.width)
		if it.Interrupted {
			out += "\n" + r.st.dim.Render("  ⏹ interrupted")
		}
		return out
	case transcript.ToolCall:
		var b strings.Builder
		if lead := clean(it.Lead); strings.TrimSpace(lead) != "" {
			b.WriteString(r.md(lead, r.width))
			b.WriteString("\n\n")
		}
		b.WriteString(r.st.tool.Render("⏺ " + toolName(it.Tool)))
		if text != "" {
			shown, hidden := fold(text, foldedCodeLines, r.expanded)
			b.WriteString("\n" + r.st.dim.Render(indent(wrap(shown, r.width-4), "    ")))
			if hidden > 0 {
				b.WriteString("\n" + r.st.dim.Render(fmt.Sprintf("    … %d more lines (ctrl+o)", hidden)))
			}
		}
		return b.String()
	case transcript.ToolResult:
		st := r.st.dim
		if it.IsError {
			st = r.st.bad
		}
		if strings.TrimSpace(text) == "" {
			return st.Render("  ⎿ (no output)")
		}
		shown, hidden := fold(text, foldedOutputLines, r.expanded)
		out := st.Render("  ⎿ " + indentRest(wrap(shown, r.width-4), "    "))
		if hidden > 0 {
			out += "\n" + r.st.dim.Render(fmt.Sprintf("    … %d more lines (ctrl+o)", hidden))
		}
		return out
	case transcript.Notice:
		return r.st.notice.Render("◆ " + indentRest(wrap(text, r.width-2), "  "))
	case transcript.Checkpoint:
		head := r.rule("earlier conversation folded into a summary")
		if !r.expanded {
			return head
		}
		return head + "\n" + r.st.dim.Render(indent(wrap(text, r.width-2), "  "))
	case transcript.Failure:
		return r.st.bad.Render("✗ no reply: " + failureText(text))
	default:
		return ""
	}
}

// pending draws a message that has been sent and not yet acknowledged.
func (r *renderer) pending(text string) string {
	return r.st.pending.Render("❯ "+indentRest(wrap(clean(text), r.width-2), "  ")) + r.st.dim.Render("  sending…")
}

// live draws the turn in flight: what the agent has said so far, and the
// tool call it is in the middle of writing.
func (r *renderer) live(l transcript.Live) string {
	var parts []string
	if text := clean(l.Text); strings.TrimSpace(text) != "" {
		parts = append(parts, plainMarkdown(text, r.width))
	}
	if l.Starting != "" {
		parts = append(parts, r.st.tool.Render("⏺ "+toolName(l.Starting))+r.st.dim.Render(" …"))
	}
	return strings.Join(parts, "\n\n")
}

// day draws the line that separates one day's conversation from the next.
func (r *renderer) day(ts int64) string {
	return r.rule(time.UnixMilli(ts).In(r.loc).Format("Mon 2 Jan 2006"))
}

func (r *renderer) rule(label string) string {
	side := max((r.width-ansi.StringWidth(label)-2)/2, 2)
	line := strings.Repeat("─", side)
	return r.st.dim.Render(line + " " + label + " " + line)
}

// sameDay reports whether two timestamps fall on one calendar day.
func (r *renderer) sameDay(a, b int64) bool {
	ya, ma, da := time.UnixMilli(a).In(r.loc).Date()
	yb, mb, db := time.UnixMilli(b).In(r.loc).Date()
	return ya == yb && ma == mb && da == db
}

// indentRest indents every line but the first, for text that follows a mark.
func indentRest(s, prefix string) string {
	return strings.ReplaceAll(s, "\n", "\n"+prefix)
}

func toolName(name string) string {
	if name == "" {
		return "tool"
	}
	return clean(name)
}

// failureText says what a turn's failure reason means, where it is one of
// the runtime's own short codes.
func failureText(reason string) string {
	if reason == "step_limit" {
		return "the agent used every step it is allowed in one turn"
	}
	return reason
}

// ago says how long before now a moment was, the way a contact list does.
func ago(now time.Time, ts int64) string {
	if ts == 0 {
		return ""
	}
	d := now.Sub(time.UnixMilli(ts))
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 14*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return time.UnixMilli(ts).In(now.Location()).Format("2 Jan 2006")
	}
}
