package markdown

import (
	"io/fs"
	"net/url"
	"strings"
	"unicode"
)

// maxPath is the longest path, in bytes, the server's file endpoint takes.
const maxPath = 1024

// Fetchable reports whether an image destination can only mean a file in the
// contact's own directory, and returns the clean relative path.
//
// The destination is percent-decoded once. It is refused when it is empty,
// when its escapes are malformed, when it holds a ":", "?", "#", "\" or a
// control character, and when it begins with "//". One leading "/" is
// dropped: the contact's working directory is its root. What is left must be
// a plain relative path of at most 1024 bytes, with no "." or ".." element
// and no empty one.
//
// So a URL with a scheme (https:, file:, data:), a host (//host/x), a query
// or a fragment is never fetchable, and neither is a path that climbs out of
// the directory: a reply cannot make the client call out.
func Fetchable(dest string) (path string, ok bool) {
	p, err := url.PathUnescape(dest)
	if err != nil || p == "" {
		return "", false
	}
	if strings.HasPrefix(p, "//") || strings.ContainsAny(p, `:?#\`) || strings.ContainsFunc(p, unicode.IsControl) {
		return "", false
	}
	p = strings.TrimPrefix(p, "/")
	if len(p) > maxPath || p == "." || !fs.ValidPath(p) {
		return "", false
	}
	return p, true
}
