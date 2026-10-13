package api

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strconv"
	"strings"

	"github.com/quinnovator/contactd/internal/core"
)

const (
	// maxFileBytes is the largest file the API serves.
	maxFileBytes = 8 << 20
	// sniffBytes is how much of a file decides its content type.
	sniffBytes = 512
)

// Files reads what a contact's tools left in its working directory.
type Files interface {
	// OpenFile opens a regular file for reading. name is slash-separated
	// and relative to the contact's directory. Whatever it will not open,
	// for whatever reason, is fs.ErrNotExist.
	OpenFile(id core.ContactID, name string) (fs.File, error)
}

// file serves one file from a contact's working directory. There is no
// listing: a client asks for a path a reply named.
//
// A tool wrote the file, so a model chose its bytes. Only an image of a
// known type is served as what it is. Everything else is a download, and
// nothing is ever a page a browser would run on this origin.
// No other origin may embed what is served.
func (s *Server) file(w http.ResponseWriter, r *http.Request) {
	// On every answer, a refusal too: a web page on another origin cannot
	// embed a contact's file from the local address, nor learn from an
	// image that loads or fails whether a file is there.
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	id, ok := s.contactID(w, r)
	if !ok {
		return
	}
	if _, err := s.store.Contact(r.Context(), id); err != nil {
		s.fail(w, r, statusOf(err), err)
		return
	}
	if s.files == nil {
		fileNotFound(w)
		return
	}
	f, err := s.files.OpenFile(id, r.PathValue("path"))
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			s.log.ErrorContext(r.Context(), "open file", "contact_id", uint64(id), "error", err)
		}
		fileNotFound(w)
		return
	}
	defer func() { _ = f.Close() }() // read-only; nothing to lose
	info, err := f.Stat()
	if err != nil {
		s.log.ErrorContext(r.Context(), "stat file", "contact_id", uint64(id), "error", err)
		fileNotFound(w)
		return
	}
	size := info.Size()
	if size > maxFileBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, errorResponse{
			Error: fmt.Sprintf("file is %d bytes; the limit is %d", size, maxFileBytes),
		})
		return
	}
	// A tool may rewrite a file under the same name, so a client keeps a
	// copy only as long as this still names it.
	etag := `"` + strconv.FormatInt(info.ModTime().UnixNano(), 16) + "-" + strconv.FormatInt(size, 16) + `"`
	h := w.Header()
	h.Set("ETag", etag)
	h.Set("Last-Modified", info.ModTime().UTC().Format(http.TimeFormat))
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	// Last-Modified is for people. It counts in seconds, so it is never
	// what a conditional request is answered by.
	if etagListed(r.Header.Values("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	head := make([]byte, min(sniffBytes, size))
	n, _ := io.ReadFull(f, head) // a file that shrank is dealt with below
	head = head[:n]
	ctype := http.DetectContentType(head)
	switch ctype {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "image/bmp":
	default:
		ctype = "application/octet-stream"
		h.Set("Content-Disposition", "attachment")
	}
	h.Set("Content-Type", ctype)
	h.Set("Content-Length", strconv.FormatInt(size, 10))
	if _, err := w.Write(head); err != nil {
		return // the client left
	}
	// The size was read once. A file that grows while it is copied is cut
	// there. One that shrinks ends the response short of its declared
	// length, which the client sees as a failed read.
	_, _ = io.CopyN(w, f, size-int64(n))
}

// fileNotFound answers 404 for a file. It writes the body itself: fail
// would say the contact was not found, and it was.
func fileNotFound(w http.ResponseWriter) {
	writeJSON(w, http.StatusNotFound, errorResponse{Error: "file not found"})
}

// etagListed reports whether an If-None-Match header names etag, or names
// every file with "*". A weak tag matches its strong form, as that header's
// comparison is defined.
func etagListed(headers []string, etag string) bool {
	for _, header := range headers {
		for tag := range strings.SplitSeq(header, ",") {
			tag = strings.TrimPrefix(strings.TrimSpace(tag), "W/")
			if tag == "*" || tag == etag {
				return true
			}
		}
	}
	return false
}
