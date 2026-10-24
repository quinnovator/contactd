// Package actor gives every contact that is currently talking one goroutine
// that owns all of its state (Piece 2), and makes it behave like a person:
// it waits for a burst of messages to finish before replying, and re-plans
// when something is added mid-reply (Piece 7). Every other contact is just
// rows on disk.
//
// Actor fields are touched only by the actor's own goroutine. Turn
// goroutines get immutable snapshots and talk back only through Cmd values.
// The only locks in this package are the registry's shard locks, which guard
// the map of live actors and never any actor's state.
package actor

import (
	"context"
	"time"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/plan"
	"github.com/quinnovator/contactd/internal/recall"
	"github.com/quinnovator/contactd/internal/turn"
)

// Cmd is a message in an actor's mailbox.
type Cmd interface{ isCmd() }

// Ack reports that an inbound message is durable, or why it is not.
type Ack struct {
	Seq uint64
	Err error
}

// Inbound is a message from the contact's human. Ack must be buffered.
type Inbound struct {
	Visit core.Visit
	Text  string
	Ack   chan<- Ack
}

// Typing is a typing indicator. It holds the reply back like a message does,
// up to the hard cap.
type Typing struct{}

// Interrupt stops the reply in flight without saying anything: what a person
// does by raising a hand. A running tool finishes first, as it does when a
// message interrupts, and the turn ends there. The message that was being
// answered stays unanswered until the next one arrives.
type Interrupt struct{}

// Hibernate asks an idle actor to retire. The registry sends it under memory
// pressure.
type Hibernate struct{}

// The remaining commands are internal: results coming back from goroutines
// the actor started, and registry plumbing.

type subscribe struct{ sub *Subscription }

type unsubscribe struct{ sub *Subscription }

// stop retires the actor at shutdown, cancelling whatever is in flight.
type stop struct{}

// flushNow starts the turn for any pending messages without waiting out the
// debounce. Shutdown sends it so an acknowledged message is answered within
// the grace period. done is buffered.
type flushNow struct{ done chan<- struct{} }

type turnDelta struct {
	id uint64
	d  core.Delta
}

// turnToolCall asks the actor to log a tool call and let the tool start.
// enter moves the turn into its tool phase; the actor calls it on its own
// goroutine, so every message it handles is cleanly before the call (and
// cancels the turn) or after it (and waits for the tool). ack is buffered.
type turnToolCall struct {
	id    uint64
	call  core.ToolCall
	enter func() bool
	ack   chan<- error
}

// turnToolResult asks the actor to log a tool result and end the tool
// phase. exit leaves the phase and reports whether a message arrived during
// it; like enter, it runs on the actor goroutine, after the result has been
// appended. ack is buffered.
type turnToolResult struct {
	id   uint64
	name string
	res  core.ToolResult
	exit func() bool
	ack  chan<- toolAck
}

type toolAck struct {
	replan bool
	err    error
}

type turnDone struct {
	id  uint64
	res turn.Result
	err error
}

// turnAttempt asks the actor to log the record of one model call.
type turnAttempt struct{ body core.AttemptBody }

// task starts a sub-agent on its job. base is set for a fork: the parent's
// prompt up to its last completed turn. ack is buffered.
type task struct {
	text string
	base *plan.PromptPlan
	ref  core.TaskRef
	ack  chan<- Ack
}

// resettle asks a sub-agent to report its task's outcome again, from its
// log. A parent sends it for each sub-agent its own log shows as started and
// not yet settled: after a restart, or after anything else that could have
// lost the first report.
type resettle struct{ parent core.ContactID }

// settled tells a parent that one of its sub-agents has finished, one way or
// another.
type settled struct {
	child  core.ContactID
	notice core.NoticeBody
}

// folded returns a checkpoint summary for a span of the tail.
type folded struct {
	// turn is the turn whose prompt the request was cut from, and absorbs
	// says that prompt already opened with an earlier checkpoint, which
	// this one therefore covers too.
	turn    uint64
	absorbs bool
	span    plan.Span
	text    string
	attempt *core.AttemptBody
	err     error
}

// embedded returns vectors for the first n queued messages.
type embedded struct {
	n    int
	vecs [][]float32
	err  error
}

type summarized struct {
	ep   *recall.Episode
	text string
	err  error
}

// writeFailed means a write the actor already applied in memory did not
// commit, so its memory can no longer be trusted.
type writeFailed struct{ err error }

// envIdle means the environment handle released an idle lease and has a
// snapshot to record.
type envIdle struct{}

