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
	// ckpt is set on the one entry that stands in for a checkpointed span:
	// the seq of the checkpoint event, and the first seq the span covered.
	ckpt, ckptFrom uint64
}

const (
	interruptedNote = "\n\n[this reply was cut off before it finished]"
	orphanResult    = "[no result: the tool run did not finish]"
	checkpointLead  = "[Summary of the conversation up to this point. The original messages are no longer shown.]\n\n"
)

// window says which part of a hot window is rendered, and for whom.
type window struct {
	from  uint64 // first seq rendered verbatim
	ckpt  uint64 // seq of the checkpoint event the previous plan applied, or 0
	route string // reasoning payloads are rendered only for this route
}

// normalize renders events with seq >= from into messages in an order every
// provider accepts, and it does so deterministically, so the same log prefix
// always renders to the same bytes.
//
// A user message that arrives while a tool is running is logged between the
// tool call and its result. It is rendered after the result: that is the
// order the model actually experienced, and providers require a result to
// follow its call directly. A call with no result (a crash mid-tool) gets a
// fixed placeholder result for the same reason.
//
// A checkpoint event replaces every event up to the end of its span with one
// summary message at the head of the tail. The events it shadows stay in the
// log. The newest applicable checkpoint wins, and one whose span was already
// cut out of the tail is not brought back.
func normalize(events []core.Event, w window) ([]entry, error) {
	var (
		ck    core.CheckpointBody
		ckSeq uint64
	)
	for i := range events {
		ev := &events[i]
		if ev.Kind != core.KindCheckpoint || ev.Seq < w.ckpt {
			continue
		}
		var body core.CheckpointBody
		if err := json.Unmarshal(ev.Body, &body); err != nil {
			return nil, fmt.Errorf("decode checkpoint seq %d: %w", ev.Seq, err)
		}
		if ev.Seq == w.ckpt || body.To >= w.from {
			ck, ckSeq = body, ev.Seq
		}
	}
	from := w.from
	if ckSeq != 0 {
		from = max(from, ck.To+1)
	}
	keep := func(r *core.Reasoning) *core.Reasoning {
		if r == nil || r.Route != w.route || w.route == "" {
			return nil
		}
		return r
	}

	out := make([]entry, 0, len(events)+1)
	if ckSeq != 0 {
		e := newEntry(core.Message{Role: core.RoleUser, Text: checkpointLead + ck.Text}, 0, 0)
		e.ckpt, e.ckptFrom = ckSeq, ck.From
		out = append(out, e)
	}
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
		case core.KindNotice:
			// The runtime speaking, in the user's position: it is something
			// for the agent to respond to, and the wrapper keeps it apart
			// from anything the contact wrote.
			var body core.NoticeBody
			if err := json.Unmarshal(ev.Body, &body); err != nil {
				return nil, fmt.Errorf("decode notice seq %d: %w", ev.Seq, err)
			}
			e := newEntry(core.Message{Role: core.RoleUser, Text: renderNotice(&body)}, ev.Seq, ev.TS)
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
			out = append(out, newEntry(core.Message{Role: core.RoleAssistant, Text: text, Reasoning: keep(body.Reasoning)}, ev.Seq, ev.TS))
		case core.KindToolCall:
			call := new(core.ToolCall)
			if err := json.Unmarshal(ev.Body, call); err != nil {
				return nil, fmt.Errorf("decode tool call seq %d: %w", ev.Seq, err)
			}
			closePending()
			call.Reasoning = keep(call.Reasoning)
			out = append(out, newEntry(core.Message{Role: core.RoleAssistant, Text: call.Text, ToolCall: call, Reasoning: call.Reasoning}, ev.Seq, ev.TS))
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
		case core.KindSummary, core.KindEnvSnapshot, core.KindAttempt, core.KindCheckpoint:
			// Bookkeeping events are never shown to the model. A checkpoint
			// is rendered above, once, not where it sits in the log.
		}
	}
	closePending()

	for i := range out {
		out[i].boundary = out[i].msg.Role == core.RoleUser &&
			(i == 0 || out[i-1].msg.Role != core.RoleUser || out[i-1].ckpt != 0)
	}
	return out, nil
}

func renderNotice(n *core.NoticeBody) string {
	return "<notice source=\"" + n.Source + "\">\n" + n.Text + "\n</notice>"
}

func newEntry(m core.Message, seq uint64, ts int64) entry {
	return entry{msg: m, seq: seq, ts: ts, tokens: EstimateTokens(&m)}
}

// alignToBoundary drops leading entries until the tail starts at a turn
// boundary. A freshly hydrated window can begin mid-turn. A checkpoint
// summary always stays.
func alignToBoundary(entries []entry) []entry {
	head := 0
	if len(entries) > 0 && entries[0].ckpt != 0 {
		head = 1
	}
	for i := head; i < len(entries); i++ {
		if entries[i].boundary {
			return append(entries[:head:head], entries[i:]...)
		}
	}
	return entries[:head]
}

// splitLive separates the trailing run of user messages, which is what this
// turn answers, from the history before it. A checkpoint summary is history.
func splitLive(entries []entry) (tail, live []entry) {
	i := len(entries)
	for i > 0 && entries[i-1].msg.Role == core.RoleUser && entries[i-1].ckpt == 0 {
		i--
	}
	return entries[:i], entries[i:]
}

// compact enforces the tail budget (Rule 2). In step mode it leaves the tail
// alone until it passes the budget, then cuts the oldest CompactFraction at a
// turn boundary: one cache miss per many turns. In slide mode it trims to the
// budget every time, which is what a naive sliding window does and what the
// simulator compares against.
//
// Where checkpoints are in use the budget is the trigger for folding, which
// happens off the turn's path, and the cut happens only at the hard budget:
// the guard for a tail that outgrew its checkpoints.
func compact(tail []entry, pr Params) []entry {
	limit := max(pr.TailBudget, pr.HardBudget)
	for {
		total := 0
		for i := range tail {
			total += tail[i].tokens
		}
		if total <= limit {
			return tail
		}
		target := int(float64(total) * pr.CompactFraction)
		if pr.Slide {
			target = total - limit
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
