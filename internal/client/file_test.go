package client

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/quinnovator/contactd/internal/api/apitest"
	"github.com/quinnovator/contactd/internal/core"
)

// These tests are inside the package, unlike the rest, for two of them: one
// needs a transport that never answers, to see the timeout on a fake clock,
// and one a transport that counts, to see that no request was made.

// written is when the fixture files say they were last written.
func written() time.Time { return time.Date(2026, time.October, 7, 12, 6, 0, 0, time.UTC) }

// fixture is a real server over a directory of working directories.
type fixture struct {
	c *Client
	// own is a contact with files, and other one with a secret.
	own, other core.ContactID
	socket     string
	root       string
	png        []byte
}

// names are files of the fixture whose names a URL cannot carry as they are.
func names() []string {
	return []string{"a b%2f..c.png", "q?x#y.png", "100%.png", "semi;colon:at@&=+$,.png", "%2e%2e", "..hidden", "sub dir/ü.png"}
}

func startFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{root: t.TempDir()}
	srv := apitest.Start(t, apitest.Options{FSRoot: f.root})
	f.socket = srv.Socket
	c, err := New(Options{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	f.c = c
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []*core.ContactID{&f.own, &f.other} {
		*id, err = c.Create(t.Context(), NewContact{Agent: core.AgentConfig{System: "You are Ada."}})
		must(err)
	}
	var buf bytes.Buffer
	must(png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 64, 36))))
	f.png = buf.Bytes()

	write := func(id core.ContactID, name string, data []byte) string {
		t.Helper()
		path := filepath.Join(f.root, id.String(), filepath.FromSlash(name))
		must(os.MkdirAll(filepath.Dir(path), 0o700))
		must(os.WriteFile(path, data, 0o600))
		must(os.Chtimes(path, written(), written()))
		return path
	}
	// sized makes a file of zeros without writing them.
	sized := func(name string, size int64) {
		t.Helper()
		path := write(f.own, name, nil)
		must(os.Truncate(path, size))
		must(os.Chtimes(path, written(), written()))
	}
	write(f.own, "out/latency.png", f.png)
	write(f.own, "notes.txt", []byte("hello"))
	write(f.own, "empty", nil)
	for _, name := range names() {
		write(f.own, name, []byte(name))
	}
	sized("edge.bin", maxFileBytes)
	sized("big.bin", maxFileBytes+1)
	write(f.other, "secret.txt", []byte("another contact's secret"))
	return f
}

