// Package config builds the one Config struct from environment variables and
// flags. It is constructed in cmd/contactd and passed down explicitly; there
// are no globals.
package config

import (
	"errors"
	"flag"
	"fmt"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Config is every runtime setting. Each field has a flag and an environment
// variable (CONTACTD_ plus the flag name in upper snake case); flags win.
type Config struct {
	Addr string
	// Socket is a Unix socket the API is also served on, for clients on the
	// same machine. Empty means contactd.sock in the data directory; "off"
	// disables it.
	Socket    string
	DataDir   string // holds contacts.db, fs/<contact>/ and cache/
	LogFormat string // json | text
	LogLevel  string
	// ShutdownGrace is how long in-flight turns get to finish on SIGTERM.
	ShutdownGrace time.Duration
	// SyncFull selects synchronous=FULL for the event log.
	SyncFull bool

	// Model provider.
	// LLM is the default provider: the one an agent config gets when it
	// names none. Every provider with credentials is available to agents
	// that name it. Empty picks the first configured one, DeepSeek first.
	LLM string

	AnthropicKey     string
	AnthropicBaseURL string
	// NoRefusalFallback stops Anthropic re-running a declined request on
	// another model.
	NoRefusalFallback bool

	DeepSeekKey     string
	DeepSeekBaseURL string
	DeepSeekWire    string // messages (default) | chat

	OpenRouterKey     string
	OpenRouterBaseURL string
	OpenRouterCatalog string              // path to a saved model catalog; empty fetches it at start-up
	OpenRouterPin     map[string][]string // model -> the upstreams it may be served by
	OpenRouterReferer string
	OpenRouterTitle   string

	OpenCodeKey     string
	OpenCodeBaseURL string

	OllamaKey     string
	OllamaBaseURL string

	OpenAIKey      string
	OpenAIBaseURLs []string // more than one: contacts are rendezvous-hashed across replicas
	OpenAIModels   []string // the models the endpoint serves; the first is its default
	OpenAIContext  int      // context window of those models

	Model           string // default model on the default provider; empty takes the provider's own
	SummaryProvider string // provider for lazy episode summaries; empty uses the default provider
	SummaryModel    string
	MaxTokens       int    // default output cap for agents that set none
	Effort          string // default effort for agents that set none
	// ProviderLimits caps concurrent model calls, keyed by provider
	// ("deepseek") or route ("deepseek/deepseek-v4-pro").
	ProviderLimits map[string]int
	// AffinitySecret keys the hash that stands in for a contact ID when a
	// provider is asked to pin a conversation. Empty keeps one in the data
	// directory.
	AffinitySecret string

	Infer        string // ort | fake
	ModelsDir    string // holds embedder/ and reranker/
	ModelVariant string // int8 | fp32
	Pooling      string // cls (bge) | mean (MiniLM)
	OnnxLib      string // path to libonnxruntime; default: $ONNXRUNTIME_LIB_DIR or /usr/lib
	InferWorkers int    // worker loops per model; default half of GOMAXPROCS

	// Environments.
	WasmDir      string // tier-0 runtimes (*.wasm); empty disables tier 0
	SandboxURL   string // tier-1 snapshot-sandbox service; empty disables tier 1
	SandboxToken string

	// Actors.
	IdleTTL       time.Duration
	Debounce      time.Duration
	DebounceCap   time.Duration
	EnvIdleTTL    time.Duration
	MaxHydrated   int   // 0 derives it from GOMEMLIMIT
	BytesPerActor int64 // measured by the density benchmark; used to derive MaxHydrated

	// Recall and planning. These are starting values; tune them with
	// cmd/replay before changing them here.
	Tau          float64
	Gap          time.Duration
	Theta        float64
	RecallK      int
	Delta        float64
	TailBudget   int
	MaxToolSteps int
	// Checkpoint folds the oldest part of an over-budget tail into a
	// summary instead of cutting it.
	Checkpoint bool
	// MaxChildren bounds the sub-agents one contact may have running.
	MaxChildren int
}

// Load parses flags and environment into a Config. args excludes the program
// name; getenv is os.Getenv in production.
func Load(args []string, getenv func(string) string) (Config, error) {
	var c Config
	fs := flag.NewFlagSet("contactd", flag.ContinueOnError)
	l := loader{fs: fs, getenv: getenv}

	l.str(&c.Addr, "addr", ":8080", "listen address")
	l.str(&c.Socket, "socket", "", "Unix socket to also serve the API on, for local clients such as contactd chat; empty means contactd.sock in the data directory, off disables it")
	l.str(&c.DataDir, "data-dir", "/data", "directory for the SQLite file and tier-0 filesystems; must be a real local filesystem")
	l.str(&c.LogFormat, "log-format", "json", "json or text")
	l.str(&c.LogLevel, "log-level", "info", "debug, info, warn or error")
	l.dur(&c.ShutdownGrace, "shutdown-grace", 20*time.Second, "how long in-flight turns get to finish on SIGTERM")
	l.boolean(&c.SyncFull, "sync-full", false, "use synchronous=FULL: never lose an acknowledged message on power loss")

	l.str(&c.LLM, "llm", "", "default model provider: "+strings.Join(providerNames(), ", ")+"; empty picks the first one that is configured")
	l.strEnv(&c.AnthropicKey, "anthropic-api-key", "ANTHROPIC_API_KEY", "", "Anthropic API key")
	l.str(&c.AnthropicBaseURL, "anthropic-base-url", "", "override the Anthropic API base URL")
	l.boolean(&c.NoRefusalFallback, "no-refusal-fallback", false, "do not let Anthropic re-run a declined request on another model (billed at that model's rates)")
	l.strEnv(&c.DeepSeekKey, "deepseek-api-key", "DEEPSEEK_API_KEY", "", "DeepSeek API key")
	l.str(&c.DeepSeekBaseURL, "deepseek-base-url", "", "override the DeepSeek API base URL")
	l.str(&c.DeepSeekWire, "deepseek-wire", "messages", "DeepSeek endpoint to use: messages (Anthropic format) or chat (Chat Completions)")
	l.strEnv(&c.OpenRouterKey, "openrouter-api-key", "OPENROUTER_API_KEY", "", "OpenRouter API key")
	l.str(&c.OpenRouterBaseURL, "openrouter-base-url", "", "override the OpenRouter API base URL")
	l.str(&c.OpenRouterCatalog, "openrouter-catalog", "", "path to a saved copy of the OpenRouter model catalog; empty fetches it at start-up")
	var openrouterPin string
	l.str(&openrouterPin, "openrouter-pin", "", "upstreams each OpenRouter model may be served by: model=provider[|provider],model=provider")
	l.str(&c.OpenRouterReferer, "openrouter-referer", "", "HTTP-Referer sent to OpenRouter to identify this app")
	l.str(&c.OpenRouterTitle, "openrouter-title", "", "X-OpenRouter-Title sent to OpenRouter to identify this app")
	l.strEnv(&c.OpenCodeKey, "opencode-api-key", "OPENCODE_API_KEY", "", "OpenCode Go API key")
	l.str(&c.OpenCodeBaseURL, "opencode-base-url", "", "override the OpenCode Go base URL")
	l.strEnv(&c.OllamaKey, "ollama-api-key", "OLLAMA_API_KEY", "", "Ollama Cloud API key")
	l.str(&c.OllamaBaseURL, "ollama-base-url", "", "override the Ollama Cloud base URL")
	l.strEnv(&c.OpenAIKey, "openai-api-key", "OPENAI_API_KEY", "", "API key for the OpenAI-compatible endpoint")
	var openaiURLs, openaiModels string
	l.str(&openaiURLs, "openai-base-urls", "", "comma-separated OpenAI-compatible base URLs; several replicas are consistent-hashed by contact")
	l.str(&openaiModels, "openai-models", "", "comma-separated models the OpenAI-compatible endpoint serves; the first is its default")
	l.integer(&c.OpenAIContext, "openai-context", 128000, "context window, in tokens, of the OpenAI-compatible endpoint's models")
	l.str(&c.Model, "model", "", "default model on the default provider; empty takes the provider's own default")
	l.str(&c.SummaryProvider, "summary-provider", "", "provider for lazy episode summaries; empty uses the default provider")
	l.str(&c.SummaryModel, "summary-model", "", "small model for lazy episode summaries; empty uses the summary provider's default")
	l.integer(&c.MaxTokens, "max-tokens", 0, "default max output tokens per model call, for agents that set none")
	l.str(&c.Effort, "effort", "", "default reasoning effort for agents that set none: none, low, medium, high, xhigh or max; each provider sends the nearest value it accepts")
	var limits string
	l.str(&limits, "provider-limits", "", "concurrent model calls allowed per provider or route: deepseek/deepseek-v4-pro=500,openrouter=64")
	l.str(&c.AffinitySecret, "affinity-secret", "", "secret that keys the per-contact hash sent to providers that pin conversations; empty keeps one in the data directory")

	l.str(&c.Infer, "infer", "ort", "local inference backend: ort or fake")
	l.str(&c.ModelsDir, "models-dir", "/models", "directory holding embedder/ and reranker/")
	l.str(&c.ModelVariant, "model-variant", "int8", "int8 or fp32 ONNX files")
	l.str(&c.Pooling, "pooling", "cls", "embedder pooling: cls (bge) or mean (all-MiniLM-L6-v2)")
	l.strEnv(&c.OnnxLib, "onnx-lib", "CONTACTD_ONNX_LIB", "", "path to the ONNX Runtime shared library")
	l.integer(&c.InferWorkers, "infer-workers", 0, "worker loops per model; 0 = half of GOMAXPROCS")

	l.str(&c.WasmDir, "wasm-dir", "/wasm", "directory of tier-0 WASM runtimes; empty disables tier 0")
	l.str(&c.SandboxURL, "sandbox-url", "", "base URL of the tier-1 snapshot-sandbox service; empty disables tier 1")
	l.str(&c.SandboxToken, "sandbox-token", "", "bearer token for the snapshot-sandbox service")

	l.dur(&c.IdleTTL, "idle-ttl", 10*time.Minute, "an idle contact is hibernated after this")
	l.dur(&c.Debounce, "debounce", 700*time.Millisecond, "quiet time that ends a burst of messages")
	l.dur(&c.DebounceCap, "debounce-cap", 3*time.Second, "longest a reply is held back after the first pending message")
	l.dur(&c.EnvIdleTTL, "env-idle-ttl", 2*time.Minute, "an unused environment lease is snapshotted and released after this")
	l.integer(&c.MaxHydrated, "max-hydrated", 0, "contacts kept in memory; 0 derives it from GOMEMLIMIT")
	var bytesPerActor int
	// BenchmarkDensity measures about 20 KB per contact (60-message tail, 40
	// episodes) before message bodies; 48 KiB leaves room for those and for
	// longer tails.
	l.integer(&bytesPerActor, "bytes-per-actor", 48<<10, "memory budgeted per hydrated contact, used to derive max-hydrated (see BenchmarkDensity)")

	l.float(&c.Tau, "tau", 0.45, "topic-drift threshold: cosine to the episode centroid")
	l.dur(&c.Gap, "gap", 30*time.Minute, "silence that opens a new episode")
	l.float(&c.Theta, "theta", 0, "rerank score an episode must clear to be recalled")
	l.integer(&c.RecallK, "recall-k", 4, "most episodes recalled per turn")
	l.float(&c.Delta, "delta", 1.0, "margin a new episode needs to displace a warm recall set")
	l.integer(&c.TailBudget, "tail-budget", 24000, "tokens the tail may reach before it is folded or cut (capped at half the model's context window)")
	l.integer(&c.MaxToolSteps, "max-tool-steps", 8, "model calls allowed per turn")
	l.boolean(&c.Checkpoint, "checkpoint", true, "fold the oldest part of an over-budget tail into a summary written by the contact's model, instead of cutting it")
	l.integer(&c.MaxChildren, "max-children", 8, "sub-agents one contact may have running at once")

	if err := fs.Parse(args); err != nil {
		return Config{}, fmt.Errorf("parse flags: %w", err)
	}
	if l.err != nil {
		return Config{}, l.err
	}

	c.BytesPerActor = int64(bytesPerActor)
	c.OpenAIBaseURLs, c.OpenAIModels = list(openaiURLs), list(openaiModels)
	var err error
	if c.OpenRouterPin, err = parsePins(openrouterPin); err != nil {
		return Config{}, err
	}
	if c.ProviderLimits, err = parseLimits(limits); err != nil {
		return Config{}, err
	}
	// DeepSeek counts concurrency per account and model; staying under its
	// documented limits beats finding them by 429.
	if c.DeepSeekKey != "" {
		for route, n := range map[string]int{"deepseek/deepseek-flash": 2500, "deepseek/deepseek-v4-pro": 500} {
			if _, set := c.ProviderLimits[route]; !set {
				c.ProviderLimits[route] = n
			}
		}
	}
	if c.LLM == "" {
		if on := c.Providers(); len(on) > 0 {
			c.LLM = on[0]
		}
	}
	if c.InferWorkers <= 0 {
		// About half of GOMAXPROCS, leaving cores for requests and SQLite.
		c.InferWorkers = max(runtime.GOMAXPROCS(0)/2, 1)
	}
	if c.OnnxLib == "" {
		dir := getenv("ONNXRUNTIME_LIB_DIR")
		if dir == "" {
			dir = "/usr/lib"
		}
		name := "libonnxruntime.so"
		if runtime.GOOS == "darwin" {
			name = "libonnxruntime.dylib"
		}
		c.OnnxLib = filepath.Join(dir, name)
	}
	return c, c.validate()
}

func (c *Config) validate() error {
	var errs []error
	check := func(ok bool, format string, args ...any) {
		if !ok {
			errs = append(errs, fmt.Errorf(format, args...))
		}
	}
	on := c.Providers()
	check(c.LLM != "", "no model provider is configured: set an API key (DEEPSEEK_API_KEY, ANTHROPIC_API_KEY, OPENROUTER_API_KEY, OPENCODE_API_KEY, OLLAMA_API_KEY, or OPENAI_API_KEY / openai-base-urls) or llm=fake")
	check(c.LLM == "" || slices.Contains(providerNames(), c.LLM), "llm must be one of %s, not %q", strings.Join(providerNames(), ", "), c.LLM)
	check(c.LLM == "" || !slices.Contains(providerNames(), c.LLM) || slices.Contains(on, c.LLM), "llm=%s has no credentials configured", c.LLM)
	check(c.SummaryProvider == "" || slices.Contains(on, c.SummaryProvider), "summary-provider=%s is not a configured provider", c.SummaryProvider)
	check(c.DeepSeekWire == "messages" || c.DeepSeekWire == "chat", "deepseek-wire must be messages or chat, not %q", c.DeepSeekWire)
	check(!slices.Contains(on, "openai") || len(c.OpenAIModels) > 0 || c.Model != "" && c.LLM == "openai",
		"the OpenAI-compatible provider needs openai-models: unknown model names are refused, not passed through")
	check(c.OpenAIContext > 0, "openai-context must be positive")
	check(c.MaxChildren > 0, "max-children must be positive")
	check(slices.Contains([]string{"", "none", "low", "medium", "high", "xhigh", "max"}, c.Effort), "effort must be none, low, medium, high, xhigh or max, not %q", c.Effort)
	check(c.Infer == "ort" || c.Infer == "fake", "infer must be ort or fake, not %q", c.Infer)
	check(c.ModelVariant == "int8" || c.ModelVariant == "fp32", "model-variant must be int8 or fp32, not %q", c.ModelVariant)
	check(c.Debounce > 0 && c.DebounceCap >= c.Debounce, "debounce must be positive and no longer than debounce-cap")
	check(c.IdleTTL > 0, "idle-ttl must be positive")
	check(c.RecallK > 0, "recall-k must be positive")
	check(c.TailBudget > 0, "tail-budget must be positive")
	check(c.MaxToolSteps > 0, "max-tool-steps must be positive")
	check(c.BytesPerActor > 0, "bytes-per-actor must be positive")
	return errors.Join(errs...)
}

// Providers lists the providers that have credentials, in the order the
// default is picked from.
func (c *Config) Providers() []string {
	var on []string
	add := func(name string, ok bool) {
		if ok {
			on = append(on, name)
		}
	}
	add("deepseek", c.DeepSeekKey != "")
	add("anthropic", c.AnthropicKey != "")
	add("openrouter", c.OpenRouterKey != "")
	add("opencode", c.OpenCodeKey != "")
	add("ollama", c.OllamaKey != "")
	add("openai", c.OpenAIKey != "" || len(c.OpenAIBaseURLs) > 0)
	add("fake", c.LLM == "fake")
	return on
}

func providerNames() []string {
	return []string{"deepseek", "anthropic", "openrouter", "opencode", "ollama", "openai", "fake"}
}

func list(csv string) []string {
	var out []string
	for v := range strings.SplitSeq(csv, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// parsePins reads "model=provider|provider,model=provider".
func parsePins(s string) (map[string][]string, error) {
	pins := make(map[string][]string)
	for _, item := range list(s) {
		model, ups, ok := strings.Cut(item, "=")
		if !ok || model == "" || ups == "" {
			return nil, fmt.Errorf("openrouter-pin: %q is not model=provider[|provider]", item)
		}
		for up := range strings.SplitSeq(ups, "|") {
			if up = strings.TrimSpace(up); up != "" {
				pins[model] = append(pins[model], up)
			}
		}
	}
	return pins, nil
}

// parseLimits reads "provider=n,provider/model=n".
func parseLimits(s string) (map[string]int, error) {
	limits := make(map[string]int)
	for _, item := range list(s) {
		key, v, ok := strings.Cut(item, "=")
		n, err := strconv.Atoi(v)
		if !ok || key == "" || err != nil || n <= 0 {
			return nil, fmt.Errorf("provider-limits: %q is not provider=n with n above zero", item)
		}
		limits[key] = n
	}
	return limits, nil
}

// SocketOff is the value of -socket that disables the Unix socket.
const SocketOff = "off"

// DefaultSocket is where the Unix socket of a server with this data
// directory is, unless it was told otherwise. The server and its local
// clients both derive it, so a client needs only the data directory.
func DefaultSocket(dataDir string) string { return filepath.Join(dataDir, "contactd.sock") }

// SocketPath is the Unix socket to serve on, or empty when it is disabled.
func (c *Config) SocketPath() string {
	switch c.Socket {
	case SocketOff:
		return ""
	case "":
		return DefaultSocket(c.DataDir)
	default:
		return c.Socket
	}
}

// DBPath is the SQLite file.
func (c *Config) DBPath() string { return filepath.Join(c.DataDir, "contacts.db") }

// FSRoot is where tier-0 per-contact filesystems live.
func (c *Config) FSRoot() string { return filepath.Join(c.DataDir, "fs") }

// CacheDir holds rebuildable caches, such as compiled WASM modules.
func (c *Config) CacheDir() string { return filepath.Join(c.DataDir, "cache") }

// OnnxFile is the ONNX file name for the configured variant.
func (c *Config) OnnxFile() string {
	if c.ModelVariant == "int8" {
		return "model_int8.onnx"
	}
	return "model.onnx"
}

// HydratedLimit returns MaxHydrated, deriving it from the Go memory limit
// when unset so hibernation kicks in before GC thrash. Half of the limit is
// budgeted for actors; the rest is headroom for turns in flight. memLimit is
// debug.SetMemoryLimit(-1); math.MaxInt64 there means no limit, which yields
// 0 (unlimited).
func (c *Config) HydratedLimit(memLimit int64) int {
	if c.MaxHydrated > 0 {
		return c.MaxHydrated
	}
	if memLimit <= 0 || memLimit > 1<<50 {
		return 0
	}
	return int(memLimit / 2 / c.BytesPerActor)
}

// loader registers each setting as a flag whose default comes from the
// environment.
type loader struct {
	fs     *flag.FlagSet
	getenv func(string) string
	err    error
}

func envName(flagName string) string {
	return "CONTACTD_" + strings.ToUpper(strings.ReplaceAll(flagName, "-", "_"))
}

func (l *loader) lookup(env string) (string, bool) {
	v := l.getenv(env)
	return v, v != ""
}

func (l *loader) fail(env string, err error) {
	l.err = errors.Join(l.err, fmt.Errorf("%s: %w", env, err))
}

func (l *loader) str(p *string, name, def, usage string) {
	l.strEnv(p, name, envName(name), def, usage)
}

func (l *loader) strEnv(p *string, name, env, def, usage string) {
	if v, ok := l.lookup(env); ok {
		def = v
	}
	l.fs.StringVar(p, name, def, usage+" ($"+env+")")
}

func (l *loader) integer(p *int, name string, def int, usage string) {
	env := envName(name)
	if v, ok := l.lookup(env); ok {
		n, err := strconv.Atoi(v)
		if err != nil {
			l.fail(env, err)
		}
		def = n
	}
	l.fs.IntVar(p, name, def, usage+" ($"+env+")")
}

func (l *loader) float(p *float64, name string, def float64, usage string) {
	env := envName(name)
	if v, ok := l.lookup(env); ok {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			l.fail(env, err)
		}
		def = f
	}
	l.fs.Float64Var(p, name, def, usage+" ($"+env+")")
}

func (l *loader) dur(p *time.Duration, name string, def time.Duration, usage string) {
	env := envName(name)
	if v, ok := l.lookup(env); ok {
		d, err := time.ParseDuration(v)
		if err != nil {
			l.fail(env, err)
		}
		def = d
	}
	l.fs.DurationVar(p, name, def, usage+" ($"+env+")")
}

func (l *loader) boolean(p *bool, name string, def bool, usage string) {
	env := envName(name)
	if v, ok := l.lookup(env); ok {
		b, err := strconv.ParseBool(v)
		if err != nil {
			l.fail(env, err)
		}
		def = b
	}
	l.fs.BoolVar(p, name, def, usage+" ($"+env+")")
}
