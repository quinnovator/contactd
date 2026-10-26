// Package apitest runs the real API over a real SQLite file with a scripted
// model, for tests of anything that talks to a server: the client, and the
// terminal interface on top of it.
package apitest

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/quinnovator/contactd/internal/actor"
	"github.com/quinnovator/contactd/internal/api"
	"github.com/quinnovator/contactd/internal/env/workdir"
	"github.com/quinnovator/contactd/internal/eventlog"
	"github.com/quinnovator/contactd/internal/fetch"
	"github.com/quinnovator/contactd/internal/helpers"
	fakeinfer "github.com/quinnovator/contactd/internal/infer/fake"
	fakellm "github.com/quinnovator/contactd/internal/llm/fake"
	"github.com/quinnovator/contactd/internal/obs"
	"github.com/quinnovator/contactd/internal/plan"
	"github.com/quinnovator/contactd/internal/recall"
	"github.com/quinnovator/contactd/internal/requester"
	"github.com/quinnovator/contactd/internal/turn"
)

// Options tunes the server. The zero value is a fast one.
type Options struct {
	// LLM scripts the model. PerChunk defaults to a millisecond.
	LLM fakellm.Options
	// Actor, when set, adjusts the actor configuration.
	Actor func(*actor.Config)
	// FSRoot, when set, is a directory the server serves contacts' files
	// from: contact 7's are under FSRoot/7. It must exist. No tool runs
	// here, so a test writes the files itself.
	FSRoot     string
	Helpers    *helpers.Config
	FetchAllow []string
	Database   string
	Address    string
}

// Server is a running API.
type Server struct {
	// URL is the server's TCP address, and Socket the path of a Unix socket
	// serving the same handler.
	URL    string
	Socket string
	Store  *eventlog.Store

	conns *tracker
	stop  func()
}

// Close stops this server, allowing a test to reopen its database.
func (s *Server) Close() { s.stop() }

// DropConnections cuts every open connection on the TCP address, WebSockets
// included, as a network failure would. The server keeps running.
func (s *Server) DropConnections() { s.conns.drop() }

// tracker is a listener that remembers what it accepted. The test server's
// own bookkeeping forgets a connection once a WebSocket takes it over.
type tracker struct {
	net.Listener

	mu    sync.Mutex
	conns []net.Conn
}

func (t *tracker) Accept() (net.Conn, error) {
	c, err := t.Listener.Accept()
	if err == nil {
		t.mu.Lock()
		t.conns = append(t.conns, c)
		t.mu.Unlock()
	}
	return c, err //nolint:wrapcheck // a listener passes its inner listener's error on unchanged
}

func (t *tracker) drop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, c := range t.conns {
		_ = c.Close() // already closed is fine
	}
	t.conns = nil
}

type embed struct{}

func (embed) Embed(_ context.Context, texts []string, _ recall.Priority) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = fakeinfer.Embed(t)
	}
	return out, nil
}

type rerank struct{}

func (rerank) Score(_ context.Context, q string, ps []string) ([]float32, error) {
	out := make([]float32, len(ps))
	for i, p := range ps {
		out[i] = fakeinfer.Score(q, p)
	}
	return out, nil
}

