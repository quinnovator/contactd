package fetch_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
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

func TestPublicAddresses(t *testing.T) {
	t.Parallel()
	for addr, want := range map[string]bool{
		"93.184.215.14": true, "2606:4700::6810:84e5": true, "8.8.8.8": true,
		"127.0.0.1": false, "10.0.0.1": false, "172.16.0.1": false, "192.168.1.1": false, "169.254.169.254": false,
		"100.64.0.1": false, "100.100.100.100": false, "100.127.255.254": false, "198.18.0.1": false, "192.0.0.8": false,
		"240.0.0.1": false, "255.255.255.255": false, "0.1.2.3": false,
		"::1": false, "fd7a:115c:a1e0::1": false, "fe80::1": false, "fec0::1": false, "::7f00:1": false, "::ffff:10.0.0.1": false,
		"64:ff9b::a00:1": false, "2002:a00:1::": false, "2001:0:a00:1::": false, "2001:db8::1": false,
	} {
		if got := fetch.Public(netip.MustParseAddr(addr)); got != want {
			t.Errorf("%s: public %v, want %v", addr, got, want)
		}
	}
}
