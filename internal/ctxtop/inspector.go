package ctxtop

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/diagnostic"
	"github.com/quinnovator/contactd/internal/tui/safe"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
)

func (m *model) inspector(s diagnostic.Snapshot, rows []item, sel, w int) []string {
	if w < 20 {
		w = max(20, m.width-2)
	}
	d, ok := m.captures[s.Call]
	if !ok {
		return []string{"Loading call details…"}
	}
	if m.raw {
		head := []string{"RAW · exact provider request body", fmt.Sprintf("%d bytes · headers excluded", d.WireBytes)}
		if d.Wire == "" {
			return append(head, "Body unavailable or exceeds capture limit.")
		}
		return append(head, m.wrap(d.Wire, w)...)
	}
	if sel >= len(rows) {
		return []string{"Select an entry to inspect it."}
	}
	row := rows[sel]
	out := []string{}
	if d.Truncated {
		out = append(out, m.tone("Capture truncated at diagnostic size limit", "warn"))
	}
	switch {
	case row.block != nil:
		b := row.block
		content := m.content(b.ID)
		if content.Text == "" && b.Kind == "checkpoint" {
			content.Text = b.Preview
		}
		out = append(out, m.tone(fmt.Sprintf("%s · tier %d", kindLabel(b.Kind), b.Tier), "accent"))
		out = append(out, fmt.Sprintf("source  seq %d · episode %d", b.Seq, b.Episode))
		if b.From != 0 {
			out = append(out, fmt.Sprintf("covers  seq %d–%d", b.From, b.To))
		}
		out = append(out, fmt.Sprintf("size    ≈%s tok · %d canonical B · %.1f%%", k(b.Tokens), b.Bytes, 100*float64(b.Tokens)/float64(max(1, s.Total()))))
		cached := float64(s.PredictedCached)
		label := "predicted"
		if s.Reported {
			cached = reportedPosition(s)
			label = "reported ratio"
		}
		hit := max(0, min(b.Tokens, int(cached)-b.Offset))
		out = append(out, fmt.Sprintf("cache   ≈%d hit / %d miss (%s)", hit, b.Tokens-hit, label), "hash    "+b.Hash, "tier chain "+b.Chain)
		first, n := s.Call, 0
		for _, call := range m.contactCalls() {
			if call.Call > s.Call {
				continue
			}
			for _, other := range call.Blocks {
				if other.Hash == b.Hash && other.Seq == b.Seq {
					first = min(first, call.Call)
					n++
					break
				}
			}
		}
		out = append(out, fmt.Sprintf("seen    since call %d · %d retained calls", first, n))
		out = append(out, m.wrap("fate    "+b.Fate, w)...)
		if s.ReportedInput > 0 {
			out = append(out, fmt.Sprintf("tokenizer  est %d / actual %d (%+.1f%%)", s.Total(), s.ReportedInput, 100*float64(s.Total()-s.ReportedInput)/float64(s.ReportedInput)))
		}
		out = append(out, strings.Repeat("─", min(w, 48)))
		if m.screen == 4 {
			events := m.events[eventKey(s, b.From, b.To)]
			if events == nil {
				return append(out, "Loading original fold span…")
			}
			var original strings.Builder
			tokens := 0
			for _, ev := range events {
				line := eventText(ev)
				original.WriteString(line + "\n")
				tokens += (len(line) + 3) / 4
			}
			out = append(out, fmt.Sprintf("ORIGINAL ≈%s → CHECKPOINT ≈%s", k(tokens), k(b.Tokens)))
			if m.keywords {
				out = append(out, "Keyword retention: ! marks absent numbers / paths.")
			}
			out = append(out, "CHECKPOINT:")
			out = append(out, m.wrap(content.Text, w)...)
			out = append(out, "", "ORIGINAL:")
			for line := range strings.SplitSeq(original.String(), "\n") {
				if m.keywords {
					line = missingWords(line, content.Text)
				}
				out = append(out, m.wrap(line, w)...)
			}
			if len(events) >= 2000 {
				out = append(out, "Original preview limited to 2,000 events.")
			}
		} else {
			out = append(out, m.wrap(content.Text, w)...)
		}
	case row.episode != nil:
		ep := row.episode
		out = append(out, fmt.Sprintf("EPISODE %d · seq %d–%d", ep.ID, ep.Start, ep.End), "■ in prompt · □ not in prompt (this call)")
		events := m.events[eventKey(s, ep.Start, ep.End)]
		if events == nil {
			return append(out, "Loading episode…")
		}
		for _, ev := range events {
			in := false
			for _, b := range s.Blocks {
				if b.Seq == ev.Seq && b.Kind != "checkpoint" {
					in = true
					break
				}
			}
			state, tone := "□", "dim"
			if in {
				state, tone = "■", "fg"
			}
			for _, line := range m.wrap(fmt.Sprintf("%s %d %s", state, ev.Seq, eventText(ev)), w) {
				out = append(out, m.tone(line, tone))
			}
		}
		if len(events) >= 2000 {
			out = append(out, "Episode preview limited to 2,000 events.")
		}
	case row.candidate != nil:
		c := row.candidate
		out = append(out, fmt.Sprintf("EPISODE %d · seq %d–%d", c.Episode, c.Start, c.End), fmt.Sprintf("centroid cosine %.3f", c.Cosine), fmt.Sprintf("rerank logit %.3f · with recency %.3f", c.Rerank, c.Score), c.Reason, "", "PASSAGE SHOWN TO RERANKER:")
		out = append(out, m.wrap(c.Passage, w)...)
	case row.tier >= 0:
		for _, t := range s.Tiers {
			if t.Index == row.tier {
				out = append(out, fmt.Sprintf("TIER %d · %s", t.Index, t.Name), fmt.Sprintf("≈%d tokens · %d messages", t.Tokens, t.Messages), "hash  "+t.Hash, "chain "+t.Chain, "", "Enter collapses / expands this tier.")
			}
		}
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
			return ev.Kind.String() + ": " + v.Text
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
