// Package api is the HTTP surface: plain net/http with pattern routing, SSE
// and one WebSocket endpoint. There are no thread endpoints; that's the
// point. A client sends messages to a contact ID and never creates, picks or
// names a thread.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/quinnovator/contactd/internal/actor"
	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/llm"
	"github.com/quinnovator/contactd/internal/obs"
)

// Limits on what a client may send.
const (
	maxBodyBytes    = 1 << 20
	maxMessageBytes = 64 << 10
	maxPageSize     = 500
	heartbeat       = 15 * time.Second
)

// Store is the part of the event log the API reads and writes directly.
type Store interface {
	CreateContact(ctx context.Context, agent core.AgentConfig, profile string, now int64) (core.ContactID, error)
	Contact(ctx context.Context, id core.ContactID) (core.Contact, error)
	EventsAfter(ctx context.Context, id core.ContactID, after uint64, limit int) ([]core.Event, error)
}

// Contacts is the part of the actor registry the API drives.
type Contacts interface {
	Message(ctx context.Context, id core.ContactID, text string) (uint64, error)
	Typing(ctx context.Context, id core.ContactID) error
	Subscribe(ctx context.Context, id core.ContactID) (*actor.Subscription, error)
	Unsubscribe(id core.ContactID, sub *actor.Subscription)
}

// Routes resolves the provider and model an agent config names. *llm.Client
// implements it.
type Routes interface {
	Route(ctx context.Context, provider, model string) (llm.Route, error)
}

// Server holds the handlers' dependencies.
type Server struct {
	store    Store
	contacts Contacts
	routes   Routes
	log      *slog.Logger
	draining atomic.Bool
}

// New builds the API server. routes checks the model an agent config names
// when a contact is created, so that a typo is a 400 there and not a failed
// first turn.
func New(store Store, contacts Contacts, routes Routes, log *slog.Logger) *Server {
	return &Server{store: store, contacts: contacts, routes: routes, log: log}
}

// Drain makes /healthz report 503 so load balancers stop sending traffic.
// It is the first step of graceful shutdown.
func (s *Server) Drain() { s.draining.Store(true) }

// Handler returns the routed handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/contacts", s.createContact)
	mux.HandleFunc("POST /v1/contacts/{id}/messages", s.postMessage)
	mux.HandleFunc("GET /v1/contacts/{id}/stream", s.stream)
	mux.HandleFunc("GET /v1/contacts/{id}/live", s.live)
	mux.HandleFunc("GET /v1/contacts/{id}/events", s.events)
	mux.Handle("GET /metrics", obs.Handler())
	mux.HandleFunc("GET /healthz", s.healthz)
	return mux
}

type createContactRequest struct {
	Agent   core.AgentConfig `json:"agent"`
	Profile string           `json:"profile"`
}

type createContactResponse struct {
	ID core.ContactID `json:"id"`
}

func (s *Server) createContact(w http.ResponseWriter, r *http.Request) {
	var req createContactRequest
	if !s.decode(w, r, &req) {
		return
	}
	if err := validateAgent(&req.Agent); err != nil {
		s.fail(w, r, http.StatusBadRequest, err)
		return
	}
	if _, err := s.routes.Route(r.Context(), req.Agent.Provider, req.Agent.Model); err != nil {
		s.fail(w, r, http.StatusBadRequest, fmt.Errorf("agent: %w", err))
		return
	}
	id, err := s.store.CreateContact(r.Context(), req.Agent, req.Profile, time.Now().UnixMilli())
	if err != nil {
		s.fail(w, r, statusOf(err), err)
		return
	}
	writeJSON(w, http.StatusCreated, createContactResponse{ID: id})
}

func validateAgent(a *core.AgentConfig) error {
	seen := make(map[string]struct{}, len(a.Tools))
	for i := range a.Tools {
		t := &a.Tools[i]
		if t.Name == "" {
			return errors.New("agent.tools: every tool needs a name")
		}
		if _, dup := seen[t.Name]; dup {
			return fmt.Errorf("agent.tools: duplicate tool %q", t.Name)
		}
		seen[t.Name] = struct{}{}
		if len(t.Input) > 0 {
			return fmt.Errorf("agent.tools: %q declares an input schema; tools take code, and only the built-in delegate tool takes anything else", t.Name)
		}
		if t.Env.Tier > 1 {
			return fmt.Errorf("agent.tools: %q has tier %d; tiers are 0 and 1", t.Name, t.Env.Tier)
		}
		if t.Env.Base == "" {
			return fmt.Errorf("agent.tools: %q needs env.base", t.Name)
		}
		if t.Env.Network && t.Env.Tier == 0 {
			return fmt.Errorf("agent.tools: %q asks for network, which only tier 1 has", t.Name)
		}
	}
	if a.MaxTokens < 0 {
		return errors.New("agent.max_tokens must not be negative")
	}
	if _, err := llm.ParseEffort(a.Effort); err != nil {
		return fmt.Errorf("agent.effort: %w", err)
	}
	if a.Parent != 0 {
		return errors.New("agent.parent is set by the runtime when it starts a sub-agent")
	}
	return nil
}

