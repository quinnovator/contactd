package ctxtop

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/quinnovator/contactd/internal/diagnostic"
)

// screen is one of the six views: its tab, the question it answers, and the
// keys that act on it alone.
type screen struct{ name, intro, keys string }

func screens() [6]screen {
	return [6]screen{
		{"Prompt", "What this call sent to the model, top to bottom", "↑↓ select · enter open/fold · s by size · g in Log · / search · r raw"},
		{"Log", "The contact's log by episode, and what this prompt took from each", "↑↓ select · enter open · / search · r raw"},
		{"Changes", "What changed since an earlier call, and what that did to the cache", "[ ] compare with another call · r exact request bodies · pgup/pgdn scroll"},
		{"Recall", "How earlier episodes were chosen for this prompt", "↑↓ select · enter open · / search · r raw"},
		{"Checkpoints", "Older history that a summary now stands in for", "↑↓ select · m mark dropped details · pgup/pgdn scroll · r raw"},
		{"Timeline", "Prompt size and cache reuse, one column per call", "←→ move between calls"},
	}
}

func (m *model) View() tea.View {
	w := max(1, min(m.width-2, 998))
	s := m.current()
	var lines []string
	switch {
	case m.help:
		lines = m.scrolled(m.helpView(), max(0, m.height-2))
	case s.Call == 0:
		lines = append(lines, m.tone("ctxtop · no model calls yet", "accent"),
			"Send a message in contactd chat and its prompt appears here.",
			m.tone("The server must run with -diagnostics; mise run dev sets it.", "dim"))
	default:
		lines = m.header(s, w)
		available := max(0, m.height-len(lines)-2)
		body := m.body(s, w, available)
		if len(body) > available {
			body = body[:available]
		}
		lines = append(lines, body...)
	}
	for len(lines) < max(0, m.height-2) {
		lines = append(lines, "")
	}
	lines = append(lines, m.statusLine(s), m.keyLine(w))
	if len(lines) > m.height {
		lines = lines[:max(0, m.height-1)]
		lines = append(lines, m.tone("? help · q quit", "dim"))
	}
	for i := range lines {
		if m.fold != nil {
			lines[i] = asciiOnly(m.fold.Replace(lines[i]))
		}
		lines[i] = m.theme.Truncate(lines[i], w)
	}
	v := tea.NewView(strings.Join(lines, "\n"))
	v.AltScreen = true
	return v
}

// header says which call is shown and how its prompt did, above the tabs.
// A short window keeps the two sentences and drops the bar.
func (m *model) header(s diagnostic.Snapshot, w int) []string {
	name := s.Name
	if name == "" {
		name = "#" + s.Contact.String()
	}
	where := fmt.Sprintf("turn %d · step %d · call %d", s.Turn, s.Step, s.Call)
	if s.Purpose != "" && s.Purpose != "reply" {
		where = s.Purpose + " call · " + where
	}
	h := m.calls()
	at := m.selected(h)
	if at < 0 {
		at = len(h) - 1
	}
	follow := m.tone(" · latest call", "dim")
	switch newer := len(h) - 1 - at; {
	case m.follow:
		follow = m.tone(" · following the latest", "dim")
	case newer > 0:
		follow = m.tone(fmt.Sprintf(" · paused, %d newer · f follows", newer), "warn")
	}
	if !s.Done {
		follow += " " + m.tone([]string{"|", "/", "-", "\\"}[m.spin%4], "accent")
	}
	lines := []string{
		m.theme.Spread(m.tone(name+" · "+s.Route, "accent"), m.tone(where, "dim")+follow, w),
		m.usage(s, w),
	}
	if m.height >= 16 {
		span := s.Window
		if m.zoom {
			span = s.Total()
		}
		_, fill := m.bar(layout(s, span, w), s)
		lines = append(lines, fill, m.legend(s))
	}
	tabs := make([]string, 0, 6)
	for i, sc := range screens() {
		label := fmt.Sprintf("%d %s", i+1, sc.name)
		if i == m.screen {
			label = m.tone("["+label+"]", "accent")
		} else {
			label = m.tone(" "+label+" ", "dim")
		}
		tabs = append(tabs, label)
	}
	return append(lines, strings.Join(tabs, " "), m.tone(strings.Repeat(m.glyphs().line, w), "dim"))
}

