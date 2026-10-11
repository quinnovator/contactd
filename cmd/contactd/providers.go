package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/quinnovator/contactd/internal/config"
	"github.com/quinnovator/contactd/internal/llm"
	fakellm "github.com/quinnovator/contactd/internal/llm/fake"
	"github.com/quinnovator/contactd/internal/llm/provider/anthropic"
	"github.com/quinnovator/contactd/internal/llm/provider/deepseek"
	"github.com/quinnovator/contactd/internal/llm/provider/ollamacloud"
	"github.com/quinnovator/contactd/internal/llm/provider/openaicompat"
	"github.com/quinnovator/contactd/internal/llm/provider/opencodego"
	"github.com/quinnovator/contactd/internal/llm/provider/openrouter"
)

// models builds the client the core reaches every provider through: one
// profile per provider that has credentials, behind shared admission and
// retries. This is the only place that knows providers by name.
func models(ctx context.Context, cfg *config.Config, log *slog.Logger) (*llm.Client, error) {
	var providers []llm.Provider
	for _, name := range cfg.Providers() {
		p, err := profile(ctx, name, cfg, log)
		if err == nil {
			providers = append(providers, p)
			continue
		}
		// The default provider is the deployment; without it there is
		// nothing to serve. Any other is an extra some agents name, and a
		// gateway that is down at start-up should not take the rest with it:
		// those agents get a clear unknown-provider error instead.
		if name == cfg.LLM {
			return nil, fmt.Errorf("configure the %s provider: %w", name, err)
		}
		log.ErrorContext(ctx, "provider unavailable; agents that name it will fail", "provider", name, "error", err)
	}
	secret, err := affinitySecret(cfg)
	if err != nil {
		return nil, err
	}
	client, err := llm.NewClient(llm.ClientConfig{
		Providers:      providers,
		Default:        cfg.LLM,
		DefaultModel:   cfg.Model,
		Limits:         cfg.ProviderLimits,
		AffinitySecret: secret,
		Logger:         log,
	})
	if err != nil {
		return nil, fmt.Errorf("configure model providers: %w", err)
	}
	// Resolve the default route now: a mistyped -model is a start-up error,
	// not every contact's first failed turn.
	route, err := client.Route(ctx, "", "")
	if err != nil {
		return nil, fmt.Errorf("default model (set -model to one the %s provider serves): %w", cfg.LLM, err)
	}
	names := make([]string, len(providers))
	for i, p := range providers {
		names[i] = p.ID()
	}
	log.InfoContext(ctx, "model providers ready",
		"providers", strings.Join(names, ","),
		"default_route", route.ID(),
		"wire", route.Wire.String(),
		"context_window", route.Caps.ContextWindow,
		"cache_ttl", route.Caps.CacheTTL.String())
	return client, nil
}

func profile(ctx context.Context, name string, cfg *config.Config, log *slog.Logger) (llm.Provider, error) {
	// Catalogs are fetched once, here; a hung gateway must not hang start-up.
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	// -model is the default model of the default provider only.
	defModel := ""
	if name == cfg.LLM {
		defModel = cfg.Model
	}
	switch name {
	case "fake":
		log.WarnContext(ctx, "using the fake model provider: replies are scripted")
		return fakellm.New(fakellm.Options{PerChunk: 20 * time.Millisecond, FirstToken: 100 * time.Millisecond}), nil
	case "anthropic":
		return wrap(anthropic.New(anthropic.Config{
			APIKey: cfg.AnthropicKey, BaseURL: cfg.AnthropicBaseURL, NoFallback: cfg.NoRefusalFallback,
		}))
	case "deepseek":
		return wrap(deepseek.New(deepseek.Config{
			APIKey: cfg.DeepSeekKey, BaseURL: cfg.DeepSeekBaseURL, Wire: cfg.DeepSeekWire,
		}))
	case "openrouter":
		var catalog []byte
		if cfg.OpenRouterCatalog != "" {
			var err error
			if catalog, err = os.ReadFile(cfg.OpenRouterCatalog); err != nil { //nolint:gosec // the operator names the file
				return nil, fmt.Errorf("read the saved catalog: %w", err)
			}
		}
		return wrap(openrouter.New(ctx, openrouter.Config{
			APIKey: cfg.OpenRouterKey, BaseURL: cfg.OpenRouterBaseURL, DefaultModel: defModel,
			Referer: cfg.OpenRouterReferer, Title: cfg.OpenRouterTitle,
			Pin: cfg.OpenRouterPin, Catalog: catalog,
		}))
	case "opencode":
		p, err := opencodego.New(ctx, opencodego.Config{
			APIKey: cfg.OpenCodeKey, BaseURL: cfg.OpenCodeBaseURL, DefaultModel: defModel,
		})
		if err != nil {
			return nil, err //nolint:wrapcheck // the caller names the provider
		}
		if responses, unknown := p.Unrouted(); len(responses)+len(unknown) > 0 {
			log.InfoContext(ctx, "some OpenCode Go models are not routable",
				"responses_wire", strings.Join(responses, ","), "unknown_family", strings.Join(unknown, ","))
		}
		return p, nil
	case "ollama":
		return wrap(ollamacloud.New(ctx, ollamacloud.Config{
			APIKey: cfg.OllamaKey, BaseURL: cfg.OllamaBaseURL, DefaultModel: defModel,
		}))
	case "openai":
		names := cfg.OpenAIModels
		if len(names) == 0 {
			names = []string{cfg.Model}
		}
		list := make([]openaicompat.Model, len(names))
		for i, n := range names {
			list[i] = openaicompat.Model{Name: n, ContextWindow: cfg.OpenAIContext}
		}
		return wrap(openaicompat.New(openaicompat.Config{
			APIKey: cfg.OpenAIKey, BaseURLs: cfg.OpenAIBaseURLs, Models: list, DefaultModel: names[0],
		}))
	default:
		return nil, fmt.Errorf("provider %q: %w", name, llm.ErrUnknownProvider)
	}
}

// wrap turns a concrete profile into the interface, keeping a nil profile
// from becoming a non-nil interface when its constructor failed.
func wrap[P llm.Provider](p P, err error) (llm.Provider, error) {
	if err != nil {
		return nil, err
	}
	return p, nil
}

// affinitySecret returns the key for the per-contact hash sent to providers
// that pin conversations. It has to survive restarts, or every contact's
// pinning key would change and every cache would go cold at once; so when
// none is configured, one is generated and kept beside the database.
func affinitySecret(cfg *config.Config) ([]byte, error) {
	if cfg.AffinitySecret != "" {
		return []byte(cfg.AffinitySecret), nil
	}
	path := filepath.Join(cfg.DataDir, "affinity.key")
	raw, err := os.ReadFile(path) //nolint:gosec // the operator chooses the data directory
	if err == nil && len(strings.TrimSpace(string(raw))) >= 32 {
		return []byte(strings.TrimSpace(string(raw))), nil
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("read affinity secret: %w", err)
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return nil, fmt.Errorf("generate affinity secret: %w", err)
	}
	secret := hex.EncodeToString(buf)
	if err := os.WriteFile(path, []byte(secret+"\n"), 0o600); err != nil { //nolint:gosec // the operator chooses the data directory
		return nil, fmt.Errorf("write affinity secret: %w", err)
	}
	return []byte(secret), nil
}
