// Package core holds the types every other package shares: contact and event
// identities, the event bodies stored in the log, prompt messages, and
// sentinel errors. It imports nothing outside the standard library.
package core

import (
	"errors"
	"slices"
	"strconv"
)

// VectorDim is the embedding width. Both supported embedders
// (bge-small-en-v1.5, all-MiniLM-L6-v2) produce 384-d vectors.
const VectorDim = 384

// ContactID identifies a contact, the only thing a client addresses.
type ContactID uint64

func (id ContactID) String() string { return strconv.FormatUint(uint64(id), 10) }

// EventKind tags what an Event records.
type EventKind uint8

const (
	KindUserMsg EventKind = iota + 1
	KindAgentMsg
	KindToolCall
	KindToolResult
	KindSummary     // lazily generated episode summary
	KindEnvSnapshot // pointer to a REPL snapshot, not the snapshot itself
)

func (k EventKind) String() string {
	switch k {
	case KindUserMsg:
		return "user_msg"
	case KindAgentMsg:
		return "agent_msg"
	case KindToolCall:
		return "tool_call"
	case KindToolResult:
		return "tool_result"
	case KindSummary:
		return "summary"
	case KindEnvSnapshot:
		return "env_snapshot"
	default:
		return "unknown"
	}
}

// IsMessage reports whether events of this kind are embedded and segmented
// into episodes.
func (k EventKind) IsMessage() bool { return k == KindUserMsg || k == KindAgentMsg }

// Event is one entry in a contact's append-only log.
type Event struct {
	Contact ContactID
	Seq     uint64 // monotonic per contact; assigned by that contact's actor
	TS      int64  // unix ms
	Kind    EventKind
	Body    []byte // JSON, opaque to the log
}

// Contact is the durable identity row.
type Contact struct {
	ID         ContactID
	Agent      AgentConfig
	Profile    string
	LastActive int64 // unix ms
}

// AgentConfig is what a contact is: its system prompt, model and tools. It is
// stored with the contact and forms the static tier of every prompt.
type AgentConfig struct {
	System    string    `json:"system"`
	Model     string    `json:"model,omitempty"`
	MaxTokens int       `json:"max_tokens,omitempty"`
	Tools     []ToolDef `json:"tools,omitempty"`
}

// ToolDef declares a code-execution tool. Every tool takes a single "code"
// argument and runs it in the environment described by Env.
type ToolDef struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Env         EnvSpec `json:"env"`
}

// EnvSpec describes the REPL environment a tool needs.
type EnvSpec struct {
	Tier     uint8    `json:"tier"` // 0 in-process WASM, 1 remote snapshot sandbox
	Base     string   `json:"base"` // "python-3.12", "quickjs", "re-ghidra" ...
	Packages []string `json:"packages,omitempty"`
	Network  bool     `json:"network,omitempty"` // tier 1 only
}

// Compatible reports whether a lease opened for s can serve a call that asks
// for other: same tier and base, every requested package already present, and
// no more network access than was granted.
func (s EnvSpec) Compatible(other EnvSpec) bool {
	if s.Tier != other.Tier || s.Base != other.Base || (other.Network && !s.Network) {
		return false
	}
	for _, want := range other.Packages {
		if !slices.Contains(s.Packages, want) {
			return false
		}
	}
	return true
}

// SnapshotID names an environment snapshot held by an EnvLeaser.
type SnapshotID string

// MsgBody is the body of KindUserMsg and KindAgentMsg events.
type MsgBody struct {
	Text string `json:"text"`
	// Interrupted marks an agent reply that was cut off by a new user
	// message, so the model knows it never finished the thought.
	Interrupted bool `json:"interrupted,omitempty"`
}

// ToolCall is the body of a KindToolCall event and the model's request to run
// code.
type ToolCall struct {
	ID   string  `json:"id"`
	Name string  `json:"name"`
	Code string  `json:"code"`
	Env  EnvSpec `json:"env"`
	// Text is whatever the model said before calling the tool in the same step.
	Text string `json:"text,omitempty"`
	// Replay is the provider's own wire form of the assistant turn that made
	// this call (for Anthropic, its content blocks including thinking). An
	// adapter sets it and, for the rest of the same turn, sends it back
	// verbatim instead of rebuilding the turn from Text and Code: providers
	// require reasoning blocks to be returned unchanged during a tool loop.
	// It is never logged. Such blocks are only valid behind the exact prompt
	// that produced them, which holds within a turn and not across turns.
	Replay []byte `json:"-"`
}

