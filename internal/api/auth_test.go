package api_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quinnovator/contactd/internal/api"
)

const testToken = "s3cret-token-0123456789"

// reached answers 200 with the path, so a test can tell the request got
// through to the API.
func reached() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(r.URL.Path))
	})
}

func get(t *testing.T, c *http.Client, url, auth string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func TestNoTokenLeavesTheAPIOpen(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(api.RequireToken(reached(), ""))
	t.Cleanup(ts.Close)
	if got := get(t, ts.Client(), ts.URL+"/v1/contacts", ""); got != http.StatusOK {
		t.Fatalf("status %d without a configured token, want 200", got)
	}
}

func TestTokenGuardsTheTCPAddress(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(api.RequireToken(reached(), testToken))
	t.Cleanup(ts.Close)
	cases := []struct {
		path, auth string
		want       int
	}{
		{"/v1/contacts", "", http.StatusUnauthorized},
		{"/v1/contacts", "Bearer wrong-token-0123456789", http.StatusUnauthorized},
		{"/v1/contacts", "Bearer " + testToken[:len(testToken)-1], http.StatusUnauthorized},
		{"/v1/contacts", "Basic " + testToken, http.StatusUnauthorized},
		{"/v1/contacts", testToken, http.StatusUnauthorized},
		{"/v1/contacts", "Bearer " + testToken, http.StatusOK},
		{"/v1/contacts", "bearer " + testToken, http.StatusOK}, // the scheme is case insensitive
		{"/v1/contacts/1/live", "", http.StatusUnauthorized},
		{"/metrics", "", http.StatusUnauthorized},
		{"/debug/plans", "", http.StatusUnauthorized},
		// Exempt: health checks, and the A2A routes, which have a token of their own.
		{"/healthz", "", http.StatusOK},
		{"/.well-known/agent-card.json", "", http.StatusOK},
		{"/a2a/helpers", "", http.StatusOK},
		{"/a2a/push", "", http.StatusOK},
		// A path that only looks like an A2A route is not one.
		{"/a2a/../v1/contacts", "", http.StatusUnauthorized},
		{"/a2abc", "", http.StatusUnauthorized},
	}
	for _, c := range cases {
		// The client would clean /a2a/../ itself; send the path as written.
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.URL.Path, req.URL.RawPath = c.path, c.path
		if c.auth != "" {
			req.Header.Set("Authorization", c.auth)
		}
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != c.want {
			t.Errorf("GET %s with %q: status %d, want %d", c.path, c.auth, resp.StatusCode, c.want)
		}
		if resp.StatusCode == http.StatusUnauthorized && !strings.HasPrefix(resp.Header.Get("WWW-Authenticate"), "Bearer") {
			t.Errorf("GET %s: 401 without a Bearer challenge", c.path)
		}
	}
}

func TestHealthzIsOnlyExemptForReads(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(api.RequireToken(reached(), testToken))
	t.Cleanup(ts.Close)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, ts.URL+"/healthz", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("POST /healthz: status %d, want 401", resp.StatusCode)
	}
}

func TestUnixSocketNeedsNoToken(t *testing.T) {
	t.Parallel()
	// A short directory: socket paths have a small length limit.
	dir, err := filepath.Abs(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "s")
	if len(sock) > 100 {
		t.Skip("temporary directory too long for a socket path")
	}
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: api.RequireToken(reached(), testToken), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	c := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		},
	}}
	if got := get(t, c, "http://contactd/v1/contacts", ""); got != http.StatusOK {
		t.Fatalf("status %d on the Unix socket without a token, want 200", got)
	}
}
