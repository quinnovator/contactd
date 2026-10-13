package modscreen

import (
	"strconv"
	"strings"
	"time"

	"github.com/quinnovator/contactd/internal/tui/mods"
)

// measure is the longest line of running text in the details, in cells. It is
// the measure of the drawing: its hand-broken lines break where these do.
const measure = 38

// kindWidth is the width of the column that names a kind of addition:
// "renderer" and a space.
const kindWidth = 9

const (
	// nameMax is the widest the name column of the ADDS block gets. A longer
	// name has a row of its own.
	nameMax = 24
	// noteMin is the least room beside a name for the note to stand there.
	// With less the note goes under the name.
	noteMin = 16
)

// The fixed text of the details.
const (
	headAdds    = "ADDS"
	headRead    = "MAY READ"
	headRuns    = "RUNS"
	headUpdates = "LAST 100 UPDATES"
	headFailed  = "FAILED"
	headOff     = "OFF"
	headStopped = "SWITCHED OFF"

	noMessages = "no messages, "
	neverReads = "no network, no files"
	runsIn     = "sandboxed WebAssembly, in this process"
	perUpdate  = " for each update"
	overBudget = "over budget three times in a row and it is switched off, with a notice"
)

// grantText says in words what a grant lets a mod read. A grant this client
// does not know is shown by its name: the list is closed, so a host only
// loads one after this function has learnt it.
func grantText(g mods.Grant) string {
	switch g {
	case mods.GrantContact:
		return "the open contact's name and id"
	case mods.GrantRoute:
		return "its provider and model"
	case mods.GrantState:
		return "whether it is connected and replying"
	case mods.GrantModelCalls:
		return "model-call records of the open contact"
	case mods.GrantMessages:
		return "the last 20 messages, when you run its command, and the items it draws"
	case mods.GrantWorkspace:
		return "the directory name and git branch where the client started"
	case mods.GrantClock:
		return "the time"
	}
	return string(g)
}

// block is one group of lines in the details. Blocks are a blank line apart,
// but for a tight one, which follows the block above directly.
type block struct {
	lines []string
	// drop is the order blocks go in when the pane is too short for all of
	// them: 1 goes first. A block with 0 stays.
	drop  int
	tight bool
}

// detailPane draws the selected mod's details: at most h lines, none wider
// than w cells.
//
// When the pane is too short the blocks that say least about this mod go
// first: how mods run, the description, what the mod adds, what it may read.
// The name and the last block (how fast it is, or why it is not running)
// stay, so a failure is never the part that is cut off.
func (m *Model) detailPane(p pen, w, h int) []string {
	if w <= 0 || h <= 0 {
		return nil
	}
	var blocks []block
	if m.sel >= 0 && m.sel < len(m.infos) {
		blocks = m.blocks(p, m.infos[m.sel], w)
	} else {
		blocks = []block{{lines: p.wrap(toneDim, howInstall, min(w, measure))}}
	}
	lines := fit(blocks, h)
	for i, line := range lines {
		lines[i] = p.th.Truncate(line, w)
	}
	return lines
}

// fit joins blocks into at most h lines, dropping blocks in their order,
// then the blank lines between them, then the lines at the end.
func fit(blocks []block, h int) []string {
	for {
		lines := join(blocks, true)
		if len(lines) <= h {
			return lines
		}
		first := -1
		for i, b := range blocks {
			if b.drop > 0 && (first < 0 || b.drop < blocks[first].drop) {
				first = i
			}
		}
		if first < 0 {
			break
		}
		blocks = append(blocks[:first:first], blocks[first+1:]...)
	}
	lines := join(blocks, false)
	return lines[:min(len(lines), h)]
}

// join lays blocks out one under the other, a blank line between them when
// gaps is set.
func join(blocks []block, gaps bool) []string {
	var lines []string
	for i, b := range blocks {
		if gaps && i > 0 && !b.tight {
			lines = append(lines, "")
		}
		lines = append(lines, b.lines...)
	}
	return lines
}

// blocks builds the details of one mod for a pane w cells wide.
func (m *Model) blocks(p pen, in mods.Info, w int) []block {
	man := in.Manifest
	text := min(w, measure)

	head := p.tone(toneAccent, p.clean(man.Name))
	if v := p.clean(man.Version); v != "" {
		head += p.tone(toneDim, " "+v)
	}
	out := []block{{lines: []string{head}}}
	// A manifest that could not be read says nothing about the mod: only
	// its name and the reason are shown.
	if man.Module != "" {
		if desc := p.clean(man.Description); desc != "" {
			out = append(out, block{lines: p.wrap(toneDim, desc, text), drop: 2, tight: true})
		}
		out = append(out,
			block{lines: addsBlock(p, man, w), drop: 3},
			block{lines: readBlock(p, man, text), drop: 4},
			block{lines: m.runsBlock(p, man, text), drop: 1},
		)
	}

	return append(out, block{lines: lastBlock(p, in, text)})
}

// lastBlock is the block the details end with: how long the mod's last calls
// took, or, for a mod that is not running, why not. A mod that failed or was
// switched off by the host shows the host's reason in the bad tone, under a
// heading that says which of the two it is.
func lastBlock(p pen, in mods.Info, w int) []string {
	said := func(heading, tone, text string) []string {
		return append([]string{p.tone(toneDim, heading)}, p.wrap(tone, text, w)...)
	}
	switch in.Status {
	case mods.StatusOn:
		if in.Calls == 0 {
			return said(headUpdates, toneDim, "none yet")
		}
		return said(headUpdates, tonePlain, p.join("median "+ms(in.Median), "slowest "+ms(in.Slowest)))
	case mods.StatusLoading:
		return said(headUpdates, toneDim, "loading"+p.th.G.Ellipsis)
	case mods.StatusOff:
		// Off with a reason: the host switched it off after an update, or
		// because its module changed.
		if why := p.clean(in.Reason); why != "" {
			return said(headOff, toneWarn, why)
		}
		return said(headUpdates, toneDim, "none: it is off")
	case mods.StatusFailed:
		return said(headFailed, toneBad, reason(p, in))
	case mods.StatusDisabled:
		return said(headStopped, toneBad, reason(p, in))
	}
	return said(headUpdates, toneDim, "none")
}

