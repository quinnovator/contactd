// Command contactd is the contact platform: one binary that lets a client
// talk to an AI contact the way it would talk to a person. main holds only
// wiring: it builds the one Config, constructs the adapters, hands them to
// the domain packages, and runs the graceful shutdown sequence.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/quinnovator/contactd/internal/actor"
	"github.com/quinnovator/contactd/internal/api"
	"github.com/quinnovator/contactd/internal/config"
	"github.com/quinnovator/contactd/internal/env/remote"
	"github.com/quinnovator/contactd/internal/env/wasm"
	"github.com/quinnovator/contactd/internal/eventlog"
	"github.com/quinnovator/contactd/internal/infer"
	fakeinfer "github.com/quinnovator/contactd/internal/infer/fake"
	"github.com/quinnovator/contactd/internal/llm"
	"github.com/quinnovator/contactd/internal/obs"
	"github.com/quinnovator/contactd/internal/plan"
	"github.com/quinnovator/contactd/internal/recall"
	"github.com/quinnovator/contactd/internal/turn"
)

func main() {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "probe" {
		os.Exit(probeMain(args[1:]))
	}
	if len(args) > 0 && args[0] == "chat" {
		os.Exit(chatMain(args[1:]))
	}
	cfg, err := config.Load(args, os.Getenv)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "contactd:", err)
		os.Exit(2)
	}
	log := obs.NewLogger(os.Stderr, cfg.LogFormat, cfg.LogLevel)
	if err := run(&cfg, log); err != nil {
		log.Error("contactd stopped with an error", "error", err)
		os.Exit(1)
	}
}

