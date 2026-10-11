package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/quinnovator/contactd/internal/config"
	"github.com/quinnovator/contactd/internal/llm/probe"
	"github.com/quinnovator/contactd/internal/obs"
)

// probeMain runs "contactd probe": a few small live requests to one route,
// and a report of where the endpoint contradicts what its profile declares.
// It takes the same flags and environment as the server, plus the route.
//
// Exit status: 0 when nothing differs, 1 when something does, 2 when the
// probe could not run.
func probeMain(args []string) int {
	fs := flag.NewFlagSet("contactd probe", flag.ContinueOnError)
	provider := fs.String("provider", "", "provider to probe; empty means the default provider")
	model := fs.String("model", "", "model to probe; empty means the provider's default")
	pause := fs.Duration("pause", 3*time.Second, "wait between two requests that should share a cache entry")
	fs.Usage = func() {
		// Help text that cannot be written has nowhere better to go.
		_, _ = fmt.Fprint(fs.Output(), "usage: contactd probe [-provider name] [-model name] [-pause 3s] [-- server flags]\n"+
			"Sends about a dozen small requests to one model route and compares what comes\n"+
			"back with the capabilities its profile declares. Credentials come from the same\n"+
			"environment variables and flags as the server (put server flags after --).\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	// The probe needs providers and nothing else, so a data directory under
	// the temp dir stands in for the server's (it holds only the affinity
	// secret, which a probe has no need to keep).
	dir, err := os.MkdirTemp("", "contactd-probe-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "contactd probe:", err)
		return 2
	}
	defer func() { _ = os.RemoveAll(dir) }() // a leftover temp dir is harmless
	serverArgs := append([]string{"-data-dir=" + dir}, fs.Args()...)
	if *provider != "" {
		serverArgs = append(serverArgs, "-llm="+*provider)
	}
	if *model != "" {
		// Also the deployment's default model, so that a provider with no
		// default of its own can start.
		serverArgs = append(serverArgs, "-model="+*model)
	}
	cfg, err := config.Load(serverArgs, os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "contactd probe:", err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	log := obs.NewLogger(os.Stderr, "text", "warn")
	client, err := models(ctx, &cfg, log)
	if err != nil {
		fmt.Fprintln(os.Stderr, "contactd probe:", err)
		return 2
	}
	p, err := client.Provider(cfg.LLM)
	if err != nil {
		fmt.Fprintln(os.Stderr, "contactd probe:", err)
		return 2
	}
	name := *model
	if name == "" {
		name = cfg.Model
	}
	report, err := probe.Run(ctx, p, name, probe.Options{Pause: *pause, Now: time.Now().UnixMilli()})
	if err != nil {
		fmt.Fprintln(os.Stderr, "contactd probe:", err)
		return 2
	}
	if err := probe.Write(os.Stdout, report); err != nil {
		fmt.Fprintln(os.Stderr, "contactd probe:", err)
		return 2
	}
	if probe.AnyDiffers(report.Findings) {
		_, _ = fmt.Fprintln(os.Stdout, "\nThe endpoint contradicts the profile where marked DIFFERS: correct the declaration in internal/llm/provider.")
		return 1
	}
	return 0
}
