//go:build unix

package api_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/env/workdir"
)

// maxFileBytes is the largest file the API serves.
const maxFileBytes = 8 << 20

// secret is what another contact's file holds. No refusal may carry it.
const secret = "another contact's secret"

// reply is what the server answered. A test is handed the status, the
// headers and the body, and never the response, so none can leave one open.
type reply struct {
	status int
	header http.Header
	body   []byte
	// err is set when the body ended before its declared length.
	err error
}

// message is the error a JSON error body carries, or "" when the body is
// anything else.
func (r reply) message() string {
	var e struct {
		Error string `json:"error"`
	}
	if !strings.HasPrefix(r.header.Get("Content-Type"), "application/json") || json.Unmarshal(r.body, &e) != nil {
		return ""
	}
	return e.Error
}

// fetch sends one request exactly as written. Nothing cleans the path or
// follows a redirect, so a test sees what the server itself said.
func fetch(t *testing.T, method, url string, ifNoneMatch ...string) reply {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tag := range ifNoneMatch {
		req.Header.Add("If-None-Match", tag)
	}
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	return reply{status: resp.StatusCode, header: resp.Header, body: body, err: err}
}

// pngBytes is a small real PNG.
func pngBytes(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 64, 36))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// Bodies a browser would run if it were served them as what they are.
const (
	htmlPage = "<html><script>alert(1)</script>"
	svgImage = `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`
	xmlSVG   = `<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"/>`
)

// filesServer is a server over a directory of working directories.
type filesServer struct {
	*server

	root string
	// own has files, links and a FIFO. other has a secret. bare has no
	// directory, and linked's directory is a link to other's.
	own, other, bare, linked string
	png                      []byte
}

func startFiles(t *testing.T) *filesServer {
	t.Helper()
	s := &filesServer{server: start(t), root: t.TempDir(), png: pngBytes(t)}
	dirs, err := workdir.Open(s.root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dirs.Close() })
	s.api.WithFiles(dirs)
	s.own, s.other, s.bare, s.linked = s.contact(), s.contact(), s.contact(), s.contact()

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	write := func(name string, data []byte) {
		t.Helper()
		must(os.WriteFile(filepath.Join(s.root, filepath.FromSlash(name)), data, 0o600))
	}
	sized := func(name string, size int64) {
		t.Helper()
		must(os.Truncate(filepath.Join(s.root, filepath.FromSlash(name)), size))
	}
	must(os.MkdirAll(filepath.Join(s.root, s.own, "out"), 0o700))
	must(os.MkdirAll(filepath.Join(s.root, s.other), 0o700))
	write(s.own+"/out/latency.png", s.png)
	write(s.own+"/a b%2f..c.png", s.png)
	write(s.own+"/photo.jpg", []byte("\xff\xd8\xff\xe0 and the rest"))
	write(s.own+"/anim.gif", []byte("GIF89a and the rest"))
	write(s.own+"/pic.webp", []byte("RIFF\x00\x00\x00\x00WEBPVP8 and the rest"))
	write(s.own+"/pic.bmp", []byte("BM and the rest"))
	write(s.own+"/polyglot.png", append(bytes.Clone(s.png[:8]), htmlPage...))
	write(s.own+"/page.html", []byte(htmlPage))
	write(s.own+"/pic.svg", []byte(svgImage))
	write(s.own+"/xml.svg", []byte(xmlSVG))
	write(s.own+"/notes.txt", []byte("hello"))
	write(s.own+"/empty", nil)
	write(s.own+"/edge.bin", nil)
	sized(s.own+"/edge.bin", maxFileBytes)
	write(s.own+"/big.bin", nil)
	sized(s.own+"/big.bin", maxFileBytes+1)
	write(s.other+"/secret.txt", []byte(secret))
	// A guest cannot make these, but a host process could.
	must(os.Symlink("out/latency.png", filepath.Join(s.root, s.own, "link-in")))
	must(os.Symlink("../"+s.other+"/secret.txt", filepath.Join(s.root, s.own, "link-out")))
	must(os.Symlink("../"+s.other, filepath.Join(s.root, s.own, "dirlink-out")))
	must(os.Symlink(s.other, filepath.Join(s.root, s.linked)))
	must(syscall.Mkfifo(filepath.Join(s.root, s.own, "fifo"), 0o600))
	return s
}