func run(cfg *config.Config, log *slog.Logger) (err error) {
	// Signals end the wait below; nothing else hangs off this context, so
	// shutdown order stays under our control.
	sigCtx, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stopSignals()

	memLimit := debug.SetMemoryLimit(-1)
	if memLimit == math.MaxInt64 {
		// Unlike GOMAXPROCS, GOMEMLIMIT does not read the cgroup limit.
		log.Warn("GOMEMLIMIT is not set; set it to the container limit minus room for ONNX Runtime's native memory")
	}

	// A stack of cleanups, run newest-first on the way out.
	var cleanups []func()
	defer func() {
		for _, cleanup := range slices.Backward(cleanups) {
			cleanup()
		}
	}()
	// base outlives everything except the writer; cancelling it stops the
	// batchers, turns and actors that Shutdown did not already stop.
	base, cancelBase := context.WithCancel(context.Background())
	defer cancelBase()

	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil { //nolint:gosec // the operator chooses the data directory
		return fmt.Errorf("create data dir: %w", err)
	}

	// Piece 1: the event log and its single writer.
	store, err := eventlog.Open(base, cfg.DBPath(), eventlog.Options{SyncFull: cfg.SyncFull})
	if err != nil {
		return fmt.Errorf("open event log: %w", err)
	}
	writerCtx, stopWriter := context.WithCancel(context.Background())
	var writer sync.WaitGroup
	writer.Go(func() { store.Run(writerCtx) })
	cleanups = append(cleanups, func() {
		// Step 4: drain the writer, close SQLite cleanly.
		stopWriter()
		writer.Wait()
		if cerr := store.Close(); cerr != nil {
			log.Error("close event log", "error", cerr)
		}
	})

	// Piece 3: the two local models behind micro-batchers.
	embedPipe, rerankPipe, closeModels, err := inference(cfg, log)
	if err != nil {
		return err
	}
	embedBatcher := infer.NewBatcher("embedder", embedPipe, cfg.InferWorkers)
	rerankBatcher := infer.NewBatcher("reranker", rerankPipe, cfg.InferWorkers)
	var batchers sync.WaitGroup
	batchers.Go(func() { embedBatcher.Run(base) })
	batchers.Go(func() { rerankBatcher.Run(base) })
	cleanups = append(cleanups, func() {
		cancelBase()
		batchers.Wait()
		if cerr := closeModels(); cerr != nil {
			log.Error("close models", "error", cerr)
		}
	})
	embedder, reranker := infer.NewEmbedder(embedBatcher), infer.NewReranker(rerankBatcher)

	// The provider core: every configured profile behind one client.
	client, err := models(base, cfg, log)
	if err != nil {
		return err
	}

	// Piece 6: environment providers, one per tier, each optional.
	envs, closeEnvs, err := environments(base, cfg, log)
	if err != nil {
		return err
	}
	cleanups = append(cleanups, closeEnvs)

	recallParams := recall.DefaultParams()
	recallParams.Tau, recallParams.GapMS, recallParams.Theta = float32(cfg.Tau), cfg.Gap.Milliseconds(), float32(cfg.Theta)
	planParams := plan.DefaultParams()
	planParams.TailBudget, planParams.K = cfg.TailBudget, cfg.RecallK
	planParams.Delta = float32(cfg.Delta)
	// The cache lifetime is not a setting any more: each route declares its
	// own, and the turn loop applies it.

	actorCfg := actor.DefaultConfig()
	actorCfg.IdleTTL, actorCfg.Debounce, actorCfg.DebounceCap = cfg.IdleTTL, cfg.Debounce, cfg.DebounceCap
	actorCfg.EnvIdleTTL, actorCfg.Recall = cfg.EnvIdleTTL, recallParams
	actorCfg.MaxHydrated = cfg.HydratedLimit(memLimit)
	actorCfg.Checkpoint, actorCfg.MaxChildren = cfg.Checkpoint, cfg.MaxChildren

	turnDeps := &turn.Deps{
		Embed:            embedder,
		Rerank:           reranker,
		Log:              store,
		Models:           client,
		Logger:           log,
		Recall:           recallParams,
		Plan:             planParams,
		Checkpoint:       cfg.Checkpoint,
		DefaultEffort:    cfg.Effort,
		DefaultMaxOutput: cfg.MaxTokens,
		MaxToolSteps:     cfg.MaxToolSteps,
		MaxToolOutput:    16 << 10,
		QueryTokens:      128,
	}
	registry := actor.New(base, actor.Deps{
		Log:       store,
		Create:    store,
		Hydrate:   store,
		Repair:    store,
		Embed:     embedder,
		Summarize: summarizer(cfg, client),
		Envs:      envs,
		Snapshots: store,
		Turn:      turnDeps,
	}, actorCfg, log)
	// The registry is what starts sub-agents, and the turn loop is what
	// asks for them; each needs the other.
	turnDeps.Delegate = registry

	server := api.New(store, registry, client, log)
	httpServer := &http.Server{
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// No WriteTimeout: SSE and WebSocket responses are long-lived.
	}
	listener, err := (&net.ListenConfig{}).Listen(base, "tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.Addr, err)
	}
	serveErr := make(chan error, 2)
	go func() { serveErr <- httpServer.Serve(listener) }()
	// The same API on a Unix socket, for clients on this machine: the file's
	// permissions are the access control, and its place in the data directory
	// is how a client finds the server.
	socket := cfg.SocketPath()
	if socket != "" {
		local, err := listenSocket(base, socket)
		switch {
		case err == nil:
			go func() { serveErr <- httpServer.Serve(local) }()
		case cfg.Socket != "":
			return errors.Join(err, listener.Close())
		default:
			// Nobody asked for the default socket by name, so a data directory
			// it cannot live in (a path too long for a socket address, most
			// often) is not a reason to refuse to start.
			log.Warn("not serving on a unix socket; contactd chat will need -url", "socket", socket, "error", err)
			socket = ""
		}
	}
	log.Info("contactd listening",
		"addr", listener.Addr().String(),
		"socket", socket,
		"llm", cfg.LLM,
		"infer", cfg.Infer,
		"infer_workers", cfg.InferWorkers,
		"max_hydrated", actorCfg.MaxHydrated,
		"env_tiers", len(envs))

	select {
	case <-sigCtx.Done():
		log.Info("shutdown signal received", "grace", cfg.ShutdownGrace.String())
	case err := <-serveErr:
		return fmt.Errorf("http server: %w", err)
	}

	// Graceful shutdown, in the order the design doc gives.
	// 1. Stop accepting new messages and return 503 from /healthz.
	server.Drain()
	// 2. Give in-flight turns a grace period; cancel them at the deadline,
	//    persisting partial replies as interrupted.
	// 3. Hibernate every actor, which snapshots environments.
	graceCtx, cancelGrace := context.WithTimeout(base, cfg.ShutdownGrace)
	registry.Shutdown(graceCtx)
	cancelGrace()
	// Streams ended when their actors did; close the listener and whatever
	// requests remain.
	httpCtx, cancelHTTP := context.WithTimeout(base, 5*time.Second)
	if herr := httpServer.Shutdown(httpCtx); herr != nil {
		log.Warn("http server did not stop cleanly", "error", herr)
	}
	cancelHTTP()
	// 4. The deferred cleanups drain the writer and close SQLite.
	log.Info("contactd stopped")
	return nil
}

