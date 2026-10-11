package plan

import (
	"encoding/json"
	"fmt"

	"github.com/quinnovator/contactd/internal/core"
)

// entry is one rendered message together with where it came from.
type entry struct {
	msg    core.Message
	seq    uint64
	ts     int64 // unix ms; 0 for synthetic entries
	tokens int
	// boundary marks a user message that opens a turn: a safe place for the
	// tail to begin.
	boundary bool
}

const (
	interruptedNote = "\n\n[this reply was cut off before it finished]"
	orphanResult    = "[no result: the tool run did not finish]"
)

// normalize renders events with seq >= from into messages in an order every
// provider accepts, and it does so deterministically, so the same log prefix
// always renders to the same bytes.
//
// A user message that arrives while a tool is running is logged between the
// tool call and its result. It is rendered after the result: that is the
// order the model actually experienced, and providers require a result to
// follow its call directly. A call with no result (a crash mid-tool) gets a
// fixed placeholder result for the same reason.
func normalize(events []core.Event, from uint64) ([]entry, error) {
	out := make([]entry, 0, len(events))
	var (
		pending  *core.ToolCall
		deferred []entry
	)
	closePending := func() {
		if pending == nil {
			return
		}
		res := core.ToolResult{ID: pending.ID, Output: orphanResult, IsError: true}
		out = append(out, newEntry(core.Message{Role: core.RoleTool, ToolResult: &res}, 0, 0))
		out = append(out, deferred...)
		pending, deferred = nil, deferred[:0]
	}

	for i := range events {
		ev := &events[i]
		if ev.Seq < from {
			continue
		}
		switch ev.Kind {
		case core.KindUserMsg:
			var body core.MsgBody
			if err := json.Unmarshal(ev.Body, &body); err != nil {
				return nil, fmt.Errorf("decode user message seq %d: %w", ev.Seq, err)
			}
			e := newEntry(core.Message{Role: core.RoleUser, Text: body.Text}, ev.Seq, ev.TS)
			if pending != nil {
				deferred = append(deferred, e)
			} else {
				out = append(out, e)
			}
		case core.KindAgentMsg:
			var body core.MsgBody
			if err := json.Unmarshal(ev.Body, &body); err != nil {
				return nil, fmt.Errorf("decode agent message seq %d: %w", ev.Seq, err)
			}
			closePending()
			text := body.Text
			if body.Interrupted {
				text += interruptedNote
			}
			out = append(out, newEntry(core.Message{Role: core.RoleAssistant, Text: text}, ev.Seq, ev.TS))
		case core.KindToolCall:
			call := new(core.ToolCall)
			if err := json.Unmarshal(ev.Body, call); err != nil {
				return nil, fmt.Errorf("decode tool call seq %d: %w", ev.Seq, err)
			}
			closePending()
			out = append(out, newEntry(core.Message{Role: core.RoleAssistant, Text: call.Text, ToolCall: call}, ev.Seq, ev.TS))
			pending = call
		case core.KindToolResult:
			res := new(core.ToolResult)
			if err := json.Unmarshal(ev.Body, res); err != nil {
				return nil, fmt.Errorf("decode tool result seq %d: %w", ev.Seq, err)
			}
			if pending == nil || pending.ID != res.ID {
				continue // its call fell before the tail; nothing to attach it to
			}
			out = append(out, newEntry(core.Message{Role: core.RoleTool, ToolResult: res}, ev.Seq, ev.TS))
			out = append(out, deferred...)
			pending, deferred = nil, deferred[:0]
		case core.KindSummary, core.KindEnvSnapshot:
			// Bookkeeping events are never shown to the model.
		}
	}
	closePending()

	for i := range out {
		out[i].boundary = out[i].msg.Role == core.RoleUser &&
			(i == 0 || out[i-1].msg.Role != core.RoleUser)
	}
	return out, nil
}

func newEntry(m core.Message, seq uint64, ts int64) entry {
	return entry{msg: m, seq: seq, ts: ts, tokens: EstimateTokens(&m)}
}

// alignToBoundary drops leading entries until the tail starts at a turn
// boundary. A freshly hydrated window can begin mid-turn.
func alignToBoundary(entries []entry) []entry {
	for i := range entries {
		if entries[i].boundary {
			return entries[i:]
		}
	}
	return nil
}

// splitLive separates the trailing run of user messages, which is what this
// turn answers, from the history before it.
func splitLive(entries []entry) (tail, live []entry) {
	i := len(entries)
	for i > 0 && entries[i-1].msg.Role == core.RoleUser {
		i--
	}
	return entries[:i], entries[i:]
}

// compact enforces the tail budget (Rule 2). In step mode it leaves the tail
// alone until it passes the budget, then cuts the oldest CompactFraction at a
// turn boundary: one cache miss per many turns. In slide mode it trims to the
// budget every time, which is what a naive sliding window does and what the
// simulator compares against.
func compact(tail []entry, pr Params) []entry {
	for {
		total := 0
		for i := range tail {
			total += tail[i].tokens
		}
		if total <= pr.TailBudget {
			return tail
		}
		target := int(float64(total) * pr.CompactFraction)
		if pr.Slide {
			target = total - pr.TailBudget
		}
		cut, removed := 0, 0
		for i := 1; i < len(tail); i++ {
			removed += tail[i-1].tokens
			if tail[i].boundary && removed >= target {
				cut = i
				break
			}
		}
		if cut == 0 {
			return tail // one oversized turn; there is no boundary to cut at
		}
		tail = tail[cut:]
	}
}

func firstSeq(tail, live []entry) uint64 {
	for i := range tail {
		if tail[i].seq != 0 {
			return tail[i].seq
		}
	}
	for i := range live {
		if live[i].seq != 0 {
			return live[i].seq
		}
	}
	return 0
}
