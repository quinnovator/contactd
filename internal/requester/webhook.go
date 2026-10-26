package requester

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/helpers"
	"github.com/quinnovator/contactd/internal/obs"
)

func partsText(parts a2a.ContentParts) string {
	var out []string
	for _, p := range parts {
		if p != nil {
			if text, ok := p.Content.(a2a.Text); ok {
				out = append(out, string(text))
			}
		}
	}
	return strings.Join(out, "\n")
}

func statusText(st a2a.TaskStatus) string {
	if st.Message == nil {
		return ""
	}
	return partsText(st.Message.Parts)
}

func applyTask(row *core.RemoteHelper, t *a2a.Task, local bool) {
	row.TaskID = string(t.ID)
	row.ContextID = t.ContextID
	applyStatus(row, t.Status, t.Metadata, local)
}

func applyStatus(row *core.RemoteHelper, st a2a.TaskStatus, meta map[string]any, local bool) {
	// A delayed SendMessage/GetTask response must not resurrect a terminal task.
	if a2a.TaskState(row.State).Terminal() && !st.State.Terminal() {
		return
	}
	row.State = string(st.State)
	row.StatusMessage = statusText(st)
	row.UpdatedAt = time.Now().UnixMilli()
	raw, err := json.Marshal(meta[helpers.ExtensionURI])
	if err != nil {
		return
	}
	var m struct {
		Every         string         `json:"every"`
		Until         string         `json:"until"`
		NextCheck     string         `json:"nextCheck"`
		LastCheck     string         `json:"lastCheck"`
		Checks        int            `json:"checks"`
		HelperContact core.ContactID `json:"helperContact"`
		LastOutcome   string         `json:"lastOutcome"`
	}
	if json.Unmarshal(raw, &m) == nil && string(raw) != "null" {
		if d, err := time.ParseDuration(m.Every); err == nil {
			row.EveryMS = d.Milliseconds()
		}
		if d, err := time.Parse(time.RFC3339Nano, m.Until); err == nil {
			row.UntilTS = d.UnixMilli()
		}
		row.NextCheck = 0
		if d, err := time.Parse(time.RFC3339Nano, m.NextCheck); err == nil {
			row.NextCheck = d.UnixMilli()
		}
		if d, err := time.Parse(time.RFC3339Nano, m.LastCheck); err == nil {
			row.LastCheckAt = d.UnixMilli()
		}
		row.Checks = max(row.Checks, m.Checks)
		row.LastOutcome = m.LastOutcome
		if local {
			row.HelperContact = m.HelperContact
		}
	}
	if st.State.Terminal() || st.State == a2a.TaskStateInputRequired {
		row.NextCheck = 0
	}
}

// Webhook receives authenticated StreamResponse pushes with durable acknowledgements.
func (r *Requester) Webhook() http.Handler { return http.HandlerFunc(r.webhook) }

