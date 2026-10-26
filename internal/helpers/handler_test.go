package helpers

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
)

func TestSDKTransports(t *testing.T) {
	t.Parallel()
	for _, binding := range []a2a.TransportProtocol{a2a.TransportProtocolJSONRPC, a2a.TransportProtocolHTTPJSON} {
		t.Run(string(binding), func(t *testing.T) {
			t.Parallel()
			s, _ := testService(t)
			handler, err := s.HTTPHandler()
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(handler)
			defer server.Close()
			cfg := s.cfg
			cfg.BaseURL = server.URL
			card := Card(cfg)
			if binding == a2a.TransportProtocolHTTPJSON {
				card.SupportedInterfaces = card.SupportedInterfaces[1:]
			}
			client, err := a2aclient.NewFromCard(t.Context(), card)
			if err != nil {
				t.Fatal(err)
			}
			result, err := client.SendMessage(t.Context(), sendRequest("watch page"))
			if err != nil {
				t.Fatal(err)
			}
			task, ok := result.(*a2a.Task)
			if !ok {
				t.Fatalf("result %T", result)
			}
			if _, err := client.GetTask(t.Context(), &a2a.GetTaskRequest{ID: task.ID}); err != nil {
				t.Fatal(err)
			}
			if rows, err := client.ListTasks(t.Context(), &a2a.ListTasksRequest{}); err != nil || len(rows.Tasks) != 1 {
				t.Fatalf("list: %+v %v", rows, err)
			}
			// A config's target is explicitly allowed; its CRUD must use the SDK on both bindings.
			s.push.allow = append(s.push.allow, strings.TrimPrefix(server.URL, "http://"))
			push, err := client.CreateTaskPushConfig(t.Context(), &a2a.PushConfig{TaskID: task.ID, ID: "p", URL: server.URL + "/push"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.GetTaskPushConfig(t.Context(), &a2a.GetTaskPushConfigRequest{TaskID: task.ID, ID: push.ID}); err != nil {
				t.Fatal(err)
			}
			if got, err := client.ListTaskPushConfigs(t.Context(), &a2a.ListTaskPushConfigRequest{TaskID: task.ID}); err != nil || len(got) != 1 {
				t.Fatalf("configs: %+v %v", got, err)
			}
			for range 2 {
				if err := client.DeleteTaskPushConfig(t.Context(), &a2a.DeleteTaskPushConfigRequest{TaskID: task.ID, ID: push.ID}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := client.GetTask(t.Context(), &a2a.GetTaskRequest{ID: "missing"}); !errors.Is(err, a2a.ErrTaskNotFound) {
				t.Fatalf("missing: %v", err)
			}
			r := sendRequest("bad")
			r.Message.Role = a2a.MessageRoleAgent
			if _, err := client.SendMessage(t.Context(), r); !errors.Is(err, a2a.ErrInvalidParams) {
				t.Fatalf("role: %v", err)
			}
			r = sendRequest("bad")
			r.Message.Parts = a2a.ContentParts{a2a.NewFileURLPart("https://example.com/a", "image/png")}
			if _, err := client.SendMessage(t.Context(), r); !errors.Is(err, a2a.ErrUnsupportedContentType) {
				t.Fatalf("content: %v", err)
			}
			if _, err := client.GetExtendedAgentCard(t.Context(), &a2a.GetExtendedAgentCardRequest{}); !errors.Is(err, a2a.ErrExtendedCardNotConfigured) {
				t.Fatalf("extended card: %v", err)
			}
			if _, err := client.CancelTask(t.Context(), &a2a.CancelTaskRequest{ID: task.ID}); err != nil {
				t.Fatal(err)
			}
			if _, err := client.CancelTask(t.Context(), &a2a.CancelTaskRequest{ID: task.ID}); !errors.Is(err, a2a.ErrTaskNotCancelable) {
				t.Fatalf("cancel twice: %v", err)
			}
			r = sendRequest("again")
			r.Message.TaskID = task.ID
			if _, err := client.SendMessage(t.Context(), r); !errors.Is(err, a2a.ErrUnsupportedOperation) {
				t.Fatalf("terminal send: %v", err)
			}
			for _, err := range client.SubscribeToTask(t.Context(), &a2a.SubscribeToTaskRequest{ID: task.ID}) {
				if !errors.Is(err, a2a.ErrUnsupportedOperation) {
					t.Fatalf("terminal subscription: %v", err)
				}
			}
		})
	}
}

func TestVersionAndCardHTTP(t *testing.T) {
	t.Parallel()
	s, _ := testService(t)
	handler, err := s.HTTPHandler()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/a2a/helpers", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"GetTask","params":{"id":"missing"}}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), `-32009`) {
		t.Fatalf("missing version: %s", w.Body.String())
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/.well-known/agent-card.json", nil))
	if w.Header().Get("ETag") == "" || w.Header().Get("Cache-Control") == "" {
		t.Fatal("card caching absent")
	}
	req = httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/.well-known/agent-card.json", nil)
	req.Header.Set("If-None-Match", w.Header().Get("ETag"))
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != 304 {
		t.Fatal(w.Code)
	}
	// Wire round trip pins enum, parts and streaming wrapper spellings.
	task := startTask(t, s)
	raw, err := json.Marshal(a2a.StreamResponse{Event: task})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(`"kind"`)) || !bytes.Contains(raw, []byte(`"ROLE_USER"`)) || !bytes.Contains(raw, []byte(`"TASK_STATE_WORKING"`)) {
		t.Fatalf("wire: %s", raw)
	}
}