// usage is the header's sentence about prompt size and cache reuse, in the
// longest form that fits.
func (m *model) usage(s diagnostic.Snapshot, w int) string {
	total, expected := s.Total(), share(s.PredictedCached, s.Total())
	prompt := fmt.Sprintf("Prompt ≈%s tokens", k(total))
	long := prompt
	if s.Window > 0 {
		prompt = fmt.Sprintf("Prompt ≈%s of %s tokens (%s)", k(total), k(s.Window), pct(total, s.Window))
		long = fmt.Sprintf("Prompt ≈%s of %s tokens (%s of the window)", k(total), k(s.Window), pct(total, s.Window))
	}
	if s.ReportedInput > 0 {
		long += " · provider counted " + k(s.ReportedInput)
	}
	cache, short, verdict := fmt.Sprintf("Cache ≈%.0f%% expected", expected*100), fmt.Sprintf("cache ≈%.0f%% expected", expected*100), ""
	switch {
	case !s.Done:
		cache += " · waiting for the provider"
	case s.Error != "":
		cache += " · the call failed"
	case !s.Reported:
		cache += " · the provider reported no cache use"
	default:
		reused := share(s.ReportedCached, s.ReportedInput)
		cache = fmt.Sprintf("Cache %.0f%% reused · %.0f%% expected", reused*100, expected*100)
		short = fmt.Sprintf("cache %.0f%% (%.0f%% expected)", reused*100, expected*100)
		if gap := (reused - expected) * 100; math.Abs(gap) > 5 {
			direction := "more"
			if gap < 0 {
				direction = "less"
			}
			verdict = m.tone(fmt.Sprintf(" · %.0f points %s than expected, see 3 Changes", math.Abs(gap), direction), "bad")
		}
	}
	for _, line := range []string{long + m.tone(" │ ", "dim") + cache + verdict, prompt + " · " + cache + verdict, prompt + " · " + cache} {
		if m.theme.Width(line) <= w {
			return line
		}
	}
	return fmt.Sprintf("≈%s/%s · %s", k(total), k(s.Window), short)
}

// legend names the three marks of the bar above it.
func (m *model) legend(s diagnostic.Snapshot) string {
	g := m.glyphs()
	hit, tone := "reused from cache", "ok"
	if !s.Reported {
		hit, tone = "expected from cache", "dim"
	}
	out := m.tone(g.hit, tone) + " " + hit + "   " + m.tone(g.miss, "warn") + " sent fresh"
	if m.zoom {
		return out + m.tone("   zoomed to the prompt · z shows the whole window", "dim")
	}
	return out + "   " + m.tone(g.empty, "dim") + " free window" + m.tone("   z zooms to the prompt", "dim")
}

func (m *model) body(s diagnostic.Snapshot, w, h int) []string {
	switch {
	case m.screen == 2:
		return m.scrolled(m.diffView(s, w, h), h)
	case m.screen == 5:
		return m.timeline(w, max(3, h-2))
	case m.raw:
		return m.scrolled(m.inspector(s, nil, 0, w), h)
	case m.screen == 4:
		return m.foldView(s, w, h)
	}
	return m.listView(s, w, h)
}

// statusLine says what is connected, which contacts are shown, and how the
// selected call went.
func (m *model) statusLine(s diagnostic.Snapshot) string {
	switch {
	case m.searching:
		return m.tone("/"+m.query+"▏ type to filter this view · enter keeps the filter · esc clears it", "accent")
	case m.query != "":
		return m.tone("showing only rows that contain \""+m.query+"\" · esc clears the filter", "accent")
	case m.notice != "":
		return m.tone(m.notice, "warn")
	}
	status := m.status
	if s.Call == 0 {
		return m.tone(status, "dim")
	}
	if m.filter == 0 {
		status += " · all contacts"
	} else {
		status += " · this contact only"
	}
	call := fmt.Sprintf("call %d", s.Call)
	switch {
	case s.Error != "":
		return m.tone(status+" · ", "dim") + m.tone(fmt.Sprintf("%s failed after %s: %s", call, took(s.MS), s.Error), "bad")
	case s.Done:
		status += fmt.Sprintf(" · %s took %s · %d output tokens", call, took(s.MS), s.OutputTokens)
	case s.TTFT == 0:
		status += " · " + call + " running · no output yet"
	default:
		status += fmt.Sprintf(" · %s running · first output after %s · ≈%s output tokens so far", call, took(s.TTFT), k((s.OutputBytes+3)/4))
	}
	return m.tone(status, "dim")
}