// listenSocket listens on a Unix socket that only this user can connect to.
// A socket file left behind by a process that died is replaced; one that a
// live process answers on is not.
func listenSocket(ctx context.Context, path string) (net.Listener, error) {
	dctx, cancel := context.WithTimeout(ctx, time.Second)
	conn, err := (&net.Dialer{}).DialContext(dctx, "unix", path)
	cancel()
	if err == nil {
		_ = conn.Close() // only asked whether anyone was there
		return nil, fmt.Errorf("listen on %s: another process is already serving there", path)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) { //nolint:gosec // G703: the operator chooses the socket path
		return nil, fmt.Errorf("remove stale socket: %w", err)
	}
	l, err := (&net.ListenConfig{}).Listen(ctx, "unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil { //nolint:gosec // G703: the operator chooses the socket path
		return nil, errors.Join(fmt.Errorf("restrict %s: %w", path, err), l.Close())
	}
	return l, nil
}

// summarizer picks the route for lazy episode summaries. A summary is a
// small job, so where the operator names no model and the provider has a
// small one, that is used instead of the provider's default.
func summarizer(cfg *config.Config, client *llm.Client) *turn.ModelSummarizer {
	provider, model := cfg.SummaryProvider, cfg.SummaryModel
	if provider == "" {
		provider = cfg.LLM
	}
	if model == "" && provider == "anthropic" {
		model = "claude-haiku-5-5"
	}
	return &turn.ModelSummarizer{Models: client, Provider: provider, Model: model}
}

// inference returns the embedding and reranking pipelines for the configured
// backend.
func inference(cfg *config.Config, log *slog.Logger) (infer.Pipeline[string, []float32], infer.Pipeline[infer.Pair, float32], func() error, error) {
	if cfg.Infer == "fake" {
		log.Warn("using fake inference: recall is lexical only; do not use this in production")
		return fakeinfer.Embedder{}, fakeinfer.Reranker{}, func() error { return nil }, nil
	}
	if hazard := infer.SignalHazard(os.Getenv("GODEBUG")); hazard != "" {
		log.Warn("local inference is unreliable on this platform", "detail", hazard)
	}
	return openModels(cfg)
}

// environments builds the tier-0 and tier-1 providers that are configured. A
// tier with no provider makes its tools report themselves unavailable rather
// than failing the turn.
func environments(ctx context.Context, cfg *config.Config, log *slog.Logger) (map[uint8]turn.EnvLeaser, func(), error) {
	envs := make(map[uint8]turn.EnvLeaser)
	closeAll := func() {}

	if bases := wasm.DefaultBases(cfg.WasmDir); cfg.WasmDir != "" && len(bases) > 0 {
		start := time.Now()
		p, err := wasm.New(ctx, wasm.Config{
			FSRoot:   cfg.FSRoot(),
			CacheDir: cfg.CacheDir(),
			Bases:    bases,
			Logger:   log,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("start tier-0 environments: %w", err)
		}
		envs[0] = p
		closeAll = func() {
			cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			if cerr := p.Close(cctx); cerr != nil {
				log.Error("close tier-0 environments", "error", cerr)
			}
		}
		log.Info("tier-0 environments ready", "bases", len(bases), "compile_ms", obs.SinceMS(start))
	} else {
		log.Warn("no tier-0 runtimes found; in-process tools are unavailable", "wasm_dir", cfg.WasmDir)
	}

	if cfg.SandboxURL != "" {
		p, err := remote.New(remote.Config{BaseURL: cfg.SandboxURL, Token: cfg.SandboxToken, Logger: log})
		if err != nil {
			closeAll()
			return nil, nil, fmt.Errorf("configure tier-1 environments: %w", err)
		}
		envs[1] = p
	}
	return envs, closeAll, nil
}