func TestFilesServed(t *testing.T) {
	t.Parallel()
	s := startFiles(t)

	const octet = "application/octet-stream"
	for _, tc := range []struct {
		name, path string
		ctype      string
		body       []byte
	}{
		{name: "a PNG", path: s.own + "/files/out/latency.png", ctype: "image/png", body: s.png},
		{name: "a name that needs escaping", path: s.own + "/files/a%20b%252f..c.png", ctype: "image/png", body: s.png},
		{name: "a link that stays inside", path: s.own + "/files/link-in", ctype: "image/png", body: s.png},
		{name: "a JPEG", path: s.own + "/files/photo.jpg", ctype: "image/jpeg", body: []byte("\xff\xd8\xff\xe0 and the rest")},
		{name: "a GIF", path: s.own + "/files/anim.gif", ctype: "image/gif", body: []byte("GIF89a and the rest")},
		{name: "a WebP", path: s.own + "/files/pic.webp", ctype: "image/webp", body: []byte("RIFF\x00\x00\x00\x00WEBPVP8 and the rest")},
		{name: "a BMP", path: s.own + "/files/pic.bmp", ctype: "image/bmp", body: []byte("BM and the rest")},
		// It starts as a PNG, so it is one: the type is from the bytes and
		// the headers stop a browser from deciding otherwise.
		{name: "a PNG with a page after it", path: s.own + "/files/polyglot.png", ctype: "image/png", body: append(bytes.Clone(s.png[:8]), htmlPage...)},
		{name: "HTML", path: s.own + "/files/page.html", ctype: octet, body: []byte(htmlPage)},
		{name: "SVG", path: s.own + "/files/pic.svg", ctype: octet, body: []byte(svgImage)},
		{name: "SVG with an XML declaration", path: s.own + "/files/xml.svg", ctype: octet, body: []byte(xmlSVG)},
		{name: "text", path: s.own + "/files/notes.txt", ctype: octet, body: []byte("hello")},
		{name: "an empty file", path: s.own + "/files/empty", ctype: octet, body: []byte{}},
		{name: "a file of exactly the limit", path: s.own + "/files/edge.bin", ctype: octet, body: make([]byte, maxFileBytes)},
		{name: "the other contact's own file", path: s.other + "/files/secret.txt", ctype: octet, body: []byte(secret)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := fetch(t, http.MethodGet, s.url+"/v1/contacts/"+tc.path)
			if got.status != http.StatusOK || got.err != nil {
				t.Fatalf("status %d, read error %v, body %.80q; want 200", got.status, got.err, got.body)
			}
			if !bytes.Equal(got.body, tc.body) {
				t.Errorf("body is %d bytes starting %.40q, want %d starting %.40q", len(got.body), got.body, len(tc.body), tc.body)
			}
			for name, want := range map[string]string{
				"Content-Type":                 tc.ctype,
				"Content-Length":               strconv.Itoa(len(tc.body)),
				"Cache-Control":                "no-cache",
				"X-Content-Type-Options":       "nosniff",
				"Content-Security-Policy":      "default-src 'none'; sandbox",
				"Cross-Origin-Resource-Policy": "same-origin",
			} {
				if h := got.header.Get(name); h != want {
					t.Errorf("%s = %q, want %q", name, h, want)
				}
			}
			// Only an image is shown in place; anything else is a download.
			wantDisposition := ""
			if tc.ctype == octet {
				wantDisposition = "attachment"
			}
			if h := got.header.Get("Content-Disposition"); h != wantDisposition {
				t.Errorf("Content-Disposition = %q, want %q", h, wantDisposition)
			}
			if tag := got.header.Get("ETag"); len(tag) < 3 || !strings.HasPrefix(tag, `"`) || !strings.HasSuffix(tag, `"`) {
				t.Errorf("ETag = %q, want a quoted tag", tag)
			}
			if _, err := http.ParseTime(got.header.Get("Last-Modified")); err != nil {
				t.Errorf("Last-Modified = %q: %v", got.header.Get("Last-Modified"), err)
			}
		})
	}
}

