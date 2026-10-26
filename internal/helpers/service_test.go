package helpers

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/quinnovator/contactd/internal/actor"
	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/eventlog"
	"github.com/quinnovator/contactd/internal/obs"
)

type testContacts struct {
	store *eventlog.Store
	mu    sync.Mutex
	seq   map[core.ContactID]uint64
	busy  bool
}

func (c *testContacts) append(ctx context.Context, id core.ContactID, kind core.EventKind, body any) (uint64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq[id]++
	seq := c.seq[id]
	raw, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}
	err = c.store.Append(ctx, core.Write{Contact: id, Event: &core.Event{Contact: id, Seq: seq, TS: time.Now().UnixMilli(), Kind: kind, Body: raw}})
	return seq, err
}

func (c *testContacts) MessageFrom(ctx context.Context, id core.ContactID, text, raw, messageID string) (uint64, error) {
	return c.append(ctx, id, core.KindUserMsg, core.MsgBody{Text: text, Raw: raw, MessageID: messageID})
}

func (c *testContacts) Check(ctx context.Context, id core.ContactID, text string) (uint64, error) {
	c.mu.Lock()
	busy := c.busy
	c.mu.Unlock()
	if busy {
		return 0, core.ErrBusy
	}
	return c.append(ctx, id, core.KindNotice, core.NoticeBody{Source: "helper-check", Text: text})
}

func testService(t *testing.T, mods ...func(*Config)) (*Service, *testContacts) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	store, err := eventlog.Open(ctx, filepath.Join(t.TempDir(), "test.db"), eventlog.Options{})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { store.Run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		<-done
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	contacts := &testContacts{store: store, seq: make(map[core.ContactID]uint64)}
	cfg := DefaultConfig()
	cfg.Auth = "off"
	for _, mod := range mods {
		mod(&cfg)
	}
	return New(Deps{Contacts: contacts, Creator: store, Events: store, Store: store}, cfg, obs.Discard()), contacts
}

func sendRequest(text string) *a2a.SendMessageRequest {
	return &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart(text)), Config: &a2a.SendMessageConfig{ReturnImmediately: true}}
}

