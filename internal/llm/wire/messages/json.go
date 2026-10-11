package messages

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// encode marshals v without HTML escaping, so "<now>" is sent as written
// rather than as <now>, and without the encoder's trailing newline.
func encode(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("messages: encode: %w", err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// compact strips insignificant whitespace from a JSON value and changes
// nothing else.
func compact(raw []byte) ([]byte, error) {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return nil, fmt.Errorf("messages: compact: %w", err)
	}
	return buf.Bytes(), nil
}

// object is a JSON object with its members in the order they arrived. The
// codec uses it wherever it must hand back something an upstream produced
// (a reasoning block, a usage record) with a member changed and the rest
// untouched, including members it has never heard of. A map would lose the
// order, and with it byte stability.
type object []member

type member struct {
	key string
	val json.RawMessage
}

// parseObject reads a JSON object. Member values are kept compact and
// otherwise as written.
func parseObject(raw []byte) (object, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, errors.New("messages: not a JSON object")
	}
	var o object
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("messages: decode object: %w", err)
		}
		key, ok := tok.(string)
		if !ok {
			return nil, errors.New("messages: object key is not a string")
		}
		var val json.RawMessage
		if err := dec.Decode(&val); err != nil {
			return nil, fmt.Errorf("messages: decode object: %w", err)
		}
		if val, err = compact(val); err != nil {
			return nil, err
		}
		o = append(o, member{key: key, val: val})
	}
	return o, nil
}

func (o object) has(key string) bool {
	for i := range o {
		if o[i].key == key {
			return true
		}
	}
	return false
}

// set replaces the value of key where it stands, or appends the member.
func (o *object) set(key string, val json.RawMessage) {
	for i := range *o {
		if (*o)[i].key == key {
			(*o)[i].val = val
			return
		}
	}
	*o = append(*o, member{key: key, val: val})
}

// setString is set with a string value.
func (o *object) setString(key, s string) error {
	val, err := encode(s)
	if err != nil {
		return err
	}
	o.set(key, val)
	return nil
}

func (o object) encode() ([]byte, error) {
	out := []byte{'{'}
	for i := range o {
		if i > 0 {
			out = append(out, ',')
		}
		key, err := encode(o[i].key)
		if err != nil {
			return nil, err
		}
		out = append(out, key...)
		out = append(out, ':')
		out = append(out, o[i].val...)
	}
	return append(out, '}'), nil
}
