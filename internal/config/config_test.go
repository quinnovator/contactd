package config_test

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/quinnovator/contactd/internal/config"
)

func env(pairs ...string) func(string) string {
	m := make(map[string]string, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		m[pairs[i]] = pairs[i+1]
	}
	return func(k string) string { return m[k] }
}

func TestDefaultsMatchTheDesignDoc(t *testing.T) {
	t.Parallel()
	c, err := config.Load(nil, env("ANTHROPIC_API_KEY", "k"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Debounce != 700*time.Millisecond || c.DebounceCap != 3*time.Second {
		t.Errorf("debounce %v cap %v, want 700ms and 3s", c.Debounce, c.DebounceCap)
	}
	if c.Gap != 30*time.Minute || c.RecallK != 4 || c.EnvIdleTTL != 2*time.Minute {
		t.Errorf("gap %v, K %d, env idle %v", c.Gap, c.RecallK, c.EnvIdleTTL)
	}
	if c.DBPath() != "/data/contacts.db" || c.FSRoot() != "/data/fs" {
		t.Errorf("paths %s %s, want the documented volume layout", c.DBPath(), c.FSRoot())
	}
	// With only an Anthropic key, Anthropic is the default provider.
	if c.LLM != "anthropic" || c.Infer != "ort" || c.OnnxFile() != "model_int8.onnx" {
		t.Errorf("llm=%s infer=%s onnx=%s", c.LLM, c.Infer, c.OnnxFile())
	}
	if c.InferWorkers < 1 {
		t.Errorf("infer workers = %d", c.InferWorkers)
	}
}

func TestFlagsBeatEnvironment(t *testing.T) {
	t.Parallel()
	c, err := config.Load(
		[]string{"-debounce=250ms", "-llm=openai", "-openai-base-urls=http://a/v1, http://b/v1", "-openai-models=qwen-coder, small"},
		env("CONTACTD_DEBOUNCE", "900ms", "CONTACTD_IDLE_TTL", "1m", "CONTACTD_LLM", "fake", "CONTACTD_SYNC_FULL", "true"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if c.Debounce != 250*time.Millisecond {
		t.Errorf("debounce = %v; the flag should win over the environment", c.Debounce)
	}
	if c.IdleTTL != time.Minute || !c.SyncFull {
		t.Errorf("idle ttl %v sync full %t; the environment should apply where no flag is given", c.IdleTTL, c.SyncFull)
	}
	if c.LLM != "openai" || len(c.OpenAIBaseURLs) != 2 || c.OpenAIBaseURLs[1] != "http://b/v1" {
		t.Errorf("llm %s urls %q", c.LLM, c.OpenAIBaseURLs)
	}
	if len(c.OpenAIModels) != 2 || c.OpenAIModels[1] != "small" {
		t.Errorf("openai models %q", c.OpenAIModels)
	}
}

func TestProviders(t *testing.T) {
	t.Parallel()

	t.Run("every provider with credentials is on, and DeepSeek is the default", func(t *testing.T) {
		t.Parallel()
		c, err := config.Load(nil, env("ANTHROPIC_API_KEY", "a", "DEEPSEEK_API_KEY", "d", "OPENROUTER_API_KEY", "o", "OLLAMA_API_KEY", "l", "OPENCODE_API_KEY", "c"))
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(c.Providers(), ","); got != "deepseek,anthropic,openrouter,opencode,ollama" {
			t.Errorf("providers = %s", got)
		}
		if c.LLM != "deepseek" || c.DeepSeekWire != "messages" {
			t.Errorf("default provider %q on the %q wire", c.LLM, c.DeepSeekWire)
		}
		// DeepSeek's documented per-model concurrency, unless overridden.
		if c.ProviderLimits["deepseek/deepseek-flash"] != 2500 || c.ProviderLimits["deepseek/deepseek-v4-pro"] != 500 {
			t.Errorf("limits = %v", c.ProviderLimits)
		}
		if !c.Checkpoint || c.MaxChildren != 8 {
			t.Errorf("checkpoint %t, max children %d", c.Checkpoint, c.MaxChildren)
		}
	})

	t.Run("an explicit default and overrides", func(t *testing.T) {
		t.Parallel()
		c, err := config.Load([]string{
			"-llm=openrouter", "-summary-provider=deepseek", "-effort=high",
			"-provider-limits=deepseek/deepseek-v4-pro=40, openrouter=16",
			"-openrouter-pin=deepseek/deepseek-v4-pro=deepseek, x/y=a|b",
		}, env("DEEPSEEK_API_KEY", "d", "OPENROUTER_API_KEY", "o"))
		if err != nil {
			t.Fatal(err)
		}
		if c.LLM != "openrouter" || c.SummaryProvider != "deepseek" || c.Effort != "high" {
			t.Errorf("llm %s, summary %s, effort %s", c.LLM, c.SummaryProvider, c.Effort)
		}
		if c.ProviderLimits["deepseek/deepseek-v4-pro"] != 40 || c.ProviderLimits["openrouter"] != 16 || c.ProviderLimits["deepseek/deepseek-flash"] != 2500 {
			t.Errorf("limits = %v", c.ProviderLimits)
		}
		if got := c.OpenRouterPin; len(got) != 2 || got["deepseek/deepseek-v4-pro"][0] != "deepseek" || len(got["x/y"]) != 2 {
			t.Errorf("pins = %v", got)
		}
	})
}

func TestValidation(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		args []string
		env  func(string) string
		want string
	}{
		"no provider at all":      {nil, env(), "no model provider is configured"},
		"a default without a key": {[]string{"-llm=deepseek"}, env("ANTHROPIC_API_KEY", "k"), "llm=deepseek has no credentials"},
		"unknown provider":        {[]string{"-llm=clippy"}, env(), "llm must be"},
		"removed provider":        {[]string{"-llm=nous"}, env(), "llm must be"},
		"summary provider is off": {[]string{"-llm=fake", "-summary-provider=anthropic"}, env(), "summary-provider"},
		"unknown deepseek wire":   {[]string{"-deepseek-wire=responses"}, env("DEEPSEEK_API_KEY", "k"), "deepseek-wire"},
		"openai without models":   {nil, env("OPENAI_API_KEY", "k"), "openai-models"},
		"unknown effort":          {[]string{"-llm=fake", "-effort=extreme"}, env(), "effort must be"},
		"malformed limit":         {[]string{"-llm=fake", "-provider-limits=deepseek"}, env(), "provider-limits"},
		"malformed pin":           {[]string{"-llm=fake", "-openrouter-pin=model"}, env(), "openrouter-pin"},
		"unknown backend":         {[]string{"-llm=fake", "-infer=gpu"}, env(), "infer must be"},
		"cap below debounce":      {[]string{"-llm=fake", "-debounce=5s"}, env(), "debounce"},
		"bad duration in env":     {[]string{"-llm=fake"}, env("CONTACTD_GAP", "soon"), "CONTACTD_GAP"},
		"bad number in env":       {[]string{"-llm=fake"}, env("CONTACTD_RECALL_K", "four"), "CONTACTD_RECALL_K"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := config.Load(tc.args, tc.env)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}

func TestHydratedLimit(t *testing.T) {
	t.Parallel()
	c, err := config.Load([]string{"-llm=fake", "-bytes-per-actor=100000"}, env())
	if err != nil {
		t.Fatal(err)
	}
	// 3 GiB limit, half budgeted for actors.
	if got, want := c.HydratedLimit(3<<30), (3<<30)/2/100000; got != want {
		t.Errorf("HydratedLimit(3GiB) = %d, want %d", got, want)
	}
	if got := c.HydratedLimit(math.MaxInt64); got != 0 {
		t.Errorf("no GOMEMLIMIT should mean unlimited (0), got %d", got)
	}
	c.MaxHydrated = 42
	if got := c.HydratedLimit(3 << 30); got != 42 {
		t.Errorf("an explicit max-hydrated must win, got %d", got)
	}
}

func TestSocketPath(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, "/data/contactd.sock"},
		{[]string{"-data-dir=/var/lib/contactd"}, "/var/lib/contactd/contactd.sock"},
		{[]string{"-socket=/run/contactd.sock"}, "/run/contactd.sock"},
		{[]string{"-socket=off"}, ""},
	} {
		c, err := config.Load(tc.args, env("ANTHROPIC_API_KEY", "k"))
		if err != nil {
			t.Fatal(err)
		}
		if got := c.SocketPath(); got != tc.want {
			t.Errorf("%v: socket = %q, want %q", tc.args, got, tc.want)
		}
	}
	// A local client is given only the data directory, and finds the same path.
	if got := config.DefaultSocket("/var/lib/contactd"); got != "/var/lib/contactd/contactd.sock" {
		t.Errorf("DefaultSocket = %q", got)
	}
}
