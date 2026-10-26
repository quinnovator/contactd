package helpers

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/quinnovator/contactd/internal/core"
)

// ExtensionURI names the optional standing-helper schedule and progress contract.
const (
	ExtensionURI    = "https://github.com/quinnovator/contactd/blob/main/docs/A2A.md#helper-extension-v1"
	timestampLayout = "2006-01-02T15:04:05.000Z"
)

type schedule struct {
	title     string
	every     time.Duration
	until     time.Time
	maxChecks int
}

func parseSchedule(meta map[string]any, text string, now time.Time, minEvery time.Duration) (schedule, error) {
	title := []rune(strings.Join(strings.Fields(text), " "))
	if len(title) > 60 {
		title = title[:60]
		if i := strings.LastIndex(string(title), " "); i > 0 {
			title = []rune(string(title)[:i])
		}
	}
	p := schedule{title: string(title), every: 15 * time.Minute, until: now.UTC().Add(24 * time.Hour)}
	data, exists := meta[ExtensionURI]
	if exists {
		m, ok := data.(map[string]any)
		if !ok {
			return p, fmt.Errorf("%w: schedule must be an object", a2a.ErrInvalidParams)
		}
		for _, key := range []string{"title", "every", "until"} {
			v, exists := m[key]
			if !exists {
				continue
			}
			text, ok := v.(string)
			if !ok {
				return p, fmt.Errorf("%w: %s must be a string", a2a.ErrInvalidParams, key)
			}
			var err error
			switch key {
			case "title":
				p.title = strings.TrimSpace(text)
			case "every":
				p.every, err = time.ParseDuration(text)
			case "until":
				p.until, err = time.Parse(time.RFC3339Nano, text)
			}
			if err != nil {
				return p, fmt.Errorf("%w: invalid %s: %w", a2a.ErrInvalidParams, key, err)
			}
		}
		if v, exists := m["maxChecks"]; exists {
			raw, err := json.Marshal(v)
			if err != nil {
				return p, fmt.Errorf("%w: maxChecks must be an integer", a2a.ErrInvalidParams)
			}
			if err := json.Unmarshal(raw, &p.maxChecks); err != nil || p.maxChecks < 1 || p.maxChecks > 500 {
				return p, fmt.Errorf("%w: maxChecks must be 1 to 500", a2a.ErrInvalidParams)
			}
		}
	}
	if len([]rune(p.title)) < 1 || len([]rune(p.title)) > 60 || strings.ContainsFunc(p.title, unicode.IsControl) {
		return p, fmt.Errorf("%w: title must be 1 to 60 characters without controls", a2a.ErrInvalidParams)
	}
	if p.every < minEvery || p.every > 24*time.Hour {
		return p, fmt.Errorf("%w: every must be from %s to 24h", a2a.ErrInvalidParams, minEvery)
	}
	_, offset := p.until.Zone()
	if offset != 0 || !p.until.After(now) || p.until.After(now.Add(168*time.Hour)) {
		return p, fmt.Errorf("%w: until must be UTC, after now and within 168h", a2a.ErrInvalidParams)
	}
	if p.maxChecks == 0 {
		p.maxChecks = min(500, int(math.Ceil(float64(p.until.Sub(now))/float64(p.every)))+1)
	}
	return p, nil
}

func taskMetadata(t core.HelperTask) map[string]any {
	m := map[string]any{"title": t.Title, "every": (time.Duration(t.EveryMS) * time.Millisecond).String(), "until": time.UnixMilli(t.UntilTS).UTC().Format(timestampLayout), "maxChecks": t.MaxChecks, "checks": t.Checks, "helperContact": t.Contact}
	if t.NextDue > 0 {
		m["nextCheck"] = time.UnixMilli(t.NextDue).UTC().Format(timestampLayout)
	}
	if t.LastCheckAt > 0 {
		m["lastCheck"] = time.UnixMilli(t.LastCheckAt).UTC().Format(timestampLayout)
	}
	if t.LastOutcome != "" {
		m["lastOutcome"] = t.LastOutcome
	}
	return map[string]any{ExtensionURI: m}
}

func messageText(m *a2a.Message) (string, error) {
	if m == nil || m.ID == "" || m.Role != a2a.MessageRoleUser {
		return "", fmt.Errorf("%w: messageId and ROLE_USER message required", a2a.ErrInvalidParams)
	}
	parts := make([]string, 0, len(m.Parts))
	for _, part := range m.Parts {
		if part == nil {
			return "", fmt.Errorf("%w: empty part", a2a.ErrInvalidParams)
		}
		switch v := part.Content.(type) {
		case a2a.Text:
			parts = append(parts, string(v))
		case a2a.Data:
			raw, err := json.MarshalIndent(v.Value, "", "  ")
			if err != nil {
				return "", fmt.Errorf("%w: invalid data part", a2a.ErrInvalidParams)
			}
			parts = append(parts, "```json\n"+string(raw)+"\n```")
		case a2a.Raw, a2a.URL:
			return "", a2a.ErrUnsupportedContentType
		default:
			return "", a2a.ErrInvalidParams
		}
	}
	text := strings.Join(parts, "\n\n")
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("%w: message needs text", a2a.ErrInvalidParams)
	}
	return text, nil
}
