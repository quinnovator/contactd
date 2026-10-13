package ctxtop

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/diagnostic"
)

type detailMsg struct {
	run    string
	call   uint64
	detail diagnostic.Details
	err    error
	done   bool
}
type eventsMsg struct {
	key    string
	events []core.Event
	err    error
}
type item struct {
	label     string
	tier      int
	block     *diagnostic.Block
	episode   *diagnostic.Episode
	candidate *diagnostic.Candidate
}

func (m *model) current() diagnostic.Snapshot {
	h := m.calls()
	i := m.selected(h)
	if i < 0 && len(h) > 0 {
		i = len(h) - 1
	}
	if i < 0 {
		return diagnostic.Snapshot{}
	}
	return h[i]
}

func (m *model) contactCalls() []diagnostic.Snapshot {
	s := m.current()
	var out []diagnostic.Snapshot
	for _, v := range m.history {
		if v.Contact == s.Contact && v.Purpose == s.Purpose {
			out = append(out, v)
		}
	}
	return out
}

func (m *model) previous() diagnostic.Snapshot {
	s := m.current()
	var prev diagnostic.Snapshot
	for _, v := range m.contactCalls() {
		if m.baseline != 0 && v.Call == m.baseline {
			return v
		}
		if v.Call < s.Call {
			prev = v
		}
	}
	return prev
}

func eventKey(s diagnostic.Snapshot, from, to uint64) string {
	return fmt.Sprintf("%s/%d/%d/%d", s.Run, s.Call, from, to)
}

func (m *model) load() tea.Cmd {
	if m.client == nil || m.sel == 0 {
		return nil
	}
	var cmds []tea.Cmd
	need := []diagnostic.Snapshot{m.current()}
	if m.screen == 2 {
		need = append(need, m.previous())
	}
	for _, s := range need {
		if s.Call == 0 {
			continue
		}
		_, ok := m.captures[s.Call]
		if m.loading[s.Call] || m.failed[s.Call] || (ok && (!s.Done || m.finalized[s.Call])) {
			continue
		}
		m.loading[s.Call] = true
		cmds = append(cmds, func() tea.Msg {
			ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
			defer cancel()
			d, err := m.client.PlanDetails(ctx, s.Run, s.Call)
			return detailMsg{run: s.Run, call: s.Call, detail: d, err: err, done: s.Done}
		})
	}
	if m.screen == 4 {
		s := m.current()
		key := eventKey(s, 0, 0)
		if m.events[key] == nil && !m.eventLoading[key] {
			m.eventLoading[key] = true
			cmds = append(cmds, func() tea.Msg {
				ctx, cancel := context.WithTimeout(m.ctx, 8*time.Second)
				defer cancel()
				var events []core.Event
				after := uint64(0)
				for len(events) < 2000 {
					batch, end, err := m.client.PlanCheckpoints(ctx, s.Run, s.Call, after)
					if err != nil {
						return eventsMsg{key: key, err: err}
					}
					if len(batch) == 0 {
						break
					}
					events = append(events, batch...)
					after = batch[len(batch)-1].Seq
					if after >= end {
						break
					}
				}
				return eventsMsg{key: key, events: events}
			})
		}
	}
	rows := m.items()
	ix := m.row[m.screen]
	if ix < len(rows) && (m.screen == 1 || m.screen == 4) {
		var from, to uint64
		if ep := rows[ix].episode; ep != nil {
			from, to = ep.Start, ep.End
		}
		if b := rows[ix].block; b != nil {
			from, to = b.From, b.To
		}
		s := m.current()
		key := eventKey(s, from, to)
		if from > 0 && to >= from && m.events[key] == nil && !m.eventLoading[key] {
			m.eventLoading[key] = true
			cmds = append(cmds, func() tea.Msg {
				ctx, cancel := context.WithTimeout(m.ctx, 8*time.Second)
				defer cancel()
				var all []core.Event
				after := from - 1
				for len(all) < 2000 && after < to {
					batch, _, err := m.client.PlanEvents(ctx, s.Run, s.Call, after)
					if err != nil {
						return eventsMsg{key: key, err: err}
					}
					if len(batch) == 0 {
						break
					}
					for _, ev := range batch {
						if ev.Seq <= to {
							all = append(all, ev)
						}
					}
					after = batch[len(batch)-1].Seq
				}
				return eventsMsg{key: key, events: all}
			})
		}
	}
	return tea.Batch(cmds...)
}