func TestFile(t *testing.T) {
	t.Parallel()
	f := startFixture(t)
	local, err := New(Options{Socket: f.socket})
	if err != nil {
		t.Fatal(err)
	}

	type row struct {
		name    string
		c       *Client
		contact core.ContactID
		path    string
		data    []byte
		ctype   string
		err     error
	}
	const octet = "application/octet-stream"
	fixed := []row{
		{name: "an image", contact: f.own, path: "out/latency.png", data: f.png, ctype: "image/png"},
		{name: "an image, over the socket", c: local, contact: f.own, path: "out/latency.png", data: f.png, ctype: "image/png"},
		{name: "a file that is not an image", contact: f.own, path: "notes.txt", data: []byte("hello"), ctype: octet},
		{name: "an empty file", contact: f.own, path: "empty", data: []byte{}, ctype: octet},
		{name: "a file of exactly the limit", contact: f.own, path: "edge.bin", data: make([]byte, maxFileBytes), ctype: octet},
		{name: "another contact's own file", contact: f.other, path: "secret.txt", data: []byte("another contact's secret"), ctype: octet},

		{name: "a file one byte over the limit", contact: f.own, path: "big.bin", err: ErrFileTooLarge},
		{name: "a missing file", contact: f.own, path: "out/missing.png", err: ErrFileNotFound},
		{name: "a directory", contact: f.own, path: "out", err: ErrFileNotFound},
		{name: "a contact with no such file", contact: f.other, path: "out/latency.png", err: ErrFileNotFound},
		{name: "an unknown contact", contact: 999, path: "out/latency.png", err: ErrFileNotFound},
		// Unescaped, each of these would be a request for the other
		// contact's file, by way of a redirect.
		{name: "a climb to another contact's file", contact: f.own, path: "../../" + f.other.String() + "/files/secret.txt", err: ErrFileNotFound},
		{name: "a climb from a directory", contact: f.own, path: "out/../../../" + f.other.String() + "/files/secret.txt", err: ErrFileNotFound},
	}
	// Each of these is the name of a file, and is fetched as that file: a
	// "?" starts no query and an escaped ".." climbs nowhere.
	named := make([]row, len(names()))
	for i, name := range names() {
		named[i] = row{name: "the name " + name, contact: f.own, path: name, data: []byte(name), ctype: octet}
	}
	for _, tc := range slices.Concat(fixed, named) {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := tc.c
			if c == nil {
				c = f.c
			}
			got, err := c.File(t.Context(), tc.contact, tc.path, "")
			if tc.err != nil {
				if !errors.Is(err, tc.err) {
					t.Errorf("err = %v, want %v", err, tc.err)
				}
				if got.Data != nil || got.ETag != "" || got.NotModified {
					t.Errorf("a failed fetch returned %d bytes, ETag %q, NotModified %v", len(got.Data), got.ETag, got.NotModified)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v, want the file", err)
			}
			if got.Data == nil || !bytes.Equal(got.Data, tc.data) {
				t.Errorf("Data is %d bytes starting %.40q, want %d starting %.40q", len(got.Data), got.Data, len(tc.data), tc.data)
			}
			if got.ContentType != tc.ctype {
				t.Errorf("ContentType = %q, want %q", got.ContentType, tc.ctype)
			}
			if got.ETag == "" || got.NotModified {
				t.Errorf("ETag = %q, NotModified = %v; want a tag and a whole file", got.ETag, got.NotModified)
			}
			if !got.Modified.Equal(written()) {
				t.Errorf("Modified = %v, want %v", got.Modified, written())
			}
		})
	}
}

func TestFileNotModified(t *testing.T) {
	t.Parallel()
	f := startFixture(t)
	const path = "out/latency.png"
	first, err := f.c.File(t.Context(), f.own, path, "")
	if err != nil || first.ETag == "" {
		t.Fatalf("first fetch: ETag %q, %v", first.ETag, err)
	}

	again, err := f.c.File(t.Context(), f.own, path, first.ETag)
	if err != nil {
		t.Fatalf("with the tag of the copy held: %v", err)
	}
	if !again.NotModified || again.Data != nil || again.ETag != first.ETag || !again.Modified.Equal(written()) {
		t.Errorf("with the tag of the copy held: NotModified %v, %d bytes, ETag %q, Modified %v; want only word that %q still stands",
			again.NotModified, len(again.Data), again.ETag, again.Modified, first.ETag)
	}

	stale, err := f.c.File(t.Context(), f.own, path, `"stale"`)
	if err != nil || stale.NotModified || !bytes.Equal(stale.Data, f.png) || stale.ETag != first.ETag {
		t.Errorf("with a stale tag: NotModified %v, %d bytes, ETag %q, %v; want the whole file", stale.NotModified, len(stale.Data), stale.ETag, err)
	}

	// The tool writes the file again: the tag held no longer names it. The
	// new time is set by hand, so the test never reads the clock.
	rewritten := filepath.Join(f.root, f.own.String(), filepath.FromSlash(path))
	if err := os.WriteFile(rewritten, []byte("GIF89a now"), 0o600); err != nil {
		t.Fatal(err)
	}
	later := written().Add(time.Hour)
	if err := os.Chtimes(rewritten, later, later); err != nil {
		t.Fatal(err)
	}
	changed, err := f.c.File(t.Context(), f.own, path, first.ETag)
	if err != nil || changed.NotModified || string(changed.Data) != "GIF89a now" || changed.ETag == first.ETag || changed.ContentType != "image/gif" {
		t.Errorf("after the file changed: NotModified %v, %.40q, ETag %q (was %q), type %q, %v",
			changed.NotModified, changed.Data, changed.ETag, first.ETag, changed.ContentType, err)
	}
	if !changed.Modified.Equal(later) {
		t.Errorf("Modified = %v, want %v", changed.Modified, later)
	}
}

