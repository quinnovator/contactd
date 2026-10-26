package requester

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/eventlog"
	"github.com/quinnovator/contactd/internal/obs"
)

type notices struct {
	mu     sync.Mutex
	bodies []core.NoticeBody
	fail   bool
}

func (n *notices) HelperNotice(_ context.Context, _ core.ContactID, b core.NoticeBody) (uint64, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.fail {
		return 0, errors.New("unavailable")
	}
	n.bodies = append(n.bodies, b)
	return uint64(len(n.bodies)), nil
}
func (*notices) Observe(context.Context, core.ContactID, core.HelperDelta) error { return nil }
func webhookHarness(t *testing.T) (*Requester, *notices, core.RemoteHelper) {
	t.Helper()
	s, err := eventlog.Open(t.Context(), filepath.Join(t.TempDir(), "test.db"), eventlog.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	n := &notices{}
	r := New(s, n, Config{}, obs.Discard())
	row := core.RemoteHelper{Parent: 1, PushToken: "test-token", PushSecret: "test-secret", State: string(a2a.TaskStateWorking)}
	row.Handle, err = s.InsertRemoteHelper(t.Context(), row)
	if err != nil {
		t.Fatal(err)
	}
	return r, n, row
}

func push(t *testing.T, r *Requester, row core.RemoteHelper, ev a2a.Event) int {
	t.Helper()
	raw, err := json.Marshal(a2a.StreamResponse{Event: ev})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/a2a/push", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+row.PushSecret)
	req.Header.Set("A2A-Notification-Token", row.PushToken)
	w := httptest.NewRecorder()
	r.Webhook().ServeHTTP(w, req)
	return w.Code
}

func TestWebhookStates(t *testing.T) {
	t.Parallel()
	for _, state := range []a2a.TaskState{a2a.TaskStateWorking, a2a.TaskStateInputRequired, a2a.TaskStateCompleted, a2a.TaskStateFailed, a2a.TaskStateRejected, a2a.TaskStateCanceled} {
		t.Run(string(state), func(t *testing.T) {
			t.Parallel()
			r, n, row := webhookHarness(t)
			ev := &a2a.TaskStatusUpdateEvent{TaskID: "task", Status: a2a.TaskStatus{State: state, Message: a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("details"))}}
			for range 2 {
				if got := push(t, r, row, ev); got != 204 {
					t.Fatalf("push %d", got)
				}
			}
			want := 1
			if state == a2a.TaskStateWorking {
				want = 0
			}
			if len(n.bodies) != want {
				t.Fatalf("notices %d, want %d", len(n.bodies), want)
			}
			saved, err := r.store.RemoteHelper(t.Context(), row.Handle)
			if err != nil || saved.TaskID != "task" {
				t.Fatalf("race adoption: %v %v", saved.TaskID, err)
			}
		})
	}
}

func TestWebhookAckRetryAndTaskMismatch(t *testing.T) {
	t.Parallel()
	r, n, row := webhookHarness(t)
	ev := &a2a.TaskArtifactUpdateEvent{TaskID: "task", Artifact: &a2a.Artifact{ID: "report-1", Parts: a2a.ContentParts{a2a.NewTextPart("report")}}}
	n.fail = true
	if got := push(t, r, row, ev); got != 503 {
		t.Fatalf("failed ack %d", got)
	}
	n.fail = false
	for range 2 {
		if got := push(t, r, row, ev); got != 204 {
			t.Fatalf("retry %d", got)
		}
	}
	if len(n.bodies) != 1 {
		t.Fatal("duplicate notice")
	}
	ev.TaskID = "another"
	if got := push(t, r, row, ev); got != 404 {
		t.Fatalf("mismatch %d", got)
	}
	if got := push(t, r, row, a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("wrong"))); got != 400 {
		t.Fatalf("message %d", got)
	}
	row, err := r.store.RemoteHelper(t.Context(), row.Handle)
	if err != nil {
		t.Fatal(err)
	}
	row.CancelRequested = true
	if err := r.store.UpdateRemoteHelper(t.Context(), row); err != nil {
		t.Fatal(err)
	}
	if got := push(t, r, row, &a2a.Task{ID: "task", Status: a2a.TaskStatus{State: a2a.TaskStateCanceled}}); got != 204 || len(n.bodies) != 1 {
		t.Fatal("requested cancellation notified")
	}
}
