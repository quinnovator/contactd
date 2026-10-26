package turn

import (
	"context"
	"time"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/llm"
	"github.com/quinnovator/contactd/internal/plan"
)

// Models is the turn loop's way to a model. *llm.Client implements it.
//
// The core reaches every provider through this and the llm contract's types,
// and acts only on a route's declared capabilities. It never names a
// provider.
type Models interface {
	// Route resolves the names in an agent config, strictly: an unknown
	// provider or model is an error, never a default.
	Route(ctx context.Context, provider, model string) (llm.Route, error)
	// Affinity returns the key that pins a contact to one backend.
	Affinity(contact core.ContactID) string
	// Call freezes and starts one model call. When it fails after a request
	// was frozen, the returned call is still non-nil so that the attempt can
	// be recorded.
	Call(ctx context.Context, r llm.Route, p *plan.PromptPlan, o llm.Options) (*llm.Call, error)
}

// Delegator starts sub-agents (the registry implements it).
type Delegator interface {
	// Delegate starts a sub-agent and returns its ID without waiting for it.
	// The parent hears how it went later, as a notice.
	Delegate(ctx context.Context, parent core.ContactID, d Delegation) (core.ContactID, error)
}

// Delegation is one request to start a sub-agent.
type Delegation struct {
	Task string
	// Turn is the parent's turn that is delegating.
	Turn uint64
	// Agent is the parent's own config: the child gets its system prompt,
	// tools and route.
	Agent *core.AgentConfig
	// Base is set for a fork: the parent's prompt up to its last completed
	// turn. The child's first request then begins with those exact bytes.
	Base *plan.PromptPlan
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

// Helpers starts and steers helpers on behalf of a contact.
type Helpers interface {
	Start(ctx context.Context, parent core.ContactID, req HelperStart) (HelperInfo, error)
	Message(ctx context.Context, parent core.ContactID, handle uint64, text string) (HelperInfo, error)
	Stop(ctx context.Context, parent core.ContactID, handle uint64) (HelperInfo, error)
	List(ctx context.Context, parent core.ContactID) ([]HelperInfo, error)
}

// HelperStart describes one standing job.
type (
	HelperStart struct {
		Title, Brief string
		Every, For   time.Duration
	}
	// HelperInfo is the model's view of a helper.
	HelperInfo struct {
		Handle               uint64
		Title, TaskID, State string
		Every                time.Duration
		Until, NextCheck     time.Time
		Checks, Reports      int
		Message              string
	}
)

// Fetcher reads a web page as text.
type (
	Fetcher interface {
		Fetch(ctx context.Context, url string) (Page, error)
	}
	// Page is the bounded text of a fetched resource.
	Page struct {
		URL               string
		Status            int
		Type, Title, Text string
	}
)