func (Inbound) isCmd()        {}
func (Typing) isCmd()         {}
func (Interrupt) isCmd()      {}
func (Hibernate) isCmd()      {}
func (subscribe) isCmd()      {}
func (unsubscribe) isCmd()    {}
func (stop) isCmd()           {}
func (flushNow) isCmd()       {}
func (turnDelta) isCmd()      {}
func (turnToolCall) isCmd()   {}
func (turnToolResult) isCmd() {}
func (turnDone) isCmd()       {}
func (turnAttempt) isCmd()    {}
func (task) isCmd()           {}
func (settled) isCmd()        {}
func (resettle) isCmd()       {}
func (folded) isCmd()         {}
func (embedded) isCmd()       {}
func (summarized) isCmd()     {}
func (writeFailed) isCmd()    {}
func (envIdle) isCmd()        {}

// Subscription is one live stream of a contact's deltas. C is closed when the
// subscription ends: because the subscriber fell too far behind, the contact
// could not be loaded, or the server is shutting down.
type Subscription struct {
	C  <-chan core.Delta
	ch chan core.Delta
}

func newSubscription(buffer int) *Subscription {
	ch := make(chan core.Delta, buffer)
	return &Subscription{C: ch, ch: ch}
}

// Writer queues writes for the group-commit writer.
type Writer interface {
	Submit(w core.Write)
}

// Hydrator loads what an actor needs to wake a contact.
type Hydrator interface {
	Hydrate(ctx context.Context, id core.ContactID, tail int) (core.Hydrated, error)
	// MessagesAfter returns user and agent messages with seq > after.
	MessagesAfter(ctx context.Context, id core.ContactID, after uint64, limit int) ([]core.Event, error)
}

// Repairer supports rebuilding a contact's derived episode index.
type Repairer interface {
	Vectors(ctx context.Context, id core.ContactID, from, to uint64) ([]core.Vector, error)
	ReplaceEpisodes(ctx context.Context, id core.ContactID, rows []core.EpisodeRow) error
}

// Creator makes new contacts. Sub-agents are contacts of their own.
type Creator interface {
	CreateContact(ctx context.Context, agent core.AgentConfig, profile string, now int64) (core.ContactID, error)
}

// Deps are the services actors use. They are shared by every contact.
type Deps struct {
	Log     Writer
	Hydrate Hydrator
	// Create makes the contact behind each sub-agent; nil disables
	// delegation.
	Create    Creator
	Repair    Repairer
	Embed     recall.Embedder
	Summarize turn.Summarizer // optional; nil disables lazy summaries
	Envs      map[uint8]turn.EnvLeaser
	Snapshots turn.SnapshotLog
	Turn      *turn.Deps
}

// Config tunes actors and the registry. Start from DefaultConfig.
type Config struct {
	IdleTTL     time.Duration // an actor idle this long retires
	Debounce    time.Duration // quiet time that ends a burst
	DebounceCap time.Duration // longest a reply is held back after the first pending message
	MaxHydrated int           // actors kept in memory before the least recently active idle ones are hibernated; 0 = unlimited
	TailEvents  int           // events loaded on hydration
	InboxSize   int
	EnvIdleTTL  time.Duration // an unused environment lease is snapshotted and released after this
	SubBuffer   int           // deltas buffered per subscriber before it is dropped
	// SummaryTokens bounds the transcript handed to the summarizer.
	SummaryTokens int
	// MaxPendingBytes bounds the unanswered messages a contact may queue.
	// Past it new messages are refused with core.ErrBacklog, so a client that
	// sends faster than the model answers cannot grow the prompt without limit.
	MaxPendingBytes int
	// Checkpoint folds the oldest part of an over-budget tail into a summary
	// written by the contact's own model, instead of cutting it.
	Checkpoint bool
	// MaxChildren bounds the sub-agents one contact may have running.
	MaxChildren int
	Recall      recall.Params
}

// DefaultConfig returns the default tuning; docs/ARCHITECTURE.md explains it.
func DefaultConfig() Config {
	return Config{
		IdleTTL:       10 * time.Minute,
		Debounce:      700 * time.Millisecond,
		DebounceCap:   3 * time.Second,
		TailEvents:    256,
		InboxSize:     64,
		EnvIdleTTL:    2 * time.Minute,
		SubBuffer:     256,
		SummaryTokens: 2000,
		// About 32k tokens: far more than a person types between replies,
		// well inside any model's context window.
		MaxPendingBytes: 128 << 10,
		Checkpoint:      true,
		MaxChildren:     8,
		Recall:          recall.DefaultParams(),
	}
}