// counting is a transport that answers every request with 204 and counts
// them.
type counting struct{ n atomic.Int64 }

func (c *counting) RoundTrip(*http.Request) (*http.Response, error) {
	c.n.Add(1)
	return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody}, nil
}

// A path the server would refuse for its shape is refused here, before any
// request: with dot elements in it a router would redirect the request, and
// what it redirected to would be some other file.
func TestFileRefusesPathsUnasked(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, path string
		// asked says the path is sound, so the server is asked.
		asked bool
	}{
		{name: "no path", path: ""},
		{name: "a dot", path: "."},
		{name: "the parent", path: ".."},
		{name: "a climb", path: "../2/files/secret.txt"},
		{name: "a climb from a directory", path: "out/../../2/files/secret.txt"},
		{name: "a climb that would come back", path: "out/../latency.png"},
		{name: "a dot element", path: "out/./latency.png"},
		{name: "an absolute path", path: "/out/latency.png"},
		{name: "a doubled slash", path: "out//latency.png"},
		{name: "a trailing slash", path: "out/"},
		{name: "a sound path", path: "out/latency.png", asked: true},
		{name: "dots that are part of a name", path: "..out/a..b/...", asked: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			transport := &counting{}
			c := &Client{http: &http.Client{Transport: transport}, base: "http://contactd", where: "a test"}
			_, err := c.File(t.Context(), 1, tc.path, "")
			if tc.asked {
				if n := transport.n.Load(); n != 1 || !IsStatus(err, http.StatusNoContent) {
					t.Errorf("%d requests, err %v; want one request and its answer", n, err)
				}
				return
			}
			if !errors.Is(err, ErrFileNotFound) {
				t.Errorf("err = %v, want ErrFileNotFound", err)
			}
			if n := transport.n.Load(); n != 0 {
				t.Errorf("%d requests were made, want none", n)
			}
		})
	}
}

// against runs a handler as a server and returns a client for it, for the
// answers the real API never gives.
func against(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := New(Options{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestFileRequest(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, path, etag string
		uri              string
	}{
		{name: "a plain path", path: "out/latency.png", uri: "/v1/contacts/7/files/out/latency.png"},
		{name: "a tag", path: "out/latency.png", etag: `"18f-9b"`, uri: "/v1/contacts/7/files/out/latency.png"},
		{name: "each segment escaped, each slash kept", path: "a b/c%2fd/é?.png#x", uri: "/v1/contacts/7/files/a%20b/c%252fd/%C3%A9%3F.png%23x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			type seen struct {
				method, uri string
				tags        []string
			}
			got := make(chan seen, 1)
			c := against(t, func(w http.ResponseWriter, r *http.Request) {
				got <- seen{method: r.Method, uri: r.RequestURI, tags: r.Header.Values("If-None-Match")}
				_, _ = w.Write([]byte("x"))
			})
			if _, err := c.File(t.Context(), 7, tc.path, tc.etag); err != nil {
				t.Fatal(err)
			}
			req := <-got
			if req.method != http.MethodGet || req.uri != tc.uri {
				t.Errorf("sent %s %s, want GET %s", req.method, req.uri, tc.uri)
			}
			var want []string
			if tc.etag != "" {
				want = []string{tc.etag}
			}
			if strings.Join(req.tags, "|") != strings.Join(want, "|") {
				t.Errorf("If-None-Match = %q, want %q", req.tags, want)
			}
		})
	}
}

// endless writes until the client stops reading.
func endless(w http.ResponseWriter, _ *http.Request) {
	chunk := make([]byte, 64<<10)
	for {
		if _, err := w.Write(chunk); err != nil {
			return
		}
	}
}