func TestFilesRefused(t *testing.T) {
	t.Parallel()
	s := startFiles(t)

	const (
		noFile    = "file not found"
		noContact = "contact not found"
	)
	for _, tc := range []struct {
		name, method, path string
		// status is what the server answers. Zero means a redirect: the
		// router chooses which kind.
		status int
		// message is the JSON error. Empty means the router answered, in
		// its own words.
		message string
	}{
		{name: "a missing file", path: s.own + "/files/out/missing.png", status: http.StatusNotFound, message: noFile},
		{name: "no path", path: s.own + "/files/", status: http.StatusNotFound, message: noFile},
		{name: "a directory", path: s.own + "/files/out", status: http.StatusNotFound, message: noFile},
		{name: "a directory with a trailing slash", path: s.own + "/files/out/", status: http.StatusNotFound, message: noFile},
		{name: "a file used as a directory", path: s.own + "/files/notes.txt/x", status: http.StatusNotFound, message: noFile},
		{name: "an encoded climb to another contact", path: s.own + "/files/..%2f" + s.other + "%2fsecret.txt", status: http.StatusNotFound, message: noFile},
		{name: "an encoded climb from a directory", path: s.own + "/files/out%2f..%2f..%2f" + s.other + "%2fsecret.txt", status: http.StatusNotFound, message: noFile},
		{name: "an encoded absolute path", path: s.own + "/files/%2fetc%2fhosts", status: http.StatusNotFound, message: noFile},
		{name: "an encoded doubled slash", path: s.own + "/files/out%2f%2flatency.png", status: http.StatusNotFound, message: noFile},
		{name: "an encoded NUL", path: s.own + "/files/out/latency.png%00.txt", status: http.StatusNotFound, message: noFile},
		{name: "a path over 1024 bytes", path: s.own + "/files/" + strings.Repeat("a", 1025), status: http.StatusNotFound, message: noFile},
		{name: "a link to another contact", path: s.own + "/files/link-out", status: http.StatusNotFound, message: noFile},
		{name: "a directory link to another contact", path: s.own + "/files/dirlink-out/secret.txt", status: http.StatusNotFound, message: noFile},
		{name: "a FIFO nobody writes to", path: s.own + "/files/fifo", status: http.StatusNotFound, message: noFile},
		{name: "a contact directory that is a link", path: s.linked + "/files/secret.txt", status: http.StatusNotFound, message: noFile},
		{name: "a contact with no directory", path: s.bare + "/files/latency.png", status: http.StatusNotFound, message: noFile},
		{
			name: "a file one byte over the limit", path: s.own + "/files/big.bin", status: http.StatusRequestEntityTooLarge,
			message: "file is 8388609 bytes; the limit is 8388608",
		},

		{name: "an unknown contact", path: "999/files/out/latency.png", status: http.StatusNotFound, message: noContact},
		{name: "contact zero", path: "0/files/out/latency.png", status: http.StatusNotFound, message: noContact},
		{name: "a contact that is not a number", path: "ada/files/out/latency.png", status: http.StatusNotFound, message: noContact},
		{name: "a climb in the contact", path: "..%2f" + s.own + "/files/out/latency.png", status: http.StatusNotFound, message: noContact},
		// An unknown contact is told so whatever the path is.
		{name: "an unknown contact and a climb", path: "999/files/..%2f" + s.other + "%2fsecret.txt", status: http.StatusNotFound, message: noContact},

		{name: "a climb with encoded dots", path: s.own + "/files/%2e%2e/" + s.other + "/secret.txt", status: http.StatusNotFound, message: noFile},

		// The router tidies a path with plain dot elements, or with no
		// trailing slash, before any handler sees it, and sends the client
		// to the tidy one. No file is read for it.
		{name: "a plain climb", path: s.own + "/files/../" + s.other + "/secret.txt"},
		{name: "a dot", path: s.own + "/files/."},
		{name: "no listing", path: s.own + "/files"},
		{name: "a write", method: http.MethodPut, path: s.own + "/files/out/latency.png", status: http.StatusMethodNotAllowed},
		{name: "a delete", method: http.MethodDelete, path: s.own + "/files/out/latency.png", status: http.StatusMethodNotAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			method := tc.method
			if method == "" {
				method = http.MethodGet
			}
			got := fetch(t, method, s.url+"/v1/contacts/"+tc.path)
			switch {
			case tc.status == 0:
				if got.status < 300 || got.status > 399 {
					t.Errorf("answered %d (%.80q), want a redirect", got.status, got.body)
				}
			case got.status != tc.status || got.message() != tc.message:
				t.Errorf("answered %d %q (%.80q), want %d %q", got.status, got.message(), got.body, tc.status, tc.message)
			}
			if bytes.Contains(got.body, []byte(secret)) {
				t.Errorf("the refusal carries another contact's file: %q", got.body)
			}
			if tag := got.header.Get("ETag"); tag != "" {
				t.Errorf("a refusal has an ETag, %q, which tells a file is there", tag)
			}
			// Every refusal in the endpoint's own words keeps other origins
			// out, so a page elsewhere cannot tell one refusal from another.
			if h := got.header.Get("Cross-Origin-Resource-Policy"); tc.message != "" && h != "same-origin" {
				t.Errorf("Cross-Origin-Resource-Policy = %q, want same-origin", h)
			}
		})
	}
}

