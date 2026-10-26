// Command demosite runs a mutable status page for the A2A helper demo.
package main

import (
	"context"
	"errors"
	"flag"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

const page = `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Demo service status</title><style>body{font:18px system-ui;max-width:760px;margin:4rem auto;padding:0 1rem;color:#172532}table{width:100%;border-collapse:collapse}td,th{text-align:left;padding:1rem;border-bottom:1px solid #ccc}small{color:#526372}</style></head><body><h1>Demo service status</h1><p>Live component health. This page is data for the monitoring helper.</p><table><tr><th>Component</th><th>State</th><th>Since (UTC)</th></tr>{{range .}}<tr><td>{{.Name}}</td><td>{{.State}}</td><td><small>{{.Since}}</small></td></tr>{{end}}</table></body></html>`

type (
	component struct{ Name, State, Since string }
	site      struct {
		mu         sync.Mutex
		components []component
		template   *template.Template
		log        *slog.Logger
	}
)

func newSite(log *slog.Logger) *site {
	s := &site{template: template.Must(template.New("status").Parse(page)), log: log}
	for _, name := range []string{"API", "Web", "Search", "Billing"} {
		s.components = append(s.components, component{Name: name, State: "operational", Since: time.Now().UTC().Format(time.RFC3339)})
	}
	return s
}

func (s *site) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		rows := append([]component(nil), s.components...)
		s.mu.Unlock()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if err := s.template.Execute(w, rows); err != nil {
			s.log.DebugContext(r.Context(), "status page disconnected", "error", err)
		}
	})
	mux.HandleFunc("POST /set", func(w http.ResponseWriter, r *http.Request) {
		name, state := r.URL.Query().Get("component"), r.URL.Query().Get("state")
		if state != "operational" && state != "degraded" && state != "outage" && state != "maintenance" {
			http.Error(w, "state must be operational, degraded, outage or maintenance", http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		for i := range s.components {
			if s.components[i].Name == name {
				s.components[i].State = state
				s.components[i].Since = time.Now().UTC().Format(time.RFC3339)
				s.log.InfoContext(r.Context(), "demo status changed", "components", s.components)
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		http.Error(w, "unknown component", http.StatusBadRequest)
	})
	mux.HandleFunc("POST /reset", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		for i := range s.components {
			s.components[i].State = "operational"
			s.components[i].Since = time.Now().UTC().Format(time.RFC3339)
		}
		s.log.InfoContext(r.Context(), "demo status reset", "components", s.components)
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

func main() {
	addr := flag.String("addr", "127.0.0.1:9090", "status page listen address")
	flag.Parse()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	server := &http.Server{Addr: *addr, Handler: newSite(log).handler(), ReadHeaderTimeout: 5 * time.Second}
	done := make(chan struct{})
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
		close(done)
	}()
	log.InfoContext(ctx, "demo status site listening", "addr", *addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.ErrorContext(ctx, "serve demo status site", "error", err)
	}
	stop()
	<-done
}
