package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

func (l *loader) helperFlags(c *Config) (*string, *string) {
	var pushAllow, fetchAllow string
	l.str(&c.Helpers, "helpers", "on", "on enables standing helpers over A2A; off disables all helper routes and tools")
	l.str(&c.A2AURL, "a2a-base-url", "", "public absolute HTTP(S) base URL for A2A discovery and callbacks")
	l.str(&c.A2AAuth, "a2a-auth", "token", "A2A authentication: token or off")
	l.str(&c.A2AToken, "a2a-token", "", "A2A bearer token; empty loads or creates data-dir/a2a.token")
	l.str(&pushAllow, "a2a-push-allow", "", "comma-separated allowed callback host:port values; defaults to the base URL")
	l.str(&c.HelperAgentURL, "helper-agent-url", "", "helper agent discovery base URL; defaults to this server")
	l.str(&c.HelperAgentToken, "helper-agent-token", "", "requester bearer token; defaults to the local agent token")
	l.str(&c.HelperProvider, "helper-provider", "", "model provider for helper contacts")
	l.str(&c.HelperModel, "helper-model", "", "model for helper contacts")
	l.str(&c.HelperEffort, "helper-effort", "", "reasoning effort for helper contacts")
	l.str(&c.HelperTools, "helper-tools", "", "optional helper REPL: python")
	l.dur(&c.HelpersMinEvery, "helpers-min-every", time.Minute, "shortest permitted helper check period")
	l.integer(&c.HelpersMaxActive, "helpers-max-active", 16, "maximum active tasks on the helper agent")
	l.integer(&c.HelpersPerContact, "helpers-per-contact", 4, "maximum active helpers per requester")
	l.integer(&c.HelpersConcurrency, "helpers-concurrency", 4, "maximum scheduled checks dispatched together")
	l.str(&fetchAllow, "fetch-allow", "", "comma-separated private host:port exceptions for fetch_url")
	return &pushAllow, &fetchAllow
}

func (c *Config) validateHelpers() error {
	var errs []error
	if c.Helpers != "on" && c.Helpers != "off" {
		errs = append(errs, errors.New("helpers must be on or off"))
	}
	if c.A2AAuth != "token" && c.A2AAuth != "off" {
		errs = append(errs, errors.New("a2a-auth must be token or off"))
	}
	if c.HelpersMinEvery <= 0 || c.HelpersMinEvery > 24*time.Hour {
		errs = append(errs, errors.New("helpers-min-every must be positive and at most 24h"))
	}
	if c.HelpersMaxActive <= 0 || c.HelpersPerContact <= 0 || c.HelpersConcurrency <= 0 {
		errs = append(errs, errors.New("helper limits must be positive"))
	}
	for _, value := range []string{c.A2ABaseURL(), c.HelperAgentURL} {
		if value == "" {
			continue
		}
		u, err := url.Parse(value)
		if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			errs = append(errs, errors.New("A2A URLs must be absolute http(s) URLs without credentials, query or fragment"))
		}
	}
	for _, addr := range append(slices.Clone(c.A2APushAllow), c.FetchAllow...) {
		if _, _, err := net.SplitHostPort(addr); err != nil {
			errs = append(errs, fmt.Errorf("helper allowlist needs host:port: %w", err))
		}
	}
	if c.HelperTools != "" && c.HelperTools != "python" {
		errs = append(errs, errors.New("helper-tools must be empty or python"))
	}
	if !slices.Contains([]string{"", "none", "low", "medium", "high", "xhigh", "max"}, c.HelperEffort) {
		errs = append(errs, errors.New("invalid helper-effort"))
	}
	return errors.Join(errs...)
}

// A2ABaseURL derives the advertised URL from the TCP listener unless explicitly set.
func (c *Config) A2ABaseURL() string {
	if c.A2AURL != "" {
		return strings.TrimRight(c.A2AURL, "/")
	}
	host, port, err := net.SplitHostPort(c.Addr)
	if err != nil {
		return "http://127.0.0.1:8080"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// A2ATokenPath names the private bearer token file.
func (c *Config) A2ATokenPath() string { return filepath.Join(c.DataDir, "a2a.token") }
