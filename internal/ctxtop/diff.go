package ctxtop

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/quinnovator/contactd/internal/diagnostic"
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

// diffView answers why the cache did or did not carry over from an earlier
// call: which tiers held, where the prompts part, and the bytes at that point.
func (m *model) diffView(s diagnostic.Snapshot, w, h int) []string {
	prev := m.previous()
	if prev.Call == 0 {
		return []string{
			m.theme.Title().Render(screens()[2].intro),
			m.tone("This is the first call shown for this contact and purpose, so there is nothing to compare it with.", "dim"),
		}
	}
	out := []string{m.theme.Title().Render(fmt.Sprintf("What changed from call %d to call %d, and what that did to the cache", prev.Call, s.Call))}
	a, okA := m.captures[prev.Call]
	b, okB := m.captures[s.Call]
	if !okA || !okB {
		return append(out, m.tone("Loading both calls…", "dim"))
	}
	fact := func(label, value string) {
		for _, line := range m.field(label, value, w-2) {
			out = append(out, "  "+line)
		}
	}
	// A short window gets the tiers in a sentence, and no blank lines.
	roomy := h >= 20
	gap := func() {
		if roomy {
			out = append(out, "")
		}
	}
	shared, changed := sharedBlocks(prev, s)
	gap()
	if roomy {
		out = append(out, m.tone(fmt.Sprintf("  %-9s %9s %9s", "TIER", fmt.Sprintf("CALL %d", prev.Call), fmt.Sprintf("CALL %d", s.Call)), "dim"))
	}
	var same, after []string
	for _, t := range s.Tiers {
		before, verdict, tone := "–", "unchanged", "ok"
		if old, _, ok := tierOf(prev, t.Index); ok {
			before = k(old.Tokens)
		}
		switch {
		case changed < 0 || t.Index < changed:
			same = append(same, t.Name)
		case t.Index == changed:
			verdict, tone = "← the first change is here", "warn"
		default:
			verdict, tone = "after the change, so sent fresh", "dim"
			after = append(after, t.Name)
		}
		if roomy {
			out = append(out, m.clean(fmt.Sprintf("  %-9s %9s %9s   ", t.Name, before, k(t.Tokens)))+m.tone(verdict, tone))
		}
	}
	if !roomy {
		tiers := "no tier changed"
		if t, _, ok := tierOf(s, changed); ok {
			tiers = "the first change is in " + t.Name
			if len(same) > 0 {
				tiers = strings.Join(same, ", ") + " unchanged · " + tiers
			}
			if len(after) > 0 {
				tiers += " · " + strings.Join(after, ", ") + " after it, so sent fresh"
			}
		}
		fact("Tiers", tiers)
	}
	gap()
	fact("Same", fmt.Sprintf("the first ≈%s tokens, %s of this prompt", k(shared), pct(shared, s.Total())))
	fact("Why", diffCause(prev, s)+" (inferred)")
	fresh := fmt.Sprintf("≈%s tokens expected fresh", k(max(0, s.Total()-s.PredictedCached)))
	expected := share(s.PredictedCached, s.Total())
	switch {
	case !s.Done:
		fact("Cache", fresh+" · waiting for the provider")
	case !s.Reported:
		fact("Cache", fresh+" · cache usage was not reported")
	default:
		reused := share(s.ReportedCached, s.ReportedInput)
		gap := (reused - expected) * 100
		fact("Cache", fmt.Sprintf("%s · the provider reused %.1f%%, %.1f%% expected (%+.1f points)", fresh, reused*100, expected*100, gap))
		if math.Abs(gap) > 5 {
			out = append(out, strings.Repeat(" ", 12)+m.tone("more than 5 points apart: worth a look, not proof of a cache bug", "bad"))
		}
	}
	gap()
	what, other := "the planner's canonical blocks", "r compares the exact request bodies instead"
	x, y := canonical(a), canonical(b)
	if m.raw {
		what, other = "the exact request bodies", "r compares the planner's blocks instead"
		x, y = a.Wire, b.Wire
	}
	if (m.raw && (x == "" || y == "")) || a.Truncated || b.Truncated {
		return append(out, m.tone("The complete bodies are not available, so their bytes cannot be compared.", "warn"))
	}
	if x == y {
		return append(out, "In "+what+", the two calls are byte for byte the same.", m.tone(other, "dim"))
	}
	at := firstDifference(x, y)
	out = append(out, m.theme.Title().Render("WHERE THEY FIRST DIFFER")+m.tone(fmt.Sprintf(" · byte %d of %s, %d and %d bytes long", at, what, len(x), len(y)), "dim"))
	start := max(0, at-40)
	context := func(v string) string { return m.clean(v[min(start, len(v)):min(len(v), at+max(20, w-60))]) }
	label := func(call uint64) string {
		return fmt.Sprintf("  call %-*d  ", len(strconv.FormatUint(max(prev.Call, s.Call), 10)), call)
	}
	out = append(out, m.tone(label(prev.Call), "dim")+context(x), m.tone(label(s.Call), "dim")+context(y))
	return append(out, strings.Repeat(" ", m.theme.Width(label(s.Call))+m.theme.Width(m.clean(y[min(start, len(y)):at])))+m.tone("▲ differs from here", "warn"))
}

