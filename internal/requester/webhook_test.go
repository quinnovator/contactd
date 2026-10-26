package requester

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/eventlog"
	"github.com/quinnovator/contactd/internal/obs"
)

type notices struct {
	mu     sync.Mutex
	bodies []core.NoticeBody
	fail   bool
	hold   chan struct{} // a reply in flight: acks wait for it to close
}

func (n *notices) HelperNotice(ctx context.Context, _ core.ContactID, b core.NoticeBody) (uint64, error) {
	if n.hold != nil {
		select {
		case <-n.hold:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.fail {
		return 0, errors.New("unavailable")
	}
	n.bodies = append(n.bodies, b)
	return uint64(len(n.bodies)), nil
}

func (n *notices) count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.bodies)
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
			r.drain(t.Context(), row.Parent)
			want := 1
			if state == a2a.TaskStateWorking {
				want = 0
			}
			if n.count() != want {
				t.Fatalf("notices %d, want %d", len(n.bodies), want)
			}
			saved, err := r.store.RemoteHelper(t.Context(), row.Handle)
			if err != nil || saved.TaskID != "task" {
				t.Fatalf("race adoption: %v %v", saved.TaskID, err)
			}
		})
	}
}

func TestWebhookInboxRetryAndTaskMismatch(t *testing.T) {
	t.Parallel()
	r, n, row := webhookHarness(t)
	ev := &a2a.TaskArtifactUpdateEvent{TaskID: "task", Artifact: &a2a.Artifact{ID: "report-1", Parts: a2a.ContentParts{a2a.NewTextPart("report")}}}
	n.fail = true
	for range 2 {
		if got := push(t, r, row, ev); got != 204 {
			t.Fatalf("accepted push %d", got)
		}
	}
	r.drain(t.Context(), row.Parent)
	if n.count() != 0 {
		t.Fatal("failed delivery recorded")
	}
	n.fail = false
	r.drain(t.Context(), row.Parent)
	r.drain(t.Context(), row.Parent)
	if n.count() != 1 {
		t.Fatalf("notices %d, want one after the retry", n.count())
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
	if got := push(t, r, row, &a2a.Task{ID: "task", Status: a2a.TaskStatus{State: a2a.TaskStateCanceled}}); got != 204 {
		t.Fatalf("cancel %d", got)
	}
	r.drain(t.Context(), row.Parent)
	if n.count() != 1 {
		t.Fatal("requested cancellation notified")
	}
}

// A report and the final status arrive while the contact is mid-reply. Both
// pushes are answered at once, a retried status adds nothing, and both
// notices land, in order, when the reply ends.
func TestWebhookDoesNotWaitForReply(t *testing.T) {
	t.Parallel()
	r, n, row := webhookHarness(t)
	n.hold = make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	report := &a2a.TaskArtifactUpdateEvent{TaskID: "task", Artifact: &a2a.Artifact{ID: "report-9", Parts: a2a.ContentParts{a2a.NewTextPart("final report")}}}
	finished := &a2a.TaskStatusUpdateEvent{TaskID: "task", Status: a2a.TaskStatus{State: a2a.TaskStateCompleted}}
	start := time.Now()
	for _, ev := range []a2a.Event{report, finished, finished} {
		if got := push(t, r, row, ev); got != 204 {
			t.Fatalf("push %d", got)
		}
	}
	if waited := time.Since(start); waited > 2*time.Second {
		t.Fatalf("webhook waited %s for the reply", waited)
	}
	close(n.hold)
	deadline := time.Now().Add(5 * time.Second)
	for n.count() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.bodies) != 2 || !strings.Contains(n.bodies[0].Text, "final report") || !strings.Contains(n.bodies[1].Text, "has stopped") {
		t.Fatalf("notices: %+v", n.bodies)
	}
}

