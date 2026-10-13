package client

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/quinnovator/contactd/internal/core"
)

// maxFrameBytes bounds one frame from the server. A committed reply or a
// tool's output arrives whole in one frame.
const maxFrameBytes = 16 << 20

// Live is one WebSocket to a contact: messages, typing and interrupts go up,
// and the contact's deltas come down. It does not reconnect; Session does.
type Live struct {
	conn *websocket.Conn
}

// Frame is one thing the server said on a Live connection. Exactly one field
// is set.
type Frame struct {
	// Delta is an item of the contact's stream.
	Delta *core.Delta
	// Ack is the sequence number of a message sent on this connection, now
	// durable. Acks and Refused arrive in the order the messages were sent,
	// one of them for each.
	Ack uint64
	// Refused is why a message sent on this connection was not accepted.
	Refused string
}

// Live opens the WebSocket to a contact.
func (c *Client) Live(ctx context.Context, id core.ContactID) (*Live, error) {
	// The websocket package takes http and https URLs, and dials through the
	// client it is given, so a Unix socket works the same as a TCP address.
	conn, resp, err := websocket.Dial(ctx, c.base+"/v1/contacts/"+id.String()+"/live", &websocket.DialOptions{HTTPClient: c.http})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close() // the handshake response carries nothing we read
	}
	if err != nil {
		if resp != nil && resp.StatusCode >= 400 {
			msg := "contact not found"
			if resp.StatusCode != 404 {
				msg = strings.ToLower(resp.Status)
			}
			return nil, &Error{Status: resp.StatusCode, Message: msg}
		}
		return nil, c.unreachable(ctx, err)
	}
	conn.SetReadLimit(maxFrameBytes)
	return &Live{conn: conn}, nil
}

// Recv returns the next frame. It fails when the connection ends.
func (l *Live) Recv(ctx context.Context) (Frame, error) {
	for {
		_, data, err := l.conn.Read(ctx)
		if err != nil {
			return Frame{}, fmt.Errorf("read from server: %w", err)
		}
		// A delta, an ack and a refusal share one envelope. A refusal and a
		// failed turn both have type "error"; the refusal is the one that
		// carries an error field.
		var in struct {
			core.Delta
			Error string `json:"error"`
		}
		if err := json.Unmarshal(data, &in); err != nil {
			continue // not something this client knows how to read
		}
		switch {
		case in.Type == "ack":
			return Frame{Ack: in.Seq}, nil
		case in.Type == "error" && in.Error != "":
			return Frame{Refused: in.Error}, nil
		default:
			return Frame{Delta: &in.Delta}, nil
		}
	}
}

// Hello describes this visit, without starting a turn. Reconnects reuse it.
func (l *Live) Hello(ctx context.Context, v core.Visit) error {
	return l.write(ctx, struct {
		Type string `json:"type"`
		core.Visit
	}{Type: "hello", Visit: v})
}

// Say sends a message. The server answers with an Ack or a Refused frame.
func (l *Live) Say(ctx context.Context, text string) error {
	return l.write(ctx, map[string]string{"type": "message", "text": text})
}

// Typing tells the contact its human is still writing, which holds a
// pending reply back for a moment longer.
func (l *Live) Typing(ctx context.Context) error {
	return l.write(ctx, map[string]string{"type": "typing"})
}

// Interrupt stops the reply in flight.
func (l *Live) Interrupt(ctx context.Context) error {
	return l.write(ctx, map[string]string{"type": "interrupt"})
}

// Ping checks that the server is still there.
func (l *Live) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := l.conn.Ping(ctx); err != nil {
		return fmt.Errorf("ping server: %w", err)
	}
	return nil
}

// Close ends the connection.
func (l *Live) Close() {
	_ = l.conn.Close(websocket.StatusNormalClosure, "") // leaving either way
}

func (l *Live) write(ctx context.Context, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encode frame: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := l.conn.Write(ctx, websocket.MessageText, raw); err != nil {
		return fmt.Errorf("write to server: %w", err)
	}
	return nil
}
