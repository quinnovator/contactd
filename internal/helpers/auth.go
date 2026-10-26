package helpers

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// LoadToken loads or exclusively creates a private token file. Explicit tokens take precedence.
func LoadToken(path, explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	raw, err := os.ReadFile(path) //nolint:gosec // path is the operator-configured token file
	if err == nil {
		info, statErr := os.Stat(path)
		if statErr != nil {
			return "", fmt.Errorf("stat A2A token: %w", statErr)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
			return "", errors.New("A2A token file must be a private regular file (chmod 600)")
		}
		token := strings.TrimSpace(string(raw))
		if token == "" {
			return "", errors.New("empty A2A token file")
		}
		return token, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read A2A token: %w", err)
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", fmt.Errorf("generate A2A token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(secret)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // operator-configured path, created exclusively with private permissions
	if errors.Is(err, os.ErrExist) {
		return LoadToken(path, "")
	}
	if err != nil {
		return "", fmt.Errorf("create A2A token: %w", err)
	}
	_, writeErr := f.WriteString(token + "\n")
	closeErr := f.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return "", fmt.Errorf("write A2A token: %w", err)
	}
	return token, nil
}

func authorize(next http.Handler, cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cfg.Auth != "off" && (cfg.Token == "" || !Bearer(r.Header.Get("Authorization"), cfg.Token)) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="contactd helpers"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		next.ServeHTTP(w, r)
	})
}

// Bearer reports, in constant time for the credential, whether an
// Authorization header carries this bearer token. The scheme is case
// insensitive (RFC 9110).
func Bearer(header, token string) bool {
	scheme, credential, ok := strings.Cut(header, " ")
	return ok && token != "" && strings.EqualFold(scheme, "Bearer") && subtle.ConstantTimeCompare([]byte(credential), []byte(token)) == 1
}
