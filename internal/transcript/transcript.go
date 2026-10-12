// Package transcript turns what a client hears about a contact into what a
// person should see: one ordered conversation, plus the reply being written.
//
// It is a pure reducer over two inputs. Events come from the log: they are
// durable and carry sequence numbers. Deltas come from the live stream: text
// fragments and typing state are ephemeral, and the deltas that announce an
// event carry its sequence number. The same event may arrive both ways, in
// either order, and more than once; the sequence number makes that harmless.
// Nothing here does I/O or reads a clock, so every behaviour of a chat view
// can be tested by feeding this package values.
package transcript

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"

	"github.com/quinnovator/contactd/internal/core"
)

// Kind is what an Item is.
type Kind uint8

const (
	// User is a message to the contact.
	User Kind = iota + 1
	// Agent is a reply, whole or cut off.
	Agent
	// ToolCall is the agent running code, or calling the delegate tool.
	ToolCall
	// ToolResult is what a tool call returned.
	ToolResult
	// Notice is the runtime telling the agent something, such as a
	// sub-agent settling.
	Notice
	// Checkpoint marks where older conversation was folded into a summary.
	Checkpoint
	// Failure is a turn that ended without a reply. It is not in the log.
	Failure
)

// Item is one entry of the conversation.
type Item struct {
	// Seq is the event's sequence number. A Failure has none.
	Seq  uint64
	TS   int64 // unix ms
	Kind Kind
	// Text is the message, the code a tool call runs, a tool's output, the
	// notice, the checkpoint's summary, or the reason a turn failed.
	Text string
	// Tool names the tool of a call or a result.
	Tool string
	// Lead is what the agent said before calling the tool, in the same step.
	Lead string
	// Interrupted marks a reply that was cut off.
	Interrupted bool
	// IsError marks the result of a tool that failed.
	IsError bool
	// Child is the sub-agent a delegation started or a notice is about.
	Child core.ContactID
	// Task marks the message that gave a sub-agent its task.
	Task bool

	// fromLog is set once the item has been read from the log, which knows
	// more about it than the stream said.
	fromLog bool
	// after anchors a Failure: it sorts after the event with this number.
	after uint64
}

// Live is the turn in flight.
type Live struct {
	// Typing says the agent is composing.
	Typing bool
	// Text is the reply so far in the current step.
	Text string
	// Starting names a tool the model has begun to call; its arguments are
	// still arriving.
	Starting string
	// Running names a tool that is executing.
	Running string
}

// Transcript is the state of one conversation view. The zero value is an
// empty transcript.
type Transcript struct {
	items   []Item
	live    Live
	pending []pending
	// calls maps a tool call's ID to its tool, so that a result read from
	// the log can be labelled.
	calls map[string]string

	attempt    core.AttemptBody
	attemptSeq uint64
	rev        uint64
	settled    uint64
}

type pending struct {
	text string
	// seq is set once the message has been seen with a sequence number, on
	// the stream or in the log, and only its acknowledgement is awaited.
	seq uint64
}

// Items returns the conversation in order. The slice is the transcript's
// own; do not modify it.
func (t *Transcript) Items() []Item { return t.items }

// Live returns the turn in flight.
func (t *Transcript) Live() Live { return t.live }

// Pending returns the messages sent and not yet known to have arrived, in
// the order they were sent.
func (t *Transcript) Pending() []string {
	var out []string
	for _, p := range t.pending {
		if p.seq == 0 {
			out = append(out, p.text)
		}
	}
	return out
}

// Attempt returns the record of the newest model call a turn made, and
// whether there is one.
func (t *Transcript) Attempt() (core.AttemptBody, bool) { return t.attempt, t.attemptSeq != 0 }

// Rev changes whenever the transcript does, so a view can tell when what it
// last drew is stale.
func (t *Transcript) Rev() uint64 { return t.rev }

// SettledRev changes whenever the items or the pending messages do, and not
// when only the turn in flight does. A view can keep what it drew for the
// settled part of the conversation while a reply streams in.
func (t *Transcript) SettledRev() uint64 { return t.settled }