func TestFilesNotModified(t *testing.T) {
	t.Parallel()
	s := startFiles(t)
	url := s.url + "/v1/contacts/" + s.own + "/files/out/latency.png"

	first := fetch(t, http.MethodGet, url)
	tag := first.header.Get("ETag")
	if first.status != http.StatusOK || tag == "" {
		t.Fatalf("first fetch: %d, ETag %q", first.status, tag)
	}

	for _, tc := range []struct {
		name string
		send []string
		want int
	}{
		{"the tag", []string{tag}, http.StatusNotModified},
		{"the tag, marked weak", []string{"W/" + tag}, http.StatusNotModified},
		{"the tag in a list", []string{`"stale", ` + tag}, http.StatusNotModified},
		{"the tag on a second header line", []string{`"stale"`, tag}, http.StatusNotModified},
		{"any tag", []string{"*"}, http.StatusNotModified},
		{"another tag", []string{`"stale"`}, http.StatusOK},
		{"the tag without its quotes", []string{strings.Trim(tag, `"`)}, http.StatusOK},
		{"no tag", nil, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := fetch(t, http.MethodGet, url, tc.send...)
			if got.status != tc.want {
				t.Fatalf("status %d, want %d", got.status, tc.want)
			}
			if h := got.header.Get("ETag"); h != tag {
				t.Errorf("ETag = %q, want %q", h, tag)
			}
			if h := got.header.Get("Cache-Control"); h != "no-cache" {
				t.Errorf("Cache-Control = %q, want no-cache", h)
			}
			switch tc.want {
			case http.StatusNotModified:
				if len(got.body) != 0 {
					t.Errorf("a 304 carries %d bytes", len(got.body))
				}
			default:
				if !bytes.Equal(got.body, s.png) {
					t.Errorf("body is %d bytes, want the file's %d", len(got.body), len(s.png))
				}
			}
		})
	}
}

// Every answer from the endpoint, a refusal too, tells a browser that no
// other origin may embed it. Without the header a web page could show a
// contact's image, or learn whether a file is there, through the local
// address.
func TestFilesCrossOriginResourcePolicy(t *testing.T) {
	t.Parallel()
	s := startFiles(t)
	bare := start(t) // a server with no files port
	bareID := bare.contact()

	picture := s.url + "/v1/contacts/" + s.own + "/files/out/latency.png"
	first := fetch(t, http.MethodGet, picture)
	tag := first.header.Get("ETag")
	if first.status != http.StatusOK || tag == "" {
		t.Fatalf("first fetch: %d, ETag %q", first.status, tag)
	}

	for _, tc := range []struct {
		name, method, url string
		send              []string
		want              int
	}{
		{name: "an image", url: picture, want: http.StatusOK},
		{name: "an image, headers only", method: http.MethodHead, url: picture, want: http.StatusOK},
		{name: "a download", url: s.url + "/v1/contacts/" + s.own + "/files/page.html", want: http.StatusOK},
		{name: "an empty file", url: s.url + "/v1/contacts/" + s.own + "/files/empty", want: http.StatusOK},
		{name: "not modified", url: picture, send: []string{tag}, want: http.StatusNotModified},
		{name: "not modified, any tag", url: picture, send: []string{"*"}, want: http.StatusNotModified},
		{name: "a missing file", url: s.url + "/v1/contacts/" + s.own + "/files/out/missing.png", want: http.StatusNotFound},
		{name: "another contact's file", url: s.url + "/v1/contacts/" + s.own + "/files/link-out", want: http.StatusNotFound},
		{name: "an unknown contact", url: s.url + "/v1/contacts/999/files/out/latency.png", want: http.StatusNotFound},
		{name: "a contact that is not a number", url: s.url + "/v1/contacts/ada/files/out/latency.png", want: http.StatusNotFound},
		{name: "no files port", url: bare.url + "/v1/contacts/" + bareID + "/files/out/latency.png", want: http.StatusNotFound},
		{name: "a file over the limit", url: s.url + "/v1/contacts/" + s.own + "/files/big.bin", want: http.StatusRequestEntityTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			method := tc.method
			if method == "" {
				method = http.MethodGet
			}
			got := fetch(t, method, tc.url, tc.send...)
			if got.status != tc.want {
				t.Fatalf("status %d (%.80q), want %d", got.status, got.body, tc.want)
			}
			if h := got.header.Values("Cross-Origin-Resource-Policy"); len(h) != 1 || h[0] != "same-origin" {
				t.Errorf("Cross-Origin-Resource-Policy = %q, want same-origin once", h)
			}
		})
	}
}

