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
	"unicode"
	"unicode/utf8"

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
	maxNameBytes    = 64
	heartbeat       = 15 * time.Second
)

// Store is the part of the event log the API reads and writes directly.
type Store interface {
	CreateNamed(ctx context.Context, name string, agent core.AgentConfig, profile string, now int64) (core.ContactID, error)
	RenameContact(ctx context.Context, id core.ContactID, name string) error
	Contact(ctx context.Context, id core.ContactID) (core.Contact, error)
	ContactByName(ctx context.Context, name string) (core.Contact, error)
	// Contacts lists the contacts a person talks to, most recently active
	// first. Sub-agents are not among them.
	Contacts(ctx context.Context, limit int) ([]core.Contact, error)
	EventsAfterOfKinds(ctx context.Context, id core.ContactID, after uint64, limit int, kinds core.KindSet) ([]core.Event, error)
	// EventsBefore returns, in log order, the newest events with seq <
	// before; a before of zero starts from the end of the log.
	EventsBefore(ctx context.Context, id core.ContactID, before uint64, limit int, kinds core.KindSet) ([]core.Event, error)
}

// Contacts is the part of the actor registry the API drives.
type Contacts interface {
	Message(ctx context.Context, id core.ContactID, text string) (uint64, error)
	Typing(ctx context.Context, id core.ContactID) error
	Interrupt(ctx context.Context, id core.ContactID) error
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
	mux.HandleFunc("GET /v1/contacts", s.listContacts)
	mux.HandleFunc("GET /v1/contacts/{id}", s.getContact)
	mux.HandleFunc("PATCH /v1/contacts/{id}", s.renameContact)
	mux.HandleFunc("POST /v1/contacts/{id}/messages", s.postMessage)
	mux.HandleFunc("POST /v1/contacts/{id}/interrupt", s.interrupt)
	mux.HandleFunc("GET /v1/contacts/{id}/stream", s.stream)
	mux.HandleFunc("GET /v1/contacts/{id}/live", s.live)
	mux.HandleFunc("GET /v1/contacts/{id}/events", s.events)
	mux.Handle("GET /metrics", obs.Handler())
	mux.HandleFunc("GET /healthz", s.healthz)
	return mux
}

