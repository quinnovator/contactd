package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/quinnovator/contactd/internal/api"
	"github.com/quinnovator/contactd/internal/client"
	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/diagnostic"
	"github.com/quinnovator/contactd/internal/obs"
)

func TestPlansAreOptIn(t *testing.T) {
	t.Parallel()
	s := api.New(nil, nil, nil, obs.Discard())
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/debug/plans", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("disabled stream status = %d", w.Code)
	}
}

func TestPlanStreamReplaysFiltersAndCompletes(t *testing.T) {
	t.Parallel()
	b := diagnostic.New(5)
	defer b.Close()
	b.Begin(diagnostic.Snapshot{Contact: 2, Name: "other"})
	snap := b.Begin(diagnostic.Snapshot{Contact: 1, Name: "ada"})
	s := httptest.NewServer(api.New(nil, nil, nil, obs.Discard()).WithPlans(b).Handler())
	defer s.Close()
	c, err := client.New(client.Options{URL: s.URL})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	got := make(chan diagnostic.Snapshot, 4)
	done := make(chan error, 1)
	go func() { done <- c.Plans(ctx, "ada", func(v diagnostic.Snapshot) { got <- v }) }()
	select {
	case v := <-got:
		if v.Call != snap.Call || v.Done {
			t.Fatalf("wrong replay: %+v", v)
		}
	case <-ctx.Done():
		t.Fatal("stream did not replay history")
	}
	snap.Reported, snap.ReportedInput, snap.ReportedCached = true, 200, 100
	b.Finish(snap)
	select {
	case v := <-got:
		if v.Call != snap.Call || !v.Done || v.ReportedCached != 100 {
			t.Fatalf("wrong completion: %+v", v)
		}
	case <-ctx.Done():
		t.Fatal("stream did not report completion")
	}
	b.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("server shutdown did not end stream")
	}
	// The retained state is replayed as complete on a later connection,
	// and the same filter accepts a numeric ID.
	var replay []diagnostic.Snapshot
	if err := c.Plans(ctx, "1", func(v diagnostic.Snapshot) { replay = append(replay, v) }); err != nil {
		t.Fatal(err)
	}
	if len(replay) != 1 || !replay[0].Done {
		t.Fatalf("reconnected stream: %+v", replay)
	}
}

type planStore struct {
	api.Store
	rows []core.Event
}

func (s planStore) EventsAfterOfKinds(_ context.Context, _ core.ContactID, after uint64, limit int, kinds core.KindSet) ([]core.Event, error) {
	var out []core.Event
	for _, ev := range s.rows {
		if ev.Seq > after && kinds.Has(ev.Kind) {
			out = append(out, ev)
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}

func TestPlanDetailsAndLogAreBoundToTheCapturedCall(t *testing.T) {
	t.Parallel()
	b := diagnostic.New(1)
	defer b.Close()
	snap := b.Begin(diagnostic.Snapshot{Contact: 7, LogEnd: 2})
	b.StoreDetails(snap.Run, snap.Call, diagnostic.Details{Wire: `{"messages":[]}`, Blocks: []diagnostic.Content{{ID: "1", Text: "source"}}})
	store := planStore{rows: []core.Event{{Contact: 7, Seq: 1, Kind: core.KindUserMsg}, {Contact: 7, Seq: 2, Kind: core.KindAgentMsg}, {Contact: 7, Seq: 3, Kind: core.KindUserMsg}}}
	srv := httptest.NewServer(api.New(store, nil, nil, obs.Discard()).WithPlans(b).Handler())
	defer srv.Close()
	c, err := client.New(client.Options{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	d, err := c.PlanDetails(t.Context(), snap.Run, snap.Call)
	if err != nil || d.Wire != `{"messages":[]}` {
		t.Fatalf("details: %+v %v", d, err)
	}
	rows, end, err := c.PlanEvents(t.Context(), snap.Run, snap.Call, 0)
	if err != nil || len(rows) != 2 || end != 2 {
		t.Fatalf("log leaked beyond call: %v %d %v", rows, end, err)
	}
	rows, _, err = c.PlanEvents(t.Context(), snap.Run, snap.Call, 1)
	if err != nil || len(rows) != 1 || rows[0].Seq != 2 {
		t.Fatal("pagination ignored")
	}
	b.Begin(diagnostic.Snapshot{})
	if _, err = c.PlanDetails(t.Context(), snap.Run, snap.Call); !client.IsStatus(err, http.StatusNotFound) {
		t.Fatalf("evicted capture: %v", err)
	}
}

func TestPlanStreamReportsConnectionWithEmptyHistory(t *testing.T) {
	t.Parallel()
	b := diagnostic.New(5)
	defer b.Close()
	server := httptest.NewServer(api.New(nil, nil, nil, obs.Discard()).WithPlans(b).Handler())
	defer server.Close()
	c, err := client.New(client.Options{URL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	connected := make(chan struct{}, 1)
	snapshots := make(chan diagnostic.Snapshot, 1)
	done := make(chan error, 1)
	go func() {
		done <- c.WatchPlans(ctx, "", client.PlanObserver{
			Connected: func() { connected <- struct{}{} },
			Snapshot:  func(s diagnostic.Snapshot) { snapshots <- s },
		})
	}()
	select {
	case <-connected:
	case <-ctx.Done():
		t.Fatal("empty stream did not announce a successful connection")
	}
	select {
	case s := <-snapshots:
		t.Fatalf("connection invented a model call: %+v", s)
	default:
	}
	b.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("closing the stream did not release the observer")
	}
}

func TestPlanStreamDoesNotAnnounceRejectedConnection(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(api.New(nil, nil, nil, obs.Discard()).Handler())
	defer server.Close()
	c, err := client.New(client.Options{URL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	connected := false
	err = c.WatchPlans(t.Context(), "", client.PlanObserver{Connected: func() { connected = true }})
	if !client.IsStatus(err, http.StatusNotFound) || connected {
		t.Fatalf("rejected stream: connected=%t, err=%v", connected, err)
	}
}
