package ctxtop

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/quinnovator/contactd/internal/diagnostic"
	"github.com/quinnovator/contactd/internal/tui/safe"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
)

type glyphs struct{ hit, miss, empty, line, budget string }

func (m *model) glyphs() glyphs {
	if m.theme.Caps.Glyphs == termcaps.GlyphsASCII {
		return glyphs{"#", ":", ".", "-", "|"}
	}
	return glyphs{"█", "▒", "·", "─", "│"}
}

// asciiFold swaps the marks the views draw with for ASCII ones of the same
// width, so that columns stay aligned on a terminal without Unicode.
func asciiFold() *strings.Replacer {
	return strings.NewReplacer("›", ">", "▾", "v", "▸", ">", "·", "-", "≈", "~", "…", "~", "–", "-", "→", ">", "←", "<",
		"↑", "^", "↓", "v", "↺", "@", "◆", "*", "■", "#", "□", "o", "▲", "^", "✓", "+", "│", "|", "─", "-", "█", "#", "▒", ":", "▏", "|")
}

// asciiOnly is the last pass over a drawn line on such a terminal: what the
// fold has no stand-in for becomes "?". The line's own styles are kept; text
// from the server was made safe before it was styled.
func asciiOnly(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= utf8.RuneSelf {
			return '?'
		}
		return r
	}, s)
}

func (m *model) clean(s string) string {
	if m.fold != nil {
		s = m.fold.Replace(s)
	}
	return safe.Line(s, 1024, m.fold != nil)
}

func (m *model) tone(s, tone string) string { return m.theme.Style(tone).Render(m.clean(s)) }

func (m *model) bar(cells []cell, s diagnostic.Snapshot) (string, string) {
	g := m.glyphs()
	cached, hitTone := float64(s.PredictedCached), "dim"
	if s.Reported {
		cached, hitTone = reportedPosition(s), "ok"
	}
	var labels, fill strings.Builder
	for _, c := range cells {
		label, symbol, tone := g.line, g.empty, "dim"
		if c.tier >= 0 {
			if c.first {
				label = strconv.Itoa(c.tier)
			}
			symbol, tone = g.miss, "warn"
			if c.start < cached-1e-9 {
				symbol, tone = g.hit, hitTone
			}
		} else {
			label = " "
		}
		labels.WriteString(m.tone(label, "dim"))
		fill.WriteString(m.tone(symbol, tone))
	}
	return labels.String(), fill.String()
}

// tail draws the recent history against its budget. Past the budget mark the
// oldest part is due to be folded into a checkpoint.
func (m *model) tail(s diagnostic.Snapshot, w int) []string {
	if s.TailBudget <= 0 {
		return []string{m.tone("This call has no tail budget.", "dim")}
	}
	t, _, _ := tierOf(s, 3)
	tokens := t.Tokens
	guard := max(s.TailBudget, s.TailGuard)
	fill := min(w, max(0, int(float64(tokens)*float64(w)/float64(guard))))
	mark := min(w-1, int(float64(s.TailBudget)*float64(w)/float64(guard)))
	g, tone, state := m.glyphs(), "ok", "of the"
	if tokens > s.TailBudget {
		tone, state = "warn", "over the"
	}
	var bar strings.Builder
	for i := range w {
		switch {
		case i == mark:
			bar.WriteString(m.tone(g.budget, "bad"))
		case i < fill:
			bar.WriteString(m.tone(g.hit, tone))
		default:
			bar.WriteString(m.tone(g.empty, "dim"))
		}
	}
	return []string{
		fmt.Sprintf("Tail now  %s  ≈%s %s %s budget", bar.String(), k(tokens), state, k(s.TailBudget)),
		m.tone("          past the ", "dim") + m.tone(g.budget, "bad") + m.tone(" mark the oldest part is folded into a checkpoint · hard limit "+k(guard), "dim"),
	}
}

func (m *model) strip(h []diagnostic.Snapshot, sel, width int) string {
	// Keep the selected call visible while scrubbing an older page.
	start := max(0, min(sel-width/2, len(h)-width))
	end := min(len(h), start+width)
	var b strings.Builder
	for i := start; i < end; i++ {
		s, tone, glyph := h[i], "dim", m.glyphs().hit
		for _, event := range []struct{ name, glyph string }{{"tool", "T"}, {"recall", "R"}, {"fold", "F"}, {"cut", "C"}, {"fold-request", "K"}, {"summary", "S"}} {
			if slices.Contains(s.Events, event.name) {
				glyph = event.glyph
			}
		}
		switch {
		case s.Error != "":
			tone, glyph = "bad", "!"
		case !s.Done:
			glyph = "_"
		case s.Reported:
			tone = "warn"
			if float64(s.ReportedCached)/float64(max(1, s.ReportedInput)) >= 0.8 {
				tone = "ok"
			}
		}
		style := m.theme.Style(tone)
		if i == sel {
			style = style.Reverse(true)
		}
		b.WriteString(style.Render(glyph))
	}
	return b.String()
}

// share is part of whole as a fraction between 0 and 1.
func share(part, whole int) float64 {
	return max(0, min(1, float64(part)/float64(max(1, whole))))
}

func pct(part, whole int) string { return fmt.Sprintf("%.0f%%", 100*share(part, whole)) }

// took writes a duration in milliseconds the way it is said.
func took(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return fmt.Sprintf("%.1fs", float64(ms)/1000)
}

// tierNote says in a few words what a tier of the prompt holds.
func tierNote(name string) string {
	switch name {
	case "static":
		return "system prompt and tool schemas"
	case "profile":
		return "the contact's profile and long-term notes"
	case "recall":
		return "earlier episodes brought back"
	case "tail":
		return "recent history, kept verbatim"
	case "live":
		return "this turn's messages and tool steps"
	}
	return ""
}

// tierOf finds a tier and the estimated token offset at which it starts.
func tierOf(s diagnostic.Snapshot, index int) (diagnostic.Tier, int, bool) {
	offset := 0
	for _, t := range s.Tiers {
		if t.Index == index {
			return t, offset, true
		}
		offset += t.Tokens
	}
	return diagnostic.Tier{}, 0, false
}

// cacheEdge is where the cached prefix ends, in estimated tokens. It is the
// provider's reported share once there is one, and the prediction until then.
func cacheEdge(s diagnostic.Snapshot) (edge float64, reported bool) {
	if s.Reported {
		return reportedPosition(s), true
	}
	return float64(s.PredictedCached), false
}

func k(n int) string {
	if n < 1000 {
		return strconv.Itoa(n)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}
