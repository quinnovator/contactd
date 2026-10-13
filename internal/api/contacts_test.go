package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/quinnovator/contactd/internal/core"
)

func TestContactNamesAndListing(t *testing.T) {
	t.Parallel()
	s := start(t)
	status, made := s.do(http.MethodPost, "/v1/contacts", `{"name":"ada","agent":{"system":"You are Ada."},"profile":"likes tea"}`)
	if status != http.StatusCreated {
		t.Fatalf("create: %d %v", status, made)
	}
	ada := fmt.Sprint(made["id"])
	unnamed := s.contact()

	status, got := s.do(http.MethodGet, "/v1/contacts/"+ada, "")
	agent, _ := got["agent"].(map[string]any)
	if status != http.StatusOK || got["name"] != "ada" || got["profile"] != "likes tea" || agent["system"] != "You are Ada." {
		t.Errorf("get: %d %v", status, got)
	}
	// The route is the one that serves the contact, defaults filled in.
	if got["provider"] != "fake" || got["model"] != "fake-1" {
		t.Errorf("route = %v/%v, want the deployment's default", got["provider"], got["model"])
	}

	status, list := s.do(http.MethodGet, "/v1/contacts", "")
	rows, _ := list["contacts"].([]any)
	if status != http.StatusOK || len(rows) != 2 {
		t.Fatalf("list: %d %v", status, list)
	}
	for _, row := range rows {
		r, _ := row.(map[string]any)
		if _, has := r["agent"]; has {
			t.Errorf("a listing carries the agent config: %v", r)
		}
	}
	_, found := s.do(http.MethodGet, "/v1/contacts?name=ada", "")
	if rows, _ := found["contacts"].([]any); len(rows) != 1 || rows[0].(map[string]any)["id"] != made["id"] {
		t.Errorf("?name=ada = %v", found)
	}
	// A search that finds nothing is an empty list, not an error.
	status, found = s.do(http.MethodGet, "/v1/contacts?name=nobody", "")
	if rows, ok := found["contacts"].([]any); status != http.StatusOK || !ok || len(rows) != 0 {
		t.Errorf("?name=nobody = %d %v", status, found)
	}

	status, renamed := s.do(http.MethodPatch, "/v1/contacts/"+unnamed, `{"name":"bob"}`)
	if status != http.StatusOK || renamed["name"] != "bob" {
		t.Errorf("rename: %d %v", status, renamed)
	}

	for _, tc := range []struct {
		name, method, path, body string
		want                     int
	}{
		{"a name that is taken, on create", http.MethodPost, "/v1/contacts", `{"name":"ada","agent":{}}`, http.StatusConflict},
		{"a name that is taken, on rename", http.MethodPatch, "/v1/contacts/" + unnamed, `{"name":"ada"}`, http.StatusConflict},
		{"a number for a name", http.MethodPost, "/v1/contacts", `{"name":"42","agent":{}}`, http.StatusBadRequest},
		{"a padded name", http.MethodPatch, "/v1/contacts/" + unnamed, `{"name":" bob "}`, http.StatusBadRequest},
		{"a control character", http.MethodPatch, "/v1/contacts/" + unnamed, `{"name":"bo\u001bb"}`, http.StatusBadRequest},
		{"an overlong name", http.MethodPatch, "/v1/contacts/" + unnamed, `{"name":"` + strings.Repeat("n", 65) + `"}`, http.StatusBadRequest},
		{"renaming nobody", http.MethodPatch, "/v1/contacts/999", `{"name":"carol"}`, http.StatusNotFound},
		{"getting nobody", http.MethodGet, "/v1/contacts/999", "", http.StatusNotFound},
		{"a bad limit", http.MethodGet, "/v1/contacts?limit=0", "", http.StatusBadRequest},
		{"interrupting nobody", http.MethodPost, "/v1/contacts/999/interrupt", "", http.StatusNotFound},
		{"interrupting with nothing in flight", http.MethodPost, "/v1/contacts/" + ada + "/interrupt", "", http.StatusAccepted},
		{"taking a name away", http.MethodPatch, "/v1/contacts/" + unnamed, `{"name":""}`, http.StatusOK},
	} {
		if status, body := s.do(tc.method, tc.path, tc.body); status != tc.want {
			t.Errorf("%s: status %d, want %d (%v)", tc.name, status, tc.want, body)
		}
	}
}

// waitLogged waits until a contact's log holds the event with a sequence
// number. The server streams a reply as soon as its event is queued for the
// log, and does not wait for the write. So a test that reads the log after
// seeing a reply on the stream waits here first, or it may read the log one
// event short.
func waitLogged(t *testing.T, s *server, id string, seq int) {
	t.Helper()
	timeout := time.After(10 * time.Second)
	again := time.NewTicker(2 * time.Millisecond)
	defer again.Stop()
	for {
		status, body := s.do(http.MethodGet, "/v1/contacts/"+id+"/events?tail=1&limit=1", "")
		if status != http.StatusOK {
			t.Fatalf("events: %d %v", status, body)
		}
		if events, _ := body["events"].([]any); len(events) == 1 {
			last, _ := events[0].(map[string]any)
			if got, _ := last["seq"].(float64); got >= float64(seq) {
				return
			}
		}
		select {
		case <-again.C:
		case <-timeout:
			t.Fatalf("event %d is not in the log after 10s: %v", seq, body)
		}
	}
}

