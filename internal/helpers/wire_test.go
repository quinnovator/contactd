package helpers

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/quinnovator/contactd/internal/obs"
)

func TestWireFixtures(t *testing.T) {
	t.Parallel()
	s, _ := testService(t)
	handler, err := s.HTTPHandler()
	if err != nil {
		t.Fatal(err)
	}
	taskID := ""
	var contextID string
	for _, method := range []string{"SendMessage", "GetTask", "CancelTask", "GetTask-missing"} {
		name := method
		var params any
		switch method {
		case "SendMessage":
			r := sendRequest("watch page")
			r.Message.ID = "fixture-message"
			r.Config.PushConfig = nil
			params = r
		case "GetTask", "CancelTask":
			params = map[string]any{"id": taskID}
		case "GetTask-missing":
			method = "GetTask"
			params = map[string]any{"id": "missing"}
		}
		req := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params}
		raw, err := json.Marshal(req)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/a2a/helpers", strings.NewReader(string(raw)))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("A2A-Version", "1.0")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		var response map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if name == "SendMessage" {
			result := response["result"].(map[string]any)
			task := result["task"].(map[string]any)
			taskID = task["id"].(string)
			contextID = task["contextId"].(string)
		}
		value := map[string]any{"request": req, "response": response}
		goldenJSON(t, "wire/"+name+".golden.json", normalizeWire(t, value, taskID, contextID))
	}
	ts := time.Date(2026, 10, 26, 14, 0, 0, 0, time.UTC)
	task := &a2a.Task{ID: "task", ContextID: "context", Status: a2a.TaskStatus{State: a2a.TaskStateWorking, Timestamp: &ts}}
	message := a2a.NewMessageForTask(a2a.MessageRoleAgent, task, a2a.NewTextPart("report"))
	message.ID = "message"
	for _, tc := range []struct {
		name  string
		event a2a.Event
	}{{"task", task}, {"message", message}, {"status", &a2a.TaskStatusUpdateEvent{TaskID: task.ID, ContextID: task.ContextID, Status: task.Status}}, {"artifact", &a2a.TaskArtifactUpdateEvent{TaskID: task.ID, ContextID: task.ContextID, LastChunk: true, Artifact: &a2a.Artifact{ID: "report-7", Name: "report", Parts: a2a.ContentParts{a2a.NewTextPart("baseline")}}}}} {
		goldenJSON(t, "wire/push-"+tc.name+".golden.json", a2a.StreamResponse{Event: tc.event})
	}
}

func normalizeWire(t *testing.T, value any, taskID, contextID string) any {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ReplaceAll(string(raw), taskID, "<task>")
	text = strings.ReplaceAll(text, contextID, "<context>")
	var v any
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		t.Fatal(err)
	}
	var walk func(any)
	walk = func(v any) {
		switch n := v.(type) {
		case map[string]any:
			for k, item := range n {
				switch k {
				case "timestamp", "until", "nextCheck":
					n[k] = "<time>"
				case "messageId":
					n[k] = "<message>"
				default:
					walk(item)
				}
			}
		case []any:
			for _, item := range n {
				walk(item)
			}
		}
	}
	walk(v)
	return v
}

func TestCapabilityErrorViaSDK(t *testing.T) {
	t.Parallel()
	s, _ := testService(t)
	card := Card(s.cfg)
	card.Capabilities.PushNotifications = false
	handler, err := NewHandler(s, card, obs.Discard())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(a2asrv.NewJSONRPCHandler(handler))
	defer server.Close()
	card.SupportedInterfaces[0].URL = server.URL
	client, err := a2aclient.NewFromCard(t.Context(), card)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.CreateTaskPushConfig(t.Context(), &a2a.PushConfig{TaskID: "missing", URL: server.URL}); !errors.Is(err, a2a.ErrPushNotificationNotSupported) {
		t.Fatalf("capability: %v", err)
	}
}
