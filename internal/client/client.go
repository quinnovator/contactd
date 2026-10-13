// Package client is a typed client for the contactd API. It is what the
// chat subcommand is built on, and it reaches the server the way any other
// client would: over HTTP and one WebSocket, on a Unix socket or a TCP
// address. It never opens the data directory. A contact's sequence numbers
// are assigned in memory by the one actor that owns it, so a second process
// writing the same log would break it.
//
// Client is the request-response half. Live is one WebSocket. Session keeps
// a Live open across reconnects and stitches it to the log, so a caller sees
// one ordered stream of updates.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/quinnovator/contactd/internal/core"
)

// ErrUnavailable means no server answered: nothing is listening on the
// socket or address, or the connection was refused.
var ErrUnavailable = errors.New("contactd is not reachable")

// Error is the server refusing a request.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return e.Message }

// IsStatus reports whether err is the server answering with this status.
func IsStatus(err error, status int) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == status
}

// Options says where the server is.
type Options struct {
	// Socket is the path of the server's Unix socket. It is used unless URL
	// is set.
	Socket string
	// URL is the server's base URL, such as http://host:8080.
	URL string
}

// Client talks to one contactd server.
type Client struct {
	http *http.Client
	base string // no trailing slash
	// where describes the server in error messages.
	where string
}

// New builds a client. It does not connect.
func New(opt Options) (*Client, error) {
	if opt.URL != "" {
		u, err := url.Parse(opt.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("server URL %q must look like http://host:port", opt.URL)
		}
		return &Client{http: &http.Client{}, base: strings.TrimRight(opt.URL, "/"), where: opt.URL}, nil
	}
	if opt.Socket == "" {
		return nil, errors.New("no server given: need a socket path or a URL")
	}
	socket := opt.Socket
	transport := &http.Transport{
		// Every request goes to the socket, whatever host its URL names.
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
	}
	return &Client{http: &http.Client{Transport: transport}, base: "http://contactd", where: socket}, nil
}

// Where describes the server this client talks to: a socket path or a URL.
func (c *Client) Where() string { return c.where }

// Contact is a contact as the API shows it.
type Contact struct {
	ID   core.ContactID `json:"id"`
	Name string         `json:"name"`
	// Provider and Model are the route that serves the contact now.
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	LastActive int64  `json:"last_active"` // unix ms
	// Agent and Profile are set when one contact is fetched, not in a list.
	Agent   *core.AgentConfig `json:"agent"`
	Profile string            `json:"profile"`
}

// Label is what to call the contact: its name, or its ID when it has none.
func (c *Contact) Label() string {
	if c.Name != "" {
		return c.Name
	}
	return "#" + c.ID.String()
}

// NewContact is a contact to create.
type NewContact struct {
	Name    string           `json:"name,omitempty"`
	Agent   core.AgentConfig `json:"agent"`
	Profile string           `json:"profile,omitempty"`
}

// Health reports whether the server is up and accepting work.
func (c *Client) Health(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/healthz", nil, nil)
}

// Contacts lists the contacts a person talks to, most recently active first.
func (c *Client) Contacts(ctx context.Context) ([]Contact, error) {
	var out struct {
		Contacts []Contact `json:"contacts"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/contacts?limit=500", nil, &out); err != nil {
		return nil, err
	}
	return out.Contacts, nil
}

// Contact fetches one contact, with its agent config and profile.
func (c *Client) Contact(ctx context.Context, id core.ContactID) (Contact, error) {
	var out Contact
	err := c.do(ctx, http.MethodGet, "/v1/contacts/"+id.String(), nil, &out)
	return out, err
}

// Find resolves what a person typed to a contact: a number is an ID, with
// or without a leading #, and anything else is a name.
func (c *Client) Find(ctx context.Context, ref string) (Contact, error) {
	if n, err := strconv.ParseUint(strings.TrimPrefix(ref, "#"), 10, 64); err == nil {
		got, err := c.Contact(ctx, core.ContactID(n))
		if IsStatus(err, http.StatusNotFound) {
			return Contact{}, fmt.Errorf("contact %s: %w", ref, core.ErrContactNotFound)
		}
		return got, err
	}
	var out struct {
		Contacts []Contact `json:"contacts"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/contacts?name="+url.QueryEscape(ref), nil, &out); err != nil {
		return Contact{}, err
	}
	if len(out.Contacts) == 0 {
		return Contact{}, fmt.Errorf("contact %q: %w", ref, core.ErrContactNotFound)
	}
	// The search answers without the agent config; fetch the whole contact.
	return c.Contact(ctx, out.Contacts[0].ID)
}

// Create makes a contact and returns its ID.
func (c *Client) Create(ctx context.Context, nc NewContact) (core.ContactID, error) {
	var out struct {
		ID core.ContactID `json:"id"`
	}
	err := c.do(ctx, http.MethodPost, "/v1/contacts", nc, &out)
	return out.ID, err
}

