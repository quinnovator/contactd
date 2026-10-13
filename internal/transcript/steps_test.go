package transcript_test

import (
	"fmt"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/transcript"
)

// streamNow is the time a test gives every delta. It is unlike any time the
// event helper stamps, so an item shows which account of it is held.
const streamNow = 7

// step is one thing that happens to a transcript.
type step func(tr *transcript.Transcript)

// page is events read from the log in one go.
func page(events ...core.Event) step {
	return func(tr *transcript.Transcript) { tr.Events(events) }
}

// delta is one item of the live stream.
func delta(d core.Delta) step {
	return func(tr *transcript.Transcript) { tr.Delta(d, streamNow) }
}

// sent is a message handed to the server.
func sent(text string) step {
	return func(tr *transcript.Transcript) { tr.Sent(text) }
}

// acked is the server saying a message sent from here is durable.
func acked(text string, seq uint64) step {
	return func(tr *transcript.Transcript) { tr.Acked(text, seq, streamNow) }
}

func feed(tr *transcript.Transcript, steps []step) {
	for _, s := range steps {
		s(tr)
	}
}

func kindName(k transcript.Kind) string {
	switch k {
	case transcript.User:
		return "user"
	case transcript.Agent:
		return "agent"
	case transcript.ToolCall:
		return "call"
	case transcript.ToolResult:
		return "result"
	case transcript.Notice:
		return "notice"
	case transcript.Checkpoint:
		return "checkpoint"
	case transcript.Failure:
		return "failure"
	default:
		return fmt.Sprintf("kind %d", k)
	}
}