func (r *Requester) webhook(w http.ResponseWriter, req *http.Request) {
	result := "ok"
	defer func() { obs.A2AWebhook.WithLabelValues(result).Inc() }()
	typ, _, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	if err != nil || (typ != "application/a2a+json" && typ != "application/json") {
		result = "bad_request"
		w.WriteHeader(http.StatusUnsupportedMediaType)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, req.Body, 1<<20))
	if err != nil {
		result = "bad_request"
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var sr a2a.StreamResponse
	decodeErr := json.Unmarshal(body, &sr)
	id := eventID(sr.Event)
	var row core.RemoteHelper
	if token := req.Header.Get("A2A-Notification-Token"); token != "" {
		row, err = r.store.RemoteHelperByToken(req.Context(), token)
	} else if decodeErr == nil && id != "" {
		row, err = r.store.RemoteHelperByTask(req.Context(), id)
	} else {
		result = "bad_request"
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if err != nil {
		if errors.Is(err, core.ErrTaskNotFound) {
			result = "not_found"
			w.WriteHeader(http.StatusNotFound)
		} else {
			result = "unavailable"
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		return
	}
	if subtle.ConstantTimeCompare([]byte(req.Header.Get("Authorization")), []byte("Bearer "+row.PushSecret)) != 1 {
		result = "unauthorized"
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if decodeErr != nil || id == "" {
		result = "bad_request"
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	unlock := r.lock(row.Handle)
	defer unlock()
	row, err = r.store.RemoteHelper(req.Context(), row.Handle)
	if err != nil {
		result = "unavailable"
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if row.TaskID != "" && row.TaskID != id {
		result = "not_found"
		w.WriteHeader(http.StatusNotFound)
		return
	}
	row.TaskID = id
	method := "statusUpdate"
	switch ev := sr.Event.(type) {
	case *a2a.TaskArtifactUpdateEvent:
		method = "artifactUpdate"
		if ev.Artifact == nil || ev.Artifact.ID == "" {
			result = "bad_request"
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		seen, err := r.store.SeenArtifact(req.Context(), row.Handle, string(ev.Artifact.ID))
		if err != nil {
			result = "unavailable"
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if seen {
			result = "duplicate"
			w.WriteHeader(http.StatusNoContent)
			return
		}
		text := fmt.Sprintf("Helper %d (%q) reports:\n%s", row.Handle, row.Title, clip(partsText(ev.Artifact.Parts), 16<<10))
		if err := r.notice(req.Context(), row, text); err != nil {
			result = "unavailable"
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if err := r.store.MarkArtifactSeen(req.Context(), row.Handle, string(ev.Artifact.ID)); err != nil {
			result = "unavailable"
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		row.Reports++
		row.LastReportAt = time.Now().UnixMilli()
	case *a2a.TaskStatusUpdateEvent:
		row.ContextID = ev.ContextID
		applyStatus(&row, ev.Status, ev.Metadata, r.cfg.AgentURL == r.cfg.BaseURL)
	case *a2a.Task:
		method = "task"
		applyTask(&row, ev, r.cfg.AgentURL == r.cfg.BaseURL)
	case *a2a.Message:
		result = "bad_request"
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if method != "artifactUpdate" && row.State != row.NotifiedState {
		text := ""
		switch a2a.TaskState(row.State) {
		case a2a.TaskStateUnspecified, a2a.TaskStateAuthRequired, a2a.TaskStateSubmitted, a2a.TaskStateWorking:
		case a2a.TaskStateInputRequired:
			text = fmt.Sprintf("Helper %d needs an answer before it can continue:\n%s\nAnswer with message_helper.", row.Handle, row.StatusMessage)
		case a2a.TaskStateCompleted, a2a.TaskStateFailed, a2a.TaskStateRejected:
			text = fmt.Sprintf("Helper %d has stopped (%s): %s", row.Handle, row.State, row.StatusMessage)
		case a2a.TaskStateCanceled:
			if !row.CancelRequested {
				text = fmt.Sprintf("Helper %d has stopped (%s): %s", row.Handle, row.State, row.StatusMessage)
			}
		}
		if text != "" {
			if err := r.notice(req.Context(), row, text); err != nil {
				result = "unavailable"
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		}
		// Also record WORKING: a later, second question must notify again.
		row.NotifiedState = row.State
	}
	if err := r.store.UpdateRemoteHelper(req.Context(), row); err != nil {
		result = "unavailable"
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	r.observe(req.Context(), row, "push", method, "in", sr, row.State)
	w.WriteHeader(http.StatusNoContent)
}

func eventID(ev a2a.Event) string {
	switch v := ev.(type) {
	case *a2a.Task:
		return string(v.ID)
	case *a2a.TaskStatusUpdateEvent:
		return string(v.TaskID)
	case *a2a.TaskArtifactUpdateEvent:
		return string(v.TaskID)
	case *a2a.Message:
		return ""
	}
	return ""
}

func (r *Requester) notice(ctx context.Context, row core.RemoteHelper, text string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err := r.registry.HelperNotice(ctx, row.Parent, core.NoticeBody{Source: "helper-update", Helper: row.Handle, Text: text})
	if err != nil {
		return fmt.Errorf("deliver helper notice: %w", err)
	}
	return nil
}
