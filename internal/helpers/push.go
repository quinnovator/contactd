package helpers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/quinnovator/contactd/internal/core"
)

// pusher resolves an allowed host at validation and dials exactly the
// addresses it found, so a name cannot be rebound between the check and the
// connection. A pin is refreshed after pinTTL, so a host whose addresses
// rotate keeps working. Redirects and proxy environment variables cannot
// bypass it.
type (
	pusher struct {
		client *http.Client
		allow  []string
		mu     sync.Mutex
		pins   map[string]pin
	}
	pin struct {
		ips []netip.Addr
		at  time.Time
	}
)

const pinTTL = 5 * time.Minute

func newPusher(allow []string) *pusher {
	p := &pusher{allow: slices.Clone(allow), pins: make(map[string]pin)}
	p.client = &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{DialContext: p.dial, IdleConnTimeout: time.Minute}, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("push redirects are not allowed") }}
	return p
}

func urlAddress(u *url.URL) string {
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	}
	return net.JoinHostPort(u.Hostname(), port)
}

func (p *pusher) validate(ctx context.Context, c core.PushConfig) error {
	u, err := url.Parse(c.URL)
	if err != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || !slices.Contains(p.allow, urlAddress(u)) {
		return fmt.Errorf("%w: push URL not allowed", a2a.ErrInvalidParams)
	}
	if c.AuthScheme != "" && authScheme(c.AuthScheme) == "" {
		return fmt.Errorf("%w: push authentication scheme not allowed", a2a.ErrInvalidParams)
	}
	addr := urlAddress(u)
	p.mu.Lock()
	cur, pinned := p.pins[addr]
	p.mu.Unlock()
	if pinned && time.Since(cur.at) < pinTTL {
		return nil
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", u.Hostname())
	if err != nil {
		return fmt.Errorf("%w: resolve push target: %w", a2a.ErrInvalidParams, err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("%w: push target has no addresses", a2a.ErrInvalidParams)
	}
	p.mu.Lock()
	p.pins[addr] = pin{ips: ips, at: time.Now()}
	p.mu.Unlock()
	return nil
}

// authScheme canonicalizes a push authentication scheme, which is case
// insensitive (RFC 9110), and returns "" for one that is not supported.
func authScheme(s string) string {
	switch {
	case strings.EqualFold(s, "Bearer"):
		return "Bearer"
	case strings.EqualFold(s, "Basic"):
		return "Basic"
	}
	return ""
}

func (p *pusher) dial(ctx context.Context, network, address string) (net.Conn, error) {
	if !slices.Contains(p.allow, address) {
		return nil, errors.New("push URL not allowed")
	}
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("push dial address: %w", err)
	}
	p.mu.Lock()
	ips := slices.Clone(p.pins[address].ips)
	p.mu.Unlock()
	if len(ips) == 0 {
		return nil, errors.New("push target was not validated")
	}
	var last error
	for _, ip := range ips {
		var d net.Dialer
		conn, err := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		last = err
	}
	if last == nil {
		last = errors.New("push target has no addresses")
	}
	return nil, fmt.Errorf("dial push target: %w", last)
}

func (p *pusher) send(ctx context.Context, c core.PushConfig, ev a2a.Event) error {
	if err := p.validate(ctx, c); err != nil {
		return err
	}
	raw, err := json.Marshal(a2a.StreamResponse{Event: ev})
	if err != nil {
		return fmt.Errorf("marshal push event: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("create push request: %w", err)
	}
	req.Header.Set("Content-Type", "application/a2a+json")
	req.Header.Set("A2A-Version", string(a2a.Version))
	if c.Token != "" {
		req.Header.Set("A2A-Notification-Token", c.Token)
	}
	if c.AuthScheme != "" {
		req.Header.Set("Authorization", c.AuthScheme+" "+c.AuthCredentials)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("deliver push: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("push returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func pushRow(c *a2a.PushConfig) core.PushConfig {
	r := core.PushConfig{TaskID: string(c.TaskID), ID: c.ID, URL: c.URL, Token: c.Token, CreatedAt: time.Now().UnixMilli()}
	if r.ID == "" {
		r.ID = "default"
	}
	if c.Auth != nil {
		r.AuthScheme = c.Auth.Scheme
		if canonical := authScheme(c.Auth.Scheme); canonical != "" {
			r.AuthScheme = canonical
		}
		r.AuthCredentials = c.Auth.Credentials
	}
	return r
}

func pushValue(c core.PushConfig) *a2a.PushConfig {
	p := &a2a.PushConfig{TaskID: a2a.TaskID(c.TaskID), ID: c.ID, URL: c.URL, Token: c.Token}
	if c.AuthScheme != "" {
		p.Auth = &a2a.PushAuthInfo{Scheme: c.AuthScheme, Credentials: c.AuthCredentials}
	}
	return p
}
