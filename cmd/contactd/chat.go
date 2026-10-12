package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/quinnovator/contactd/internal/client"
	"github.com/quinnovator/contactd/internal/config"
	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/tui"
)

const chatUsage = `usage: contactd chat [flags] [contact [message]]

Talk to a contact on a running contactd, from the terminal.

  contactd chat                  pick a contact from the list
  contactd chat ada              open the conversation with ada
  contactd chat ada "question"   send one message, print the reply, and exit
  contactd chat ada -            the same, with the message read from stdin
  contactd chat -list            print the contacts
  contactd chat -new ada -system "You are Ada." -tools python

A contact is named by its name or its ID. The server is found through its
Unix socket, contactd.sock in the data directory, unless -url is given.

`

// chatMain runs "contactd chat": the terminal client. It is a client of a
// running server like any other and never opens the data directory itself.
//
// Exit status: 0 on success, 1 when a message got no reply, 2 when the
// command could not run.
func chatMain(args []string) int {
	fs := flag.NewFlagSet("contactd chat", flag.ContinueOnError)
	env := func(name, def string) string {
		if v := os.Getenv(name); v != "" {
			return v
		}
		return def
	}
	dataDir := fs.String("data-dir", env("CONTACTD_DATA_DIR", "/data"), "the server's data directory, where its socket is ($CONTACTD_DATA_DIR)")
	socket := fs.String("socket", env("CONTACTD_SOCKET", ""), "the server's Unix socket; empty means contactd.sock in the data directory ($CONTACTD_SOCKET)")
	url := fs.String("url", env("CONTACTD_URL", ""), "reach the server over TCP instead, such as http://host:8080 ($CONTACTD_URL)")
	list := fs.Bool("list", false, "print the contacts and exit")
	timeout := fs.Duration("timeout", 10*time.Minute, "how long a one-shot message waits for its reply")

	newName := fs.String("new", "", "create a contact with this name, then open it")
	system := fs.String("system", "", "with -new: the system prompt")
	profile := fs.String("profile", "", "with -new: what the contact knows about you")
	provider := fs.String("provider", "", "with -new: the model provider; empty takes the server's default")
	model := fs.String("model", "", "with -new: the model; empty takes the provider's default")
	effort := fs.String("effort", "", "with -new: reasoning effort: none, low, medium, high, xhigh or max")
	tools := fs.String("tools", "", "with -new: code-execution tools to give the agent, comma-separated: python, js")
	delegate := fs.Bool("delegate", false, "with -new: let the agent start sub-agents")
	fs.Usage = func() {
		// Help text that cannot be written has nowhere better to go.
		_, _ = fmt.Fprint(fs.Output(), chatUsage)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	fail := func(err error) int {
		msg := err.Error()
		if errors.Is(err, client.ErrUnavailable) {
			msg += "\nIs contactd running? Point at it with -data-dir, -socket or -url."
		}
		fmt.Fprintln(os.Stderr, "contactd chat:", msg)
		return 2
	}

	opt := client.Options{URL: *url, Socket: *socket}
	if opt.URL == "" && opt.Socket == "" {
		opt.Socket = config.DefaultSocket(*dataDir)
	}
	c, err := client.New(opt)
	if err != nil {
		return fail(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	if *list {
		return listContacts(ctx, c, fail)
	}

	ref, message := fs.Arg(0), fs.Arg(1)
	if fs.NArg() > 2 {
		return fail(errors.New("too many arguments: quote the message"))
	}
	var open *client.Contact
	switch {
	case *newName != "" || *system != "":
		if ref != "" && message != "" {
			return fail(errors.New("with -new, give at most the message to send"))
		}
		message = ref
		agent := core.AgentConfig{System: *system, Provider: *provider, Model: *model, Effort: *effort, Delegate: *delegate}
		if agent.Tools, err = toolPresets(*tools); err != nil {
			return fail(err)
		}
		if agent.System == "" {
			return fail(errors.New("-new needs -system: say who the contact is"))
		}
		rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		id, err := c.Create(rctx, client.NewContact{Name: *newName, Agent: agent, Profile: *profile})
		if err == nil {
			var made client.Contact
			made, err = c.Contact(rctx, id)
			open = &made
		}
		cancel()
		if err != nil {
			return fail(err)
		}
	case ref != "":
		rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		found, err := c.Find(rctx, ref)
		cancel()
		if errors.Is(err, core.ErrContactNotFound) {
			return fail(fmt.Errorf("no contact called %q; contactd chat -list shows them", ref))
		}
		if err != nil {
			return fail(err)
		}
		open = &found
	}

	if message != "" {
		if message == "-" {
			raw, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
			if err != nil {
				return fail(fmt.Errorf("read the message from stdin: %w", err))
			}
			message = string(raw)
		}
		ctx, cancel := context.WithTimeout(ctx, *timeout)
		defer cancel()
		return oneShot(ctx, c, open, strings.TrimSpace(message), fail)
	}

	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return fail(errors.New("the chat interface needs a terminal; to send one message and print the reply, give the message as an argument"))
	}
	if err := tui.Run(ctx, tui.Options{Backend: tui.Remote{Client: c}, Open: open}); err != nil {
		return fail(err)
	}
	return 0
}

// toolPresets turns the names given to -tools into tool definitions. Both
// run in the server's in-process sandbox.
func toolPresets(csv string) ([]core.ToolDef, error) {
	var out []core.ToolDef
	for name := range strings.SplitSeq(csv, ",") {
		switch strings.TrimSpace(name) {
		case "":
		case "python":
			out = append(out, core.ToolDef{
				Name:        "python",
				Description: "Run Python 3.12. Files persist between calls; variables may not.",
				Env:         core.EnvSpec{Tier: 0, Base: "python-3.12"},
			})
		case "js":
			out = append(out, core.ToolDef{
				Name:        "js",
				Description: "Run JavaScript in QuickJS. Files persist between calls; variables may not.",
				Env:         core.EnvSpec{Tier: 0, Base: "quickjs"},
			})
		default:
			return nil, fmt.Errorf("-tools: %q is not one of python, js", name)
		}
	}
	return out, nil
}

func listContacts(ctx context.Context, c *client.Client, fail func(error) int) int {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	contacts, err := c.Contacts(ctx)
	if err != nil {
		return fail(err)
	}
	for i := range contacts {
		ct := &contacts[i]
		active := time.UnixMilli(ct.LastActive).Format(time.RFC3339)
		if _, err := fmt.Fprintf(os.Stdout, "%d\t%s\t%s/%s\t%s\n", ct.ID, ct.Name, ct.Provider, ct.Model, active); err != nil {
			return fail(err)
		}
	}
	return 0
}

// oneShot sends one message and prints the reply to it: the first whole
// reply committed after the message. Tool calls along the way are not
// shown, and a reply the message cut off is not the one waited for.
func oneShot(ctx context.Context, c *client.Client, contact *client.Contact, text string, fail func(error) int) int {
	if contact == nil {
		return fail(errors.New("say which contact the message is for"))
	}
	if text == "" {
		return fail(errors.New("the message is empty"))
	}
	sess := c.Follow(ctx, contact.ID)
	defer sess.Close()
	var sent bool
	var mine uint64 // the message's sequence number, once known
	for u := range sess.Updates() {
		switch u.Kind {
		case client.UpdateState:
			switch u.State {
			case client.StateLive:
				if !sent {
					sent = true
					sess.Say(text)
				}
			case client.StateDown, client.StateGone:
				// A person at a terminal can wait out a restart; a script
				// wants to know now.
				return fail(u.Err)
			case client.StateConnecting:
			}
		case client.UpdateAck:
			mine = u.Seq
		case client.UpdateRefused:
			return fail(fmt.Errorf("the message was not accepted: %s", u.Reason))
		case client.UpdateDelta:
			d := u.Delta
			switch {
			case d.Type == core.DeltaUser && sent && mine == 0 && d.Text == text:
				mine = d.Seq // seen on the stream before its acknowledgement
			case d.Type == core.DeltaMessage && mine != 0 && d.Seq > mine:
				if _, err := fmt.Fprintln(os.Stdout, d.Text); err != nil {
					return fail(err)
				}
				return 0
			case d.Type == core.DeltaError && mine != 0:
				fmt.Fprintln(os.Stderr, "contactd chat: no reply:", d.Text)
				return 1
			}
		case client.UpdateEvents, client.UpdateOlder, client.UpdateSynced:
		}
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		fmt.Fprintln(os.Stderr, "contactd chat: no reply before the timeout")
		return 1
	}
	return 2 // interrupted by a signal
}