// Rename gives a contact a name, or with an empty name takes it away.
func (c *Client) Rename(ctx context.Context, id core.ContactID, name string) (Contact, error) {
	var out Contact
	err := c.do(ctx, http.MethodPatch, "/v1/contacts/"+id.String(), map[string]string{"name": name}, &out)
	return out, err
}

// Send posts a message and returns its sequence number once it is durable.
// The reply arrives on the contact's stream.
func (c *Client) Send(ctx context.Context, id core.ContactID, text string, visits ...core.Visit) (uint64, error) {
	var visit core.Visit
	if len(visits) > 0 {
		visit = visits[0]
	}
	var out struct {
		Seq uint64 `json:"seq"`
	}
	err := c.do(ctx, http.MethodPost, "/v1/contacts/"+id.String()+"/messages", struct {
		Text string `json:"text"`
		core.Visit
	}{Text: text, Visit: visit}, &out)
	return out.Seq, err
}

// Interrupt stops the reply the contact has in flight, if it has one.
func (c *Client) Interrupt(ctx context.Context, id core.ContactID) error {
	return c.do(ctx, http.MethodPost, "/v1/contacts/"+id.String()+"/interrupt", nil, nil)
}

// Page is one page of a contact's log, in log order.
type Page struct {
	Events []core.Event
	// More reports whether there are events beyond this page in the
	// direction it was read.
	More bool
}

// After reads forward: the oldest events of the given kinds with seq > after.
func (c *Client) After(ctx context.Context, id core.ContactID, after uint64, limit int, kinds core.KindSet) (Page, error) {
	return c.events(ctx, id, "after="+strconv.FormatUint(after, 10), limit, kinds)
}

// Before reads backward: the newest events of the given kinds with seq <
// before.
func (c *Client) Before(ctx context.Context, id core.ContactID, before uint64, limit int, kinds core.KindSet) (Page, error) {
	return c.events(ctx, id, "before="+strconv.FormatUint(before, 10), limit, kinds)
}

// Tail reads the newest events of the given kinds.
func (c *Client) Tail(ctx context.Context, id core.ContactID, limit int, kinds core.KindSet) (Page, error) {
	return c.events(ctx, id, "tail=1", limit, kinds)
}

func (c *Client) events(ctx context.Context, id core.ContactID, cursor string, limit int, kinds core.KindSet) (Page, error) {
	path := "/v1/contacts/" + id.String() + "/events?" + cursor + "&limit=" + strconv.Itoa(limit)
	if kinds != core.AllKinds {
		var names []string
		for k := core.KindUserMsg; k <= core.KindNotice; k++ {
			if kinds.Has(k) {
				names = append(names, k.String())
			}
		}
		path += "&kinds=" + strings.Join(names, ",")
	}
	var out struct {
		Events []struct {
			Seq  uint64          `json:"seq"`
			TS   int64           `json:"ts"`
			Kind string          `json:"kind"`
			Body json.RawMessage `json:"body"`
		} `json:"events"`
		NextAfter  *uint64 `json:"next_after"`
		NextBefore *uint64 `json:"next_before"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return Page{}, err
	}
	page := Page{Events: make([]core.Event, 0, len(out.Events)), More: out.NextAfter != nil || out.NextBefore != nil}
	for _, ev := range out.Events {
		kind, ok := core.ParseEventKind(ev.Kind)
		if !ok {
			continue // a kind newer than this client; nothing it could show
		}
		page.Events = append(page.Events, core.Event{Contact: id, Seq: ev.Seq, TS: ev.TS, Kind: kind, Body: ev.Body})
	}
	return page, nil
}

// do sends one request and decodes the answer into out, when out is set.
func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return c.unreachable(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }() // nothing to do about a failed close of a read body
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &e) != nil || e.Error == "" {
			e.Error = http.StatusText(resp.StatusCode)
		}
		return &Error{Status: resp.StatusCode, Message: e.Error}
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode response from %s: %w", path, err)
	}
	return nil
}

// unreachable turns a transport error into one that says what to do about
// it. A cancelled request stays a cancellation.
func (c *Client) unreachable(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("request cancelled: %w", ctx.Err())
	}
	// The request's method and URL say nothing a person can act on; the
	// reason underneath does.
	if ue, ok := errors.AsType[*url.Error](err); ok {
		err = ue.Err
	}
	return fmt.Errorf("%w at %s: %w", ErrUnavailable, c.where, err)
}

// backoff is the wait before reconnect attempt n, counting from zero.
func backoff(n int) time.Duration {
	return min(250*time.Millisecond<<min(n, 5), 5*time.Second)
}