func TestFileAnswers(t *testing.T) {
	t.Parallel()
	const tag = `"18f-9b"`
	for _, tc := range []struct {
		name    string
		etag    string
		handler http.HandlerFunc
		// check is handed what File returned.
		check func(t *testing.T, got File, err error)
	}{
		{
			// A wrong reading of the limit would read until the timeout.
			name:    "a body that never ends is cut at the limit",
			handler: endless,
			check: func(t *testing.T, got File, err error) {
				t.Helper()
				if !errors.Is(err, ErrFileTooLarge) || got.Data != nil {
					t.Errorf("%d bytes, err %v; want ErrFileTooLarge and nothing kept", len(got.Data), err)
				}
			},
		},
		{
			name: "a body one byte over the limit",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "image/png")
				// Flushed first, so the length is not declared.
				w.(http.Flusher).Flush()
				_, _ = w.Write(make([]byte, maxFileBytes+1))
			},
			check: func(t *testing.T, got File, err error) {
				t.Helper()
				if !errors.Is(err, ErrFileTooLarge) || got.Data != nil {
					t.Errorf("%d bytes, err %v; want ErrFileTooLarge and nothing kept", len(got.Data), err)
				}
			},
		},
		{
			name: "a body of exactly the limit, its length not declared",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.(http.Flusher).Flush()
				_, _ = w.Write(make([]byte, maxFileBytes))
			},
			check: func(t *testing.T, got File, err error) {
				t.Helper()
				if err != nil || len(got.Data) != maxFileBytes {
					t.Errorf("%d bytes, err %v; want all %d", len(got.Data), err, maxFileBytes)
				}
			},
		},
		{
			name: "a declared length over the limit",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Length", strconv.Itoa(maxFileBytes+1))
				_, _ = w.Write(make([]byte, maxFileBytes+1))
			},
			check: func(t *testing.T, got File, err error) {
				t.Helper()
				if !errors.Is(err, ErrFileTooLarge) || got.Data != nil {
					t.Errorf("%d bytes, err %v; want ErrFileTooLarge and nothing kept", len(got.Data), err)
				}
			},
		},
		{
			// What the server does when a file shrinks while it is sent.
			name: "a body shorter than its declared length",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Length", "600")
				_, _ = w.Write(make([]byte, 100))
			},
			check: func(t *testing.T, got File, err error) {
				t.Helper()
				if err == nil || errors.Is(err, ErrFileNotFound) || errors.Is(err, ErrFileTooLarge) || got.Data != nil {
					t.Errorf("%d bytes, err %v; want a failed read and nothing kept", len(got.Data), err)
				}
			},
		},
		{
			name: "a server error",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":"internal error"}`))
			},
			check: func(t *testing.T, _ File, err error) {
				t.Helper()
				if !IsStatus(err, http.StatusInternalServerError) || err.Error() != "internal error" {
					t.Errorf("err = %v, want the server's 500 and its message", err)
				}
			},
		},
		{
			name: "an error that is not JSON",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "<html>bad gateway</html>", http.StatusBadGateway)
			},
			check: func(t *testing.T, _ File, err error) {
				t.Helper()
				if !IsStatus(err, http.StatusBadGateway) || err.Error() != "the server answered 502 Bad Gateway" {
					t.Errorf("err = %v, want the 502 said plainly", err)
				}
			},
		},
		{
			name: "a 404 from something that is not contactd",
			handler: func(w http.ResponseWriter, r *http.Request) {
				http.NotFound(w, r)
			},
			check: func(t *testing.T, _ File, err error) {
				t.Helper()
				if !errors.Is(err, ErrFileNotFound) {
					t.Errorf("err = %v, want ErrFileNotFound", err)
				}
			},
		},
		{
			name: "a 304 nobody asked for",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNotModified)
			},
			check: func(t *testing.T, got File, err error) {
				t.Helper()
				if !IsStatus(err, http.StatusNotModified) || got.NotModified {
					t.Errorf("NotModified %v, err %v; want an error: there is no copy for it to be about", got.NotModified, err)
				}
			},
		},
		{
			name: "a 304 with no tag of its own",
			etag: tag,
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNotModified)
			},
			check: func(t *testing.T, got File, err error) {
				t.Helper()
				if err != nil || !got.NotModified || got.ETag != tag || got.Data != nil || !got.Modified.IsZero() {
					t.Errorf("got %+v, %v; want NotModified under the tag that was sent", got, err)
				}
			},
		},
		{
			// A caller keeps the tag with the file, so a tag has a limit too.
			name: "a tag and a type too long to be the server's",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("ETag", `"`+strings.Repeat("e", maxValidatorBytes)+`"`)
				w.Header().Set("Content-Type", strings.Repeat("t", maxValidatorBytes+1))
				_, _ = w.Write([]byte("x"))
			},
			check: func(t *testing.T, got File, err error) {
				t.Helper()
				if err != nil || string(got.Data) != "x" || got.ETag != "" || got.ContentType != "" {
					t.Errorf("%d bytes, a tag of %d and a type of %d, %v; want the bytes and neither header",
						len(got.Data), len(got.ETag), len(got.ContentType), err)
				}
			},
		},
		{
			name: "a tag and a type of exactly the limit",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("ETag", `"`+strings.Repeat("e", maxValidatorBytes-2)+`"`)
				w.Header().Set("Content-Type", strings.Repeat("t", maxValidatorBytes))
				_, _ = w.Write([]byte("x"))
			},
			check: func(t *testing.T, got File, err error) {
				t.Helper()
				if err != nil || len(got.ETag) != maxValidatorBytes || len(got.ContentType) != maxValidatorBytes {
					t.Errorf("a tag of %d and a type of %d, %v; want both kept at %d", len(got.ETag), len(got.ContentType), err, maxValidatorBytes)
				}
			},
		},
		{
			name: "a 304 with a tag too long to be the server's",
			etag: tag,
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("ETag", `"`+strings.Repeat("e", maxValidatorBytes)+`"`)
				w.WriteHeader(http.StatusNotModified)
			},
			check: func(t *testing.T, got File, err error) {
				t.Helper()
				if err != nil || !got.NotModified || got.ETag != tag {
					t.Errorf("got NotModified %v, a tag of %d, %v; want NotModified under the tag that was sent", got.NotModified, len(got.ETag), err)
				}
			},
		},
		{
			name: "no validators at all",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Last-Modified", "yesterday")
				_, _ = w.Write([]byte("x"))
			},
			check: func(t *testing.T, got File, err error) {
				t.Helper()
				if err != nil || string(got.Data) != "x" || got.ETag != "" || !got.Modified.IsZero() {
					t.Errorf("got %+v, %v; want the bytes, no tag and no time", got, err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := against(t, tc.handler).File(t.Context(), 7, "out/latency.png", tc.etag)
			tc.check(t, got, err)
		})
	}
}