func (m *model) items() []item {
	s := m.current()
	d := m.captures[s.Call]
	var out []item
	match := func(text string) bool { return strings.Contains(strings.ToLower(text), strings.ToLower(m.query)) }
	switch m.screen {
	case 0:
		blocks := slices.Clone(s.Blocks)
		if m.sizeSort {
			slices.SortStableFunc(blocks, func(a, b diagnostic.Block) int { return b.Tokens - a.Tokens })
		}
		for _, b := range blocks {
			if !match(b.Preview + " " + b.Kind + " " + m.content(b.ID).Text) {
				continue
			}
			if !m.sizeSort && m.query == "" {
				// A collapsed tier has no block rows, so check the last header too.
				seen := false
				for _, row := range out {
					if row.tier == b.Tier {
						seen = true
					}
				}
				if !seen {
					out = append(out, item{tier: b.Tier, label: fmt.Sprintf("tier %d", b.Tier)})
				}
			}
			if !m.sizeSort && m.collapsed[b.Tier] {
				continue
			}
			copy := b
			out = append(out, item{tier: -1, block: &copy})
		}
	case 1:
		for _, ep := range d.Episodes {
			label := fmt.Sprintf("ep %d  seq %d–%d", ep.ID, ep.Start, ep.End)
			var searchable strings.Builder
			searchable.WriteString(label)
			for _, ev := range m.events[eventKey(s, ep.Start, ep.End)] {
				searchable.WriteString(" ")
				searchable.WriteString(eventText(ev))
			}
			if match(searchable.String()) {
				e := ep
				out = append(out, item{tier: -1, label: label, episode: &e})
			}
		}
	case 3:
		if d.Recall != nil {
			for _, c := range d.Recall.Candidates {
				if match(c.Passage + strconv.FormatUint(uint64(c.Episode), 10)) {
					v := c
					out = append(out, item{tier: -1, candidate: &v})
				}
			}
		}
	case 4:
		seen := make(map[uint64]bool)
		for _, ev := range m.events[eventKey(s, 0, 0)] {
			var cp core.CheckpointBody
			if json.Unmarshal(ev.Body, &cp) != nil {
				continue
			}
			block := diagnostic.Block{ID: fmt.Sprintf("checkpoint:%d", ev.Seq), Kind: "checkpoint", Tier: 3, Seq: ev.Seq, From: cp.From, To: cp.To, Tokens: (len(cp.Text) + 3) / 4, Preview: cp.Text, Fate: "summary retained in the log"}
			seen[ev.Seq] = true
			out = append(out, item{tier: -1, block: &block})
		}
		for _, b := range s.Blocks {
			if b.Kind == "checkpoint" && !seen[b.Seq] {
				v := b
				out = append(out, item{tier: -1, block: &v})
			}
		}
		slices.Reverse(out)

	}
	return out
}

func (m *model) rememberSelection() {
	rows := m.items()
	i := m.row[m.screen]
	if i >= len(rows) {
		return
	}
	if b := rows[i].block; b != nil {
		m.selectionSeq, m.selectionHash = b.Seq, b.Hash
	}
	if ep := rows[i].episode; ep != nil {
		m.selectionSeq = ep.Start
	}
}

func (m *model) syncSelection() {
	for i, row := range m.items() {
		if b := row.block; b != nil && ((m.selectionSeq != 0 && b.Seq == m.selectionSeq) || (m.selectionSeq == 0 && b.Hash == m.selectionHash)) {
			m.row[m.screen] = i
			return
		}
		if ep := row.episode; ep != nil && m.selectionSeq >= ep.Start && m.selectionSeq <= ep.End {
			m.row[m.screen] = i
			return
		}
	}
	m.row[m.screen] = 0
}

func (m *model) content(id string) diagnostic.Content {
	for _, b := range m.captures[m.sel].Blocks {
		if b.ID == id {
			return b
		}
	}
	return diagnostic.Content{}
}
