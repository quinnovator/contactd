package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/coder/websocket"

	"github.com/quinnovator/contactd/internal/core"
)

// stream is the SSE endpoint: reply deltas, tool activity and typing state.
// It ends when the client leaves, when the subscriber falls too far behind,
// or at shutdown; clients reconnect and catch up through /events. The first
// delta, "ready", carries the sequence number the stream begins after: a
// client that reads the log up to there has missed nothing.
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	id, ok := s.contactID(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if _, err := s.store.Contact(ctx, id); err != nil {
		s.fail(w, r, statusOf(err), err)
		return
	}
	sub, err := s.contacts.Subscribe(ctx, id)
	if err != nil {
		s.fail(w, r, statusOf(err), err)
		return
	}
	defer s.contacts.Unsubscribe(id, sub)

	rc := http.NewResponseController(w)
	// A stream outlives any server-wide write timeout.
	if err := rc.SetWriteDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
		s.fail(w, r, http.StatusInternalServerError, err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no") // ask reverse proxies not to buffer
	w.WriteHeader(http.StatusOK)
	if _, err := fmt.Fprint(w, ": connected\n\n"); err != nil || rc.Flush() != nil {
		return
	}

	beat := time.NewTicker(heartbeat)
	defer beat.Stop()
	for {
		select {
		case d, open := <-sub.C:
			if !open {
				return
			}
			b, err := json.Marshal(d)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", d.Type, b); err != nil || rc.Flush() != nil {
				return
			}
		case <-beat.C:
			// Comments keep idle connections alive through proxies.
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil || rc.Flush() != nil {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// liveIn is a frame from the client on the WebSocket.
type liveIn struct {
	core.Visit
	Type string `json:"type"` // "hello", "message", "typing" or "interrupt"
	Text string `json:"text,omitempty"`
}

// liveAck confirms a message is durable; liveError rejects a frame.
type liveAck struct {
	Type string `json:"type"` // "ack"
	Seq  uint64 `json:"seq"`
}

type liveError struct {
	Type  string `json:"type"` // "error"
	Error string `json:"error"`
}

// live is the WebSocket endpoint: send messages, typing indicators and
// interrupts, receive the same deltas as the SSE stream.
func (s *Server) live(w http.ResponseWriter, r *http.Request) {
	id, ok := s.contactID(w, r)
	if !ok {
		return
	}
	if _, err := s.store.Contact(r.Context(), id); err != nil {
		s.fail(w, r, statusOf(err), err)
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return // Accept already wrote the error response
	}
	defer conn.CloseNow() //nolint:errcheck // best effort after a normal close below
	conn.SetReadLimit(maxBodyBytes)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	sub, err := s.contacts.Subscribe(ctx, id)
	if err != nil {
		_ = conn.Close(websocket.StatusTryAgainLater, "shutting down")
		return
	}
	defer s.contacts.Unsubscribe(id, sub)

	// The reader runs beside the writer loop and ends the connection's
	// context when the client goes away.
	readErr := make(chan error, 1)
	go func() { readErr <- s.liveRead(ctx, conn, id) }()

	for {
		select {
		case d, open := <-sub.C:
			if !open {
				_ = conn.Close(websocket.StatusTryAgainLater, "stream ended; reconnect")
				return
			}
			if err := writeFrame(ctx, conn, d); err != nil {
				return
			}
		case err := <-readErr:
			status := websocket.CloseStatus(err)
			if status == -1 {
				status = websocket.StatusInternalError
			}
			_ = conn.Close(status, "")
			return
		case <-ctx.Done():
			return
		}
	}
}

func (s *Server) liveRead(ctx context.Context, conn *websocket.Conn, id core.ContactID) error {
	var visit core.Visit
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return fmt.Errorf("read frame: %w", err)
		}
		var in liveIn
		if err := json.Unmarshal(data, &in); err != nil {
			if err := writeFrame(ctx, conn, liveError{Type: "error", Error: "frames must be JSON objects"}); err != nil {
				return err
			}
			continue
		}
		switch in.Type {
		case "hello":
			if invalid := in.Validate(); invalid != nil {
				err = writeFrame(ctx, conn, liveError{Type: "error", Error: invalid.Error()})
			} else {
				visit = in.Visit
			}
		case "typing":
			err = s.contacts.Typing(ctx, id)
		case "interrupt":
			err = s.contacts.Interrupt(ctx, id)
		case "message":
			if err = validateText(in.Text); err != nil {
				if err := writeFrame(ctx, conn, liveError{Type: "error", Error: err.Error()}); err != nil {
					return err
				}
				continue
			}
			var seq uint64
			seq, err = s.contacts.Message(ctx, id, in.Text, visit)
			switch {
			case err == nil:
				err = writeFrame(ctx, conn, liveAck{Type: "ack", Seq: seq})
			case errors.Is(err, core.ErrClosed), ctx.Err() != nil:
				// Shutting down, or the client left: the connection ends.
			default:
				// Every message gets an ack or an error frame, in the order
				// the messages were sent, and the connection stays up: a
				// refusal is about one message, not about the stream.
				reason := "the message could not be saved; send it again"
				if errors.Is(err, core.ErrBacklog) {
					reason = "too many unanswered messages; wait for a reply"
				} else {
					s.log.ErrorContext(ctx, "message refused", "contact_id", uint64(id), "error", err)
				}
				err = writeFrame(ctx, conn, liveError{Type: "error", Error: reason})
			}
		default:
			err = writeFrame(ctx, conn, liveError{Type: "error", Error: `type must be "hello", "message", "typing" or "interrupt"`})
		}
		if err != nil {
			return fmt.Errorf("handle %q frame: %w", in.Type, err)
		}
	}
}

func writeFrame(ctx context.Context, conn *websocket.Conn, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encode frame: %w", err)
	}
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := conn.Write(wctx, websocket.MessageText, b); err != nil {
		return fmt.Errorf("write frame: %w", err)
	}
	return nil
}
