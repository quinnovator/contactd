// Package helpers serves one durable A2A agent. Each task owns a contact;
// SQLite owns task state and the contact log owns reports. Push is at least
// once, with deterministic artifact IDs. Terminal tasks cannot be resumed.
package helpers

import (
	"time"

	"github.com/quinnovator/contactd/internal/core"
)

// Config describes the helper agent and its resource limits.
type Config struct {
	BaseURL, Auth, Token, Version             string
	PushAllow                                 []string
	MinEvery                                  time.Duration
	MaxActive, Concurrency                    int
	HelperProvider, HelperModel, HelperEffort string
	HelperTools                               []core.ToolDef
}

// DefaultConfig returns the deployment defaults.
func DefaultConfig() Config {
	return Config{BaseURL: "http://127.0.0.1:8080", Auth: "token", Version: "0.1.0", MinEvery: time.Minute, MaxActive: 16, Concurrency: 4}
}
