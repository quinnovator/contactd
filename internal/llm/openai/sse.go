package openai

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
)

// sseEvent is one Server-Sent Event. Only the fields the API uses are kept.
type sseEvent struct {
	name string
	data []byte
}

// sseReader splits a response body into events. It reads whole lines of any
// length: one delta can carry a long run of text.
type sseReader struct {
	r *bufio.Reader
}

func newSSEReader(r io.Reader) *sseReader {
	return &sseReader{r: bufio.NewReader(r)}
}

// next returns the next event. It returns an error wrapping io.EOF when the
// body ends cleanly.
func (s *sseReader) next() (sseEvent, error) {
	var ev sseEvent
	seen, lines := false, 0
	for {
		line, err := s.r.ReadBytes('\n')
		line = bytes.TrimRight(line, "\r\n")
		switch {
		case len(line) == 0:
			// A blank line ends the event.
			if seen && err == nil {
				return ev, nil
			}
		case line[0] == ':':
			// Comment, used by servers as a keep-alive.
		default:
			field, value, _ := bytes.Cut(line, []byte(":"))
			value = bytes.TrimPrefix(value, []byte(" "))
			switch string(field) {
			case "event":
				ev.name = string(value)
				seen = true
			case "data":
				if lines > 0 {
					ev.data = append(ev.data, '\n')
				}
				ev.data = append(ev.data, value...)
				seen = true
				lines++
			}
		}
		if err != nil {
			// Some servers drop the blank line after their last event.
			if seen && errors.Is(err, io.EOF) {
				return ev, nil
			}
			return sseEvent{}, fmt.Errorf("openai: read stream: %w", err)
		}
	}
}
