package ctxtop

import (
	"fmt"
	"math"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/quinnovator/contactd/internal/diagnostic"
	"github.com/quinnovator/contactd/internal/tui/safe"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
)

func (m *model) View() tea.View {
	w := max(1, min(m.width-2, 998))
	s := m.current()
	lines := []string{}
	if s.Call == 0 {
		lines = append(lines, m.tone("ctxtop · waiting for a model call", "accent"), "Send a message with contactd chat. The server needs -diagnostics.")
	} else {
		name := s.Name
		if name == "" {
			name = "#" + s.Contact.String()
		}
		pred := float64(s.PredictedCached) / float64(max(1, s.Total()))
		cache := "cache pending"
		cue := m.tone("▲", "dim")
		if s.Reported {
			reported := float64(s.ReportedCached) / float64(max(1, s.ReportedInput))
			cache = fmt.Sprintf("cached %.0f%% (pred %.0f%%)", reported*100, pred*100)
			if math.Abs(reported-pred) > .05 {
				cue = m.tone("▲", "bad")
			}
		}
		if !s.Done {
			cue = m.tone([]string{"|", "/", "-", "\\"}[m.spin%4], "accent")
		}
		header := fmt.Sprintf("%s · %s · turn %d call %d │ ≈%s/%s │ %s", name, s.Route, s.Turn, s.Call, k(s.Total()), k(s.Window), cache)
		lines = append(lines, m.tone(m.theme.Truncate(m.clean(header), max(0, w-2)), "accent")+" "+cue)
		span := s.Window
		if m.zoom {
			span = s.Total()
		}
		_, fill := m.bar(layout(s, span, w), s)
		lines = append(lines, fill)
		tabs := make([]string, 0, 6)
		for i, name := range []string{"Blocks", "Log", "Diff", "Recall", "Folds", "Timeline"} {
			label := fmt.Sprintf("%d %s", i+1, name)
			if i == m.screen {
				label = m.tone("["+label+"]", "accent")
			} else {
				label = m.tone(label, "dim")
			}
			tabs = append(tabs, label)
		}
		lines = append(lines, strings.Join(tabs, "  "), m.tone(strings.Repeat(m.glyphs().line, w), "dim"))
		available := max(0, m.height-6)
		var body []string
		switch {
		case m.help:
			body = m.helpView()
		case m.screen == 2:
			body = m.diffView(s, w)
		case m.screen == 4:
			if m.raw {
				body = m.scrolled(m.inspector(s, m.items(), m.row[4], w), available)
			} else {
				body = m.foldView(s, w, available)
			}
		case m.screen == 5:
			body = m.timeline(w, max(3, available-2))
		default:
			body = m.listView(s, w, available)
		}
		if len(body) > available {
			body = body[:available]
		}
		lines = append(lines, body...)
		for len(lines) < max(0, m.height-2) {
			lines = append(lines, "")
		}
	}
	status := m.status
	if m.notice != "" {
		status = m.notice
	}
	if m.query != "" || m.searching {
		status = "/" + m.query + " · search loaded view · esc clears"
	}
	if s.Call != 0 && !s.Done {
		status += fmt.Sprintf(" · first event %dms · output ≈%s tok (%d B)", s.TTFT, k((s.OutputBytes+3)/4), s.OutputBytes)
	}
	if s.Done {
		status += fmt.Sprintf(" · %dms · output %d tokens", s.MS, s.OutputTokens)
	}
	lines = append(lines, m.tone(status, "dim"))
	lines = append(lines, m.tone("↑↓ select · enter expand · ←→ calls · / search · s sort · r raw · ? help · q quit", "dim"))
	if len(lines) > m.height {
		lines = lines[:max(0, m.height-1)]
		lines = append(lines, m.tone("? help · q quit", "dim"))
	}
	for i := range lines {
		if m.theme.Caps.Glyphs == termcaps.GlyphsASCII {
			lines[i] = safe.Line(lines[i], 10000, true)
		}
		lines[i] = m.theme.Truncate(lines[i], w)
	}
	v := tea.NewView(strings.Join(lines, "\n"))
	v.AltScreen = true
	return v
}

func (m *model) helpView() []string {
	return []string{
		"1 Blocks  2 Log  3 Diff  4 Recall  5 Folds  6 Timeline", "",
		"↑↓ or j/k select · ←→ or h/l scrub calls · f follow latest", "enter collapse a tier / expand inspector · g jump to Log",
		"s prompt/size order · / search current loaded view · esc clear", "r readable / exact provider request body · z window / zoom",
		"[ / ] choose Diff baseline from this contact and call purpose", "page up/down scroll inspector · tab/shift-tab cycle contacts",
		"k in Folds: mark numbers and paths absent from the checkpoint", "? close help · q quit", "",
		"≈ counts and cache boundaries use planner estimates.", "The provider reports only totals, not per-block cache hits.", "Canonical block bytes are for hashing; raw mode is the sent body.", "Log previews load up to 2,000 events in the selected span.", "Prices and parent-fork attribution are not collected yet.",
	}
}