// keyLine lists the keys of the current view, then the ones that work
// everywhere. The way out stays visible however narrow the window is.
func (m *model) keyLine(w int) string {
	keys, always := screens()[m.screen].keys, "←→ call · tab contact · ? help · q quit"
	switch {
	case m.help:
		keys, always = "pgup/pgdn scroll", "? or esc closes help · q quit"
	case m.current().Call == 0:
		return m.tone("? help · q quit", "dim")
	case m.searching:
		keys = "type to filter · enter keep · esc clear"
	case m.raw && m.screen != 5:
		keys = "r back to the readable view · pgup/pgdn scroll"
	case m.expanded && m.screen != 2 && m.screen != 4 && m.screen != 5:
		keys = "esc back to the list · ↑↓ next entry · pgup/pgdn scroll"
	}
	if room := w - m.theme.Width(always) - 3; room >= 12 {
		return m.tone(m.theme.Truncate(keys, room)+" │ "+always, "dim")
	}
	return m.tone(always, "dim")
}

func (m *model) helpView() []string {
	g := m.glyphs()
	out := []string{
		m.tone("ctxtop shows what each model call sent, and how much of it the provider reused from cache.", "accent"), "",
		m.theme.Title().Render("KEYS"),
		"  1–6        switch view",
		"  ←→  h l    earlier or later call · f or end follows the latest",
		"  ↑↓  j k    move the selection · pgup/pgdn scroll long content",
		"  enter      open the selection full width, or fold a tier · esc closes",
		"  tab        next contact · shift+tab goes back",
		"  /          filter the view by text · esc clears",
		"  r          show the exact request body sent to the provider",
		"  z          scale the bar to the whole window, or to the prompt",
		"  s  g       Prompt: sort by size · find the selection in Log",
		"  [  ]       Changes: compare with an earlier or later call",
		"  m          Checkpoints: mark numbers and paths the summary dropped",
		"  ?  q       close help · quit", "",
		m.theme.Title().Render("READING IT"),
		"  " + m.tone(g.hit, "ok") + " reused from cache   " + m.tone(g.miss, "warn") + " sent fresh   " + m.tone(g.empty, "dim") + " free context window   ≈ planner estimate",
		"  A prompt is five tiers, ordered from the least to the most often changed:",
	}
	for _, name := range []string{"static", "profile", "recall", "tail", "live"} {
		out = append(out, fmt.Sprintf("    %-8s %s", name, tierNote(name)))
	}
	return append(out,
		"  The cache reuses a prompt up to its first changed byte, so a change in an",
		"  early tier sends every later tier again. A checkpoint is a summary that",
		"  replaced the oldest part of the tail; an episode is one stretch of the log.", "",
		m.theme.Title().Render("LIMITS"),
		"  Providers report cache totals only: reuse per part, and where the cache",
		"  ends, are estimates. Log previews load up to 2,000 events. Prices and",
		"  parent-fork attribution are not collected.")
}

