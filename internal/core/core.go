// Package core holds the types every other package shares: contact and event
// identities, the event bodies stored in the log, prompt messages, and
// sentinel errors. It imports nothing outside the standard library.
package core

import (
	"encoding/json"
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
	KindAttempt     // one model call: what was sent and how it went; never shown to the model
	KindCheckpoint  // a summary that replaces the oldest span of the tail
	KindNotice      // the runtime telling the agent something, such as a sub-agent settling
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
	case KindAttempt:
		return "attempt"
	case KindCheckpoint:
		return "checkpoint"
	case KindNotice:
		return "notice"
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
	System string `json:"system"`
	// Provider and Model name the route that serves this contact. Either may
	// be empty to take the deployment's default. They are resolved strictly:
	// a name the deployment does not know is an error, never a fallback.
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	// Effort is the reasoning effort on the neutral scale: none, low,
	// medium, high, xhigh or max. Empty leaves it to the route.
	Effort    string    `json:"effort,omitempty"`
	MaxTokens int       `json:"max_tokens,omitempty"`
	Tools     []ToolDef `json:"tools,omitempty"`
	// Delegate gives the agent the delegate tool, which starts sub-agents.
	Delegate bool `json:"delegate,omitempty"`
	// Parent is set on a sub-agent: the contact that started it. A
	// sub-agent cannot delegate, whatever Delegate says.
	Parent ContactID `json:"parent,omitempty"`
}

// ToolDef declares a code-execution tool. Every tool takes a single "code"
// argument and runs it in the environment described by Env.
type ToolDef struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Env         EnvSpec `json:"env"`
	// Input is a JSON Schema for the tool's arguments. Empty means the
	// default: one string, "code", which is what every REPL tool takes. A
	// tool with its own schema receives its arguments raw, in ToolCall.Args.
	Input json.RawMessage `json:"input,omitempty"`
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
	// Reasoning is the provider's payload for this reply, stored only on
	// routes that require it back on later turns.
	Reasoning *Reasoning `json:"reasoning,omitempty"`
}

// Reasoning is a provider's own record of a model's reasoning for one
// assistant turn: thinking blocks with their signatures, a reasoning_content
// string, or whatever else the route needs returned. It is opaque to the
// core, which stores it and hands it back untouched, and only ever to the
// route that produced it.
type Reasoning struct {
	Route string `json:"route"` // the llm route ID allowed to receive it back
	Data  []byte `json:"data"`
}

// ToolCall is the body of a KindToolCall event and the model's request to run
// code.
type ToolCall struct {
	ID   string  `json:"id"`
	Name string  `json:"name"`
	Code string  `json:"code"`
	Env  EnvSpec `json:"env"`
	// Args is the call's arguments as the model wrote them, for a tool that
	// declares its own Input schema. Code is empty for such a call.
	Args json.RawMessage `json:"args,omitempty"`
	// Sibling is the call's position among the calls the model made in one
	// step: 0 for the first, and above 0 for a call that belongs to the same
	// assistant message as the call before it. Calls are run and logged one
	// at a time; this is what lets a request put them back together.
	Sibling int `json:"sibling,omitempty"`
	// Text is whatever the model said before calling the tool in the same
	// step. It is set on the first call of a step only.
	Text string `json:"text,omitempty"`
	// Reasoning is the provider's payload for the assistant turn that made
	// this call, on the first call of a step only. Within the turn it is
	// always carried, so a tool loop can return it; it is logged only on
	// routes that need it on later turns.
	Reasoning *Reasoning `json:"reasoning,omitempty"`
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

// AttemptBody is the body of a KindAttempt event: the record of one model
// call. The request itself is not stored; it is reconstructable from the log
// and the planner, and BodyHash is there to prove a reconstruction right.
type AttemptBody struct {
	Turn uint64 `json:"turn"`
	Step int    `json:"step"`
	// Purpose is empty for a turn's own calls, and names the job otherwise:
	// "checkpoint" or "summary".
	Purpose  string `json:"purpose,omitempty"`
	Route    string `json:"route"`
	BodyHash string `json:"body_hash"` // hex xxhash64 of the request bytes
	Bytes    int    `json:"bytes"`
	Effort   string `json:"effort,omitempty"` // as sent, after mapping
	Tries    int    `json:"tries"`
	// Estimated and Predicted are the planner's estimates: the size of the
	// prompt, and how much of it was expected to come from the provider's
	// cache. Input, Cached, Output and Reasoning are what the provider
	// reported, in its own tokens.
	Estimated int    `json:"estimated"`
	Predicted int    `json:"predicted"`
	Input     int    `json:"input"`
	Cached    int    `json:"cached"`
	Output    int    `json:"output"`
	Reasoning int    `json:"reasoning,omitempty"`
	Stop      string `json:"stop,omitempty"`
	Error     string `json:"error,omitempty"` // error class when the call failed
	MS        int64  `json:"ms"`
}

// CheckpointBody is the body of a KindCheckpoint event: a summary that
// stands in for the tail events with From <= seq <= To.
type CheckpointBody struct {
	From uint64 `json:"from"`
	To   uint64 `json:"to"`
	Text string `json:"text"`
}

// NoticeBody is the body of a KindNotice event. Source says what kind of
// runtime message it is, so the model and the log can tell it from anything
// a contact wrote.
type NoticeBody struct {
	Source string `json:"source"` // e.g. "subagent-settled"
	Text   string `json:"text"`
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
	// Reasoning accompanies an assistant message on routes that replay it.
	// The planner sets it, for a tool call as for a reply, and only when the
	// payload belongs to the route the prompt is being built for. It is the
	// one place a wire codec reads reasoning from.
	Reasoning *Reasoning `json:"reasoning,omitempty"`
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
	DeltaNotice      DeltaType = "notice"      // the runtime told the agent something; Text and Seq are set
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
	// ErrTooManyChildren means a contact already has as many sub-agents
	// running as it may.
	ErrTooManyChildren = errors.New("too many active sub-agents")
)
