package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/colorprofile"
	"golang.org/x/term"

	"github.com/quinnovator/contactd/internal/client"
	"github.com/quinnovator/contactd/internal/config"
	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/tui"
	"github.com/quinnovator/contactd/internal/tui/conf"
	"github.com/quinnovator/contactd/internal/tui/mods"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
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

The interface draws for what the terminal can do: its colours, its
background, its font. It works that out from the environment and from what
the terminal answers when asked. -color, -glyphs (or -ascii), -theme and
-images say otherwise, for this run; chat.toml in the config directory says
so for every run. A flag wins over its CONTACTD_ variable, which wins over
chat.toml.

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
	look := addDisplayFlags(fs)
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
	// A system with no home or cache directory has no settings to read, and
	// one with no working directory has none to show: each is left empty.
	home, _ := os.UserHomeDir()
	cache, _ := os.UserCacheDir()
	workDir, _ := os.Getwd()
	d, err := look.settle(os.Getenv, home, cache, colours(colorprofile.Detect(os.Stdout, os.Environ())))
	if err != nil {
		return fail(err)
	}
	// Said before the screen starts, so they are there to read after it.
	for _, w := range d.warnings {
		fmt.Fprintln(os.Stderr, "contactd chat:", w)
	}
	ui := d.options(home, workDir)
	if d.mods && ui.Mods == nil {
		fmt.Fprintln(os.Stderr, "contactd chat: starting without mods:", ui.ModsOff)
	}
	ui.Backend, ui.Open = tui.Remote{Client: c}, open
	// Run closes the mod host when the interface ends.
	err = tui.Run(ctx, ui)
	if err != nil {
		return fail(err)
	}
	return 0
}

// displayFlags are the flags that say how the full-screen interface draws.
// Each has an environment variable, read when the flag is not given.
type displayFlags struct {
	fs                                      *flag.FlagSet
	color, glyphs, theme, images, configDir *string
	ascii, probe, mods                      *bool
}

// addDisplayFlags declares the display flags on fs.
func addDisplayFlags(fs *flag.FlagSet) *displayFlags {
	return &displayFlags{
		fs:        fs,
		color:     fs.String("color", "", "how many colours to draw with: auto, truecolor, 256, 16 or none ($CONTACTD_COLOR)"),
		glyphs:    fs.String("glyphs", "", "the symbols to draw with: auto, unicode or ascii ($CONTACTD_GLYPHS)"),
		ascii:     fs.Bool("ascii", false, "short for -glyphs=ascii"),
		theme:     fs.String("theme", "", "the terminal's background: auto, dark or light ($CONTACTD_THEME)"),
		images:    fs.String("images", "", "how an image in a reply is shown: auto, pixels, blocks or text ($CONTACTD_IMAGES)"),
		probe:     fs.Bool("probe", true, "ask the terminal what it can do when starting ($CONTACTD_PROBE)"),
		mods:      fs.Bool("mods", true, "load the installed mods ($CONTACTD_MODS)"),
		configDir: fs.String("config-dir", "", "where chat.toml, statusline.toml and the mods are ($CONTACTD_CONFIG_DIR)"),
	}
}

// display is what "contactd chat" settles before the screen starts: where
// the client's files are, what chat.toml holds, and what is known and set
// about the terminal.
type display struct {
	dirs conf.Dirs
	chat conf.Chat
	env  termcaps.Env
	over termcaps.Overrides
	// mods says whether the installed mods are loaded, and modsWhy which
	// setting said they are not.
	mods    bool
	modsWhy string
	// warnings are for the person, to print before the screen starts.
	warnings []string
}