// timeline draws one column per call of this contact and purpose: the prompt
// stacked by tier, then how much of it was cached and what happened.
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
	tones := []string{"dim", "dim", "accent", "ok", "warn"}
	rows := max(2, min(12, h-5))
	out := []string{m.theme.Title().Render(fmt.Sprintf("%s · calls %d–%d", screens()[5].intro, calls[0].Call, calls[len(calls)-1].Call))}
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
				b.WriteString(m.tone(m.glyphs().hit, tones[min(4, max(0, tier))]))
			}
		}
		out = append(out, m.tone(fmt.Sprintf("%6s │", k(threshold)), "dim")+b.String())
	}
	var cache strings.Builder
	for _, c := range calls {
		glyph := "?"
		tone := "dim"
		if c.Reported {
			ratio := share(c.ReportedCached, c.ReportedInput)
			level := min(7, int(ratio*8))
			glyph = string([]rune("▁▂▃▄▅▆▇█")[level])
			tone = "warn"
			if ratio >= .8 {
				tone = "ok"
			}
			if m.fold != nil {
				glyph = strconv.Itoa(min(9, int(ratio*10)))
			}
		}
		if !c.Done {
			glyph = "_"
		}
		cache.WriteString(m.theme.Style(tone).Render(glyph))
	}
	out = append(out, m.tone(" cache  ", "dim")+cache.String())
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
	levels := "taller means more of the prompt was reused"
	if m.fold != nil {
		levels = "tenths of the prompt that were reused"
	}
	g := m.glyphs().hit
	out = append(out, m.tone(" event  ", "dim")+events.String(),
		strings.Repeat(" ", 8+sel-start)+m.tone(fmt.Sprintf("▲ call %d, the one shown above", s.Call), "accent"),
		m.tone(" cache row   "+levels+" · ? not reported · _ still running", "dim"),
		m.tone(" event row   T tool step · R recall changed · F folded into a checkpoint · C old history cut", "dim"),
		m.tone(" columns     bottom to top: ", "dim")+m.tone(g, tones[0])+m.tone(" static and profile  ", "dim")+m.tone(g, tones[2])+m.tone(" recall  ", "dim")+m.tone(g, tones[3])+m.tone(" tail  ", "dim")+m.tone(g, tones[4])+m.tone(" live", "dim"))
	seenChildren := make(map[uint64]bool)
	for _, child := range m.history {
		id := uint64(child.Contact)
		if child.Parent == s.Contact && child.InheritedTokens > 0 && !seenChildren[id] {
			seenChildren[id] = true
			out = append(out, fmt.Sprintf(" fork #%d inherited ≈%s tokens from this contact, %s of its own prompt", child.Contact, k(child.InheritedTokens), pct(child.InheritedTokens, child.Total())))
		}
	}
	return out
}