// A tag stops matching once the file is another file: first with a new time
// and the same size, then with the same time and a new size.
func TestFilesChanged(t *testing.T) {
	t.Parallel()
	s := startFiles(t)
	url := s.url + "/v1/contacts/" + s.own + "/files/out/latency.png"
	path := filepath.Join(s.root, s.own, "out", "latency.png")

	first := fetch(t, http.MethodGet, url)
	tag := first.header.Get("ETag")
	if first.status != http.StatusOK || tag == "" {
		t.Fatalf("first fetch: %d, ETag %q", first.status, tag)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	later := info.ModTime().Add(3 * time.Second)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	touched := fetch(t, http.MethodGet, url, tag)
	newer := touched.header.Get("ETag")
	if touched.status != http.StatusOK || newer == tag || !bytes.Equal(touched.body, s.png) {
		t.Fatalf("after a new modification time: %d, ETag %q (was %q), %d bytes", touched.status, newer, tag, len(touched.body))
	}
	if h, want := touched.header.Get("Last-Modified"), later.UTC().Format(http.TimeFormat); h != want {
		t.Errorf("Last-Modified = %q, want %q", h, want)
	}

	if err := os.WriteFile(path, []byte("GIF89a now"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	resized := fetch(t, http.MethodGet, url, newer)
	if resized.status != http.StatusOK || resized.header.Get("ETag") == newer || string(resized.body) != "GIF89a now" {
		t.Errorf("after a new size: %d, ETag %q (was %q), body %.40q", resized.status, resized.header.Get("ETag"), newer, resized.body)
	}
	if h := resized.header.Get("Content-Type"); h != "image/gif" {
		t.Errorf("Content-Type = %q, want the type of the new bytes", h)
	}
}

// A server that was given no files port still knows its contacts, and has no
// file for any of them.
func TestFilesWithoutPort(t *testing.T) {
	t.Parallel()
	s := start(t)
	id := s.contact()
	for _, tc := range []struct {
		name, path, message string
	}{
		{"a contact's file", id + "/files/out/latency.png", "file not found"},
		{"no path", id + "/files/", "file not found"},
		{"an unknown contact", "999/files/out/latency.png", "contact not found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := fetch(t, http.MethodGet, s.url+"/v1/contacts/"+tc.path)
			if got.status != http.StatusNotFound || got.message() != tc.message {
				t.Errorf("answered %d %q, want 404 %q", got.status, got.message(), tc.message)
			}
		})
	}
}

// scripted is a files port that answers from a table, for the cases a real
// directory cannot be made to produce on demand.
type scripted map[string]func() (fs.File, error)

func (p scripted) OpenFile(_ core.ContactID, name string) (fs.File, error) {
	open, ok := p[name]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return open()
}

// scriptedFile says it holds size bytes and then yields data, which may be
// more or fewer.
type scriptedFile struct {
	io.Reader

	size    int64
	statErr error
}

func (f *scriptedFile) Stat() (fs.FileInfo, error) {
	if f.statErr != nil {
		return nil, f.statErr
	}
	return scriptedInfo{size: f.size}, nil
}

func (f *scriptedFile) Close() error { return nil }

type scriptedInfo struct{ size int64 }

func (i scriptedInfo) Name() string     { return "scripted" }
func (i scriptedInfo) Size() int64      { return i.size }
func (scriptedInfo) Mode() fs.FileMode  { return 0o600 }
func (scriptedInfo) ModTime() time.Time { return time.Unix(1_760_000_000, 0) }
func (scriptedInfo) IsDir() bool        { return false }
func (scriptedInfo) Sys() any           { return nil }

// zeros is a reader that never ends.
type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

func TestFilesPortFailures(t *testing.T) {
	t.Parallel()
	s := start(t)
	long := strings.Repeat("x", 2000)
	s.api.WithFiles(scripted{
		"denied": func() (fs.File, error) { return nil, errors.New("permission denied: /data/fs/1/denied") },
		"unstattable": func() (fs.File, error) {
			return &scriptedFile{Reader: strings.NewReader("x"), statErr: errors.New("stale handle")}, nil
		},
		"grew":   func() (fs.File, error) { return &scriptedFile{Reader: strings.NewReader(long), size: 600}, nil },
		"shrank": func() (fs.File, error) { return &scriptedFile{Reader: strings.NewReader(long[:100]), size: 600}, nil },
		// At the limit when it was measured, and still being written.
		"outgrew": func() (fs.File, error) { return &scriptedFile{Reader: zeros{}, size: maxFileBytes}, nil },
		"huge":    func() (fs.File, error) { return &scriptedFile{Reader: strings.NewReader("x"), size: 1 << 40}, nil },
	})
	base := s.url + "/v1/contacts/" + s.contact() + "/files/"

	// A failure that is not "no such file" is still only that to a client:
	// the reason, and the path in it, stay in the server's log.
	for _, name := range []string{"denied", "unstattable", "unscripted"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := fetch(t, http.MethodGet, base+name)
			if got.status != http.StatusNotFound || got.message() != "file not found" {
				t.Errorf("answered %d %q (%.80q), want 404 file not found", got.status, got.message(), got.body)
			}
			if h := got.header.Get("Cross-Origin-Resource-Policy"); h != "same-origin" {
				t.Errorf("Cross-Origin-Resource-Policy = %q, want same-origin", h)
			}
		})
	}
	t.Run("a size too large to be true", func(t *testing.T) {
		t.Parallel()
		got := fetch(t, http.MethodGet, base+"huge")
		if got.status != http.StatusRequestEntityTooLarge {
			t.Errorf("status %d, want 413", got.status)
		}
		if h := got.header.Get("Cross-Origin-Resource-Policy"); h != "same-origin" {
			t.Errorf("Cross-Origin-Resource-Policy = %q, want same-origin", h)
		}
	})
	// The size is read once. A file that grows while it is served is cut
	// at that size.
	t.Run("a file that grows", func(t *testing.T) {
		t.Parallel()
		got := fetch(t, http.MethodGet, base+"grew")
		if got.status != http.StatusOK || got.err != nil || string(got.body) != long[:600] {
			t.Errorf("status %d, read error %v, %d bytes; want the first 600", got.status, got.err, len(got.body))
		}
	})
	// So a file that passes the limit after it was measured is never served
	// past the limit.
	t.Run("a file that grows past the limit", func(t *testing.T) {
		t.Parallel()
		got := fetch(t, http.MethodGet, base+"outgrew")
		if got.status != http.StatusOK || got.err != nil || len(got.body) != maxFileBytes {
			t.Errorf("status %d, read error %v, %d bytes; want exactly the limit of %d", got.status, got.err, len(got.body), maxFileBytes)
		}
		if h := got.header.Get("Content-Length"); h != strconv.Itoa(maxFileBytes) {
			t.Errorf("Content-Length = %q, want %d", h, maxFileBytes)
		}
	})
	// One that shrinks cannot be made whole, and must not pass for whole.
	t.Run("a file that shrinks", func(t *testing.T) {
		t.Parallel()
		got := fetch(t, http.MethodGet, base+"shrank")
		if got.status != http.StatusOK || got.header.Get("Content-Length") != "600" {
			t.Fatalf("status %d, Content-Length %q; want 200 and 600", got.status, got.header.Get("Content-Length"))
		}
		if got.err == nil {
			t.Errorf("a body of %d bytes read cleanly, want a failed read: 600 were promised", len(got.body))
		}
	})
}