func TestEventsBackwardAndByKind(t *testing.T) {
	t.Parallel()
	s := start(t)
	id := s.contact()
	deltas := s.sse(id)
	for _, text := range []string{"one", "two"} {
		if status, body := s.do(http.MethodPost, "/v1/contacts/"+id+"/messages", `{"text":"`+text+`"}`); status != http.StatusAccepted {
			t.Fatalf("post: %d %v", status, body)
		}
		waitMessage(t, deltas)
	}
	// The stream shows a reply before the log holds it, so the second
	// reply's event, the sixth, is waited for before the log is read.
	waitLogged(t, s, id, 7)
	// The log now reads: message, model call, reply, twice over.
	page := func(query string) (string, map[string]any) {
		t.Helper()
		status, body := s.do(http.MethodGet, "/v1/contacts/"+id+"/events?"+query, "")
		if status != http.StatusOK {
			t.Fatalf("events?%s: %d %v", query, status, body)
		}
		events, _ := body["events"].([]any)
		seqs := make([]string, 0, len(events))
		for _, ev := range events {
			seqs = append(seqs, fmt.Sprint(ev.(map[string]any)["seq"]))
		}
		return strings.Join(seqs, " "), body
	}

	if got, body := page("tail=1&limit=2"); got != "6 7" || body["next_before"] != float64(6) || body["next_after"] != nil {
		t.Errorf("tail, limit 2 = %q %v", got, body)
	}
	if got, body := page("before=5&limit=2"); got != "3 4" || body["next_before"] != float64(3) {
		t.Errorf("before 5 = %q %v", got, body)
	}
	if got, body := page("before=3&limit=5"); got != "1 2" || body["next_before"] != nil {
		t.Errorf("before 3 = %q %v; want the first two and no cursor", got, body)
	}
	if got, _ := page("kinds=user_msg,agent_msg"); got != "2 4 5 7" {
		t.Errorf("messages only = %q", got)
	}
	if got, body := page("tail=1&limit=1&kinds=attempt"); got != "6" || body["next_before"] != float64(6) {
		t.Errorf("the last model call = %q %v", got, body)
	}

	for _, bad := range []string{"after=1&before=3", "before=3&tail=1", "before=0", "before=x", "tail=0", "kinds=user_msg,thought", "limit=-1"} {
		if status, body := s.do(http.MethodGet, "/v1/contacts/"+id+"/events?"+bad, ""); status != http.StatusBadRequest {
			t.Errorf("events?%s: status %d, want 400 (%v)", bad, status, body)
		}
	}
}

// A stream says where it begins before anything else, and shows a message
// to every stream, not only to whoever sent it.
func TestStreamOpensWithReadyAndEchoesMessages(t *testing.T) {
	t.Parallel()
	s := start(t)
	id := s.contact()
	deltas := s.sse(id)
	next := func() core.Delta {
		t.Helper()
		select {
		case d, ok := <-deltas:
			if !ok {
				t.Fatal("stream ended")
			}
			return d
		case <-time.After(10 * time.Second):
			t.Fatal("nothing on the stream")
			return core.Delta{}
		}
	}
	if d := next(); d != (core.Delta{Type: core.DeltaReady}) {
		t.Fatalf("first delta = %+v, want ready", d)
	}
	if status, _ := s.do(http.MethodPost, "/v1/contacts/"+id+"/messages", `{"text":"hello"}`); status != http.StatusAccepted {
		t.Fatal("post failed")
	}
	if d := next(); d != (core.Delta{Type: core.DeltaUser, Text: "hello", Seq: 2}) {
		t.Errorf("second delta = %+v, want the message as accepted", d)
	}
	waitMessage(t, deltas)

	// A stream opened later begins after everything so far.
	if d := <-s.sse(id); d.Type != core.DeltaReady || d.Seq != 4 {
		t.Errorf("a later stream opened with %+v, want ready at seq 4", d)
	}
}

func TestLiveWebSocketInterruptFrame(t *testing.T) {
	t.Parallel()
	s := start(t)
	id := s.contact()
	conn, _, err := websocket.Dial(t.Context(), "ws"+strings.TrimPrefix(s.url, "http")+"/v1/contacts/"+id+"/live", nil) //nolint:bodyclose // the library owns the handshake response
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow() //nolint:errcheck // test teardown
	for _, frame := range []string{`{"type":"interrupt"}`, `{"type":"message","text":"still here?"}`} {
		if err := conn.Write(t.Context(), websocket.MessageText, []byte(frame)); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var frame map[string]any
		if err := json.Unmarshal(data, &frame); err != nil {
			t.Fatal(err)
		}
		if _, refused := frame["error"]; refused {
			t.Fatalf("the interrupt frame was refused: %v", frame)
		}
		if frame["type"] == "ack" {
			return // an interrupt with nothing to stop changes nothing, and the connection carries on
		}
	}
}
