package helpers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/obs"
)

// A brief retried with the same messageId, even concurrently, is answered
// with the task the first one created.
func TestRetriedBriefReturnsItsTask(t *testing.T) {
	t.Parallel()
	s, _ := testService(t)
	brief := sendRequest("watch the page")
	brief.Message.ID = "brief-1"
	var wg sync.WaitGroup
	ids := make([]a2a.TaskID, 4)
	for i := range ids {
		wg.Go(func() {
			w, err := s.Send(t.Context(), brief, false)
			if err != nil {
				t.Error(err)
				return
			}
			defer w.Close()
			ids[i] = w.Task.ID
		})
	}
	wg.Wait()
	for _, id := range ids[1:] {
		if id != ids[0] {
			t.Fatalf("retries created tasks %v", ids)
		}
	}
	if n, err := s.deps.Store.CountActiveHelperTasks(t.Context()); err != nil || n != 1 {
		t.Fatalf("active tasks %d %v", n, err)
	}
	row, err := s.deps.Store.HelperTask(t.Context(), string(ids[0]))
	if err != nil {
		t.Fatal(err)
	}
	users, err := s.deps.Events.EventsAfterOfKinds(t.Context(), row.Contact, 0, 10, core.KindSet(0).With(core.KindUserMsg))
	if err != nil || len(users) != 1 {
		t.Fatalf("brief delivered %d times: %v", len(users), err)
	}
	// After the task ends, a retry still gets that task, not an error.
	if _, err := s.Cancel(t.Context(), ids[0]); err != nil {
		t.Fatal(err)
	}
	w, err := s.Send(t.Context(), brief, false)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if w.Task.ID != ids[0] || w.Task.Status.State != a2a.TaskStateCanceled {
		t.Fatalf("retry after cancel: %s %s", w.Task.ID, w.Task.Status.State)
	}
}

// The incremental index gives the snapshot a full read of the log would:
// the newest 100 messages and 50 reports, with their triggers and checks.
func TestSnapshotIndexMatchesFullRead(t *testing.T) {
	t.Parallel()
	s, c := testService(t)
	task := startTask(t, s)
	row, err := s.deps.Store.HelperTask(t.Context(), string(task.ID))
	if err != nil {
		t.Fatal(err)
	}
	grow := func(n int) {
		for i := range n {
			if _, err := c.Check(t.Context(), row.Contact, "check"); err != nil {
				t.Fatal(err)
			}
			if _, err := c.append(t.Context(), row.Contact, core.KindAgentMsg, core.MsgBody{Text: fmt.Sprint("report ", i)}); err != nil {
				t.Fatal(err)
			}
			if _, err := c.MessageFrom(t.Context(), row.Contact, "note", "note", fmt.Sprint("m", i)); err != nil {
				t.Fatal(err)
			}
		}
	}
	grow(70)
	first, err := s.Snapshot(t.Context(), row.ID, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	grow(40) // read incrementally
	got, err := s.Snapshot(t.Context(), row.ID, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	clear(s.indexes) // a full read from the log
	s.mu.Unlock()
	want, err := s.Snapshot(t.Context(), row.ID, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.History) != 100 || len(got.Artifacts) != 50 {
		t.Fatalf("caps: %d messages, %d reports", len(got.History), len(got.Artifacts))
	}
	for i := range got.Artifacts {
		g, w := got.Artifacts[i], want.Artifacts[i]
		if g.ID != w.ID || fmt.Sprint(g.Metadata) != fmt.Sprint(w.Metadata) {
			t.Fatalf("artifact %d: %v %v, want %v %v", i, g.ID, g.Metadata, w.ID, w.Metadata)
		}
	}
	for i := range got.History {
		if got.History[i].ID != want.History[i].ID {
			t.Fatalf("message %d: %s, want %s", i, got.History[i].ID, want.History[i].ID)
		}
	}
	if first.Artifacts[len(first.Artifacts)-1].ID == got.Artifacts[len(got.Artifacts)-1].ID {
		t.Fatal("new reports not read")
	}
	if two, err := s.Snapshot(t.Context(), row.ID, new(2), false); err != nil || len(two.History) != 2 || two.Artifacts != nil {
		t.Fatalf("historyLength: %+v %v", two, err)
	}
}

type failingBackend struct{ Backend }

func (failingBackend) Snapshot(context.Context, string, *int, bool) (*a2a.Task, error) {
	return nil, errors.New("UpdateHelperTask: disk I/O error at /home/me/data/contactd.db")
}

func (failingBackend) Cancel(context.Context, a2a.TaskID) (*a2a.Task, error) {
	return nil, fmt.Errorf("%w: task is terminal", a2a.ErrTaskNotCancelable)
}

// Internal failures reach the client as a plain internal error; protocol
// errors keep the message written for the client.
func TestErrorsDoNotLeakInternals(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	cfg.Auth = "off"
	handler, err := HTTPHandler(failingBackend{}, cfg, obs.Discard())
	if err != nil {
		t.Fatal(err)
	}
	for method, want := range map[string]string{"GetTask": `"message":"internal error"`, "CancelTask": `"message":"task cannot be canceled: task is terminal"`} {
		body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":%q,"params":{"id":"t"}}`, method)
		r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/a2a/helpers", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("A2A-Version", "1.0")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		raw, _ := io.ReadAll(w.Result().Body)
		if !strings.Contains(string(raw), want) || strings.Contains(string(raw), "disk I/O") || strings.Contains(string(raw), "helper backend") {
			t.Fatalf("%s: %s", method, raw)
		}
	}
}