// Oldest returns the lowest sequence number in the transcript, or zero.
func (t *Transcript) Oldest() uint64 {
	for i := range t.items {
		if t.items[i].Seq != 0 {
			return t.items[i].Seq
		}
	}
	return 0
}

// Events applies events read from the log, in any order.
func (t *Transcript) Events(events []core.Event) {
	t.rev++
	for i := range events {
		t.event(&events[i])
	}
}

func (t *Transcript) event(ev *core.Event) {
	it := Item{Seq: ev.Seq, TS: ev.TS, fromLog: true}
	switch ev.Kind {
	case core.KindUserMsg:
		var b core.MsgBody
		if json.Unmarshal(ev.Body, &b) != nil {
			return
		}
		it.Kind, it.Text, it.Task = User, b.Text, b.Task != nil
	case core.KindAgentMsg:
		var b core.MsgBody
		if json.Unmarshal(ev.Body, &b) != nil {
			return
		}
		it.Kind, it.Text, it.Interrupted = Agent, b.Text, b.Interrupted
	case core.KindToolCall:
		var b core.ToolCall
		if json.Unmarshal(ev.Body, &b) != nil {
			return
		}
		it.Kind, it.Tool, it.Text, it.Lead = ToolCall, b.Name, b.Code, b.Text
		if b.Code == "" && len(b.Args) > 0 {
			it.Text = compactJSON(b.Args)
		}
		if t.calls == nil {
			t.calls = make(map[string]string)
		}
		t.calls[b.ID] = b.Name
	case core.KindToolResult:
		var b core.ToolResult
		if json.Unmarshal(ev.Body, &b) != nil {
			return
		}
		it.Kind, it.Tool, it.Text, it.IsError, it.Child = ToolResult, t.calls[b.ID], b.Output, b.IsError, b.Child
	case core.KindNotice:
		var b core.NoticeBody
		if json.Unmarshal(ev.Body, &b) != nil {
			return
		}
		it.Kind, it.Text, it.Child = Notice, b.Text, b.Child
	case core.KindCheckpoint:
		var b core.CheckpointBody
		if json.Unmarshal(ev.Body, &b) != nil {
			return
		}
		it.Kind, it.Text = Checkpoint, b.Text
	case core.KindAttempt:
		var b core.AttemptBody
		// Only a turn's own calls: a checkpoint or a summary is a side job.
		if json.Unmarshal(ev.Body, &b) == nil && b.Purpose == "" && ev.Seq > t.attemptSeq {
			t.attempt, t.attemptSeq = b, ev.Seq
		}
		return
	case core.KindSummary, core.KindEnvSnapshot:
		return
	default:
		return
	}
	if t.put(it) && it.Kind == User {
		t.arrived(it.Text, it.Seq)
	}
}

// Delta applies one item of the live stream. now stamps an item the delta
// creates, since the stream carries no times.
func (t *Transcript) Delta(d core.Delta, now int64) {
	t.rev++
	it := Item{Seq: d.Seq, TS: now}
	switch d.Type {
	case core.DeltaReady:
	case core.DeltaTyping:
		if d.Typing {
			t.live = Live{Typing: true}
		} else {
			// The turn is over. Whatever it said that mattered was committed
			// by an earlier delta.
			t.live = Live{}
		}
	case core.DeltaText:
		t.live.Typing = true
		t.live.Starting = ""
		t.live.Text += d.Text
	case core.DeltaToolStart:
		t.live.Typing = true
		t.live.Starting = d.Tool
	case core.DeltaToolCall:
		it.Kind, it.Tool, it.Text, it.Lead = ToolCall, d.Tool, d.Text, t.live.Text
		t.put(it)
		t.live = Live{Typing: true, Running: d.Tool}
	case core.DeltaToolResult:
		it.Kind, it.Tool, it.Text = ToolResult, d.Tool, d.Text
		t.put(it)
		t.live.Running = ""
	case core.DeltaMessage:
		it.Kind, it.Text = Agent, d.Text
		t.put(it)
		t.live.Text = ""
	case core.DeltaInterrupted:
		// The delta says a reply was cut off, not what it was: that is the
		// text streamed so far.
		it.Kind, it.Text, it.Interrupted = Agent, t.live.Text, true
		t.put(it)
		t.live.Text = ""
	case core.DeltaError:
		it.Kind, it.Text, it.after = Failure, d.Text, t.lastSeq()
		t.put(it)
		t.live.Text = ""
	case core.DeltaNotice:
		it.Kind, it.Text = Notice, d.Text
		t.put(it)
	case core.DeltaUser:
		it.Kind, it.Text = User, d.Text
		if t.put(it) {
			t.arrived(d.Text, d.Seq)
		}
	}
}