func startTask(t *testing.T, s *Service) *a2a.Task {
	t.Helper()
	w, err := s.Send(t.Context(), sendRequest("watch the page"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	return w.Task
}

func TestLifecycleAndSnapshots(t *testing.T) {
	t.Parallel()
	s, c := testService(t)
	task := startTask(t, s)
	ctx := t.Context()
	row, err := s.deps.Store.HelperTask(ctx, string(task.ID))
	if err != nil {
		t.Fatal(err)
	}
	if task.Status.State != a2a.TaskStateWorking || len(task.History) != 1 || task.History[0].Parts[0].Text() != "watch the page" {
		t.Fatalf("task: %+v", task)
	}
	r := sendRequest("answer")
	r.Message.TaskID = task.ID
	r.Message.ContextID = "wrong"
	if _, err := s.Send(ctx, r, false); !errors.Is(err, a2a.ErrInvalidParams) {
		t.Fatal(err)
	}
	r.Message.ContextID = task.ContextID
	if _, err := s.Send(ctx, r, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Send(ctx, r, false); err != nil {
		t.Fatal(err)
	}
	seq, err := c.append(ctx, row.Contact, core.KindAgentMsg, core.MsgBody{Text: "baseline"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.outcome(ctx, row.Contact, actor.HelperOutcome{Kind: actor.OutcomeReplied, Seq: seq}); err != nil {
		t.Fatal(err)
	}
	snap, err := s.Snapshot(ctx, string(task.ID), nil, true)
	if err != nil || len(snap.History) != 2 || len(snap.Artifacts) != 1 {
		t.Fatalf("snapshot: %+v %v", snap, err)
	}
	zero := 0
	snap, err = s.Snapshot(ctx, string(task.ID), &zero, false)
	if err != nil || len(snap.History) != 0 || len(snap.Artifacts) != 0 {
		t.Fatalf("bounded: %+v %v", snap, err)
	}
	if err := s.outcome(ctx, row.Contact, actor.HelperOutcome{Kind: actor.OutcomeAsked, Text: "which page?"}); err != nil {
		t.Fatal(err)
	}
	row, err = s.deps.Store.HelperTask(ctx, row.ID)
	if err != nil || row.State != string(a2a.TaskStateInputRequired) || row.NextDue != 0 {
		t.Fatalf("asked: %+v %v", row, err)
	}
	r.Message.ID = a2a.NewMessageID()
	if _, err := s.Send(ctx, r, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Cancel(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.append(ctx, row.Contact, core.KindAgentMsg, core.MsgBody{Text: "too late"}); err != nil {
		t.Fatal(err)
	}
	snap, err = s.Snapshot(ctx, row.ID, nil, true)
	if err != nil || len(snap.Artifacts) != 1 {
		t.Fatalf("late reply escaped terminal boundary: %+v %v", snap, err)
	}
	if _, err := s.Cancel(ctx, task.ID); !errors.Is(err, a2a.ErrTaskNotCancelable) {
		t.Fatal(err)
	}
	if _, err := s.Send(ctx, r, false); !errors.Is(err, a2a.ErrUnsupportedOperation) {
		t.Fatal(err)
	}
	if _, err := s.Subscribe(ctx, task.ID); !errors.Is(err, a2a.ErrUnsupportedOperation) {
		t.Fatal(err)
	}
}

func TestTaskCapsFailuresAndExpiry(t *testing.T) {
	t.Parallel()
	s, _ := testService(t, func(c *Config) { c.MaxActive = 1 })
	task := startTask(t, s)
	rejected := startTask(t, s)
	if rejected.Status.State != a2a.TaskStateRejected {
		t.Fatal("cap ignored")
	}
	row, err := s.deps.Store.HelperTask(t.Context(), string(task.ID))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := s.outcome(t.Context(), row.Contact, actor.HelperOutcome{Kind: actor.OutcomeFailed}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.outcome(t.Context(), row.Contact, actor.HelperOutcome{Kind: actor.OutcomeQuiet}); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := s.outcome(t.Context(), row.Contact, actor.HelperOutcome{Kind: actor.OutcomeFailed, Text: "test"}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Snapshot(t.Context(), row.ID, nil, true)
	if err != nil || got.Status.State != a2a.TaskStateFailed {
		t.Fatalf("failures: %+v %v", got, err)
	}
	task = startTask(t, s)
	row, err = s.deps.Store.HelperTask(t.Context(), string(task.ID))
	if err != nil {
		t.Fatal(err)
	}
	row.State = string(a2a.TaskStateInputRequired)
	row.NextDue = 0
	row.UntilTS = time.Now().Add(-time.Second).UnixMilli()
	if err := s.deps.Store.UpdateHelperTask(t.Context(), row); err != nil {
		t.Fatal(err)
	}
	if err := s.check(t.Context(), row.ID); err != nil {
		t.Fatal(err)
	}
	got, err = s.Snapshot(t.Context(), row.ID, nil, true)
	if err != nil || got.Status.State != a2a.TaskStateCompleted {
		t.Fatalf("waiting expiry: %+v %v", got, err)
	}
}

func TestRestartResolvesInterruptedCreation(t *testing.T) {
	t.Parallel()
	s, _ := testService(t)
	interrupted := startTask(t, s)
	working := startTask(t, s)
	row, err := s.deps.Store.HelperTask(t.Context(), string(interrupted.ID))
	if err != nil {
		t.Fatal(err)
	}
	row.State = string(a2a.TaskStateSubmitted)
	if err := s.deps.Store.UpdateHelperTask(t.Context(), row); err != nil {
		t.Fatal(err)
	}
	restarted := New(s.deps, s.cfg, obs.Discard())
	restarted.recoverSubmitted(t.Context())
	got, err := restarted.Snapshot(t.Context(), string(interrupted.ID), nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status.State != a2a.TaskStateFailed || got.Status.Message == nil {
		t.Fatalf("interrupted creation: %+v", got)
	}
	stillWorking, err := restarted.Snapshot(t.Context(), string(working.ID), nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if stillWorking.Status.State != a2a.TaskStateWorking {
		t.Fatalf("changed healthy task: %+v", stillWorking)
	}
}