// reason is why a mod is not running, as the host gave it.
func reason(p pen, in mods.Info) string {
	if why := p.clean(in.Reason); why != "" {
		return why
	}
	return "the host gave no reason"
}

// addsBlock lists what a mod adds: one row for each segment, command and
// renderer, with its kind, its name and where it shows. A command shows its
// summary there.
//
// Nothing a mod says about itself is cut. A note too long for its row wraps
// under its column. A name too long for the name column, or a pane too
// narrow to hold the three side by side, puts the note under the name, and
// the name itself breaks where it is longer than the pane.
func addsBlock(p pen, man mods.Manifest, w int) []string {
	type added struct{ kind, name, note string }
	items := make([]added, 0, len(man.Segments)+len(man.Commands)+len(man.Renderers))
	for _, s := range man.Segments {
		items = append(items, added{"segment", p.clean(s.Name), "status line"})
	}
	for _, c := range man.Commands {
		items = append(items, added{"command", "/" + p.clean(c.Name), p.clean(c.Summary)})
	}
	for _, r := range man.Renderers {
		items = append(items, added{"renderer", p.clean(r.Kind), "transcript"})
	}
	lines := make([]string, 0, 1+len(items))
	lines = append(lines, p.tone(toneDim, headAdds))
	if len(items) == 0 {
		return append(lines, p.tone(toneDim, "nothing"))
	}
	// The name column is as wide as the longest name that fits one.
	names := 0
	for _, it := range items {
		if n := p.th.Width(it.name) + 3; n <= nameMax {
			names = max(names, n)
		}
	}
	notes := w - kindWidth - names
	under := max(1, w-kindWidth)
	indent := strings.Repeat(" ", kindWidth)
	for _, it := range items {
		kind := p.cell(toneMod, it.kind, kindWidth)
		if p.th.Width(it.name) < names && notes >= noteMin {
			lead := kind + p.cell(tonePlain, it.name, names)
			for i, part := range p.wrap(toneDim, it.note, notes) {
				if i > 0 {
					lead = indent + strings.Repeat(" ", names)
				}
				lines = append(lines, lead+part)
			}
			continue
		}
		for i, part := range p.wrap(tonePlain, it.name, under) {
			if i > 0 {
				kind = indent
			}
			lines = append(lines, kind+part)
		}
		if it.note == "" {
			continue
		}
		for _, part := range p.wrap(toneDim, it.note, under) {
			lines = append(lines, indent+part)
		}
	}
	return lines
}

// readBlock lists what a mod may read: a line for each grant in words, then
// what no mod can reach. "no messages" stands in front of that only when the
// mod does not hold the messages grant.
func readBlock(p pen, man mods.Manifest, w int) []string {
	lines := []string{p.tone(toneDim, headRead)}
	for _, g := range man.Read {
		lines = append(lines, p.hang(tonePlain, p.clean(grantText(g)), w)...)
	}
	never := neverReads
	if !man.Has(mods.GrantMessages) {
		never = noMessages + never
	}
	return append(lines, p.wrap(toneDim, never, w)...)
}

// runsBlock says how every mod is run, and the time this one gets for a
// call: the host's budget, or the mod's own where its manifest lowers it.
func (m *Model) runsBlock(p pen, man mods.Manifest, w int) []string {
	budget := m.budget
	if man.Budget > 0 && man.Budget < budget {
		budget = man.Budget
	}
	lines := []string{p.tone(toneDim, headRuns)}
	lines = append(lines, p.wrap(tonePlain, runsIn, w)...)
	lines = append(lines, p.wrap(tonePlain, ms(budget)+perUpdate, w)...)
	return append(lines, p.wrap(toneDim, overBudget, w)...)
}

// wrap breaks text into lines of at most w cells, each drawn in a tone.
func (p pen) wrap(tone, s string, w int) []string {
	lines := p.th.Wrap(s, w)
	for i, line := range lines {
		lines[i] = p.tone(tone, line)
	}
	return lines
}

// hang is wrap for one entry of a list: the lines after the first are
// indented two cells, so the next entry is seen to start.
func (p pen) hang(tone, s string, w int) []string {
	if p.th.Width(s) <= w || w < 8 {
		return p.wrap(tone, s, w)
	}
	lines := p.wrap(tone, s, w-2)
	for i := 1; i < len(lines); i++ {
		lines[i] = "  " + lines[i]
	}
	return lines
}

// ms writes a duration in milliseconds as the drawing does: "5 ms",
// "0.4 ms", "24 ms". There is one decimal under ten milliseconds and a
// second one under a tenth.
func ms(d time.Duration) string {
	if d <= 0 {
		return "0 ms"
	}
	decimals := 0
	switch {
	case d < 100*time.Microsecond:
		decimals = 2
	case d < 10*time.Millisecond:
		decimals = 1
	}
	s := strconv.FormatFloat(float64(d)/float64(time.Millisecond), 'f', decimals, 64)
	if strings.Contains(s, ".") {
		s = strings.TrimSuffix(strings.TrimRight(s, "0"), ".")
	}
	return s + " ms"
}
