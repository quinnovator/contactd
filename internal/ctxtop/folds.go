package ctxtop

import (
	"fmt"
	"slices"

	"github.com/quinnovator/contactd/internal/diagnostic"
)

// foldView lists the contact's checkpoints, newest first, and sets the
// selected one beside the log events it replaced.
func (m *model) foldView(s diagnostic.Snapshot, w, h int) []string {
	rows := m.items()
	out := append([]string{m.theme.Title().Render(screens()[4].intro)}, m.tail(s, max(1, min(30, w-6)))...)
	out = append(out, "")
	if len(rows) == 0 {
		return append(out, m.tone("No checkpoints yet: none of this contact's history has been summarized.", "dim"))
	}
	out = append(out, m.tone(fmt.Sprintf("  %-12s %-18s %-14s %s", "CHECKPOINT", "REPLACES", "SUMMARY", "NEWEST FIRST"), "dim"))
	sel := min(m.row[4], len(rows)-1)
	start := max(0, sel-1)
	for i := start; i < min(len(rows), start+3); i++ {
		b := rows[i].block
		mark, where := "  ", "no longer in this prompt"
		if i == sel {
			mark = m.tone("› ", "accent")
		}
		if slices.ContainsFunc(s.Blocks, func(o diagnostic.Block) bool { return o.Kind == "checkpoint" && o.Seq == b.Seq }) {
			where = "in this prompt"
		}
		out = append(out, mark+m.clean(fmt.Sprintf("%-12s %-18s %-14s %s", fmt.Sprintf("#%d", b.Seq), fmt.Sprintf("events %d–%d", b.From, b.To), "≈"+k(b.Tokens)+" tokens", where)))
	}
	b := rows[sel].block
	content := m.content(b.ID).Text
	if content == "" {
		content = b.Preview
	}
	events := m.events[eventKey(s, b.From, b.To)]
	half := max(1, (w-3)/2)
	if w < 80 {
		half = w
	}
	var originals []string
	tokens := 0
	for _, ev := range events {
		text := eventText(ev)
		tokens += (len(text) + 3) / 4
		if m.keywords {
			text = missingWords(text, content)
		}
		originals = append(originals, m.prose(text, half)...)
	}
	before := fmt.Sprintf("BEFORE · log events %d–%d · ≈%s tokens", b.From, b.To, k(tokens))
	if events == nil {
		before = fmt.Sprintf("BEFORE · log events %d–%d", b.From, b.To)
		originals = append(originals, m.tone("Loading the original events…", "dim"))
	}
	originals = append([]string{m.tone(before, "accent")}, originals...)
	summary := append([]string{m.tone(fmt.Sprintf("AFTER · checkpoint #%d · ≈%s tokens", b.Seq, k(b.Tokens)), "accent")}, m.prose(content, half)...)
	if m.keywords {
		summary = append(summary, "", m.tone("!…! on the left marks a number or path the summary does not contain.", "warn"))
	}
	out = append(out, m.theme.Rule(w, ""))
	height := max(0, h-len(out))
	if w < 80 {
		return append(out, m.scrolled(append(append(originals, ""), summary...), height)...)
	}
	return append(out, m.columns(m.scrolled(originals, height), m.scrolled(summary, height), half, height)...)
}
