package requester

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/quinnovator/contactd/internal/core"
)

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func redact(v any) {
	switch x := v.(type) {
	case map[string]any:
		for k, value := range x {
			if strings.EqualFold(k, "credentials") || strings.EqualFold(k, "token") {
				x[k] = "…"
			} else {
				redact(value)
			}
		}
	case []any:
		for _, value := range x {
			redact(value)
		}
	}
}

func wire(v any) string {
	if v == nil {
		return ""
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	redact(value)
	raw, err = json.Marshal(value)
	if err != nil {
		return ""
	}
	return clip(string(raw), 2048)
}

func (r *Requester) observe(ctx context.Context, h core.RemoteHelper, event, method, dir string, payload any, text string) {
	d := core.HelperDelta{Handle: h.Handle, Contact: h.HelperContact, Title: h.Title, TaskID: h.TaskID, State: h.State, Event: event, Method: method, Dir: dir, Text: clip(strings.Join(strings.Fields(text), " "), 200), Wire: wire(payload), NextCheck: h.NextCheck, Checks: h.Checks, Reports: h.Reports, Outcome: h.LastOutcome}
	if err := r.registry.Observe(ctx, h.Parent, d); err != nil {
		r.log.DebugContext(ctx, "observe helper exchange", "error", err)
	}
}