// Start runs a server until the test ends.
func Start(tb testing.TB, opt Options) *Server {
	tb.Helper()
	db := opt.Database
	if db == "" {
		db = filepath.Join(tb.TempDir(), "contacts.db")
	}
	store, err := eventlog.Open(tb.Context(), db, eventlog.Options{})
	if err != nil {
		tb.Fatal(err)
	}
	wctx, stopWriter := context.WithCancel(context.Background())
	writerDone := make(chan struct{})
	go func() { store.Run(wctx); close(writerDone) }()

	if opt.LLM.PerChunk == 0 {
		opt.LLM.PerChunk = time.Millisecond
	}
	models := fakellm.NewClient(fakellm.New(opt.LLM))
	cfg := actor.DefaultConfig()
	cfg.Debounce, cfg.DebounceCap = 20*time.Millisecond, 100*time.Millisecond
	if opt.Actor != nil {
		opt.Actor(&cfg)
	}
	ctx, cancel := context.WithCancel(context.Background())
	td := &turn.Deps{Embed: embed{}, Rerank: rerank{}, Log: store, Models: models, Logger: obs.Discard(), Recall: cfg.Recall, Plan: plan.DefaultParams(), MaxToolSteps: 4, MaxToolOutput: 4096, QueryTokens: 128}
	reg := actor.New(ctx, actor.Deps{
		Log: store, Create: store, Hydrate: store, Repair: store, Embed: embed{}, Snapshots: store,
		Summarize: &turn.ModelSummarizer{Models: models},
		Turn:      td,
	}, cfg, obs.Discard())

	server := api.New(store, reg, models, obs.Discard())
	closeFiles := func() {}
	if opt.FSRoot != "" {
		dirs, err := workdir.Open(opt.FSRoot)
		if err != nil {
			tb.Fatal(err)
		}
		server.WithFiles(dirs)
		closeFiles = func() { _ = dirs.Close() } // read-only; nothing to lose
	}
	tcp := httptest.NewUnstartedServer(nil)
	if opt.Address != "" {
		_ = tcp.Listener.Close()
		listener, err := (&net.ListenConfig{}).Listen(tb.Context(), "tcp", opt.Address)
		if err != nil {
			tb.Fatal(err)
		}
		tcp.Listener = listener
	}
	stopHelpers := func() {}
	if opt.Helpers != nil {
		hc := *opt.Helpers
		hc.BaseURL = "http://" + tcp.Listener.Addr().String()
		hc.PushAllow = []string{tcp.Listener.Addr().String()}
		svc := helpers.New(helpers.Deps{Contacts: reg, Creator: store, Events: store, Store: store}, hc, obs.Discard())
		reg.SetHelperHook(svc)
		req := requester.New(store, reg, requester.Config{AgentURL: hc.BaseURL, BaseURL: hc.BaseURL, Token: hc.Token}, obs.Discard())
		td.Helpers = req
		td.Fetch = fetch.New(fetch.Config{Allow: opt.FetchAllow})
		h, err := svc.HTTPHandler()
		if err != nil {
			tb.Fatal(err)
		}
		mux := http.NewServeMux()
		mux.Handle("/", h)
		mux.Handle("POST /a2a/push", req.Webhook())
		server.WithA2A(mux).WithHelpers(req)
		hctx, hcancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() {
			var wg sync.WaitGroup
			wg.Go(func() { svc.Run(hctx) })
			wg.Go(func() { req.Run(hctx) })
			wg.Wait()
			close(done)
		}()
		stopHelpers = func() { hcancel(); <-done }
	}
	handler := server.Handler()
	tcp.Config.Handler = handler
	conns := &tracker{Listener: tcp.Listener}
	tcp.Listener = conns
	tcp.Start()

	// A socket address has room for about a hundred bytes, and a test's
	// temporary directory is often longer than that on its own.
	dir, err := os.MkdirTemp("", "ctd") //nolint:usetesting // tb.TempDir is too long a path for a socket address
	if err != nil {
		tb.Fatal(err)
	}
	socket := filepath.Join(dir, "s")
	local, err := (&net.ListenConfig{}).Listen(tb.Context(), "unix", socket)
	if err != nil {
		tb.Fatal(err)
	}
	unix := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = unix.Serve(local) }() // ends when the test closes it

	stop := sync.OnceFunc(func() {
		reg.Shutdown(ctx)
		stopHelpers()
		tcp.Close()
		_ = unix.Close()
		closeFiles()
		_ = os.RemoveAll(dir)
		cancel()
		stopWriter()
		<-writerDone
		if err := store.Close(); err != nil {
			tb.Errorf("close store: %v", err)
		}
	})
	tb.Cleanup(stop)
	return &Server{URL: tcp.URL, Socket: socket, Store: store, conns: conns, stop: stop}
}