// listView draws the Prompt, Log and Recall views: a list on the left and
// what is known about the selected row on the right.
func (m *model) listView(s diagnostic.Snapshot, w, h int) []string {
	rows := m.items()
	sel := min(m.row[m.screen], max(0, len(rows)-1))
	if m.expanded {
		return m.scrolled(m.inspector(s, rows, sel, w), h)
	}
	leftW := w
	split := w >= 100
	if split {
		leftW = max(48, w*3/5)
	}
	left := m.listHead(s, leftW)
	listHeight := h
	if !split {
		// Stacked, the list has half the room, and always a few rows of its own.
		listHeight = max(3, h/2, min(len(left)+3, h-4))
	}
	visible := max(1, listHeight-len(left)-1)
	start := max(0, min(sel-visible/2, len(rows)-visible))
	for i := start; i < min(len(rows), start+visible); i++ {
		line := m.rowText(s, rows[i])
		if i != sel {
			left = append(left, "  "+line)
		} else if line = m.tone("› ", "accent") + line; m.theme.BarFilled {
			left = append(left, m.theme.Selected(line, leftW))
		} else {
			left = append(left, line)
		}
		if b := rows[i].block; b != nil {
			if note := m.cacheEnd(s, b); note != "" {
				left = append(left, note)
			}
		}
	}
	switch {
	case len(rows) > 0:
	case m.query != "":
		left = append(left, m.tone("  Nothing here matches the filter.", "dim"))
	default:
		left = append(left, m.tone("  Nothing to list for this call.", "dim"))
	}
	right := m.inspector(s, rows, sel, max(1, w-leftW-3))
	if !split {
		left = left[:min(len(left), listHeight)]
		left = append(left, m.tone(strings.Repeat(m.glyphs().line, w), "dim"))
		return append(left, m.scrolled(right, max(0, h-len(left)))...)
	}
	return m.columns(left, m.scrolled(right, h), leftW, h)
}

// columns sets two blocks of lines side by side, the left one leftW wide.
func (m *model) columns(left, right []string, leftW, h int) []string {
	n := min(h, max(len(left), len(right)))
	out := make([]string, 0, max(0, n))
	for i := range n {
		l, r := "", ""
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		out = append(out, m.theme.Pad(l, leftW)+m.tone(" │ ", "dim")+r)
	}
	return out
}

// listHead is what stands above a list: the view's question, what was found,
// and the column names.
func (m *model) listHead(s diagnostic.Snapshot, w int) []string {
	head := []string{m.theme.Title().Render(screens()[m.screen].intro)}
	columns := ""
	switch m.screen {
	case 0:
		if m.sizeSort {
			head[0] = m.theme.Title().Render("What this call sent to the model, largest part first")
		}
		columns = fmt.Sprintf("  %-17s %6s %5s  %-6s  %s", "PART", "TOKENS", "SHARE", "CACHE", "CONTENT")
	case 1:
		columns = fmt.Sprintf("  %-9s %-14s %s", "EPISODE", "EVENTS", "IN THIS PROMPT")
	case 3:
		d, ok := m.captures[s.Call]
		switch {
		case !ok:
			return append(head, m.tone("Loading call details…", "dim"))
		case d.Recall == nil:
			return append(head, m.tone("Recall did not run for this call.", "dim"))
		}
		tr := d.Recall
		head = append(head, m.field("Query", tr.Query, w)...)
		head = append(head, m.field("Outcome", recallOutcome(s.Recall, tr.Warm), w)...)
		head = append(head, m.field("Method", tr.Status, w)...)
		head = append(head, m.field("Rule", fmt.Sprintf("a final score of %.2f or more passes; while the cache is warm, a newcomer must beat the current set by %.2f to replace it", tr.Threshold, tr.Margin), w)...)
		head = append(head, "")
		columns = fmt.Sprintf("    %-8s %10s %7s %6s   %s", "EPISODE", "SIMILARITY", "RERANK", "FINAL", "DECISION")
	}
	return append(head, m.tone(columns, "dim"))
}

// rowText is one row of a list, without its selection mark.
func (m *model) rowText(s diagnostic.Snapshot, row item) string {
	switch {
	case row.tier >= 0:
		t, offset, ok := tierOf(s, row.tier)
		if !ok {
			return ""
		}
		arrow := "▾"
		if m.collapsed[row.tier] {
			arrow = "▸"
		}
		return m.tone(fmt.Sprintf("%s %-15s %6s %5s", arrow, t.Name, k(t.Tokens), pct(t.Tokens, s.Total())), "accent") +
			"  " + m.meter(s, offset, t.Tokens) + "  " + m.tone(tierNote(t.Name), "dim")
	case row.block != nil:
		b := row.block
		return m.clean(fmt.Sprintf("  %-15s %6s %5s", kindLabel(b.Kind), k(b.Tokens), pct(b.Tokens, s.Total()))) +
			"  " + m.meter(s, b.Offset, b.Tokens) + "  " + m.clean(b.Preview)
	case row.episode != nil:
		ep := row.episode
		return fmt.Sprintf("%-9d %-14s %s", ep.ID, fmt.Sprintf("%d–%d", ep.Start, ep.End), episodeState(s, *ep))
	case row.candidate != nil:
		c := row.candidate
		mark := " "
		if slices.Contains(s.Recall, c.Episode) {
			mark = "✓"
		}
		threshold := float32(0)
		if tr := m.captures[s.Call].Recall; tr != nil {
			threshold = tr.Threshold
		}
		return m.clean(fmt.Sprintf("%s %-8d %10.2f %7.2f %6.2f   %s", mark, c.Episode, c.Cosine, c.Rerank, c.Score, decision(c.Reason, threshold)))
	}
	return ""
}