// ToolResult is the body of a KindToolResult event.
type ToolResult struct {
	ID      string `json:"id"` // the ToolCall.ID it answers
	Output  string `json:"output"`
	IsError bool   `json:"is_error,omitempty"`
}

// SummaryBody is the body of a KindSummary event.
type SummaryBody struct {
	Episode uint32 `json:"episode"`
	// Start and End are the summarized seq span. They let a rebuilt episode
	// index, whose IDs may differ, find its summary again.
	Start uint64 `json:"start"`
	End   uint64 `json:"end"`
	Text  string `json:"text"`
}

// EnvSnapshotBody is the body of a KindEnvSnapshot event.
type EnvSnapshotBody struct {
	Spec     EnvSpec    `json:"spec"`
	Snapshot SnapshotID `json:"snapshot"`
}

// Role is who a prompt message is from.
type Role uint8

const (
	RoleSystem Role = iota + 1
	RoleUser
	RoleAssistant
	RoleTool
)

func (r Role) String() string {
	switch r {
	case RoleSystem:
		return "system"
	case RoleUser:
		return "user"
	case RoleAssistant:
		return "assistant"
	case RoleTool:
		return "tool"
	default:
		return "unknown"
	}
}

// Message is one provider-neutral prompt message. Adapters translate a run of
// these into their wire format.
//
// An assistant message may carry Text, a ToolCall, or both. A RoleTool
// message carries exactly one ToolResult.
type Message struct {
	Role       Role        `json:"role"`
	Text       string      `json:"text,omitempty"`
	ToolCall   *ToolCall   `json:"tool_call,omitempty"`
	ToolResult *ToolResult `json:"tool_result,omitempty"`
}

// DeltaType tags what a Delta carries.
type DeltaType string

const (
	DeltaTyping      DeltaType = "typing"      // Typing says whether the agent is composing
	DeltaText        DeltaType = "text"        // Text is a fragment of the reply
	DeltaToolStart   DeltaType = "tool_start"  // the model named a tool; args still streaming
	DeltaToolCall    DeltaType = "tool_call"   // Tool, Text (code) and Seq are set
	DeltaToolResult  DeltaType = "tool_result" // Tool, Text (output) and Seq are set
	DeltaMessage     DeltaType = "message"     // the committed reply; Text and Seq are set
	DeltaInterrupted DeltaType = "interrupted" // the reply in flight was cut off
	DeltaError       DeltaType = "error"       // the turn failed; Text is a short reason
)

// Delta is one item on a contact's live stream.
type Delta struct {
	Type   DeltaType `json:"type"`
	Turn   uint64    `json:"turn,omitempty"`
	Text   string    `json:"text,omitempty"`
	Tool   string    `json:"tool,omitempty"`
	Seq    uint64    `json:"seq,omitempty"`
	Typing bool      `json:"typing,omitempty"`
}

// Sentinel errors, checked with errors.Is.
var (
	ErrContactNotFound = errors.New("contact not found")
	// ErrClosed is returned once shutdown has begun.
	ErrClosed = errors.New("closed")
	// ErrWriteRefused means an earlier write for the same contact failed and
	// its writer has not reset yet. Committing later writes anyway would leave
	// a gap in the contact's sequence numbers.
	ErrWriteRefused = errors.New("an earlier write for this contact failed")
	// ErrBacklog means a contact has too many unanswered messages queued.
	ErrBacklog = errors.New("too many unanswered messages")
	// ErrUnknownTool means the model called a tool the agent does not declare.
	ErrUnknownTool = errors.New("unknown tool")
	// ErrNoEnvProvider means no provider is configured for a tool's tier.
	ErrNoEnvProvider = errors.New("no environment provider for tier")
)