func (m *model) listView(s diagnostic.Snapshot, w, h int) []string {
	rows := m.items()
	sel := min(m.row[m.screen], max(0, len(rows)-1))
	leftW := w
	split := w >= 100 && !m.expanded
	if split {
		leftW = max(48, w*3/5)
	}
	left := []string{}
	switch m.screen {
	case 0:
		order := "prompt order"
		if m.sizeSort {
			order = "size order"
		}
		left = append(left, m.tone("OFFSET  T  KIND           ≈TOKENS  CACHE  "+order, "dim"))
	case 1:
		left = append(left, "LOG · select an episode to inspect its messages")
	case 3:
		tr := m.captures[s.Call].Recall
		if tr == nil {
			left = append(left, "No recall trace for this call.")
		} else {
			left = append(left, m.clean("query: "+tr.Query), m.clean(fmt.Sprintf("threshold %.2f · margin %.2f · warm %v → %v", tr.Threshold, tr.Margin, tr.Warm, tr.Chosen)), m.clean(tr.Status), m.tone("EPISODE     COS   RERANK  +AGE SCORE  DECISION", "dim"))
		}
	case 4:
		left = append(left, m.tail(s, max(1, min(28, leftW-6))), "CHECKPOINTS currently represented in this prompt")
	}
	listHeight := h
	if !split && !m.expanded {
		listHeight = max(3, h/2)
	}
	start := max(0, sel-max(1, listHeight-len(left)-2)/2)
	end := min(len(rows), start+max(1, listHeight-len(left)-1))
	boundary := float64(s.PredictedCached)
	symbol := "△"
	if s.Reported {
		boundary = reportedPosition(s)
		symbol = "▲"
	}
	for i := start; i < end; i++ {
		row := rows[i]
		line := ""
		switch {
		case row.tier >= 0:
			arrow := "▾"
			if m.collapsed[row.tier] {
				arrow = "▸"
			}
			for _, t := range s.Tiers {
				if t.Index == row.tier {
					line = fmt.Sprintf("%s tier %d · %-9s ≈%s", arrow, t.Index, t.Name, k(t.Tokens))
				}
			}
		case row.block != nil:
			b := row.block
			if m.screen == 4 {
				line = fmt.Sprintf("◆ #%d  seq %d–%d  → ≈%s tok", b.Seq, b.From, b.To, k(b.Tokens))
			} else {
				hits := max(0, min(float64(b.Tokens), boundary-float64(b.Offset)))
				nc := int(math.Round(6 * hits / float64(max(1, b.Tokens))))
				bar := m.tone(strings.Repeat(m.glyphs().hit, nc), "ok") + m.tone(strings.Repeat(m.glyphs().miss, 6-nc), "warn")
				flag := ""
				if b.Tokens > s.Total()/10 {
					flag = " ⚑ heavy"
				}
				line = fmt.Sprintf("%6d  %d  %-14s %7d  %s %s%s", b.Offset, b.Tier, m.clean(kindLabel(b.Kind)), b.Tokens, bar, m.clean(b.Preview), flag)
				if !m.sizeSort && boundary >= float64(b.Offset) && boundary < float64(b.Offset+b.Tokens) {
					left = append(left, m.tone(fmt.Sprintf("  %s prefix ends ≈%.0f tokens, within next block", symbol, boundary), "warn"))
				}
			}
		case row.episode != nil:
			ep := row.episode
			state := "not selected"
			if ep.End >= tailStart(s) {
				state = "tail (partial)"
			}
			for _, b := range s.Blocks {
				if b.Kind == "checkpoint" && ep.Start <= b.To {
					state = "folded / outside tail"
				}
			}
			for _, id := range s.Recall {
				if id == ep.ID {
					state = "↺ recalled"
				}
			}
			line = fmt.Sprintf("ep %-4d seq %6d–%-6d  %s", ep.ID, ep.Start, ep.End, state)
		case row.candidate != nil:
			c := row.candidate
			line = fmt.Sprintf("ep %-5d %5.2f  %6.2f   %7.2f  %s", c.Episode, c.Cosine, c.Rerank, c.Score, m.clean(c.Reason))
		}
		prefix := "  "
		if i == sel {
			prefix = m.tone("› ", "accent")
		}
		left = append(left, prefix+line)
	}
	if len(rows) == 0 {
		left = append(left, m.tone("No entries for this call or search.", "dim"))
	}
	right := m.inspector(s, rows, sel, max(1, w-leftW-3))
	if m.expanded {
		return m.scrolled(right, h)
	}
	if !split {
		n := min(len(left), max(3, h/2))
		left = left[:n]
		left = append(left, m.tone(strings.Repeat(m.glyphs().line, w), "dim"))
		return append(left, m.scrolled(right, max(0, h-len(left)))...)
	}
	right = m.scrolled(right, h)
	n := min(h, max(len(left), len(right)))
	out := make([]string, 0, n)
	for i := range n {
		l, r := "", ""
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		l = m.theme.Truncate(l, leftW)
		out = append(out, l+strings.Repeat(" ", max(0, leftW-m.theme.Width(l)))+" │ "+r)
	}
	return out
}

func (m *model) scrolled(lines []string, h int) []string {
	start := min(m.scroll, max(0, len(lines)-1))
	return lines[start:min(len(lines), start+max(0, h))]
}

func kindLabel(kind string) string {
	switch kind {
	case "tool_call":
		return "→ tool call"
	case "tool_result":
		return "← tool result"
	case "recall":
		return "↺ recall"
	case "checkpoint":
		return "◆ checkpoint"
	case "time":
		return "⏱ time note"
	}
	return kind
}

func tailStart(s diagnostic.Snapshot) uint64 {
	var n uint64
	for _, b := range s.Blocks {
		if b.Tier == 3 && b.Kind != "checkpoint" && b.Seq != 0 && (n == 0 || b.Seq < n) {
			n = b.Seq
		}
	}
	if n == 0 {
		return ^uint64(0)
	}
	return n
}