// meter draws how much of one part of the prompt came from the cache.
func (m *model) meter(s diagnostic.Snapshot, offset, tokens int) string {
	edge, reported := cacheEdge(s)
	tone := "ok"
	if !reported {
		tone = "dim"
	}
	hits := max(0, min(float64(tokens), edge-float64(offset)))
	n := int(math.Round(6 * hits / float64(max(1, tokens))))
	g := m.glyphs()
	return m.tone(strings.Repeat(g.hit, n), tone) + m.tone(strings.Repeat(g.miss, 6-n), "warn")
}

// cacheEnd is the note under the part in which the cached prefix ends. Only
// the prompt's own order has such a place.
func (m *model) cacheEnd(s diagnostic.Snapshot, b *diagnostic.Block) string {
	edge, reported := cacheEdge(s)
	from, to := float64(b.Offset), float64(b.Offset+b.Tokens)
	if m.screen != 0 || m.sizeSort || m.query != "" || edge <= from || edge > to || edge >= float64(s.Total()) {
		return ""
	}
	note := "cache ends"
	if !reported {
		note = "cache should end"
	}
	if edge < to {
		note += fmt.Sprintf(" ≈%s into the part above", k(int(edge-from)))
	} else {
		note += " here"
	}
	return m.tone("    ▲ "+note+" · the rest is sent fresh", "warn")
}

func (m *model) scrolled(lines []string, h int) []string {
	start := min(m.scroll, max(0, len(lines)-1))
	return lines[start:min(len(lines), start+max(0, h))]
}

// episodeState says what a prompt took from an episode of the log.
func episodeState(s diagnostic.Snapshot, ep diagnostic.Episode) string {
	state := "not in this prompt"
	if ep.End >= tailStart(s) {
		state = "in the tail, verbatim"
	}
	for _, b := range s.Blocks {
		if b.Kind == "checkpoint" && ep.Start <= b.To {
			state = "folded into a checkpoint"
		}
	}
	if slices.Contains(s.Recall, ep.ID) {
		state = "↺ recalled into this prompt"
	}
	return state
}

// recallOutcome compares the episodes in this prompt with the ones before.
func recallOutcome(now, before []uint32) string {
	switch {
	case len(now) == 0 && len(before) == 0:
		return "nothing recalled"
	case len(now) == 0:
		return "nothing recalled · " + episodes(before) + " dropped, so the recall tier changes"
	case slices.Equal(now, before):
		return "recalled " + episodes(now) + " · same as the call before, so the recall tier stays cached"
	case len(before) == 0:
		return "recalled " + episodes(now) + " · nothing was recalled before"
	}
	return "recalled " + episodes(now) + " · replaces " + episodes(before) + ", so the recall tier changes"
}

func episodes(ids []uint32) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatUint(uint64(id), 10)
	}
	if len(ids) == 1 {
		return "episode " + parts[0]
	}
	return "episodes " + strings.Join(parts, ", ")
}

// decision puts the planner's reason for a recall candidate in plain words.
func decision(reason string, threshold float32) string {
	switch reason {
	case "selected":
		return "recalled"
	case "below threshold":
		return fmt.Sprintf("below the %.2f threshold", threshold)
	case "outside top K":
		return "passed; others scored higher"
	case "warm set retained by hysteresis":
		return "passed; current set kept"
	}
	return reason
}

func kindName(kind string) string {
	switch kind {
	case "tool_call":
		return "tool call"
	case "tool_result":
		return "tool result"
	case "time":
		return "time note"
	}
	return kind
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
	}
	return kindName(kind)
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
