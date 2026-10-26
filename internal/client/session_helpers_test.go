package client

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/quinnovator/contactd/internal/api/apitest"
	"github.com/quinnovator/contactd/internal/core"
)

// heldHelpers is a transport that holds every request for a helper list
// until release is closed.
type heldHelpers struct {
	next    http.RoundTripper
	release chan struct{}
}

func (h heldHelpers) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.HasSuffix(req.URL.Path, "/helpers") {
		select {
		case <-h.release:
		case <-req.Context().Done():
			return nil, fmt.Errorf("held: %w", req.Context().Err())
		}
	}
	resp, err := h.next.RoundTrip(req)
	if err != nil {
		return nil, fmt.Errorf("held: %w", err)
	}
	return resp, nil
}

// A slow helper list does not hold up the log: the first page is read while
// the snapshot is on its way, and the session goes live once it is in, so no
// delta of the stream comes before it.
func TestHelperSnapshotDoesNotHoldUpTheLog(t *testing.T) {
	t.Parallel()
	srv := apitest.Start(t, apitest.Options{})
	id, err := srv.Store.CreateContact(t.Context(), core.AgentConfig{}, "", time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(Options{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	c.http = &http.Client{Transport: heldHelpers{next: http.DefaultTransport, release: release}}
	sess := c.Follow(t.Context(), id)
	defer sess.Close()
	next := func() Update {
		t.Helper()
		select {
		case u := <-sess.Updates():
			return u
		case <-time.After(20 * time.Second):
			t.Fatal("timed out waiting for the session")
			return Update{}
		}
	}
	for u := next(); u.Kind != UpdateEvents; u = next() {
		if u.Kind == UpdateHelpers || (u.Kind == UpdateState && u.State == StateLive) {
			t.Fatalf("update %+v before the log was read", u)
		}
	}
	close(release)
	snapshots := 0
	for u := next(); u.Kind != UpdateState || u.State != StateLive; u = next() {
		if u.Kind == UpdateHelpers {
			snapshots++
		}
	}
	if snapshots != 1 {
		t.Fatalf("%d snapshots before live, want 1", snapshots)
	}
}