// A start the restart cut off is failed and frees its slot. If its task
// turns up after all, its pushes are taken without a notice.
func TestStrandedStartIsAbandoned(t *testing.T) {
	t.Parallel()
	r, n, _ := webhookHarness(t)
	row := core.RemoteHelper{Parent: 2, PushToken: "stranded-token", PushSecret: "stranded-secret", State: string(a2a.TaskStateSubmitted)}
	var err error
	row.Handle, err = r.store.InsertRemoteHelper(t.Context(), row)
	if err != nil {
		t.Fatal(err)
	}
	r.recoverStarts(t.Context())
	saved, err := r.store.RemoteHelper(t.Context(), row.Handle)
	if err != nil || saved.State != string(a2a.TaskStateFailed) || !saved.CancelRequested {
		t.Fatalf("stranded start: %+v %v", saved, err)
	}
	if active, err := r.store.CountActiveRemoteHelpers(t.Context(), 2); err != nil || active != 0 {
		t.Fatalf("slot held: %d %v", active, err)
	}
	report := &a2a.TaskArtifactUpdateEvent{TaskID: "late", Artifact: &a2a.Artifact{ID: "report-1", Parts: a2a.ContentParts{a2a.NewTextPart("baseline")}}}
	stopped := &a2a.TaskStatusUpdateEvent{TaskID: "late", Status: a2a.TaskStatus{State: a2a.TaskStateCompleted}}
	for _, ev := range []a2a.Event{report, stopped} {
		if got := push(t, r, row, ev); got != 204 {
			t.Fatalf("push %d", got)
		}
	}
	r.drain(t.Context(), 2)
	if n.count() != 0 {
		t.Fatalf("abandoned helper notified: %+v", n.bodies)
	}
}

func TestAnswerReopensQuestionNotices(t *testing.T) {
	t.Parallel()
	r, n, row := webhookHarness(t)
	question := &a2a.TaskStatusUpdateEvent{TaskID: "task", Status: a2a.TaskStatus{State: a2a.TaskStateInputRequired}}
	if got := push(t, r, row, question); got != 204 {
		t.Fatalf("push %d", got)
	}
	// The answer's reply shows WORKING; no WORKING push arrives before the
	// next question.
	if _, err := r.saveTask(t.Context(), row.Handle, &a2a.Task{ID: "task", Status: a2a.TaskStatus{State: a2a.TaskStateWorking}}); err != nil {
		t.Fatal(err)
	}
	if got := push(t, r, row, question); got != 204 {
		t.Fatalf("push %d", got)
	}
	r.drain(t.Context(), row.Parent)
	if n.count() != 2 {
		t.Fatalf("questions notified %d times, want 2", n.count())
	}
}

func TestLocalAgentIgnoresTrailingSlash(t *testing.T) {
	t.Parallel()
	r := New(nil, nil, Config{AgentURL: "http://127.0.0.1:8080/", BaseURL: "http://127.0.0.1:8080"}, obs.Discard())
	if !r.local() {
		t.Fatal("trailing slash made the local agent remote")
	}
}

func TestHandleLocksAreReleased(t *testing.T) {
	t.Parallel()
	r := New(nil, nil, Config{}, obs.Discard())
	for h := range uint64(100) {
		r.lock(h)()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.locks) != 0 {
		t.Fatalf("%d handle locks retained", len(r.locks))
	}
}

func TestFailedStopClearsCancelIntent(t *testing.T) {
	t.Parallel()
	r, _, row := webhookHarness(t)
	r.cfg.AgentURL = "http://127.0.0.1:1" // nothing listens
	row.TaskID = "task"
	if err := r.store.UpdateRemoteHelper(t.Context(), row); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Stop(t.Context(), row.Parent, row.Handle); err == nil {
		t.Fatal("stop reached nothing and succeeded")
	}
	saved, err := r.store.RemoteHelper(t.Context(), row.Handle)
	if err != nil || saved.CancelRequested {
		t.Fatalf("cancel intent kept after a failed stop: %+v %v", saved, err)
	}
}
