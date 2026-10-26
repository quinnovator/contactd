package requester_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/quinnovator/contactd/internal/api/apitest"
	"github.com/quinnovator/contactd/internal/client"
	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/helpers"
	fakellm "github.com/quinnovator/contactd/internal/llm/fake"
)

func eventually(t *testing.T, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition did not become true")
}

func newClient(t *testing.T, s *apitest.Server) *client.Client {
	t.Helper()
	c, err := client.New(client.Options{URL: s.URL})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func send(t *testing.T, c *client.Client, id core.ContactID, text string) {
	t.Helper()
	if _, err := c.Send(t.Context(), id, text); err != nil {
		t.Fatal(err)
	}
}

func postPush(t *testing.T, url string, row core.RemoteHelper, ev a2a.Event, token, secret, typ string) int {
	t.Helper()
	body, err := json.Marshal(a2a.StreamResponse{Event: ev})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url+"/a2a/push", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", typ)
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("A2A-Notification-Token", token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}

func TestLoop(t *testing.T) {
	t.Parallel()
	var visits atomic.Int64
	var changed atomic.Bool
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		visit := visits.Add(1)
		w.Header().Set("Content-Type", "text/html")
		text := "SAME"
		if visit == 1 {
			text = "BASELINE"
		}
		if changed.Swap(false) {
			text = "CHANGED"
		}
		_, _ = io.WriteString(w, "<title>Status</title><p>"+text+"</p>")
	}))
	defer page.Close()
	cfg := helpers.DefaultConfig()
	cfg.MinEvery = 100 * time.Millisecond
	cfg.Auth = "token"
	cfg.Token = "test-agent-secret"
	db := filepath.Join(t.TempDir(), "loop.db")
	opt := apitest.Options{Database: db, Helpers: &cfg, FetchAllow: []string{strings.TrimPrefix(page.URL, "http://")}, LLM: fakellm.Options{Rules: []fakellm.Rule{
		{Match: regexp.MustCompile(`(?s)source="helper-update"`), Tool: "stay_quiet", Code: `{"reason":"Report received"}`},
		{Match: regexp.MustCompile(`(?s)source="helper-check"`), Tool: "fetch_url", Code: fmt.Sprintf(`{"url":%q}`, page.URL)},
		{Match: regexp.MustCompile(`(?s)Status: 200.*SAME`), ToolResult: true, Tool: "stay_quiet", Code: `{"reason":"Nothing changed"}`},
		{Match: regexp.MustCompile(`(?s)Status: 200.*CHANGED`), ToolResult: true, Reply: "Report: CHANGED"},
		{Match: regexp.MustCompile(`(?s)<brief.*WATCH_PAGE`), Tool: "fetch_url", Code: fmt.Sprintf(`{"url":%q}`, page.URL)},
		{Match: regexp.MustCompile(`^ASK_AGAIN$`), Tool: "ask_requester", Code: `{"question":"Should I keep watching?"}`},
	}}}
	s := apitest.Start(t, opt)
	c := newClient(t, s)
	id, err := c.Create(t.Context(), client.NewContact{Name: "Ada", Agent: core.AgentConfig{Helpers: true}})
	if err != nil {
		t.Fatal(err)
	}
	send(t, c, id, `!start_helper {"title":"Status watch","brief":"WATCH_PAGE","every":"200ms","duration":"1h"}`)
	var row core.RemoteHelper
	eventually(t, func() bool {
		rows, e := s.Store.ListRemoteHelpers(t.Context(), id, 20)
		if e != nil || len(rows) != 1 {
			return false
		}
		row = rows[0]
		return row.Reports >= 1 && row.Checks >= 2
	})
	if visits.Load() < 1 {
		t.Fatalf("fetch count %d", visits.Load())
	}
	changed.Store(true)
	eventually(t, func() bool {
		h, e := s.Store.RemoteHelper(t.Context(), row.Handle)
		if e == nil && h.Reports >= 2 {
			row = h
			return true
		}
		return false
	})

	if row.HelperContact == 0 {
		t.Fatal("local helper contact not exposed")
	}
	contacts, err := c.Contacts(t.Context())
	if err != nil || len(contacts) != 1 {
		t.Fatalf("helper exposed in contacts: %v %v", contacts, err)
	}
	// The artifact has already been acknowledged; replaying it does not add a notice.
	ev := &a2a.TaskArtifactUpdateEvent{TaskID: a2a.TaskID(row.TaskID), ContextID: row.ContextID, Artifact: &a2a.Artifact{ID: "test-replayed", Parts: a2a.ContentParts{a2a.NewTextPart("New report")}}}
	for range 2 {
		if status := postPush(t, s.URL, row, ev, row.PushToken, row.PushSecret, "application/a2a+json"); status != 204 {
			t.Fatalf("push status %d", status)
		}
	}
	refreshed, err := s.Store.RemoteHelper(t.Context(), row.Handle)
	if err != nil || refreshed.Reports != row.Reports+1 {
		t.Fatalf("dedup: %+v %v", refreshed, err)
	}
	for _, tc := range []struct {
		token, secret, typ string
		status             int
	}{
		{row.PushToken, "bad", "application/json", 401}, {"missing", row.PushSecret, "application/json", 404}, {row.PushToken, row.PushSecret, "text/plain", 415},
	} {
		if got := postPush(t, s.URL, row, ev, tc.token, tc.secret, tc.typ); got != tc.status {
			t.Fatalf("status %d, want %d", got, tc.status)
		}
	}
	send(t, c, id, fmt.Sprintf(`!message_helper {"helper":%d,"text":"ASK_AGAIN"}`, row.Handle))
	eventually(t, func() bool {
		h, e := s.Store.RemoteHelper(t.Context(), row.Handle)
		return e == nil && h.NotifiedState == string(a2a.TaskStateInputRequired)
	})
	send(t, c, id, fmt.Sprintf(`!stop_helper {"helper":%d}`, row.Handle))
	eventually(t, func() bool {
		h, e := s.Store.RemoteHelper(t.Context(), row.Handle)
		return e == nil && h.State == string(a2a.TaskStateCanceled)
	})
	// Reopen the same SQLite file: accepted artifact IDs and counters survive.
	s.Close()
	s = apitest.Start(t, opt)
	if got := postPush(t, s.URL, row, ev, row.PushToken, row.PushSecret, "application/json"); got != 204 {
		t.Fatalf("restart duplicate: %d", got)
	}
	h, err := s.Store.RemoteHelper(t.Context(), row.Handle)
	if err != nil || h.Reports != refreshed.Reports {
		t.Fatalf("restart counters: %+v %v", h, err)
	}
}

