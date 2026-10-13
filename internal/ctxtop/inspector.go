package ctxtop

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/diagnostic"
	"github.com/quinnovator/contactd/internal/tui/safe"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
)

// inspector says what is known about the selected row, then shows its text.
// In raw mode it shows the request body, which belongs to no row.
func (m *model) inspector(s diagnostic.Snapshot, rows []item, sel, w int) []string {
	if w < 20 {
		w = max(20, m.width-2)
	}
	d, ok := m.captures[s.Call]
	if !ok {
		return []string{m.tone("Loading call details…", "dim")}
	}
	if m.raw {
		head := []string{
			m.theme.Title().Render("The exact request body sent to the provider"),
			m.tone(fmt.Sprintf("%d bytes · HTTP headers are not captured", d.WireBytes), "dim"), "",
		}
		if d.Wire == "" {
			return append(head, "The body is unavailable, or larger than the capture limit.")
		}
		return append(head, m.wrap(d.Wire, w)...)
	}
	if sel >= len(rows) {
		return []string{m.tone("Select a row to see it in full.", "dim")}
	}
	row := rows[sel]
	var out []string
	if d.Truncated {
		out = append(out, m.tone("This capture was cut at the diagnostic size limit.", "warn"))
	}
	switch {
	case row.block != nil:
		b := row.block
		t, _, _ := tierOf(s, b.Tier)
		out = append(out, m.tone(strings.ToUpper(kindName(b.Kind))+" · in the "+t.Name+" tier", "accent"))
		size := fmt.Sprintf("≈%s tokens · %s of this prompt", k(b.Tokens), pct(b.Tokens, s.Total()))
		if !slices.ContainsFunc(s.Blocks, func(o diagnostic.Block) bool { return o.Tokens > b.Tokens }) && len(s.Blocks) > 1 {
			size += " · the largest part"
		}
		out = append(out, m.field("Size", size, w)...)
		out = append(out, m.field("Cache", cacheSplit(s, b.Offset, b.Tokens), w)...)
		switch {
		case b.From != 0:
			out = append(out, m.field("Covers", fmt.Sprintf("log events %d–%d, summarized · 5 Checkpoints shows the original", b.From, b.To), w)...)
		case b.Seq != 0 && b.Episode != 0:
			out = append(out, m.field("From", fmt.Sprintf("log event %d in episode %d · g shows it in Log", b.Seq, b.Episode), w)...)
		case b.Seq != 0:
			out = append(out, m.field("From", fmt.Sprintf("log event %d · g shows it in Log", b.Seq), w)...)
		}
		first, n := s.Call, 0
		for _, call := range m.contactCalls() {
			if call.Call <= s.Call && slices.ContainsFunc(call.Blocks, func(o diagnostic.Block) bool { return o.Hash == b.Hash && o.Seq == b.Seq }) {
				first = min(first, call.Call)
				n++
			}
		}
		if n > 1 {
			out = append(out, m.field("Age", fmt.Sprintf("unchanged since call %d · in %d of the calls shown", first, n), w)...)
		} else {
			out = append(out, m.field("Age", "new in this call", w)...)
		}
		if b.Fate != "" {
			out = append(out, m.field("Next", b.Fate, w)...)
		}
		id := "hash " + b.Hash
		if b.Chain != "" {
			id += " · tier chain " + b.Chain
		}
		out = append(out, m.field("ID", id, w)...)
		out = append(out, m.theme.Rule(w, "content"))
		text := m.content(b.ID).Text
		if text == "" {
			text = b.Preview
		}
		out = append(out, m.wrap(text, w)...)
	case row.episode != nil:
		ep := row.episode
		out = append(out, m.tone(fmt.Sprintf("EPISODE %d · log events %d–%d", ep.ID, ep.Start, ep.End), "accent"))
		out = append(out, m.field("Here", episodeState(s, *ep), w)...)
		out = append(out, m.tone("■ sent as written · □ not sent as written", "dim"), m.theme.Rule(w, "events"))
		events := m.events[eventKey(s, ep.Start, ep.End)]
		if events == nil {
			return append(out, m.tone("Loading episode…", "dim"))
		}
		for _, ev := range events {
			state, tone := "□", "dim"
			if slices.ContainsFunc(s.Blocks, func(b diagnostic.Block) bool { return b.Seq == ev.Seq && b.Kind != "checkpoint" }) {
				state, tone = "■", "fg"
			}
			for _, line := range m.prose(fmt.Sprintf("%s %d %s", state, ev.Seq, eventText(ev)), w) {
				out = append(out, m.tone(line, tone))
			}
		}
		if len(events) >= 2000 {
			out = append(out, m.tone("Only the first 2,000 events are shown.", "warn"))
		}
	case row.candidate != nil:
		c := row.candidate
		threshold := float32(0)
		if d.Recall != nil {
			threshold = d.Recall.Threshold
		}
		out = append(out, m.tone(fmt.Sprintf("EPISODE %d · log events %d–%d", c.Episode, c.Start, c.End), "accent"))
		out = append(out, m.field("Decision", decision(c.Reason, threshold), w)...)
		out = append(out, m.field("1 Similar", fmt.Sprintf("%.3f · cosine of the search text and the episode's centroid", c.Cosine), w)...)
		out = append(out, m.field("2 Rerank", fmt.Sprintf("%.3f · the reranker's relevance score", c.Rerank), w)...)
		out = append(out, m.field("3 Final", fmt.Sprintf("%.3f · rerank plus a recency bonus; %.2f or more passes", c.Score, threshold), w)...)
		out = append(out, m.theme.Rule(w, "passage the reranker read"))
		out = append(out, m.wrap(c.Passage, w)...)
	case row.tier >= 0:
		t, offset, ok := tierOf(s, row.tier)
		if !ok {
			break
		}
		out = append(out, m.tone(strings.ToUpper(t.Name)+" TIER · "+tierNote(t.Name), "accent"))
		out = append(out, m.field("Size", fmt.Sprintf("≈%s tokens in %d messages · %s of this prompt", k(t.Tokens), t.Messages, pct(t.Tokens, s.Total())), w)...)
		out = append(out, m.field("Cache", cacheSplit(s, offset, t.Tokens), w)...)
		out = append(out, m.field("ID", "hash "+t.Hash+" · chain "+t.Chain, w)...)
		out = append(out, "", m.tone("Enter folds or unfolds this tier.", "dim"))
	}
	return out
}

