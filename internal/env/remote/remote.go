// Package remote is the tier-1 REPL environment: a thin client for the
// external snapshot-sandbox service, which runs commands in microVMs and
// snapshots them.
//
// A lease is a chain of snapshots and nothing else. Every Exec runs the code
// from the chain's head, asks the service to keep the result, and moves the
// head to the snapshot that comes back. Nothing is held open between calls,
// so a lease costs the service nothing while idle and Release has no remote
// work to do.
//
// Each Exec is a new process fed the code on stdin. Files and installed
// packages carry over through the snapshot; interpreter variables do not.
package remote

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/obs"
	"github.com/quinnovator/contactd/internal/turn"
)

// Sentinel errors, checked with errors.Is.
var (
	// ErrUnknownEnv means the service has no environment named EnvSpec.Base.
	ErrUnknownEnv = errors.New("unknown environment")
	// ErrStatus means the service answered with an HTTP status that is not
	// success.
	ErrStatus = errors.New("unexpected HTTP status")
	// ErrBadResponse means the service answered 2xx with a body that is not
	// what its API promises.
	ErrBadResponse = errors.New("malformed response")
	// ErrUnsupportedSpec means the spec asks for something this provider
	// cannot arrange: another tier, or packages on a base it has no installer
	// for.
	ErrUnsupportedSpec = errors.New("not supported by tier 1")
)

const (
	defaultExecTimeout = 120 * time.Second
	// controlTimeout bounds the calls that do no work in a sandbox: looking
	// up an environment, pinning a snapshot.
	controlTimeout = 15 * time.Second
	// transportGrace is how long past timeout_ms the client waits for the
	// service's own verdict before giving up on the connection.
	transportGrace = 10 * time.Second
	// fallbackRunner runs code for a base no Runners prefix matches.
	fallbackRunner = "sh -s"
)

// Config configures a Provider.
type Config struct {
	BaseURL     string
	Token       string        // sent as a Bearer token when set
	HTTPClient  *http.Client  // default: a client with sane timeouts
	ExecTimeout time.Duration // default 120s
	// Runners maps a base-name prefix to the command that runs code from
	// stdin. Entries are added to the defaults (python: python3 -, node:
	// node -); the longest matching prefix wins and anything unmatched runs
	// under sh -s.
	Runners map[string][]string
	Logger  *slog.Logger
}

// Provider implements turn.EnvLeaser for tier 1.
type Provider struct {
	base    *url.URL
	token   string
	http    *http.Client
	timeout time.Duration
	runners map[string][]string
	log     *slog.Logger
}

// New validates cfg. It does not contact the service.
func New(cfg Config) (*Provider, error) {
	base, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("remote: parse BaseURL: %w", err)
	}
	if (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return nil, fmt.Errorf("remote: BaseURL %q is not an http(s) URL", cfg.BaseURL)
	}
	p := &Provider{
		base:    base,
		token:   cfg.Token,
		http:    cfg.HTTPClient,
		timeout: cfg.ExecTimeout,
		runners: map[string][]string{
			"python": {"python3", "-"},
			"node":   {"node", "-"},
		},
		log: cfg.Logger,
	}
	if p.http == nil {
		p.http = defaultClient()
	}
	if p.timeout <= 0 {
		p.timeout = defaultExecTimeout
	}
	if p.log == nil {
		p.log = slog.New(slog.DiscardHandler)
	}
	for prefix, cmd := range cfg.Runners {
		if prefix == "" || len(cmd) == 0 {
			return nil, fmt.Errorf("remote: runner %q: prefix and command must not be empty", prefix)
		}
	}
	maps.Copy(p.runners, cfg.Runners)
	return p, nil
}

// defaultClient bounds connecting but not the request as a whole: an exec
// legitimately takes as long as its timeout_ms, and each request carries its
// own deadline through its context.
func defaultClient() *http.Client {
	return &http.Client{Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConnsPerHost:   16,
		ForceAttemptHTTP2:     true,
	}}
}

// runner returns the command that runs code from stdin on base.
func (p *Provider) runner(base string) []string {
	best, cmd := -1, strings.Fields(fallbackRunner)
	for prefix, c := range p.runners {
		if strings.HasPrefix(base, prefix) && len(prefix) > best {
			best, cmd = len(prefix), c
		}
	}
	return cmd
}

// installer returns the command that installs packages on base.
func installer(base string, packages []string) ([]string, error) {
	for _, name := range packages {
		// Package names end up in an argv. One that starts with a dash would
		// be read as an option.
		if name == "" || strings.HasPrefix(name, "-") {
			return nil, fmt.Errorf("package name %q: %w", name, ErrUnsupportedSpec)
		}
	}
	switch {
	case strings.HasPrefix(base, "python"):
		return append([]string{"pip", "install"}, packages...), nil
	case strings.HasPrefix(base, "node"):
		return append([]string{"npm", "install", "-g"}, packages...), nil
	default:
		return nil, fmt.Errorf("packages on base %q (only python and node bases have an installer): %w", base, ErrUnsupportedSpec)
	}
}

// Lease opens a snapshot chain. With a resume ID the chain starts there and
// the service is not contacted until the first Exec. Without one, Lease
// checks that the environment exists, so a bad base fails here rather than
// mid-turn, and installs spec.Packages into a first snapshot.
func (p *Provider) Lease(ctx context.Context, contact core.ContactID, spec core.EnvSpec, resume core.SnapshotID) (turn.Lease, error) {
	start := time.Now()
	l, err := p.lease(ctx, contact, spec, resume)
	obs.EnvLeaseMS.WithLabelValues("1").Observe(obs.SinceMS(start))
	if err != nil {
		return nil, fmt.Errorf("remote lease for contact %d: %w", contact, err)
	}
	return l, nil
}

func (p *Provider) lease(ctx context.Context, contact core.ContactID, spec core.EnvSpec, resume core.SnapshotID) (*lease, error) {
	if spec.Tier != 1 {
		return nil, fmt.Errorf("tier %d: %w", spec.Tier, ErrUnsupportedSpec)
	}
	l := &lease{p: p, contact: contact, base: spec.Base, cmd: p.runner(spec.Base)}
	if resume != "" {
		l.head, l.note = string(resume), noteRestored
		return l, nil
	}

	var install []string
	if len(spec.Packages) > 0 {
		var err error
		if install, err = installer(spec.Base, spec.Packages); err != nil {
			return nil, err
		}
	}
	if err := p.checkEnv(ctx, spec.Base); err != nil {
		return nil, err
	}
	if install != nil {
		out, err := p.exec(ctx, execRequest{Env: spec.Base, Cmd: install, Keep: true})
		if err != nil {
			return nil, fmt.Errorf("install packages: %w", err)
		}
		if out.Exit != 0 {
			return nil, fmt.Errorf("install packages: %s exited with status %d: %s", install[0], out.Exit, tail(out.Stderr, 512))
		}
		l.head = out.SnapshotID
	}
	p.log.DebugContext(ctx, "remote lease opened",
		slog.Uint64("contact_id", uint64(contact)),
		slog.String("base", spec.Base),
		slog.Int("packages", len(spec.Packages)))
	return l, nil
}

// tail returns the last n bytes of s, for quoting a tool's complaint in an
// error.
func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		s = strings.ToValidUTF8(s[len(s)-n:], "")
	}
	return s
}
