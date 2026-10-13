package ctxtop

import (
	"fmt"
	"strings"

	"github.com/quinnovator/contactd/internal/diagnostic"
)

func (m *model) foldView(s diagnostic.Snapshot, w, h int) []string {
	rows := m.items()
	out := []string{m.tail(s, max(1, min(30, w-6))), "CHECKPOINT HISTORY · source log, as of selected call"}
	if len(rows) == 0 {
		return append(out, "No checkpoints recorded for this call.")
	}
	sel := min(m.row[4], len(rows)-1)
	start := max(0, sel-1)
	for i := start; i < min(len(rows), start+3); i++ {
		b := rows[i].block
		mark := "  "
		if i == sel {
			mark = "› "
		}
		out = append(out, m.clean(fmt.Sprintf("%s#%d  seq %d–%d  summary ≈%s tokens", mark, b.Seq, b.From, b.To, k(b.Tokens))))
	}
	b := rows[sel].block
	content := m.content(b.ID).Text
	if content == "" {
		content = b.Preview
	}
	events := m.events[eventKey(s, b.From, b.To)]
	out = append(out, m.tone(strings.Repeat(m.glyphs().line, w), "dim"))
	half := max(1, (w-3)/2)
	originals := []string{fmt.Sprintf("ORIGINAL · seq %d–%d", b.From, b.To)}
	if events == nil {
		originals = append(originals, "Loading source span…")
	}
	for _, ev := range events {
		text := eventText(ev)
		if m.keywords {
			text = missingWords(text, content)
		}
		originals = append(originals, m.wrap(text, half)...)
	}
	summary := []string{fmt.Sprintf("CHECKPOINT #%d", b.Seq)}
	summary = append(summary, m.wrap(content, half)...)
	if m.keywords {
		summary = append(summary, "", "! marks numbers / paths absent from summary.")
	}
	if w < 80 {
		return append(out, m.scrolled(append(append(originals, ""), summary...), max(0, h-len(out)))...)
	}
	height := max(0, h-len(out))
	left := m.scrolled(originals, height)
	right := m.scrolled(summary, height)
	for i := range max(len(left), len(right)) {
		l, r := "", ""
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		l = m.theme.Truncate(l, half)
		out = append(out, l+strings.Repeat(" ", max(0, half-m.theme.Width(l)))+" │ "+r)
	}
	return out
}
