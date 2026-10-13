package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/coder/websocket"
)

func TestHelloIsSilentAndReconnectKeepsVisit(t *testing.T) {
	t.Parallel()
	s := start(t)
	id := s.contact()
	connect := func() *websocket.Conn {
		t.Helper()
		conn, _, err := websocket.Dial(t.Context(), "ws"+strings.TrimPrefix(s.url, "http")+"/v1/contacts/"+id+"/live", nil) //nolint:bodyclose // the websocket library owns the handshake response
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.CloseNow() })
		if err := conn.Write(t.Context(), websocket.MessageText, []byte(`{"type":"hello","session_id":"visit-1","time_zone":"America/Chicago","presentation":"avatar"}`)); err != nil {
			t.Fatal(err)
		}
		return conn
	}
	conn := connect()
	// Read the ready frame, then a deliberately invalid frame's error as a barrier:
	// the server has processed hello before the log is inspected.
	if err := conn.Write(t.Context(), websocket.MessageText, []byte(`{"type":"barrier"}`)); err != nil {
		t.Fatal(err)
	}
	for {
		_, raw, err := conn.Read(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		var frame struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(raw, &frame); err != nil {
			t.Fatal(err)
		}
		if frame.Error != "" {
			break
		}
	}
	_, page := s.do(http.MethodGet, "/v1/contacts/"+id+"/events", "")
	if events, _ := page["events"].([]any); len(events) != 0 {
		t.Fatalf("hello logged events: %v", events)
	}
	send := func(conn *websocket.Conn) {
		t.Helper()
		if err := conn.Write(t.Context(), websocket.MessageText, []byte(`{"type":"message","text":"hello"}`)); err != nil {
			t.Fatal(err)
		}
		for {
			_, raw, err := conn.Read(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			var frame struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(raw, &frame); err != nil {
				t.Fatal(err)
			}
			if frame.Type == "ack" {
				return
			}
		}
	}
	send(conn)
	_ = conn.CloseNow()
	send(connect())
	_, page = s.do(http.MethodGet, "/v1/contacts/"+id+"/events?kinds=notice", "")
	events, _ := page["events"].([]any)
	if len(events) != 1 {
		t.Fatalf("reconnect notices = %v", events)
	}
	body := events[0].(map[string]any)["body"].(map[string]any)
	if body["session_id"] != "visit-1" || !strings.Contains(body["text"].(string), "America/Chicago") {
		t.Fatalf("notice = %v", body)
	}
}

func TestPostRejectsInvalidVisit(t *testing.T) {
	t.Parallel()
	s := start(t)
	id := s.contact()
	for _, body := range []string{`{"text":"hello","time_zone":"Local"}`, `{"text":"hello","time_zone":"bad/zone"}`, `{"text":"hello","presentation":"unknown"}`} {
		if status, _ := s.do(http.MethodPost, "/v1/contacts/"+id+"/messages", body); status != http.StatusBadRequest {
			t.Errorf("status = %d for %s", status, body)
		}
	}
}
