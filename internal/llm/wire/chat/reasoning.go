package chat

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// payload is the codec's encoding of core.Reasoning.Data: everything one
// assistant turn's reasoning needs to be sent back exactly, whichever style
// the route's dialect uses. It is opaque outside this package.
type payload struct {
	// Text is the reasoning text as streamed in reasoning_content or
	// reasoning. It is what the reasoning_content style returns.
	Text string `json:"text,omitempty"`
	// Details is the reasoning_details array, in order, as the upstream
	// produced it. It is what the reasoning_details style returns.
	Details []json.RawMessage `json:"details,omitempty"`
}

func (p *payload) empty() bool { return p.Text == "" && len(p.Details) == 0 }

func decodePayload(data []byte) (*payload, error) {
	var p payload
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return &p, nil
}

// reasoning gathers a stream's reasoning.
type reasoning struct {
	text    strings.Builder
	details []detail
}

// add folds one delta in and returns the visible text it carried. Upstreams
// that stream reasoning_details repeat the same text in reasoning, so only
// one of the three sources counts per delta.
func (r *reasoning) add(d *chunkDelta) (string, error) {
	visible := string(d.Reasoning)
	if visible == "" {
		visible = string(d.ReasoningContent)
	}
	var fromDetails strings.Builder
	for _, raw := range d.ReasoningDetails {
		item, err := parseDetail(raw)
		if err != nil {
			return "", err
		}
		fromDetails.WriteString(item.visible())
		if n := len(r.details); n > 0 && r.details[n-1].continuedBy(&item) {
			r.details[n-1].merge(&item)
			continue
		}
		r.details = append(r.details, item)
	}
	if visible == "" {
		visible = fromDetails.String()
	}
	r.text.WriteString(visible)
	return visible, nil
}

// payload encodes what was gathered, or returns nil when the stream carried
// no reasoning.
func (r *reasoning) payload() ([]byte, error) {
	p := payload{Text: r.text.String()}
	for i := range r.details {
		raw, err := r.details[i].encode()
		if err != nil {
			return nil, err
		}
		p.Details = append(p.Details, raw)
	}
	if p.empty() {
		return nil, nil
	}
	return encode(p)
}

// Reasoning detail item types that carry text.
const (
	detailText    = "reasoning.text"
	detailSummary = "reasoning.summary"
)

// detail is one item of a reasoning_details array. A stream delivers an item
// in fragments: the text arrives a piece at a time, and a signature in a
// last fragment of its own. The item sent back must be the whole one, so
// fragments are joined; an item that arrived whole is kept byte for byte.
//
// OpenRouter documents this only as "the complete reasoning sequence is
// built by concatenating all chunks in order", and gives each item a type
// and an optional index. Taking fragments of one type at one index to be one
// item is this codec's reading of that, and a thing for the probe to check.
type detail struct {
	raw    json.RawMessage // as received; stale once merged is set
	fields []member        // the object's members, in the order received
	merged bool
}

type member struct {
	key   string
	value json.RawMessage
}

func parseDetail(raw json.RawMessage) (detail, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return detail{}, errors.New("a reasoning_details item is not an object")
	}
	d := detail{raw: raw}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return detail{}, fmt.Errorf("decode a reasoning_details item: %w", err)
		}
		key, _ := tok.(string)
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return detail{}, fmt.Errorf("decode a reasoning_details item: %w", err)
		}
		d.fields = append(d.fields, member{key: key, value: value})
	}
	if _, err := dec.Token(); err != nil && !errors.Is(err, io.EOF) {
		return detail{}, fmt.Errorf("decode a reasoning_details item: %w", err)
	}
	return d, nil
}

func (d *detail) get(key string) (json.RawMessage, bool) {
	for i := range d.fields {
		if d.fields[i].key == key {
			return d.fields[i].value, true
		}
	}
	return nil, false
}

func (d *detail) str(key string) string {
	raw, _ := d.get(key)
	s, _ := jsonString(raw)
	return s
}

// visible is the item's text a reader may be shown: reasoning text or a
// summary of it. Encrypted items have none.
func (d *detail) visible() string {
	switch d.str("type") {
	case detailText:
		return d.str("text")
	case detailSummary:
		return d.str("summary")
	default:
		return ""
	}
}

// continuedBy reports whether next is a further fragment of d: the same type
// at the same index. Text fragments that carry no index continue the text
// item before them, since nothing else could.
func (d *detail) continuedBy(next *detail) bool {
	typ := d.str("type")
	if typ == "" || typ != next.str("type") {
		return false
	}
	a, hasA := d.get("index")
	b, hasB := next.get("index")
	if hasA || hasB {
		return hasA && hasB && bytes.Equal(a, b)
	}
	return typ == detailText
}

// merge joins a later fragment onto d. The growing string fields are
// concatenated; any other field keeps its first value that is not null.
func (d *detail) merge(next *detail) {
	d.merged = true
	for _, f := range next.fields {
		i := 0
		for i < len(d.fields) && d.fields[i].key != f.key {
			i++
		}
		if i == len(d.fields) {
			d.fields = append(d.fields, f)
			continue
		}
		have := &d.fields[i]
		switch f.key {
		case "text", "summary", "data":
			a, okA := jsonString(have.value)
			b, okB := jsonString(f.value)
			if okA && okB {
				if joined, err := encode(a + b); err == nil {
					have.value = joined
				}
				continue
			}
		}
		if isNull(have.value) && !isNull(f.value) {
			have.value = f.value
		}
	}
}

func (d *detail) encode() (json.RawMessage, error) {
	if !d.merged {
		return d.raw, nil
	}
	var o object
	for _, f := range d.fields {
		o.raw(f.key, f.value)
	}
	return o.bytes()
}

func jsonString(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || raw[0] != '"' {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

func isNull(raw json.RawMessage) bool { return string(bytes.TrimSpace(raw)) == "null" }
