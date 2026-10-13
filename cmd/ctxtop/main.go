// ctxtop shows the context composition and cache behavior of a dev server.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/charmbracelet/colorprofile"
	"golang.org/x/term"

	"github.com/quinnovator/contactd/internal/client"
	"github.com/quinnovator/contactd/internal/ctxtop"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ctxtop:", err)
		os.Exit(1)
	}
}

func run() error {
	data := os.Getenv("CONTACTD_DATA_DIR")
	if data == "" {
		data = "./data"
	}
	fs := flag.NewFlagSet("ctxtop", flag.ContinueOnError)
	dir := fs.String("data-dir", data, "the dev server's data directory")
	socket := fs.String("socket", os.Getenv("CONTACTD_SOCKET"), "the server's Unix socket")
	url := fs.String("url", os.Getenv("CONTACTD_URL"), "server HTTP URL instead of a Unix socket")
	contact := fs.String("contact", "", "only show a contact's name or ID; empty shows all contacts")
	ascii := fs.Bool("ascii", false, "draw with ASCII characters")
	lifetime := fs.String("lifetime-file", "", "dev launcher: close when this file is removed")
	if err := fs.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err //nolint:wrapcheck // flag already names the invalid argument
	}
	if fs.NArg() > 0 {
		return errors.New("unexpected arguments; use -contact=name to filter")
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return errors.New("the viewer needs an interactive terminal")
	}
	if *socket == "" {
		*socket = filepath.Join(*dir, "contactd.sock")
	}
	c, err := client.New(client.Options{Socket: *socket, URL: *url})
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	color := termcaps.Color256
	if colorprofile.Detect(os.Stdout, os.Environ()) == colorprofile.NoTTY || os.Getenv("NO_COLOR") != "" {
		color = termcaps.ColorNone
	}
	o := termcaps.Overrides{NoProbe: true}
	if *ascii {
		o.Glyphs = "ascii"
	}
	caps := termcaps.Resolve(termcaps.ReadEnv(os.Getenv, color), o, termcaps.Replies{})
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if *lifetime != "" {
		go func() {
			ticker := time.NewTicker(250 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if _, err := os.Stat(*lifetime); errors.Is(err, os.ErrNotExist) {
						cancel()
						return
					}
				}
			}
		}()
	}
	if err := ctxtop.Run(ctx, c, ctxtop.Options{Contact: *contact, Caps: caps}); err != nil {
		return fmt.Errorf("viewer: %w", err)
	}
	return nil
}
