package mods

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"unicode/utf8"

	"github.com/quinnovator/contactd/internal/tui/mods/wire"
	"github.com/quinnovator/contactd/internal/tui/safe"
)

// The operations of ABI 1.
const (
	OpHello   = "hello"
	OpSegment = "segment"
	OpCommand = "command"
	OpRender  = "render"
)

// glyphsASCII is the wire.Term.Glyphs value that asks for plain ASCII.
const glyphsASCII = "ascii"

// declares reports whether the manifest declares what a call asks for: the
// segment, the command, or a renderer of the kind.
func (m Manifest) declares(op, name string) error {
	var ok bool
	switch op {
	case OpSegment:
		ok = slices.ContainsFunc(m.Segments, func(s SegmentDecl) bool { return s.Name == name })
	case OpCommand:
		ok = slices.ContainsFunc(m.Commands, func(c CommandDecl) bool { return c.Name == name })
	case OpRender:
		ok = slices.Contains(m.Renderers, RendererDecl{Kind: name})
	default:
		return fmt.Errorf("mod %s: unknown op %q", m.Name, safe.Line(op, 40, false))
	}
	if !ok {
		return fmt.Errorf("mod %s declares no %s %q", m.Name, op, safe.Line(name, 80, false))
	}
	return nil
}

// buildRequest makes the request for one call: everything of in that the
// manifest's grants allow and the op carries, and nothing else.
func buildRequest(m Manifest, op, name string, in Input) wire.Request {
	req := wire.Request{
		ABI:   wire.ABI,
		Op:    op,
		Name:  name,
		Width: in.Width,
		Term:  in.Term,
		Data:  filterData(m, op, in.Data),
	}
	if op == OpCommand {
		req.Args = in.Args
	}
	if op == OpRender && m.Has(GrantMessages) {
		req.Item = in.Item
	}
	return req
}

// encodeRequest writes a request as the JSON a mod is sent.
//
// It leaves <, > and & as they are, where json.Marshal would write each as
// six characters. Nothing here goes into a web page, and a mod reads a
// string with no escape in it several times faster than one with: the error
// page of a failed model call is mostly those three characters, and after an
// outage every record a mod is sent can hold one.
func encodeRequest(req wire.Request) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(req); err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	// Encode ends what it writes with a newline.
	return bytes.TrimSuffix(buf.Bytes(), []byte{'\n'}), nil
}

// filterData keeps the fields a mod holds the grant for. Messages go to a
// command only. Lists are cut to what the ABI promises: the newest ones, and
// no more text in a model-call record than MaxCallText.
func filterData(m Manifest, op string, d wire.Data) wire.Data {
	var out wire.Data
	if op == OpHello {
		return out
	}
	for _, g := range m.Read {
		switch g {
		case GrantContact:
			out.Contact = d.Contact
		case GrantRoute:
			out.Route = d.Route
		case GrantState:
			out.State = d.State
		case GrantModelCalls:
			out.ModelCalls = capCalls(newest(d.ModelCalls, MaxModelCalls))
		case GrantMessages:
			if op == OpCommand {
				out.Messages = capMessages(d.Messages)
			}
		case GrantWorkspace:
			out.Workspace = d.Workspace
		case GrantClock:
			out.Now = d.Now
		}
	}
	return out
}

// newest returns the last n of a list that is oldest first.
func newest[T any](list []T, n int) []T {
	if len(list) > n {
		return list[len(list)-n:]
	}
	return list
}

// capCalls cuts the route and the error of each record to MaxCallText bytes.
// A provider's error can be a whole page, and fifty of those would not fit in
// a request. The caller's records are left as they are.
func capCalls(list []wire.ModelCall) []wire.ModelCall {
	long := func(c wire.ModelCall) bool { return len(c.Route) > MaxCallText || len(c.Error) > MaxCallText }
	if !slices.ContainsFunc(list, long) {
		return list
	}
	out := slices.Clone(list)
	for i := range out {
		out[i].Route = cutBytes(out[i].Route, MaxCallText)
		out[i].Error = cutBytes(out[i].Error, MaxCallText)
	}
	return out
}

// capMessages keeps the newest messages that fit: at most MaxMessages, and
// MaxMessagesBytes of text in all. The newest message is cut when it alone is
// too long, so a command always sees the last thing said.
func capMessages(list []wire.Message) []wire.Message {
	list = newest(list, MaxMessages)
	room := MaxMessagesBytes
	first := len(list)
	for first > 0 {
		n := len(list[first-1].Text)
		if n > room {
			break
		}
		room -= n
		first--
	}
	if first < len(list) || len(list) == 0 {
		return list[first:]
	}
	last := list[len(list)-1]
	last.Text = cutBytes(last.Text, MaxMessagesBytes)
	return []wire.Message{last}
}

// cutBytes cuts s to at most n bytes without splitting a character.
func cutBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// tones are the tone names a reply may use.
func tones() []string {
	return []string{"", "dim", "accent", "tool", "ok", "warn", "bad", "mod"}
}

// cleanTone returns a tone the client knows, or the plain tone.
func cleanTone(s string) string {
	if slices.Contains(tones(), s) {
		return s
	}
	return ""
}

// cleanReply makes a mod's reply safe to draw: every string is one line with
// no escape sequence in it, at most MaxSpanRunes runes, and ASCII when the
// terminal draws ASCII; a tone the client does not know is plain; lines
// after the first MaxLines are dropped.
func cleanReply(r wire.Reply, ascii bool) wire.Reply {
	line := func(s string) string { return safe.Line(s, MaxSpanRunes, ascii) }
	out := wire.Reply{
		ABI:    r.ABI,
		Text:   line(r.Text),
		Short:  line(r.Short),
		Tone:   cleanTone(r.Tone),
		Insert: line(r.Insert),
		Note:   line(r.Note),
		Pass:   r.Pass,
		Error:  line(r.Error),
	}
	lines := r.Lines
	if len(lines) > MaxLines {
		lines = lines[:MaxLines]
	}
	if len(lines) > 0 {
		out.Lines = make([]wire.Line, len(lines))
	}
	for i, l := range lines {
		spans := make(wire.Line, len(l))
		for j, s := range l {
			spans[j] = wire.Span{T: line(s.T), Tone: cleanTone(s.Tone), Bold: s.Bold}
		}
		out.Lines[i] = spans
	}
	return out
}
