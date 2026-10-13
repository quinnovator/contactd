package ctxtop

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/quinnovator/contactd/internal/diagnostic"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
)

// firstDifference compares bytes, including when one request is a prefix.
func firstDifference(a, b string) int {
	n := min(len(a), len(b))
	for i := range n {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

func canonical(d diagnostic.Details) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, c := range d.Blocks {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(c.Canonical)
	}
	b.WriteByte(']')
	return b.String()
}

func sharedBlocks(a, b diagnostic.Snapshot) (int, int) {
	tokens, tier := 0, -1
	for i, v := range b.Blocks {
		if i >= len(a.Blocks) || a.Blocks[i].Hash != v.Hash {
			return tokens, v.Tier
		}
		tokens += v.Tokens
	}
	return tokens, tier
}

func diffCause(a, b diagnostic.Snapshot) string {
	if a.Route != b.Route {
		return "route changed"
	}
	_, t := sharedBlocks(a, b)
	switch t {
	case -1:
		return "same logical prompt; provider/cache lifetime may differ"
	case 0:
		return "system prompt or tool schema changed"
	case 1:
		return "profile changed"
	case 2:
		return "recall rendering changed"
	case 3:
		for _, e := range b.Events {
			if e == "fold" {
				return "checkpoint changed the tail"
			}
			if e == "cut" {
				return "tail guard cut history"
			}
		}
		return "tail changed; inspect first differing block"
	case 4:
		if a.Turn == b.Turn && b.Step > a.Step {
			return "tool step appended in the live tier"
		}
		return "new turn / time note / live messages changed"
	}
	return "unknown"
}

func (m *model) diffView(s diagnostic.Snapshot, w int) []string {
	prev := m.previous()
	if prev.Call == 0 {
		return []string{"No earlier call for this contact and purpose."}
	}
	a, okA := m.captures[prev.Call]
	b, okB := m.captures[s.Call]
	if !okA || !okB {
		return []string{"Loading both call captures…"}
	}
	shared, tier := sharedBlocks(prev, s)
	mode := "canonical logical blocks (hash input, not wire)"
	x, y := canonical(a), canonical(b)
	if m.raw {
		mode = "exact provider request bodies"
		x, y = a.Wire, b.Wire
	}
	out := []string{fmt.Sprintf("DIFF call %d → %d · shared blocks ≈%s tokens · first changed tier %d", prev.Call, s.Call, k(shared), tier), m.clean(mode), ""}
	if (m.raw && (x == "" || y == "")) || a.Truncated || b.Truncated {
		return append(out, "Complete bodies unavailable; byte comparison withheld.")
	}
	if x == y {
		return append(out, "Requests are byte-identical.", "", m.delta(s))
	}
	at := firstDifference(x, y)
	out = append(out, fmt.Sprintf("first differing byte @ %d · lengths %d → %d", at, len(x), len(y)))
	context := func(v string) string {
		start := max(0, at-40)
		end := min(len(v), at+max(20, w-60))
		if start > len(v) {
			start = len(v)
		}
		return v[start:end]
	}
	out = append(out, fmt.Sprintf("%d  %s", prev.Call, m.clean(context(x))), fmt.Sprintf("%d  %s", s.Call, m.clean(context(y))))
	out = append(out, "", m.clean("prompt change (inferred): "+diffCause(prev, s)))
	miss := s.Total() - s.PredictedCached
	out = append(out, fmt.Sprintf("predicted re-read ≈%s tokens; monetary cost unavailable", k(max(0, miss))), m.delta(s), "", "[ / ] baseline · r canonical / exact wire")
	return out
}

func (m *model) timeline(w, h int) []string {
	calls := m.contactCalls()
	if len(calls) == 0 {
		return nil
	}
	s := m.current()
	sel := 0
	for i, c := range calls {
		if c.Call == s.Call {
			sel = i
		}
	}
	n := max(1, w-10)
	start := max(0, min(sel-n/2, len(calls)-n))
	end := min(len(calls), start+n)
	calls = calls[start:end]
	high := 1
	for _, c := range calls {
		high = max(high, c.Total())
	}
	rows := max(2, min(12, h-5))
	out := []string{fmt.Sprintf("TOKENS · %s max · each column is one call", k(high))}
	for row := rows; row > 0; row-- {
		threshold := high * row / rows
		var b strings.Builder
		for _, c := range calls {
			off, tier := 0, -1
			for _, t := range c.Tiers {
				off += t.Tokens
				if threshold <= off {
					tier = t.Index
					break
				}
			}
			if tier < 0 {
				b.WriteByte(' ')
			} else {
				tone := []string{"dim", "dim", "accent", "ok", "warn"}[min(4, max(0, tier))]
				glyph := m.glyphs().hit
				if c.Call == s.Call {
					glyph = "│"
				}
				b.WriteString(m.tone(glyph, tone))
			}
		}
		out = append(out, fmt.Sprintf("%6s │%s", k(threshold), b.String()))
	}
	var cache strings.Builder
	for _, c := range calls {
		glyph := "?"
		tone := "dim"
		if c.Reported {
			ratio := max(0, min(1, float64(c.ReportedCached)/float64(max(1, c.ReportedInput))))
			level := min(7, int(ratio*8))
			glyph = string([]rune("▁▂▃▄▅▆▇█")[level])
			tone = "warn"
			if ratio >= .8 {
				tone = "ok"
			}
			if m.theme.Caps.Glyphs == termcaps.GlyphsASCII {
				glyph = strconv.Itoa(min(9, int(ratio*10)))
			}
		}
		if !c.Done {
			glyph = "_"
		}
		style := m.theme.Style(tone)
		if c.Call == s.Call {
			style = style.Reverse(true)
		}
		cache.WriteString(style.Render(glyph))
	}
	out = append(out, " cache  "+cache.String())
	var events strings.Builder
	for _, c := range calls {
		g := " "
		for _, e := range c.Events {
			switch e {
			case "tool":
				g = "T"
			case "recall":
				g = "R"
			case "fold":
				g = "F"
			case "cut":
				g = "C"
			}
		}
		events.WriteString(g)
	}
	out = append(out, " event  "+events.String(), fmt.Sprintf(" calls  %d … %d · selected %d", calls[0].Call, calls[len(calls)-1].Call, s.Call), "tiers  0–1 config/profile · 2 recall · 3 tail · 4 live")
	seenChildren := make(map[uint64]bool)
	for _, child := range m.history {
		id := uint64(child.Contact)
		if child.Parent == s.Contact && child.InheritedTokens > 0 && !seenChildren[id] {
			seenChildren[id] = true
			out = append(out, fmt.Sprintf("fork #%d · inherited ≈%s tokens (%.0f%% of child)", child.Contact, k(child.InheritedTokens), 100*float64(child.InheritedTokens)/float64(max(1, child.Total()))))
		}
	}
	return out
}
