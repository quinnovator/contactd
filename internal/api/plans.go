package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/diagnostic"
)

// WithPlans enables the diagnostic stream. A nil bus leaves it disabled.
// Call before serving. Detail endpoints include bounded prompt contents.
func (s *Server) WithPlans(bus *diagnostic.Bus) *Server {
	s.plans = bus
	return s
}

func (s *Server) planStream(w http.ResponseWriter, r *http.Request) {
	if s.plans == nil {
		http.NotFound(w, r)
		return
	}
	history, updates, cancel := s.plans.Subscribe()
	defer cancel()
	rc := http.NewResponseController(w)
	deadline := func() bool {
		err := rc.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return err == nil || errors.Is(err, http.ErrNotSupported)
	}
	if !deadline() {
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if rc.Flush() != nil {
		return
	}
	enc := json.NewEncoder(w)
	want := strings.TrimPrefix(r.URL.Query().Get("contact"), "#")
	send := func(v diagnostic.Snapshot) bool {
		if want != "" && v.Contact.String() != want && v.Name != want {
			return true
		}
		return deadline() && enc.Encode(v) == nil && rc.Flush() == nil
	}
	for _, v := range history {
		if !send(v) {
			return
		}
	}
	ticker := time.NewTicker(heartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case v, ok := <-updates:
			if !ok || !send(v) {
				return
			}
		case <-ticker.C:
			if !deadline() {
				return
			}
			if _, err := fmt.Fprint(w, "\n"); err != nil || rc.Flush() != nil {
				return
			}
		}
	}
}

func (s *Server) capture(w http.ResponseWriter, r *http.Request) (diagnostic.Snapshot, diagnostic.Details, bool) {
	if s.plans != nil {
		call, err := strconv.ParseUint(r.PathValue("call"), 10, 64)
		if err == nil {
			snap, detail, ok := s.plans.Details(r.PathValue("run"), call)
			if ok {
				w.Header().Set("Cache-Control", "no-store")
				return snap, detail, true
			}
		}
	}
	http.NotFound(w, r)
	return diagnostic.Snapshot{}, diagnostic.Details{}, false
}

func (s *Server) planDetails(w http.ResponseWriter, r *http.Request) {
	_, d, ok := s.capture(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// Events are paginated and bounded by the log head captured for this call.
func (s *Server) planEvents(w http.ResponseWriter, r *http.Request) {
	snap, _, ok := s.capture(w, r)
	if !ok {
		return
	}
	after, _ := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
	var out []core.Event
	if snap.LogEnd > after && s.store != nil {
		kinds := core.AllKinds
		if r.URL.Query().Get("kind") == "checkpoint" {
			kinds = core.KindSet(0).With(core.KindCheckpoint)
		}
		events, err := s.store.EventsAfterOfKinds(r.Context(), snap.Contact, after, 200, kinds)
		if err != nil {
			s.fail(w, r, http.StatusInternalServerError, err)
			return
		}
		bytes := 0
		for _, ev := range events {
			if ev.Seq <= snap.LogEnd {
				if len(out) > 0 && bytes+len(ev.Body) > 2<<20 {
					break
				}
				bytes += len(ev.Body)
				out = append(out, ev)
			}
		}
	}
	writeJSON(w, http.StatusOK, struct {
		Events []core.Event `json:"events"`
		End    uint64       `json:"end"`
	}{out, snap.LogEnd})
}
