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
		t.Errorf("paths %s %s, want the volume layout from the doc", c.DBPath(), c.FSRoot())
	}
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
		[]string{"-debounce=250ms", "-llm=openai", "-openai-base-urls=http://a/v1, http://b/v1"},
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
}

func TestValidation(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		args []string
		env  func(string) string
		want string
	}{
		"anthropic without a key": {nil, env(), "ANTHROPIC_API_KEY"},
		"unknown provider":        {[]string{"-llm=clippy"}, env(), "llm must be"},
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
