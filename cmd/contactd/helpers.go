package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"slices"
	"time"

	"github.com/quinnovator/contactd/internal/actor"
	"github.com/quinnovator/contactd/internal/api"
	"github.com/quinnovator/contactd/internal/config"
	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/eventlog"
	"github.com/quinnovator/contactd/internal/fetch"
	"github.com/quinnovator/contactd/internal/helpers"
	"github.com/quinnovator/contactd/internal/turn"
)

func wireHelpers(ctx context.Context, cfg *config.Config, store *eventlog.Store, registry *actor.Registry, td *turn.Deps, server *api.Server, log *slog.Logger) (func(), error) {
	if cfg.Helpers == "off" {
		return func() {}, nil
	}
	token := ""
	var err error
	if cfg.A2AAuth != "off" {
		token, err = helpers.LoadToken(cfg.A2ATokenPath(), cfg.A2AToken)
		if err != nil {
			return nil, fmt.Errorf("configure helper agent: %w", err)
		}
	}
	hc := helpers.DefaultConfig()
	hc.BaseURL = cfg.A2ABaseURL()
	hc.Auth = cfg.A2AAuth
	hc.Token = token
	hc.MinEvery = cfg.HelpersMinEvery
	hc.MaxActive = cfg.HelpersMaxActive
	hc.Concurrency = cfg.HelpersConcurrency
	hc.HelperProvider = cfg.HelperProvider
	hc.HelperModel = cfg.HelperModel
	hc.HelperEffort = cfg.HelperEffort
	hc.PushAllow = slices.Clone(cfg.A2APushAllow)
	if len(hc.PushAllow) == 0 {
		u, err := url.Parse(hc.BaseURL)
		if err != nil {
			return nil, fmt.Errorf("parse helper base URL: %w", err)
		}
		port := u.Port()
		if port == "" {
			port = "80"
			if u.Scheme == "https" {
				port = "443"
			}
		}
		hc.PushAllow = []string{net.JoinHostPort(u.Hostname(), port)}
	}
	if cfg.HelperTools == "python" {
		hc.HelperTools = []core.ToolDef{{Name: "python", Description: "Run Python.", Env: core.EnvSpec{Tier: 0, Base: "python-3.12"}}}
	}
	td.Fetch = fetch.New(fetch.Config{Allow: cfg.FetchAllow})
	svc := helpers.New(helpers.Deps{Contacts: registry, Creator: store, Events: store, Store: store}, hc, log)
	// Complete the registry/service cycle before any actor can be hydrated.
	registry.SetHelperHook(svc)
	handler, err := svc.HTTPHandler()
	if err != nil {
		return nil, fmt.Errorf("configure helper agent: %w", err)
	}
	server.WithA2A(handler)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { svc.Run(runCtx); close(done) }()
	log.InfoContext(ctx, "a2a helper agent", "card", hc.BaseURL+"/.well-known/agent-card.json", "auth", hc.Auth)
	return func() {
		cancel()
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
			log.Warn("helper service shutdown timed out")
		}
	}, nil
}
