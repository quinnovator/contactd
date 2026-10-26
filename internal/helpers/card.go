package helpers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

// Card advertises both bindings with the exact protocol version the SDK implements.
func Card(cfg Config) *a2a.AgentCard {
	base := strings.TrimRight(cfg.BaseURL, "/")
	version := cfg.Version
	if version == "" {
		version = "0.1.0"
	}
	c := &a2a.AgentCard{
		Name: "contactd helpers", Description: "Standing helpers. Each task is one helper with its own memory that checks something on a schedule and reports only what changed. Tasks run for hours or days: send with returnImmediately and a push notification config, or stream.", Version: version,
		DocumentationURL: "https://github.com/quinnovator/contactd/blob/main/docs/A2A.md", Provider: &a2a.AgentProvider{Org: "contactd", URL: "https://github.com/quinnovator/contactd"},
		SupportedInterfaces: []*a2a.AgentInterface{a2a.NewAgentInterface(base+"/a2a/helpers", a2a.TransportProtocolJSONRPC), a2a.NewAgentInterface(base+"/a2a/helpers/rest", a2a.TransportProtocolHTTPJSON)},
		Capabilities:        a2a.AgentCapabilities{Streaming: true, PushNotifications: true, Extensions: []a2a.AgentExtension{{URI: ExtensionURI, Description: "Schedule a helper's checks and read its progress. Optional: without it a task checks every 15m for 24h.", Params: map[string]any{"minEvery": cfg.MinEvery.String(), "maxEvery": "24h", "maxDuration": "168h", "maxChecks": 500}}}},
		DefaultInputModes:   []string{"text/plain", "application/json"}, DefaultOutputModes: []string{"text/plain"},
		Skills: []a2a.AgentSkill{{ID: "watch", Name: "Watch and report", Description: "Check a web page or endpoint on a schedule and report meaningful changes. Stays silent when nothing changed. Asks with TASK_STATE_INPUT_REQUIRED when the brief is ambiguous.", Tags: []string{"monitoring", "schedule", "web"}, Examples: []string{"Check https://example.com/status every 5 minutes for the next 2 hours and tell me when it stops saying operational."}, InputModes: []string{"text/plain", "application/json"}, OutputModes: []string{"text/plain"}}},
	}
	if cfg.Auth != "off" {
		c.SecuritySchemes = a2a.NamedSecuritySchemes{"bearer": a2a.HTTPAuthSecurityScheme{Scheme: "Bearer", Description: "The token in a2a.token in the server's data directory."}}
		c.SecurityRequirements = a2a.SecurityRequirementsOptions{{"bearer": a2a.SecuritySchemeScopes{}}}
	}
	return c
}

func cardHandler(card *a2a.AgentCard) (http.Handler, error) {
	raw, err := json.Marshal(card)
	if err != nil {
		return nil, fmt.Errorf("marshal agent card: %w", err)
	}
	sum := sha256.Sum256(raw)
	etag := `"` + hex.EncodeToString(sum[:16]) + `"`
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=300")
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = w.Write(raw)
	}), nil
}
