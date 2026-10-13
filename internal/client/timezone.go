package client

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// localZone returns a loadable IANA name, never a machine-specific "Local".
func localZone() string {
	candidates := []string{strings.TrimPrefix(os.Getenv("TZ"), ":"), time.Local.String()}
	if path, err := filepath.EvalSymlinks("/etc/localtime"); err == nil {
		if _, zone, ok := strings.Cut(path, "/zoneinfo/"); ok {
			candidates = append(candidates, zone)
		}
	}
	if data, err := os.ReadFile("/etc/timezone"); err == nil {
		candidates = append(candidates, strings.TrimSpace(string(data)))
	}
	for _, name := range candidates {
		if name == "" || name == "Local" {
			continue
		}
		if _, err := time.LoadLocation(name); err == nil {
			return name
		}
	}
	return "UTC"
}
