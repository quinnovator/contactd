package fetch_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/quinnovator/contactd/internal/fetch"
)

func TestFetchGuardAndHTML(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<html><head><title>Status page</title><script>bad</script></head><body><h1>API</h1><div>All   systems <b>operational</b></div><style>hidden</style></body></html>`)
	}))
	defer srv.Close()
	if _, err := fetch.New(fetch.Config{}).Fetch(t.Context(), srv.URL); err == nil {
		t.Fatal("loopback allowed")
	}
	f := fetch.New(fetch.Config{Allow: []string{strings.TrimPrefix(srv.URL, "http://")}})
	p, err := f.Fetch(t.Context(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if p.Title != "Status page" || p.Text != "API\nAll systems operational" || p.Status != 200 {
		t.Fatalf("page: %+v", p)
	}
}

func TestFetchRedirectAndLimit(t *testing.T) {
	t.Parallel()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, "private") }))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, target.URL, http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = fmt.Fprint(w, strings.Repeat("x", 100))
	}))
	defer srv.Close()
	f := fetch.New(fetch.Config{Allow: []string{strings.TrimPrefix(srv.URL, "http://")}, MaxBytes: 20})
	if _, err := f.Fetch(t.Context(), srv.URL+"/redirect"); err == nil {
		t.Fatal("redirect bypassed guard")
	}
	p, err := f.Fetch(t.Context(), srv.URL)
	if err != nil || !strings.HasPrefix(p.Text, strings.Repeat("x", 20)+"\n[… page cut") {
		t.Fatalf("cap: %+v %v", p, err)
	}
	for _, u := range []string{"file:///tmp/a", "http://user:password@example.com", "/relative"} {
		if _, err := f.Fetch(t.Context(), u); err == nil {
			t.Fatalf("bad URL accepted: %s", u)
		}
	}
}
