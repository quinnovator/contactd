package helpers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/quinnovator/contactd/internal/actor"
	"github.com/quinnovator/contactd/internal/core"
)

// hangup is a caller that goes away the moment its message is durable.
type hangup struct {
	*testContacts
	cancel context.CancelFunc
}

func (h hangup) MessageFrom(ctx context.Context, id core.ContactID, text, raw, messageID string) (uint64, error) {
	seq, err := h.testContacts.MessageFrom(ctx, id, text, raw, messageID)
	h.cancel()
	return seq, err
}

func ask(t *testing.T, s *Service, contact core.ContactID) {
	t.Helper()
	if err := s.outcome(t.Context(), contact, actor.HelperOutcome{Kind: actor.OutcomeAsked, Text: "which component?"}); err != nil {
		t.Fatal(err)
	}
}

func TestAnswerResumesAfterHangupAndRetry(t *testing.T) {
	t.Parallel()
	s, c := testService(t)
	task := startTask(t, s)
	row, err := s.deps.Store.HelperTask(t.Context(), string(task.ID))
	if err != nil {
		t.Fatal(err)
	}
	ask(t, s, row.Contact)

	// The caller hangs up after the answer is durable: the task still resumes.
	ctx, cancel := context.WithCancel(t.Context())
	s.deps.Contacts = hangup{c, cancel}
	r := sendRequest("the API")
	r.Message.TaskID = task.ID
	_, _ = s.Send(ctx, r, false)
	s.deps.Contacts = c
	got, err := s.deps.Store.HelperTask(t.Context(), row.ID)
	if err != nil || got.State != string(a2a.TaskStateWorking) {
		t.Fatalf("after hangup: %s %v", got.State, err)
	}

	// An answer whose transition was lost, retried after a busy stretch of
	// tool calls, is not delivered again and resumes the task.
	ask(t, s, row.Contact)
	if _, err := c.MessageFrom(t.Context(), row.Contact, "billing", "billing", "answer-2"); err != nil {
		t.Fatal(err)
	}
	for range 60 {
		if _, err := c.append(t.Context(), row.Contact, core.KindToolCall, core.ToolCall{ID: "x", Name: "fetch_url"}); err != nil {
			t.Fatal(err)
		}
	}
	retry := sendRequest("billing")
	retry.Message.ID = "answer-2"
	retry.Message.TaskID = task.ID
	if _, err := s.Send(t.Context(), retry, false); err != nil {
		t.Fatal(err)
	}
	got, err = s.deps.Store.HelperTask(t.Context(), row.ID)
	if err != nil || got.State != string(a2a.TaskStateWorking) {
		t.Fatalf("after retry: %s %v", got.State, err)
	}
	users, err := s.deps.Events.EventsAfterOfKinds(t.Context(), row.Contact, 0, 100, core.KindSet(0).With(core.KindUserMsg))
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 3 {
		t.Fatalf("retried answer delivered again: %d user messages", len(users))
	}
}

// A second question after an answer goes back to the state last pushed. It
// must still be pushed as a status change, not as a best-effort heartbeat.
func TestSecondQuestionIsPushed(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var states []a2a.TaskState
	failNext := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		fail := failNext
		failNext = false
		mu.Unlock()
		if fail {
			w.WriteHeader(503)
			return
		}
		var sr a2a.StreamResponse
		if err := json.NewDecoder(r.Body).Decode(&sr); err == nil {
			if e, ok := sr.Event.(*a2a.TaskStatusUpdateEvent); ok {
				mu.Lock()
				states = append(states, e.Status.State)
				mu.Unlock()
			}
		}
		w.WriteHeader(204)
	}))
	defer target.Close()
	s, _ := testService(t, func(cfg *Config) { cfg.PushAllow = []string{strings.TrimPrefix(target.URL, "http://")} })
	task := startTask(t, s)
	if _, err := s.CreatePush(t.Context(), &a2a.PushConfig{TaskID: task.ID, URL: target.URL}); err != nil {
		t.Fatal(err)
	}
	row, err := s.deps.Store.HelperTask(t.Context(), string(task.ID))
	if err != nil {
		t.Fatal(err)
	}
	d := &delivery{}
	ask(t, s, row.Contact)
	if err := s.deliver(t.Context(), row.ID, d, true); err != nil {
		t.Fatal(err)
	}
	r := sendRequest("the API")
	r.Message.TaskID = task.ID
	if _, err := s.Send(t.Context(), r, false); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond) // a distinct status timestamp
	ask(t, s, row.Contact)
	// The first attempt fails. A heartbeat would be dropped; a status
	// change is retried.
	mu.Lock()
	failNext = true
	mu.Unlock()
	if err := s.deliver(t.Context(), row.ID, d, true); err == nil {
		t.Fatal("failed push acknowledged")
	}
	if err := s.deliver(t.Context(), row.ID, d, true); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	questions := 0
	for _, st := range states {
		if st == a2a.TaskStateInputRequired {
			questions++
		}
	}
	if questions != 2 {
		t.Fatalf("status pushes %v, want both questions", states)
	}
}

func TestTaskGatesAreReleased(t *testing.T) {
	t.Parallel()
	s, _ := testService(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for i := range 100 {
		id := a2a.NewTaskID()
		if _, err := s.Snapshot(t.Context(), string(id), nil, false); err == nil {
			t.Fatalf("unknown task %d found", i)
		}
		unlock, err := s.lock(t.Context(), "held")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.lock(ctx, "held"); err == nil {
			t.Fatal("cancelled wait acquired a held gate")
		}
		unlock()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.gates) != 0 {
		t.Fatalf("%d gates retained", len(s.gates))
	}
}

func TestPushPinsDialValidatedAddressesAndRefresh(t *testing.T) {
	t.Parallel()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer target.Close()
	addr := strings.TrimPrefix(target.URL, "http://")
	p := newPusher([]string{addr})
	ev := a2a.NewStatusUpdateEvent(a2a.TaskInfo{TaskID: "t"}, a2a.TaskStateWorking, nil)
	unroutable := []netip.Addr{netip.MustParseAddr("192.0.2.1")}
	setPin := func(at time.Time) {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.pins[addr] = pin{ips: unroutable, at: at}
	}
	setPin(time.Now())
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	if err := p.send(ctx, core.PushConfig{URL: target.URL}, ev); err == nil {
		t.Fatal("dialed an address other than the pinned one")
	}
	setPin(time.Now().Add(-pinTTL))
	if err := p.send(t.Context(), core.PushConfig{URL: target.URL}, ev); err != nil {
		t.Fatalf("stale pin not refreshed: %v", err)
	}
}

func TestBearerAndPushSchemes(t *testing.T) {
	t.Parallel()
	for header, want := range map[string]bool{"Bearer tok": true, "bearer tok": true, "BEARER tok": true, "Basic tok": false, "Bearer other": false, "Bearertok": false} {
		if Bearer(header, "tok") != want {
			t.Errorf("%q: %v", header, !want)
		}
	}
	if Bearer("Bearer ", "") {
		t.Error("empty token accepted")
	}
	if got := pushRow(&a2a.PushConfig{URL: "http://x", Auth: &a2a.PushAuthInfo{Scheme: "bearer", Credentials: "c"}}).AuthScheme; got != "Bearer" {
		t.Errorf("scheme %q not canonical", got)
	}
}