// counted is a transport that counts the bytes File takes from the body of
// an answer.
type counted struct {
	next http.RoundTripper
	read atomic.Int64
}

func (c *counted) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := c.next.RoundTrip(req)
	if err != nil {
		return nil, fmt.Errorf("counted: %w", err)
	}
	resp.Body = &countedBody{ReadCloser: resp.Body, read: &c.read}
	return resp, nil
}

type countedBody struct {
	io.ReadCloser

	read *atomic.Int64
}

func (b *countedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.read.Add(int64(n))
	return n, err
}

// unpacking answers with a small compressed body that unpacks to 64 MiB.
// The body is compressed once, before any request, so the fetch's own
// deadline covers only what File does with it: compressing 64 MiB under the
// race detector, on a busy machine, can take longer than the deadline.
func unpacking(t *testing.T) http.HandlerFunc {
	t.Helper()
	var packed bytes.Buffer
	zw := gzip.NewWriter(&packed)
	chunk := make([]byte, 1<<20)
	for range 64 {
		if _, err := zw.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(packed.Bytes())
	}
}

// Saying "too large" is not enough: a client that read 64 MiB to find that
// out would have held 64 MiB. It reads one byte past the limit, to tell a
// body that goes on from one that ends there, and no more.
func TestFileReadsNoFurtherThanTheLimit(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"a body that never ends", endless},
		// The transport unpacks it, so the limit has to be on what comes out.
		{"a compressed body that unpacks past the limit", unpacking(t)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := against(t, tc.handler)
			transport := &counted{next: http.DefaultTransport}
			c.http = &http.Client{Transport: transport}
			got, err := c.File(t.Context(), 7, "out/latency.png", "")
			if !errors.Is(err, ErrFileTooLarge) || got.Data != nil {
				t.Errorf("%d bytes, err %v; want ErrFileTooLarge and nothing kept", len(got.Data), err)
			}
			if n := transport.read.Load(); n != maxFileBytes+1 {
				t.Errorf("read %d bytes of the body, want %d: the limit and one byte to see that it goes on", n, maxFileBytes+1)
			}
		})
	}
}

