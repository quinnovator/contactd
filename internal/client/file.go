package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/quinnovator/contactd/internal/core"
)

const (
	// maxFileBytes is the most of a file the client keeps, whatever the
	// server sends. It is the server's own limit.
	maxFileBytes = 8 << 20
	// fileTimeout bounds one fetch, the body included.
	fileTimeout = 5 * time.Second
	// maxRefusalBytes is the most of an error body the client reads.
	maxRefusalBytes = 64 << 10
	// maxValidatorBytes is the longest ETag or Content-Type the client
	// keeps. The server's own are a few dozen bytes.
	maxValidatorBytes = 256
)

// ErrFileNotFound means the server has no such file to give. It does not
// say why: a missing file, a directory, a path that leads out of the
// contact's working directory and an unknown contact all answer the same.
var ErrFileNotFound = errors.New("file not found")

// ErrFileTooLarge means the file is over 8 MiB: the server refused to send
// it, or sent more than that.
var ErrFileTooLarge = errors.New("file is too large")

// File is a file from a contact's working directory, or word that the copy
// the caller holds is still good.
type File struct {
	// Data is the file's bytes. It is nil when NotModified is set.
	Data []byte
	// ContentType is what the server says the bytes are: an image type it
	// recognized, or application/octet-stream for anything else. A tool
	// wrote the file, so treat it as a claim. It is empty when the server
	// did not say, or said more than 256 bytes.
	ContentType string
	// ETag names this version of the file. Hand it back to File to ask
	// whether the file has changed. It is empty when the server gave none,
	// or one over 256 bytes.
	ETag string
	// Modified is when the file was last written, to the second. It is the
	// zero time when the server did not say.
	Modified    time.Time
	NotModified bool // the server answered 304 to etag
}

// File fetches one file from a contact's working directory. path is
// slash-separated and relative to that directory, as a reply names it.
//
// It never returns more than 8 MiB, whatever the server says: a longer body
// is cut off there and reported as ErrFileTooLarge. It times out after 5 s,
// the body included, and escapes each path segment. etag may be empty; when
// it is the tag of a copy the caller holds and the file has not changed,
// the answer is a File with NotModified set and no Data.
//
// A path the server would refuse for its shape is ErrFileNotFound with no
// request made, and File follows no redirect. So a path with ".." in it can
// only ever be not found: it cannot be turned into a request for something
// else.
func (c *Client) File(ctx context.Context, id core.ContactID, path, etag string) (File, error) {
	if path == "." || !fs.ValidPath(path) {
		return File{}, ErrFileNotFound
	}
	ctx, cancel := context.WithTimeout(ctx, fileTimeout)
	defer cancel()

	segments := strings.Split(path, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	target := c.base + "/v1/contacts/" + id.String() + "/files/" + strings.Join(segments, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return File{}, fmt.Errorf("build request: %w", err)
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	// A copy, so that only this request refuses redirects. It shares the
	// client's connections.
	direct := *c.http
	direct.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := direct.Do(req)
	if err != nil {
		return File{}, c.unreachable(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }() // nothing to do about a failed close of a read body

	out := File{ContentType: short(resp.Header.Get("Content-Type")), ETag: short(resp.Header.Get("ETag"))}
	if t, err := http.ParseTime(resp.Header.Get("Last-Modified")); err == nil {
		out.Modified = t
	}
	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode == http.StatusNotModified && etag != "":
		out.NotModified = true
		if out.ETag == "" {
			out.ETag = etag
		}
		return out, nil
	case resp.StatusCode == http.StatusNotFound:
		return File{}, ErrFileNotFound
	case resp.StatusCode == http.StatusRequestEntityTooLarge:
		return File{}, ErrFileTooLarge
	default:
		return File{}, refusal(resp)
	}
	if resp.ContentLength > maxFileBytes {
		return File{}, ErrFileTooLarge
	}
	// One byte past the limit is read, and never kept: it is how a body
	// that goes on is told from one that ends exactly there.
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxFileBytes+1))
	if err != nil {
		return File{}, fmt.Errorf("read file: %w", err)
	}
	if len(data) > maxFileBytes {
		return File{}, ErrFileTooLarge
	}
	out.Data = data
	return out, nil
}

// short is a header value File keeps, or "" for one too long to be the
// server's. A caller holds the tag for as long as it holds the file, so the
// limit on the file's bytes would mean little without one on the tag.
func short(value string) string {
	if len(value) > maxValidatorBytes {
		return ""
	}
	return value
}

// refusal is the error for an answer File has no name for.
func refusal(resp *http.Response) error {
	var e struct {
		Error string `json:"error"`
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxRefusalBytes))
	if err != nil || json.Unmarshal(raw, &e) != nil || e.Error == "" {
		e.Error = "the server answered " + resp.Status
	}
	return &Error{Status: resp.StatusCode, Message: e.Error}
}
