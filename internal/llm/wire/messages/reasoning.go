package messages

import (
	"encoding/json"

	"github.com/quinnovator/contactd/internal/core"
)

// A reasoning payload (core.Reasoning.Data) on this wire is a small JSON
// object:
//
//	{"v":1,"turn":[
//	  {"block":{"type":"thinking","thinking":"","signature":"..."}},
//	  {"text":21},
//	  {"block":{"type":"redacted_thinking","data":"..."}},
//	  {"tool":true}
//	]}
//
// "turn" is the assistant turn's content, in the order the upstream sent
// it. A "block" is a thinking or redacted_thinking block exactly as it
// arrived, signature included. A "text" stands for a text block and holds
// only its length in bytes; a "tool" stands for a tool_use block and holds
// nothing.
//
// The order matters because a signature covers what came before its block:
// a turn sent back with its text moved behind a later thinking block is not
// the turn that was signed. The text and the calls themselves are left out
// on purpose. The log is the record of what was said and run, so they are
// always rendered from the message; the payload only says where they sat.

// payloadVersion is the "v" this codec writes and the only one it reads.
const payloadVersion = 1

type payload struct {
	V    int    `json:"v"`
	Turn []slot `json:"turn"`
}

// slot is one content block of a recorded turn. Exactly one member is set.
type slot struct {
	// Block is a reasoning block as it arrived.
	Block json.RawMessage `json:"block,omitempty"`
	// Text is the length in bytes of a text block.
	Text int `json:"text,omitempty"`
	// Tool marks where a tool_use block sat.
	Tool bool `json:"tool,omitempty"`
}

// encodePayload writes a turn as a payload.
func encodePayload(turn []slot) ([]byte, error) {
	return encode(payload{V: payloadVersion, Turn: turn})
}

// recordedTurn unpacks a reasoning payload. A payload tagged for another
// route yields nothing: its signatures mean nothing there, and its text is
// not that route's to read. So does anything that is not a payload of this
// codec's own version, whole and well formed; half a turn's reasoning is
// worse than none.
func recordedTurn(r *core.Reasoning, route string) []slot {
	if r == nil || r.Route != route || len(r.Data) == 0 {
		return nil
	}
	var p payload
	if err := json.Unmarshal(r.Data, &p); err != nil || p.V != payloadVersion || len(p.Turn) == 0 {
		return nil
	}
	for i := range p.Turn {
		s := &p.Turn[i]
		kinds := 0
		if len(s.Block) > 0 {
			kinds++
			if blockType(s.Block) == "" {
				return nil
			}
		}
		if s.Text != 0 {
			kinds++
		}
		if s.Tool {
			kinds++
		}
		if kinds != 1 || s.Text < 0 {
			return nil
		}
	}
	return p.Turn
}

// splitText cuts a message's text into the text blocks a recorded turn
// had. It reports false when the text is not the length the turn recorded,
// which means the text is not what was streamed and there is no telling
// where its blocks ended.
func splitText(turn []slot, text string) ([]string, bool) {
	total := 0
	for i := range turn {
		total += turn[i].Text
	}
	if total != len(text) {
		return nil, false
	}
	var pieces []string
	for i := range turn {
		if n := turn[i].Text; n > 0 {
			pieces = append(pieces, text[:n])
			text = text[n:]
		}
	}
	return pieces, true
}