// A redirect is an answer, and never followed: what it points at is not the
// file that was asked for.
func TestFileFollowsNoRedirect(t *testing.T) {
	t.Parallel()
	var followed atomic.Bool
	c := against(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/elsewhere" {
			followed.Store(true)
			_, _ = w.Write([]byte("another file"))
			return
		}
		http.Redirect(w, r, "/elsewhere", http.StatusTemporaryRedirect)
	})
	got, err := c.File(t.Context(), 7, "out/latency.png", "")
	if !IsStatus(err, http.StatusTemporaryRedirect) || got.Data != nil {
		t.Errorf("%q, err %v; want the 307 as an error", got.Data, err)
	}
	if followed.Load() {
		t.Error("the redirect was followed")
	}
	// The client's other requests are unchanged by it.
	if c.http.CheckRedirect != nil {
		t.Error("File changed how the whole client treats redirects")
	}
}

func TestFileUnreachable(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.NotFoundHandler())
	c, err := New(Options{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	srv.Close()
	if _, err := c.File(t.Context(), 7, "out/latency.png", ""); !errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	live := against(t, func(http.ResponseWriter, *http.Request) {})
	if _, err := live.File(ctx, 7, "out/latency.png", ""); !errors.Is(err, context.Canceled) || errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v, want the caller's cancellation and no claim that the server is down", err)
	}
}

// stalled is a transport whose server goes quiet. It gives up only when the
// request's context ends, as a real transport does.
type stalled struct {
	// headers says the answer's headers arrive, and then no body does.
	headers bool
}

func (s stalled) RoundTrip(req *http.Request) (*http.Response, error) {
	if !s.headers {
		<-req.Context().Done()
		return nil, req.Context().Err()
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, ContentLength: -1, Body: quiet{req.Context()}}, nil
}

// quiet is a body that never arrives.
type quiet struct{ ctx context.Context } //nolint:containedctx // a response body has no other way to learn its request ended

func (q quiet) Read([]byte) (int, error) {
	<-q.ctx.Done()
	return 0, q.ctx.Err()
}

func (quiet) Close() error { return nil }

// The clock here is synctest's, which jumps ahead when everything is
// waiting: the test takes no real time and measures none.
func TestFileTimesOut(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		transport stalled
	}{
		{"no answer", stalled{}},
		{"headers and then no body", stalled{headers: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				c := &Client{http: &http.Client{Transport: tc.transport}, base: "http://contactd", where: "a test"}
				start := time.Now()
				got, err := c.File(t.Context(), 7, "out/latency.png", "")
				if !errors.Is(err, context.DeadlineExceeded) || got.Data != nil {
					t.Errorf("%d bytes, err %v; want a timeout", len(got.Data), err)
				}
				if waited := time.Since(start); waited != 5*time.Second {
					t.Errorf("gave up after %v, want 5s", waited)
				}
			})
		})
	}
}