// field is a labelled fact. A value too long for the pane wraps under itself.
func (m *model) field(label, value string, w int) []string {
	const indent = 10
	lines := m.theme.Wrap(m.clean(value), max(8, w-indent))
	for i := range lines {
		head := ""
		if i == 0 {
			head = label
		}
		lines[i] = m.tone(fmt.Sprintf("%-*s", indent, head), "dim") + lines[i]
	}
	return lines
}

// cacheSplit says how much of one part of the prompt came from the cache.
func cacheSplit(s diagnostic.Snapshot, offset, tokens int) string {
	edge, reported := cacheEdge(s)
	hit := max(0, min(tokens, int(edge)-offset))
	reused, fresh, basis := "reused", "sent fresh", "estimated from the provider's total"
	if !reported {
		reused, fresh, basis = "expected from cache", "expected fresh", "the planner's prediction"
	}
	switch hit {
	case tokens:
		return "all of it " + reused + " · " + basis
	case 0:
		return "all of it " + fresh + " · " + basis
	}
	return fmt.Sprintf("≈%s %s, ≈%s %s · %s", k(hit), reused, k(tokens-hit), fresh, basis)
}

// prose wraps a message at its spaces. Each line is made safe first: the
// theme's own wrapping keeps escape sequences, and this text is the server's.
func (m *model) prose(s string, w int) []string {
	var out []string
	for line := range strings.SplitSeq(s, "\n") {
		out = append(out, m.theme.Wrap(safe.Line(line, 2<<20, m.fold != nil), w)...)
	}
	return out
}

func (m *model) wrap(s string, w int) []string {
	// Sanitize each logical line before measuring it; never interpret content as ANSI.
	var out []string
	for line := range strings.SplitSeq(s, "\n") {
		clean := safe.Line(line, 2<<20, m.theme.Caps.Glyphs == termcaps.GlyphsASCII)
		if clean == "" {
			out = append(out, "")
			continue
		}
		var b strings.Builder
		cells := 0
		for _, r := range clean {
			n := m.theme.Width(string(r))
			if cells+n > w && cells > 0 {
				out = append(out, b.String())
				b.Reset()
				cells = 0
			}
			b.WriteRune(r)
			cells += n
		}
		out = append(out, b.String())
	}
	return out
}

func eventText(ev core.Event) string {
	switch ev.Kind {
	case core.KindUserMsg, core.KindAgentMsg:
		var v core.MsgBody
		if json.Unmarshal(ev.Body, &v) == nil {
			if ev.Kind == core.KindUserMsg {
				return "user: " + v.Text
			}
			return "agent: " + v.Text
		}
	case core.KindToolCall:
		var v core.ToolCall
		if json.Unmarshal(ev.Body, &v) == nil {
			return "→ " + v.Name + " " + v.Code + string(v.Args)
		}
	case core.KindToolResult:
		var v core.ToolResult
		if json.Unmarshal(ev.Body, &v) == nil {
			return "← " + v.Output
		}
	case core.KindSummary, core.KindEnvSnapshot, core.KindAttempt, core.KindNotice:
		// Bookkeeping is shown as its tagged JSON.
	case core.KindCheckpoint:
		var v core.CheckpointBody
		if json.Unmarshal(ev.Body, &v) == nil {
			return fmt.Sprintf("◆ seq %d–%d: %s", v.From, v.To, v.Text)
		}
	}
	return ev.Kind.String() + ": " + string(ev.Body)
}

func missingWords(original, summary string) string {
	return regexp.MustCompile(`(?:[[:alnum:]_.-]+/)+[[:alnum:]_.-]+|\b[0-9]+(?:\.[0-9]+)?\b`).ReplaceAllStringFunc(original, func(s string) string {
		if !strings.Contains(summary, s) {
			return "!" + s + "!"
		}
		return s
	})
}
