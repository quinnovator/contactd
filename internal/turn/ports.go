package turn

import (
	"context"
	"iter"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/plan"
)

// LLM streams one model call for a laid-out prompt. Adapters translate the
// plan's tiers into their wire format and put a cache breakpoint at the end
// of each non-empty tier 0-3 where the provider takes explicit breakpoints.
type LLM interface {
	Stream(ctx context.Context, p *plan.PromptPlan) (Stream, error)
}

// Stream is one in-flight model call.
//
// Events yields deltas until the model stops, the context is cancelled, or
// the call fails. It may be ranged over once. Breaking out of the loop early
// releases the underlying connection. Stop is valid only after Events has
// returned; it reports how the call ended, or the error that ended it.
type Stream interface {
	Events() iter.Seq[StreamEvent]
	Stop() (Stop, error)
}

// StreamEventType tags a StreamEvent.
type StreamEventType uint8

const (
	// TextDelta carries a fragment of reply text in Text.
	TextDelta StreamEventType = iota + 1
	// ToolStart announces a tool call as soon as its name is known, before
	// its arguments have finished streaming. Name is set.
	ToolStart
)

// StreamEvent is one item from a model stream.
type StreamEvent struct {
	Type StreamEventType
	Text string
	Name string
}

// Stop is how a model call ended.
type Stop struct {
	// Text is the full reply text of this call.
	Text string
	// ToolCall is set when the model stopped to run a tool. ID, Name and Code
	// are filled by the adapter; the turn loop fills Env and Text.
	ToolCall *core.ToolCall
	Usage    Usage
}

// Usage is the provider's token accounting for one call.
type Usage struct {
	InputTokens  int // every input token, cached or not
	CachedTokens int // input tokens served from the provider's prompt cache
	OutputTokens int
}

// Summarizer writes a short summary of an episode transcript with a small,
// cheap model.
type Summarizer interface {
	Summarize(ctx context.Context, transcript string) (string, error)
}

// EnvLeaser hands out REPL environments for one tier.
//
// resume is the snapshot to restore, or "" for a fresh environment. contact
// lets a provider keep per-contact state such as a working directory.
type EnvLeaser interface {
	Lease(ctx context.Context, contact core.ContactID, spec core.EnvSpec, resume core.SnapshotID) (Lease, error)
}

// Lease is a live environment. Exec calls on one lease are never concurrent.
type Lease interface {
	// Exec runs code and returns what it printed. A non-zero exit code is a
	// result, not an error; err is for failures of the environment itself.
	// Cancelling ctx must stop runaway code.
	Exec(ctx context.Context, code string) (ExecResult, error)
	// Snapshot persists the environment and returns an ID that a later
	// Lease call can resume from. The caller releases the lease afterwards.
	Snapshot(ctx context.Context) (core.SnapshotID, error)
	// Release frees the environment. It is safe to call more than once.
	Release()
}

// ExecResult is the outcome of one Exec.
type ExecResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
	// Note tells the agent what did and did not survive since it last used
	// the environment, for example that files persisted but interpreter
	// variables did not. Providers set it on the first Exec after a lease is
	// opened or an instance is restarted, and leave it empty otherwise.
	Note string
}