type createContactRequest struct {
	Name    string           `json:"name"`
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
	if err := validateName(req.Name); err != nil {
		s.fail(w, r, http.StatusBadRequest, err)
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
	id, err := s.store.CreateNamed(r.Context(), req.Name, req.Agent, req.Profile, time.Now().UnixMilli())
	if err != nil {
		s.fail(w, r, statusOf(err), err)
		return
	}
	writeJSON(w, http.StatusCreated, createContactResponse{ID: id})
}

// validateName checks a contact's name. A name is optional. One made only
// of digits is refused, because a client that accepts "a name or an ID"
// could not tell which it had been given.
func validateName(name string) error {
	switch {
	case name == "":
		return nil
	case len(name) > maxNameBytes:
		return fmt.Errorf("name is %d bytes; the limit is %d", len(name), maxNameBytes)
	case !utf8.ValidString(name):
		return errors.New("name must be valid UTF-8")
	case strings.TrimSpace(name) != name:
		return errors.New("name must not begin or end with a space")
	case strings.ContainsFunc(name, unicode.IsControl):
		return errors.New("name must not contain control characters")
	case strings.Trim(name, "0123456789") == "":
		return errors.New("name must not be a number: it would read as a contact ID")
	default:
		return nil
	}
}

// contactJSON is a contact as the API shows it. Provider and Model are the
// route that serves it now, with the deployment's defaults filled in. Agent
// and Profile are left out of a listing.
type contactJSON struct {
	ID         core.ContactID    `json:"id"`
	Name       string            `json:"name,omitempty"`
	Provider   string            `json:"provider,omitempty"`
	Model      string            `json:"model,omitempty"`
	LastActive int64             `json:"last_active"`
	Agent      *core.AgentConfig `json:"agent,omitempty"`
	Profile    string            `json:"profile,omitempty"`
}

func (s *Server) contactJSON(ctx context.Context, c *core.Contact, full bool) contactJSON {
	out := contactJSON{ID: c.ID, Name: c.Name, Provider: c.Agent.Provider, Model: c.Agent.Model, LastActive: c.LastActive}
	// A contact whose provider is no longer configured is still listed, under
	// the names its agent config gives.
	if route, err := s.routes.Route(ctx, c.Agent.Provider, c.Agent.Model); err == nil {
		out.Provider, out.Model = route.Provider, route.Model
	}
	if full {
		out.Agent, out.Profile = &c.Agent, c.Profile
	}
	return out
}

type listContactsResponse struct {
	Contacts []contactJSON `json:"contacts"`
}

// listContacts returns the contacts a person talks to, most recently active
// first, or with ?name= the one contact of that name.
func (s *Server) listContacts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	resp := listContactsResponse{Contacts: []contactJSON{}}
	if q.Has("name") {
		c, err := s.store.ContactByName(r.Context(), q.Get("name"))
		switch {
		case errors.Is(err, core.ErrContactNotFound): // an empty list, not a 404: this is a search
		case err != nil:
			s.fail(w, r, statusOf(err), err)
			return
		default:
			resp.Contacts = append(resp.Contacts, s.contactJSON(r.Context(), &c, false))
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}
	limit, ok := s.limit(w, r, 100)
	if !ok {
		return
	}
	contacts, err := s.store.Contacts(r.Context(), limit)
	if err != nil {
		s.fail(w, r, statusOf(err), err)
		return
	}
	for i := range contacts {
		resp.Contacts = append(resp.Contacts, s.contactJSON(r.Context(), &contacts[i], false))
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) getContact(w http.ResponseWriter, r *http.Request) {
	id, ok := s.contactID(w, r)
	if !ok {
		return
	}
	c, err := s.store.Contact(r.Context(), id)
	if err != nil {
		s.fail(w, r, statusOf(err), err)
		return
	}
	writeJSON(w, http.StatusOK, s.contactJSON(r.Context(), &c, true))
}

type renameContactRequest struct {
	Name string `json:"name"`
}

// renameContact gives a contact a name, or with an empty name takes it away.
// The name is no part of the prompt, so nothing about the contact's
// conversation or its cache changes.
func (s *Server) renameContact(w http.ResponseWriter, r *http.Request) {
	id, ok := s.contactID(w, r)
	if !ok {
		return
	}
	var req renameContactRequest
	if !s.decode(w, r, &req) {
		return
	}
	if err := validateName(req.Name); err != nil {
		s.fail(w, r, http.StatusBadRequest, err)
		return
	}
	if err := s.store.RenameContact(r.Context(), id, req.Name); err != nil {
		s.fail(w, r, statusOf(err), err)
		return
	}
	c, err := s.store.Contact(r.Context(), id)
	if err != nil {
		s.fail(w, r, statusOf(err), err)
		return
	}
	writeJSON(w, http.StatusOK, s.contactJSON(r.Context(), &c, true))
}

// interrupt stops the reply in flight. It returns 202: the reply's stream
// says what became of it.
func (s *Server) interrupt(w http.ResponseWriter, r *http.Request) {
	id, ok := s.contactID(w, r)
	if !ok {
		return
	}
	if _, err := s.store.Contact(r.Context(), id); err != nil {
		s.fail(w, r, statusOf(err), err)
		return
	}
	if err := s.contacts.Interrupt(r.Context(), id); err != nil {
		s.fail(w, r, statusOf(err), err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// limit reads ?limit=, capped at the largest page.
func (s *Server) limit(w http.ResponseWriter, r *http.Request, def int) (int, bool) {
	v := r.URL.Query().Get("limit")
	if v == "" {
		return def, true
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		s.fail(w, r, http.StatusBadRequest, errors.New("limit must be a positive integer"))
		return 0, false
	}
	return min(n, maxPageSize), true
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
	// NextAfter is the cursor for the next page forward, and NextBefore for
	// the next page backward. Each is absent on the last page.
	NextAfter  *uint64 `json:"next_after,omitempty"`
	NextBefore *uint64 `json:"next_before,omitempty"`
}

// events pages through a contact's log, always answering in log order.
//
//	?after=N    forward from N, the default
//	?before=N   backward: the newest events older than N
//	?tail=1     backward from the end of the log
//	?kinds=...  only these kinds, comma-separated (user_msg, agent_msg, ...)
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	id, ok := s.contactID(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	seq := func(name string) (uint64, bool) {
		n, err := strconv.ParseUint(q.Get(name), 10, 64)
		if err != nil {
			s.fail(w, r, http.StatusBadRequest, fmt.Errorf("%s must be a sequence number", name))
		}
		return n, err == nil
	}
	var after, before uint64
	backward := false
	switch {
	case q.Has("after") && (q.Has("before") || q.Has("tail")), q.Has("before") && q.Has("tail"):
		s.fail(w, r, http.StatusBadRequest, errors.New("give one of after, before and tail"))
		return
	case q.Has("after"):
		if after, ok = seq("after"); !ok {
			return
		}
	case q.Has("before"):
		if before, ok = seq("before"); !ok {
			return
		}
		if before == 0 {
			s.fail(w, r, http.StatusBadRequest, errors.New("before must be at least 1; use tail=1 for the end of the log"))
			return
		}
		backward = true
	case q.Has("tail"):
		if on, err := strconv.ParseBool(q.Get("tail")); err != nil || !on {
			s.fail(w, r, http.StatusBadRequest, errors.New("tail must be 1"))
			return
		}
		backward = true
	}
	limit, ok := s.limit(w, r, 100)
	if !ok {
		return
	}
	kinds, err := parseKinds(q.Get("kinds"))
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err)
		return
	}
	if _, err := s.store.Contact(r.Context(), id); err != nil {
		s.fail(w, r, statusOf(err), err)
		return
	}
	var events []core.Event
	if backward {
		events, err = s.store.EventsBefore(r.Context(), id, before, limit, kinds)
	} else {
		events, err = s.store.EventsAfterOfKinds(r.Context(), id, after, limit, kinds)
	}
	if err != nil {
		s.fail(w, r, statusOf(err), err)
		return
	}
	resp := eventsResponse{Events: make([]eventJSON, len(events))}
	for i, ev := range events {
		resp.Events[i] = eventJSON{Seq: ev.Seq, TS: ev.TS, Kind: ev.Kind.String(), Body: ev.Body}
	}
	switch {
	case len(events) < limit:
	case backward:
		resp.NextBefore = &events[0].Seq
	default:
		resp.NextAfter = &events[len(events)-1].Seq
	}
	writeJSON(w, http.StatusOK, resp)
}

// parseKinds reads a comma-separated list of event kinds. Empty means all.
func parseKinds(csv string) (core.KindSet, error) {
	if csv == "" {
		return core.AllKinds, nil
	}
	var set core.KindSet
	for name := range strings.SplitSeq(csv, ",") {
		k, ok := core.ParseEventKind(strings.TrimSpace(name))
		if !ok {
			return 0, fmt.Errorf("kinds: %q is not an event kind", name)
		}
		set = set.With(k)
	}
	return set, nil
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
	case errors.Is(err, core.ErrNameTaken):
		return http.StatusConflict
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
	case status == http.StatusConflict:
		msg = "another contact has that name"
	}
	writeJSON(w, status, errorResponse{Error: msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// A write error means the client left; there is no one to tell.
	_ = json.NewEncoder(w).Encode(v)
}
