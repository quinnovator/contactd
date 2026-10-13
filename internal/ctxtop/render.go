package ctxtop

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/quinnovator/contactd/internal/diagnostic"
	"github.com/quinnovator/contactd/internal/tui/safe"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
)

type glyphs struct{ hit, miss, empty, prediction, report, line, budget string }

func (m *model) glyphs() glyphs {
	if m.theme.Caps.Glyphs == termcaps.GlyphsASCII {
		return glyphs{"#", ":", ".", "^", "v", "-", "|"}
	}
	return glyphs{"█", "▒", "·", "△", "▲", "─", "│"}
}

func (m *model) clean(s string) string {
	return safe.Line(s, 1024, m.theme.Caps.Glyphs == termcaps.GlyphsASCII)
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

func (m *model) delta(s diagnostic.Snapshot) string {
	g := m.glyphs()
	pred := float64(s.PredictedCached) / float64(max(1, s.Total()))
	lead := fmt.Sprintf("%s predicted %s (%.1f%%)", g.prediction, k(s.PredictedCached), pred*100)
	if !s.Done {
		return lead + m.tone(" · waiting on provider...", "dim")
	}
	if !s.Reported {
		return lead + m.tone(" · provider cache usage unavailable", "dim")
	}
	reported := float64(s.ReportedCached) / float64(max(1, s.ReportedInput))
	delta := (reported - pred) * 100
	tone := "dim"
	if math.Abs(delta) > 5 {
		tone = "bad"
	}
	return m.clean(lead+fmt.Sprintf(" · %s reported %s/%s (%.1f%%) · ", g.report, k(s.ReportedCached), k(s.ReportedInput), reported*100)) +
		m.tone(fmt.Sprintf("%+.1f pp", delta), tone)
}

func (m *model) tail(s diagnostic.Snapshot, w int) string {
	if s.TailBudget <= 0 {
		return m.tone("tail budget not applicable to this call", "dim")
	}
	tokens := 0
	for _, tier := range s.Tiers {
		if tier.Index == 3 {
			tokens = tier.Tokens
		}
	}
	guard := max(s.TailBudget, s.TailGuard)
	fill := min(w, max(0, int(float64(tokens)*float64(w)/float64(guard))))
	mark := min(w-1, int(float64(s.TailBudget)*float64(w)/float64(guard)))
	g, tone := m.glyphs(), "ok"
	if tokens > s.TailBudget {
		tone = "warn"
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
	return fmt.Sprintf("tail %s %s / %s (guard %s)", bar.String(), k(tokens), k(s.TailBudget), k(guard))
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

func k(n int) string {
	if n < 1000 {
		return strconv.Itoa(n)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}