// Sent records a message handed to the server and not yet acknowledged.
func (t *Transcript) Sent(text string) {
	t.rev++
	t.settled++
	t.pending = append(t.pending, pending{text: text})
}

// Acked records that a message sent from here is durable.
func (t *Transcript) Acked(text string, seq uint64, now int64) {
	t.rev++
	t.settled++
	t.put(Item{Seq: seq, TS: now, Kind: User, Text: text})
	i := slices.IndexFunc(t.pending, func(p pending) bool { return p.seq == seq })
	if i < 0 {
		i = slices.IndexFunc(t.pending, func(p pending) bool { return p.seq == 0 && p.text == text })
	}
	if i >= 0 {
		t.pending = slices.Delete(t.pending, i, i+1)
	}
}

// Refused records that a message sent from here was not accepted. The
// caller still has its text and decides what becomes of it.
func (t *Transcript) Refused(text string) {
	t.rev++
	t.settled++
	for i, p := range slices.Backward(t.pending) {
		if p.seq == 0 && p.text == text {
			t.pending = slices.Delete(t.pending, i, i+1)
			return
		}
	}
}

// DropPending forgets every message still awaiting acknowledgement and
// returns the ones that never turned up, for the caller to offer again. It
// is what to do once a reconnect has caught up with the log: by then a
// message is either in the transcript or lost.
func (t *Transcript) DropPending() []string {
	lost := t.Pending()
	if len(t.pending) > 0 {
		t.rev++
		t.settled++
	}
	t.pending = nil
	return lost
}

// arrived notes that a message with this text now has a sequence number. If
// it is one sent from here, it stops being shown as pending.
func (t *Transcript) arrived(text string, seq uint64) {
	for i := range t.pending {
		if t.pending[i].seq == 0 && t.pending[i].text == text {
			t.pending[i].seq = seq
			t.settled++
			return
		}
	}
}

// put inserts an item in order and reports whether it is new. An item
// already present is replaced only by the log's account of it.
func (t *Transcript) put(it Item) (added bool) {
	if it.Seq == 0 && it.Kind != Failure {
		return false // an announcement without a sequence number has no place
	}
	i, found := slices.BinarySearchFunc(t.items, it, order)
	if found {
		// The log's account wins, and so does a newer failure after the
		// same event.
		if (it.fromLog && !t.items[i].fromLog) || it.Kind == Failure {
			t.items[i] = it
			t.settled++
		}
		return false
	}
	t.items = slices.Insert(t.items, i, it)
	t.settled++
	return true
}

// order sorts items by sequence number, a Failure directly after the event
// it followed.
func order(a, b Item) int {
	ka, kb := a.Seq, b.Seq
	if a.Kind == Failure {
		ka = a.after
	}
	if b.Kind == Failure {
		kb = b.after
	}
	switch {
	case ka != kb:
		if ka < kb {
			return -1
		}
		return 1
	case a.Kind == Failure && b.Kind != Failure:
		return 1
	case a.Kind != Failure && b.Kind == Failure:
		return -1
	default:
		return 0
	}
}

func (t *Transcript) lastSeq() uint64 {
	for _, it := range slices.Backward(t.items) {
		if it.Seq != 0 {
			return it.Seq
		}
	}
	return 0
}

// compactJSON renders a tool's arguments on one line.
func compactJSON(raw []byte) string {
	var b bytes.Buffer
	if json.Compact(&b, raw) != nil {
		return strings.TrimSpace(string(raw))
	}
	return b.String()
}