// settle merges the display settings from their four sources. For each
// setting the first that speaks wins: the flag, then its CONTACTD_ variable,
// then chat.toml. What none of them says is left for the client to detect,
// where NO_COLOR is heard before the terminal is. It must be called after
// the flags are parsed.
//
// getenv reads the environment; home and userCache are the home and cache
// directories, or empty; detected is the number of colours the terminal
// seems to show.
//
// A terminal that says it cannot place the cursor, TERM empty or "dumb", is
// refused unless -color was given: the person who gives it knows better.
func (f *displayFlags) settle(getenv func(string) string, home, userCache string, detected termcaps.Color) (display, error) {
	given := make(map[string]bool)
	f.fs.Visit(func(fl *flag.Flag) { given[fl.Name] = true })

	env := termcaps.ReadEnv(getenv, detected)
	if (env.Term == "" || strings.EqualFold(env.Term, "dumb")) && !given["color"] {
		is := "TERM is not set"
		if env.Term != "" {
			is = "TERM is " + strconv.Quote(env.Term)
		}
		return display{}, errors.New(is + `, so this terminal cannot show the chat interface; ` +
			`to send one message and print the reply, give the message as an argument: contactd chat ada "question". ` +
			`If the terminal can do more than it says, start the interface with -color (none, 16, 256 or truecolor)`)
	}

	d := display{env: env}
	d.dirs = conf.Locate(func(name string) string {
		if name == "CONTACTD_CONFIG_DIR" && given["config-dir"] {
			return *f.configDir
		}
		return getenv(name)
	}, home, userCache)
	chat, warnings, err := conf.LoadChat(d.dirs.ChatFile())
	if err != nil {
		return display{}, err //nolint:wrapcheck // the error names the file and the line already
	}
	d.chat = chat
	for _, w := range warnings {
		d.warnings = append(d.warnings, d.dirs.ChatFile()+": "+w)
	}

	word := func(name string, flagged *string, file string) string {
		if given[name] {
			return *flagged
		}
		if v := getenv("CONTACTD_" + strings.ToUpper(name)); v != "" {
			return v
		}
		return file
	}
	glyphs := word("glyphs", f.glyphs, chat.Glyphs)
	if *f.ascii {
		if given["glyphs"] && !strings.EqualFold(strings.TrimSpace(*f.glyphs), "ascii") {
			return display{}, fmt.Errorf("-ascii and -glyphs=%s disagree: give one of them", *f.glyphs)
		}
		glyphs = "ascii"
	}
	yes := func(name string, flagged *bool, file bool) (bool, error) {
		if given[name] {
			return *flagged, nil
		}
		variable := "CONTACTD_" + strings.ToUpper(name)
		v := getenv(variable)
		if v == "" {
			return file, nil
		}
		on, err := strconv.ParseBool(v)
		if err != nil {
			return false, fmt.Errorf("%s is %q; it can be true or false", variable, v)
		}
		return on, nil
	}
	probe, err := yes("probe", f.probe, chat.Probe)
	if err != nil {
		return display{}, err
	}
	if d.mods, err = yes("mods", f.mods, chat.Mods); err != nil {
		return display{}, err
	}
	if !d.mods {
		switch {
		case given["mods"]:
			d.modsWhy = "the client was started with -mods=false"
		case getenv("CONTACTD_MODS") != "":
			d.modsWhy = "CONTACTD_MODS is " + strconv.Quote(getenv("CONTACTD_MODS"))
		default:
			d.modsWhy = "chat.toml has mods = false"
		}
	}
	d.over, err = termcaps.ParseOverrides(
		word("color", f.color, chat.Color), glyphs,
		word("theme", f.theme, chat.Theme), word("images", f.images, chat.Images), !probe,
	)
	if err != nil {
		return display{}, fmt.Errorf("display settings: %w", err)
	}
	return d, nil
}

// options is what the full-screen interface is started with, but for the
// server and the contact to open: what was settled about the terminal, where
// the client's files are and where it was started, and the mod host, which
// is opened here.
func (d display) options(home, workDir string) tui.Options {
	host, modsOff := openMods(d)
	return tui.Options{
		Env: d.env, Overrides: d.over,
		Dirs: d.dirs, Home: home, Chat: d.chat, WorkDir: workDir,
		Mods: host, ModsOff: modsOff,
	}
}

// The limits a mod runs under: how long a call may take before it counts
// against the mod, how long before it is stopped, how many such calls in a
// row switch the mod off, and how much memory it may have, in 64 KiB pages.
const (
	modBudget  = 5 * time.Millisecond
	modKill    = 50 * time.Millisecond
	modStrikes = 3
	modPages   = 256
)

// openMods opens the host that runs the installed mods, which are in the
// config directory. It reads their manifests and loads nothing: the
// interface does that once it is up. With mods switched off, or when the
// record of which mods are on cannot be read, there is no host, and the
// string says why.
func openMods(d display) (tui.ModHost, string) {
	if !d.mods {
		return nil, d.modsWhy
	}
	host, err := mods.Open(mods.Options{
		Dir: d.dirs.ModsDir(), StatePath: d.dirs.ModsState(), CacheDir: d.dirs.WasmCache(),
		Budget: modBudget, Kill: modKill, Strikes: modStrikes, MemoryPages: modPages,
	})
	if err != nil {
		return nil, err.Error()
	}
	return host, ""
}

// colours is the number of colours a detected profile shows.
func colours(p colorprofile.Profile) termcaps.Color {
	switch p {
	case colorprofile.TrueColor:
		return termcaps.ColorTrue
	case colorprofile.ANSI256:
		return termcaps.Color256
	case colorprofile.ANSI:
		return termcaps.Color16
	case colorprofile.ASCII, colorprofile.NoTTY, colorprofile.Unknown:
		return termcaps.ColorNone
	default:
		return termcaps.ColorNone
	}
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
