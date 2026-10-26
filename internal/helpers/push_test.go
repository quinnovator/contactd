package helpers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/quinnovator/contactd/internal/core"
)

func TestOutboxRetryRestartAndPushHeaders(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	fail := true
	var received []a2a.StreamResponse
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/a2a+json" || r.Header.Get("A2A-Version") != "1.0" || r.Header.Get("Authorization") != "Bearer secret" || r.Header.Get("A2A-Notification-Token") != "token" {
			t.Error("wrong push headers")
		}
		var event a2a.StreamResponse
		if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		received = append(received, event)
		if fail {
			w.WriteHeader(503)
		} else {
			w.WriteHeader(204)
		}
	}))
	defer target.Close()
	s, c := testService(t, func(cfg *Config) { cfg.PushAllow = []string{strings.TrimPrefix(target.URL, "http://")} })
	task := startTask(t, s)
	if _, err := s.CreatePush(t.Context(), &a2a.PushConfig{TaskID: task.ID, URL: target.URL, Token: "token", Auth: &a2a.PushAuthInfo{Scheme: "Bearer", Credentials: "secret"}}); err != nil {
		t.Fatal(err)
	}
	row, err := s.deps.Store.HelperTask(t.Context(), string(task.ID))
	if err != nil {
		t.Fatal(err)
	}
	seq, err := c.append(t.Context(), row.Contact, core.KindAgentMsg, core.MsgBody{Text: "baseline"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.deliver(t.Context(), row.ID, &delivery{}); err == nil {
		t.Fatal("failed push acknowledged")
	}
	row, err = s.deps.Store.HelperTask(t.Context(), row.ID)
	if err != nil || row.PushedSeq != 0 {
		t.Fatalf("failed cursor: %+v %v", row, err)
	}
	mu.Lock()
	fail = false
	mu.Unlock()
	restarted := New(s.deps, s.cfg, s.log)
	if err := restarted.deliver(t.Context(), row.ID, &delivery{}); err != nil {
		t.Fatal(err)
	}
	row, err = s.deps.Store.HelperTask(t.Context(), row.ID)
	if err != nil || row.PushedSeq != seq || row.PushedState != row.State {
		t.Fatalf("acknowledged cursor: %+v %v", row, err)
	}
	mu.Lock()
	n := len(received)
	if n != 3 {
		t.Errorf("received %d, want failed artifact, retry artifact, status", n)
	}
	if n >= 2 && received[0].Event.(*a2a.TaskArtifactUpdateEvent).Artifact.ID != received[1].Event.(*a2a.TaskArtifactUpdateEvent).Artifact.ID {
		t.Error("retry artifact ID changed")
	}
	mu.Unlock()
	if err := New(s.deps, s.cfg, s.log).deliver(t.Context(), row.ID, &delivery{}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(received) != n {
		t.Fatal("restart repeated acknowledged reports")
	}
}

func TestPushRejectsRedirectAndUnknownHost(t *testing.T) {
	t.Parallel()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:1", http.StatusFound)
	}))
	defer target.Close()
	p := newPusher([]string{strings.TrimPrefix(target.URL, "http://")})
	cfg := core.PushConfig{URL: target.URL}
	if err := p.send(t.Context(), cfg, a2a.NewStatusUpdateEvent(a2a.TaskInfo{TaskID: "t"}, a2a.TaskStateWorking, nil)); err == nil {
		t.Fatal("redirect accepted")
	}
	if err := p.validate(t.Context(), core.PushConfig{URL: "http://127.0.0.1:1"}); err == nil {
		t.Fatal("unknown target accepted")
	}
	if _, err := p.dial(t.Context(), "tcp", "127.0.0.1:1"); err == nil {
		t.Fatal("dial bypassed allowlist")
	}
}

func TestDueChecksBusyAndMissedPeriods(t *testing.T) {
	t.Parallel()
	s, c := testService(t)
	task := startTask(t, s)
	row, err := s.deps.Store.HelperTask(t.Context(), string(task.ID))
	if err != nil {
		t.Fatal(err)
	}
	row.NextDue = time.Now().Add(-time.Hour).UnixMilli()
	if err := s.deps.Store.UpdateHelperTask(t.Context(), row); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.busy = true
	c.mu.Unlock()
	if err := s.check(t.Context(), row.ID); err != nil {
		t.Fatal(err)
	}
	busy, err := s.deps.Store.HelperTask(t.Context(), row.ID)
	if err != nil || busy.Checks != row.Checks || busy.NextDue <= time.Now().UnixMilli() {
		t.Fatalf("busy: %+v %v", busy, err)
	}
	c.mu.Lock()
	c.busy = false
	c.mu.Unlock()
	busy.NextDue = row.NextDue
	if err := s.deps.Store.UpdateHelperTask(t.Context(), busy); err != nil {
		t.Fatal(err)
	}
	if err := s.check(t.Context(), row.ID); err != nil {
		t.Fatal(err)
	}
	done, err := s.deps.Store.HelperTask(t.Context(), row.ID)
	if err != nil || done.Checks != row.Checks+1 {
		t.Fatalf("missed checks replayed: %+v %v", done, err)
	}
}