func TestLoopRestartActive(t *testing.T) {
	t.Parallel()
	cfg := helpers.DefaultConfig()
	cfg.Auth = "off"
	cfg.MinEvery = 200 * time.Millisecond
	opt := apitest.Options{Database: filepath.Join(t.TempDir(), "restart.db"), Helpers: &cfg, LLM: fakellm.Options{Rules: []fakellm.Rule{
		{Match: regexp.MustCompile(`(?s)source="helper-update"`), Tool: "stay_quiet", Code: `{}`},
		{Match: regexp.MustCompile(`(?s)source="helper-check"`), Reply: "A scheduled report"},
	}}}
	s := apitest.Start(t, opt)
	c := newClient(t, s)
	id, err := c.Create(t.Context(), client.NewContact{Name: "parent", Agent: core.AgentConfig{Helpers: true}})
	if err != nil {
		t.Fatal(err)
	}
	send(t, c, id, `!start_helper {"title":"Restart","brief":"Send a baseline","every":"500ms","duration":"1h"}`)
	var row core.RemoteHelper
	eventually(t, func() bool {
		rows, e := s.Store.ListRemoteHelpers(t.Context(), id, 20)
		if e == nil && len(rows) == 1 && rows[0].Reports > 0 {
			row = rows[0]
			return true
		}
		return false
	})
	opt.Address = strings.TrimPrefix(s.URL, "http://")
	s.Close()
	s = apitest.Start(t, opt)
	eventually(t, func() bool {
		h, e := s.Store.RemoteHelper(t.Context(), row.Handle)
		return e == nil && h.Reports > row.Reports
	})
	c = newClient(t, s)
	send(t, c, id, fmt.Sprintf(`!stop_helper {"helper":%d}`, row.Handle))
	eventually(t, func() bool {
		h, e := s.Store.RemoteHelper(t.Context(), row.Handle)
		return e == nil && h.State == string(a2a.TaskStateCanceled)
	})
}
