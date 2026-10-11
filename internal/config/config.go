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
	"strconv"
	"strings"
	"time"
)

// Config is every runtime setting. Each field has a flag and an environment
// variable (CONTACTD_ plus the flag name in upper snake case); flags win.
type Config struct {
	Addr      string
	DataDir   string // holds contacts.db, fs/<contact>/ and cache/
	LogFormat string // json | text
	LogLevel  string
	// ShutdownGrace is how long in-flight turns get to finish on SIGTERM.
	ShutdownGrace time.Duration
	// SyncFull selects synchronous=FULL for the event log.
	SyncFull bool

	// Model provider.
	LLM              string // anthropic | openai | fake
	AnthropicKey     string
	AnthropicBaseURL string
	OpenAIKey        string
	OpenAIBaseURLs   []string // more than one: contacts are rendezvous-hashed across replicas
	Model            string   // default model when an agent names none
	SummaryModel     string
	MaxTokens        int
	// Effort is sent to Anthropic as output_config.effort when set.
	Effort string
	// NoRefusalFallback stops the Anthropic adapter from asking the API to
	// re-run a classifier-declined request on another model.
	NoRefusalFallback bool

	// Local inference.
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
	CacheTTL     time.Duration
	TailBudget   int
	MaxToolSteps int
}

// Load parses flags and environment into a Config. args excludes the program
// name; getenv is os.Getenv in production.
func Load(args []string, getenv func(string) string) (Config, error) {
	var c Config
	fs := flag.NewFlagSet("contactd", flag.ContinueOnError)
	l := loader{fs: fs, getenv: getenv}

	l.str(&c.Addr, "addr", ":8080", "listen address")
	l.str(&c.DataDir, "data-dir", "/data", "directory for the SQLite file and tier-0 filesystems; must be a real local filesystem")
	l.str(&c.LogFormat, "log-format", "json", "json or text")
	l.str(&c.LogLevel, "log-level", "info", "debug, info, warn or error")
	l.dur(&c.ShutdownGrace, "shutdown-grace", 20*time.Second, "how long in-flight turns get to finish on SIGTERM")
	l.boolean(&c.SyncFull, "sync-full", false, "use synchronous=FULL: never lose an acknowledged message on power loss")

	l.str(&c.LLM, "llm", "anthropic", "model provider: anthropic, openai or fake")
	l.strEnv(&c.AnthropicKey, "anthropic-api-key", "ANTHROPIC_API_KEY", "", "Anthropic API key")
	l.str(&c.AnthropicBaseURL, "anthropic-base-url", "", "override the Anthropic API base URL")
	l.strEnv(&c.OpenAIKey, "openai-api-key", "OPENAI_API_KEY", "", "API key for the OpenAI-compatible endpoint")
	var openaiURLs string
	l.str(&openaiURLs, "openai-base-urls", "", "comma-separated OpenAI-compatible base URLs; several replicas are consistent-hashed by contact")
	l.str(&c.Model, "model", "", "default model when an agent config names none")
	l.str(&c.SummaryModel, "summary-model", "", "small model for lazy episode summaries")
	l.integer(&c.MaxTokens, "max-tokens", 0, "default max output tokens per model call")
	l.str(&c.Effort, "effort", "", "Anthropic effort level (low, medium, high, xhigh, max); empty uses the model default")
	l.boolean(&c.NoRefusalFallback, "no-refusal-fallback", false, "do not let Anthropic re-run a declined request on another model (billed at that model's rates)")

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
	l.dur(&c.CacheTTL, "cache-ttl", 5*time.Minute, "how long the provider prompt cache is assumed warm")
	l.integer(&c.TailBudget, "tail-budget", 24000, "tokens the tail may reach before step compaction")
	l.integer(&c.MaxToolSteps, "max-tool-steps", 8, "model calls allowed per turn")

	if err := fs.Parse(args); err != nil {
		return Config{}, fmt.Errorf("parse flags: %w", err)
	}
	if l.err != nil {
		return Config{}, l.err
	}

	c.BytesPerActor = int64(bytesPerActor)
	for u := range strings.SplitSeq(openaiURLs, ",") {
		if u = strings.TrimSpace(u); u != "" {
			c.OpenAIBaseURLs = append(c.OpenAIBaseURLs, u)
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
	check(c.LLM == "anthropic" || c.LLM == "openai" || c.LLM == "fake", "llm must be anthropic, openai or fake, not %q", c.LLM)
	check(c.LLM != "anthropic" || c.AnthropicKey != "", "llm=anthropic needs ANTHROPIC_API_KEY")
	check(c.LLM != "openai" || len(c.OpenAIBaseURLs) > 0 || c.OpenAIKey != "", "llm=openai needs OPENAI_API_KEY or openai-base-urls")
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