type postMessageRequest struct {
	Text string `json:"text"`
}

type postMessageResponse struct {
	Seq uint64 `json:"seq"`
}

// postMessage returns 202 once the message is durable: it has been accepted
// and logged, and the reply will arrive on the contact's stream.
func (s *Server) postMessage(w http.ResponseWriter, r *http.Request) {
	id, ok := s.contactID(w, r)
	if !ok {
		return
	}
	var req postMessageRequest
	if !s.decode(w, r, &req) {
		return
	}
	if err := validateText(req.Text); err != nil {
		s.fail(w, r, http.StatusBadRequest, err)
		return
	}
	seq, err := s.contacts.Message(r.Context(), id, req.Text)
	if err != nil {
		s.fail(w, r, statusOf(err), err)
		return
	}
	writeJSON(w, http.StatusAccepted, postMessageResponse{Seq: seq})
}

func validateText(text string) error {
	switch {
	case text == "":
		return errors.New("text must not be empty")
	case len(text) > maxMessageBytes:
		return fmt.Errorf("text is %d bytes; the limit is %d", len(text), maxMessageBytes)
	case strings.ContainsRune(text, 0):
		// The tokenizer reads C strings, so everything after a NUL would be
		// silently invisible to recall.
		return errors.New("text must not contain NUL characters")
	default:
		return nil
	}
}

type eventJSON struct {
	Seq  uint64          `json:"seq"`
	TS   int64           `json:"ts"`
	Kind string          `json:"kind"`
	Body json.RawMessage `json:"body"`
}

type eventsResponse struct {
	Events []eventJSON `json:"events"`
	// NextAfter is the cursor for the next page; absent on the last page.
	NextAfter *uint64 `json:"next_after,omitempty"`
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	id, ok := s.contactID(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	after, limit := uint64(0), 100
	if v := q.Get("after"); v != "" {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			s.fail(w, r, http.StatusBadRequest, errors.New("after must be a sequence number"))
			return
		}
		after = n
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			s.fail(w, r, http.StatusBadRequest, errors.New("limit must be a positive integer"))
			return
		}
		limit = min(n, maxPageSize)
	}
	if _, err := s.store.Contact(r.Context(), id); err != nil {
		s.fail(w, r, statusOf(err), err)
		return
	}
	events, err := s.store.EventsAfter(r.Context(), id, after, limit)
	if err != nil {
		s.fail(w, r, statusOf(err), err)
		return
	}
	resp := eventsResponse{Events: make([]eventJSON, len(events))}
	for i, ev := range events {
		resp.Events[i] = eventJSON{Seq: ev.Seq, TS: ev.TS, Kind: ev.Kind.String(), Body: ev.Body}
	}
	if len(events) == limit {
		resp.NextAfter = &events[len(events)-1].Seq
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	if s.draining.Load() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "draining"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) contactID(w http.ResponseWriter, r *http.Request) (core.ContactID, bool) {
	n, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil || n == 0 {
		s.fail(w, r, http.StatusNotFound, core.ErrContactNotFound)
		return 0, false
	}
	return core.ContactID(n), true
}

func (s *Server) decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		s.fail(w, r, http.StatusBadRequest, fmt.Errorf("invalid JSON body: %w", err))
		return false
	}
	return true
}

func statusOf(err error) int {
	switch {
	case errors.Is(err, core.ErrContactNotFound):
		return http.StatusNotFound
	case errors.Is(err, core.ErrClosed):
		return http.StatusServiceUnavailable
	case errors.Is(err, core.ErrBacklog):
		return http.StatusTooManyRequests
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return http.StatusRequestTimeout
	default:
		return http.StatusInternalServerError
	}
}

type errorResponse struct {
	Error string `json:"error"`
}

// fail writes an error response. Server-side failures are logged and
// reported generically; client errors are echoed back.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, status int, err error) {
	msg := err.Error()
	switch {
	case status >= http.StatusInternalServerError && status != http.StatusServiceUnavailable:
		s.log.ErrorContext(r.Context(), "request failed", "path", r.URL.Path, "status", status, "error", err)
		msg = "internal error"
	case status == http.StatusServiceUnavailable:
		msg = "shutting down"
	case status == http.StatusNotFound:
		msg = "contact not found"
	case status == http.StatusTooManyRequests:
		msg = "too many unanswered messages; wait for a reply"
	}
	writeJSON(w, status, errorResponse{Error: msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// A write error means the client left; there is no one to tell.
	_ = json.NewEncoder(w).Encode(v)
}
