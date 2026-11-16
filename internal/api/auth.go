package api

import (
	"crypto/sha256"
	"crypto/subtle"
	"net"
	"net/http"
	"path"
	"strings"
)

// RequireToken guards an API handler with a bearer token, for the TCP
// address. It is off when token is empty: the handler is returned as it is.
//
// Three kinds of request pass without the token:
//
//   - Requests on the Unix socket. Its file permissions are its access
//     control, and contactd chat finds the server there with nothing else.
//   - The A2A routes, /a2a/ and the agent card. They have their own bearer
//     token (-a2a-token), and the card is public by design.
//   - GET /healthz, so that a load balancer or container health check needs
//     no secret. It says only whether the server is up and accepting work.
//
// Everything else, /metrics and /debug/plans included, needs
// "Authorization: Bearer <token>". The token is compared in constant time.
func RequireToken(next http.Handler, token string) http.Handler {
	if token == "" {
		return next
	}
	// Hashing both sides first makes the comparison constant time in the
	// length of the token as well as its contents.
	want := sha256.Sum256([]byte(token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if onSocket(r) || exempt(r) || bearerMatches(r.Header.Get("Authorization"), want) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="contactd"`)
		writeJSON(w, http.StatusUnauthorized, errorResponse{Error: "missing or wrong API token"})
	})
}

// onSocket reports whether the request came in on a Unix socket.
func onSocket(r *http.Request) bool {
	addr, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	return ok && addr.Network() == "unix"
}

func exempt(r *http.Request) bool {
	// Cleaned, so that /a2a/../v1/contacts is not taken for an A2A route.
	p := path.Clean("/" + r.URL.Path)
	switch {
	case p == "/healthz" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		return true
	case p == "/.well-known/agent-card.json", strings.HasPrefix(p+"/", "/a2a/"):
		return true
	}
	return false
}

// bearerMatches reports whether an Authorization header carries the token
// whose SHA-256 is want. The scheme is case insensitive (RFC 9110).
func bearerMatches(header string, want [sha256.Size]byte) bool {
	scheme, credential, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return false
	}
	got := sha256.Sum256([]byte(strings.TrimSpace(credential)))
	return subtle.ConstantTimeCompare(got[:], want[:]) == 1
}
