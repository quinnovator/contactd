package main

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStatusChanges(t *testing.T) {
	t.Parallel()
	s := newSite(slog.New(slog.DiscardHandler))
	h := s.handler()
	for _, step := range []struct {
		method, path, want string
		status             int
	}{
		{"GET", "/", "operational", 200}, {"POST", "/set?component=API&state=degraded", "", 204}, {"GET", "/", "degraded", 200}, {"POST", "/reset", "", 204}, {"GET", "/", "operational", 200}, {"POST", "/set?component=missing&state=degraded", "unknown", 400},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), step.method, step.path, http.NoBody))
		if w.Code != step.status || !strings.Contains(w.Body.String(), step.want) {
			t.Fatalf("%s: %d %s", step.path, w.Code, w.Body.String())
		}
	}
}
