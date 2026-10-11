// Command fakellm serves the fake model over the OpenAI Chat Completions
// wire format, for load tests. Point contactd at it with
//
//	contactd -llm=openai -openai-base-urls=http://localhost:9090/v1 -model=fake
//
// It streams scripted replies with configurable pacing and keeps a real
// prefix cache, so the cached-token counts it reports respond to prompt
// layout the way a provider's would.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/quinnovator/contactd/internal/llm/fake"
	"github.com/quinnovator/contactd/internal/obs"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fakellm:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		addr     = flag.String("addr", ":9090", "listen address")
		ttft     = flag.Duration("first-token", 300*time.Millisecond, "delay before the first token of a fully cached prompt")
		uncached = flag.Duration("per-uncached-token", 50*time.Microsecond, "extra first-token delay per input token that missed the cache")
		chunk    = flag.Duration("per-chunk", 20*time.Millisecond, "delay between streamed chunks")
		words    = flag.Int("reply-words", 40, "length of unscripted replies")
		cacheTTL = flag.Duration("cache-ttl", 5*time.Minute, "how long a cached prefix lives")
	)
	flag.Parse()
	log := obs.NewLogger(os.Stderr, "text", "info")

	srv := &http.Server{
		Addr: *addr,
		Handler: fake.Handler(fake.Options{
			FirstToken:       *ttft,
			PerUncachedToken: *uncached,
			PerChunk:         *chunk,
			ReplyWords:       *words,
			CacheTTL:         *cacheTTL,
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("fakellm listening", "addr", *addr)

	select {
	case err := <-errc:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}
