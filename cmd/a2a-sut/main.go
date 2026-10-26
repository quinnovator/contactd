// Command a2a-sut runs the production A2A transports with a scripted, in-memory
// backend for the official TCK. It never loads a provider or a contact database.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/quinnovator/contactd/internal/helpers"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:9999", "listen address")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg := helpers.DefaultConfig()
	cfg.BaseURL, cfg.Auth = "http://"+*addr, "off"
	h, err := helpers.HTTPHandler(newBackend(), cfg, log)
	if err != nil {
		log.Error("create SUT", "error", err)
		os.Exit(1)
	}
	srv := &http.Server{Addr: *addr, Handler: h, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdown); err != nil {
			log.Error("stop SUT", "error", err)
		}
	}()
	log.Info("A2A scripted SUT ready", "addr", *addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("serve SUT", "error", err)
		os.Exit(1)
	}
}
