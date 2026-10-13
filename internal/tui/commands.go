package tui

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/quinnovator/contactd/internal/client"
	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/tui/commands"
	"github.com/quinnovator/contactd/internal/tui/conf"
	"github.com/quinnovator/contactd/internal/tui/mods/wire"
	"github.com/quinnovator/contactd/internal/tui/safe"
	"github.com/quinnovator/contactd/internal/tui/statusline"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
	"github.com/quinnovator/contactd/internal/tui/theme"
)

// What the commands of the input box do. A command is a line that starts
// with a slash: it is run here and never sent to the contact. Which commands
// there are, and how a draft is read as one, is the commands package's; this
// file is what each of them does to the interface.

const (
	// helpColumn is where a command's summary starts in /help, and
	// statusColumn where a value starts in /status.
	helpColumn   = 28
	statusColumn = 13
	// nameInNote is the most characters of a typed name a note repeats.
	nameInNote = 40
)

// handlers returns what each core command does, by name. A handler is
// called with the open conversation in m.chat. The command it returns is run
// by the program, as any other.
func handlers() map[string]func(m *model, l commands.Line) tea.Cmd {
	return map[string]func(m *model, l commands.Line) tea.Cmd{
		"statusline": cmdStatusLine,
		"status":     cmdStatus,
		"stop":       cmdStop,
		"help":       cmdHelp,
		"contacts":   cmdContacts,
		"open":       cmdOpen,
		"new":        cmdNew,
		"rename":     cmdRename,
		"details":    cmdDetails,
		"mods":       cmdMods,
		"image":      cmdImage,
		"theme":      cmdSetting,
		"glyphs":     cmdSetting,
		"color":      cmdSetting,
		"top":        cmdTop,
		"bottom":     cmdBottom,
		"redraw":     cmdRedraw,
		"reload":     cmdReload,
		"quit":       cmdQuit,
	}
}

// runCommand runs the command a draft held. The draft leaves the box before
// the command runs, so that what the command draws is laid out for the
// screen as it will be. It stays when there is no such command, and is put
// back when the command refused what it was given, so that it can be put
// right.
func (m *model) runCommand(l commands.Line) tea.Cmd {
	c := m.chat
	spec, ok := m.cmds.Lookup(l.Name)
	if !ok {
		c.setNote("no command /" + safe.Line(l.Name, nameInNote, false) + noteUnknown)
		return nil
	}
	run, ok := m.run[spec.Name]
	switch {
	case !ok:
		// Only a mod's command has no handler here: its mod is asked.
		return m.runModCommand(spec, l)
	case spec.NeedsArg && l.Rest == "":
		c.setNote(usage(spec))
		return nil
	case spec.Usage == "" && l.Rest != "":
		c.setNote("/" + spec.Name + " takes no argument")
		return nil
	}
	draft := c.input.Value()
	c.clearInput()
	m.kept = false
	cmd := run(m, l)
	if m.kept && m.chat == c {
		c.input.SetValue(draft)
		c.inputChanged()
	}
	return cmd
}

// usage is the note a command answers a wrong argument with.
func usage(s commands.Spec) string {
	if s.Usage == "" {
		return "usage: /" + s.Name
	}
	return "usage: /" + s.Name + " " + s.Usage
}

// refuse is how a handler says it did nothing: the note says why, and the
// draft stays in the box.
func (m *model) refuse(note string) tea.Cmd {
	m.chat.setNote(note)
	m.kept = true
	return nil
}

// wrong refuses a command whose argument is not one it takes.
func (m *model) wrong(l commands.Line) tea.Cmd {
	spec, _ := m.cmds.Lookup(l.Name)
	return m.refuse(usage(spec))
}

// one is a command's whole argument as one word: what was typed after the
// name, without the quotes that kept its spaces together.
func one(l commands.Line) string {
	if len(l.Args) == 1 {
		return l.Args[0]
	}
	return l.Rest
}

// cmdStatus prints what the client knows about the connection, the contact
// and the terminal.
func cmdStatus(m *model, _ commands.Line) tea.Cmd {
	c, now := m.chat, m.opt.Now()
	c.show(c.print("status", m.statusLines(now)), now)
	return nil
}

// statusLines is what /status prints: the connection, the server, the
// contact and its route, the last model call, and then what the terminal
// can do and why the client thinks so, and the mods.
func (m *model) statusLines(now time.Time) []wire.Line {
	c := m.chat
	snap := c.snapshot(now)
	vals := statusline.CoreValues(snap, statusline.Marks{})
	row := func(name, value, tone string) wire.Line {
		return wire.Line{
			{T: name + strings.Repeat(" ", max(statusColumn-len(name), 1)), Tone: "dim"},
			{T: value, Tone: tone},
		}
	}
	state := vals["core:state"]
	conn := state.Vars["state"]
	if snap.ConnErr != "" {
		conn += ": " + snap.ConnErr
	}
	route := c.model
	if c.provider != "" {
		route = c.provider + "/" + c.model
	}
	terminal := termcaps.Explain(m.opt.Env, m.over, m.replies)
	lines := make([]wire.Line, 0, 6+len(terminal)+len(m.mods.infos))
	lines = append(lines,
		row("connection", conn, state.Tone),
		row("server", m.opt.Backend.Where(), ""),
		row("contact", c.label+", id "+c.contact.ID.String(), ""),
		row("route", route, ""),
		row("last call", c.lastCall(vals), ""),
	)
	for _, line := range terminal {
		name, rest, _ := strings.Cut(line, ": ")
		lines = append(lines, row(name, rest, ""))
	}
	return append(lines, m.modLines(row)...)
}

// lastCall says what the last model call cost: how much of its prompt came
// from the provider's cache, the tokens it wrote, and how long it took.
func (c *chat) lastCall(vals map[string]statusline.Value) string {
	a, ok := c.tr.Attempt()
	switch {
	case !ok:
		return "none yet"
	case a.Error != "":
		return "failed: " + a.Error
	}
	var parts []string
	if v := vals["core:cache"]; !v.Hide {
		parts = append(parts, "cache "+v.Vars["pct"]+"% of "+v.Vars["input"]+" tokens")
	}
	if v := vals["core:tokens"]; !v.Hide {
		parts = append(parts, v.Vars["output"]+" out")
	}
	if v := vals["core:latency"]; !v.Hide {
		parts = append(parts, v.Vars["secs"]+"s")
	}
	if len(parts) == 0 {
		return "nothing reported"
	}
	return strings.Join(parts, ", ")
}

// cmdStop stops the reply in flight, as esc does.
func cmdStop(m *model, _ commands.Line) tea.Cmd {
	c := m.chat
	if !c.tr.Live().Typing {
		c.setNote("no reply is being written")
		return nil
	}
	c.sess.Interrupt()
	return nil
}

// cmdHelp prints the commands and the keys, or one command.
func cmdHelp(m *model, l commands.Line) tea.Cmd {
	c, now := m.chat, m.opt.Now()
	switch len(l.Args) {
	case 0:
		specs := m.cmds.Match("")
		lines := make([]wire.Line, 0, len(specs))
		for _, spec := range specs {
			lines = append(lines, helpLines(spec)...)
		}
		first := c.print("commands", lines)
		c.print("keys", keyLines())
		c.show(first, now)
	case 1:
		name := strings.TrimPrefix(l.Args[0], "/")
		spec, ok := m.cmds.Lookup(name)
		if !ok {
			return m.refuse("no command /" + safe.Line(name, nameInNote, false) + "; /help lists them")
		}
		c.show(c.print("help", helpLines(spec)), now)
	default:
		return m.wrong(l)
	}
	return nil
}

// helpLines is one command in /help: its name, what it takes, and its
// summary, which names the key that does the same where there is one. The
// summaries stand in a column; one whose command is too long to leave room
// for it goes on a line of its own.
func helpLines(s commands.Spec) []wire.Line {
	head := wire.Line{{T: "/" + s.Name, Tone: "accent"}}
	n := 1 + utf8.RuneCountInString(s.Name)
	if s.Usage != "" {
		head = append(head, wire.Span{T: " " + s.Usage, Tone: "dim"})
		n += 1 + utf8.RuneCountInString(s.Usage)
	}
	summary := wire.Line{{T: s.Summary}}
	if s.Source != commands.SourceCore {
		summary = append(summary, wire.Span{T: "  " + s.Source, Tone: "mod"})
	}
	if n+2 > helpColumn {
		return []wire.Line{head, append(wire.Line{{T: strings.Repeat(" ", helpColumn)}}, summary...)}
	}
	return []wire.Line{append(append(head, wire.Span{T: strings.Repeat(" ", helpColumn-n)}), summary...)}
}

// keyLines is the keys of a conversation, for /help.
func keyLines() []wire.Line {
	keys := [][2]string{
		{"enter", "send the message, or run the command"},
		{"ctrl+j", "a new line; so are alt+enter, shift+enter, and a backslash before enter"},
		{"/", "start a command; // starts a message with a slash"},
		{"tab", "complete the command"},
		{"up, down", "choose a command; else scroll, or move in a draft of several rows"},
		{"pgup, pgdn", "scroll a page"},
		{"esc", "close the list of commands; else stop the reply"},
		{"ctrl+o", "fold or unfold tool calls"},
		{"ctrl+p", "back to the list of contacts"},
		{"ctrl+l", "clear the screen and draw it again"},
		{"ctrl+c", "empty the box; leave when it is empty"},
		{"ctrl+d", "leave, when the box is empty"},
	}
	lines := make([]wire.Line, len(keys))
	for i, k := range keys {
		lines[i] = wire.Line{
			{T: k[0] + strings.Repeat(" ", max(statusColumn-len(k[0]), 1)), Tone: "accent"},
			{T: k[1]},
		}
	}
	return lines
}

// cmdContacts goes back to the list of contacts.
func cmdContacts(m *model, _ commands.Line) tea.Cmd {
	m.closeChat()
	return m.showContacts()
}

// foundMsg is the answer to /open: the contact that was asked for, or why
// there is none. sess is the conversation that asked.
type foundMsg struct {
	sess    Session
	contact client.Contact
	err     error
}

// cmdOpen opens another contact, named by its name or its id.
func cmdOpen(m *model, l commands.Line) tea.Cmd {
	want := one(l)
	if want == "" {
		return m.wrong(l)
	}
	b, ctx, sess, current := m.opt.Backend, m.ctx, m.chat.sess, m.chat.contact.ID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		contacts, err := b.Contacts(ctx)
		if err != nil {
			return foundMsg{sess: sess, err: err}
		}
		c, err := pick(contacts, want)
		if err == nil && c.ID == current {
			err = errors.New(c.Label() + " is the contact that is open")
		}
		return foundMsg{sess: sess, contact: c, err: err}
	}
}

// onFound opens the contact /open asked for. An answer that comes after the
// conversation that asked was left is dropped.
func (m *model) onFound(msg foundMsg) tea.Cmd {
	if m.chat == nil || m.chat.sess != msg.sess || m.screen != screenChat {
		return nil
	}
	if msg.err != nil {
		m.chat.setNote(explain(msg.err))
		return nil
	}
	return m.open(msg.contact)
}

// pick finds the contact a person means by a word: the one with that id,
// else the one with that name, else the only one whose name starts with it.
// Names are compared without regard to case.
func pick(contacts []client.Contact, want string) (client.Contact, error) {
	if id, err := strconv.ParseUint(strings.TrimPrefix(want, "#"), 10, 64); err == nil {
		for _, c := range contacts {
			if c.ID == core.ContactID(id) {
				return c, nil
			}
		}
	}
	var named, starts []client.Contact
	for _, c := range contacts {
		switch name := strings.ToLower(c.Name); {
		case name == "":
		case name == strings.ToLower(want):
			named = append(named, c)
		case strings.HasPrefix(name, strings.ToLower(want)):
			starts = append(starts, c)
		}
	}
	if len(named) == 0 {
		named = starts
	}
	switch len(named) {
	case 0:
		return client.Contact{}, fmt.Errorf("no contact is called %q", want)
	case 1:
		return named[0], nil
	}
	ids := make([]string, len(named))
	for i, c := range named {
		ids[i] = c.ID.String()
	}
	return client.Contact{}, fmt.Errorf("%d contacts match %q; open one by its id: %s", len(named), want, strings.Join(ids, ", "))
}

// cmdNew leaves the conversation for the new-contact form.
func cmdNew(m *model, _ commands.Line) tea.Cmd {
	m.closeChat()
	m.newForm()
	return nil
}

// cmdRename gives the contact the name that was typed, or with none asks
// for one in the rename prompt. The conversation stays open under the
// prompt, and is what the prompt returns to.
func cmdRename(m *model, l commands.Line) tea.Cmd {
	c := m.chat
	name := one(l)
	if name == "" {
		m.renameForm(c.contact)
		return nil
	}
	if utf8.RuneCountInString(name) > nameLimit {
		return m.refuse(fmt.Sprintf("a name has at most %d characters", nameLimit))
	}
	return m.rename(c.contact.ID, name)
}

// cmdDetails folds or unfolds tool calls and their output, as ctrl+o does.
func cmdDetails(m *model, _ commands.Line) tea.Cmd {
	m.chat.unfold()
	return nil
}

// cmdSetting is /theme, /glyphs, /color and the levels of /image: one
// setting of the terminal, for this session. What the terminal can do is
// worked out again with it, and the note names the line of chat.toml that
// makes the choice last. A level of pictures the terminal cannot show gives
// the best one it can, and the note says which.
func cmdSetting(m *model, l commands.Line) tea.Cmd {
	if len(l.Args) != 1 {
		return m.wrong(l)
	}
	value, over, key := strings.ToLower(l.Args[0]), m.over, l.Name
	if value == "" {
		return m.wrong(l)
	}
	switch l.Name {
	case "theme":
		over.Theme = value
	case "glyphs":
		over.Glyphs = value
	case "color":
		over.Color = value
	case "image":
		over.Images, key = value, "images"
	}
	over, err := termcaps.ParseOverrides(over.Color, over.Glyphs, over.Theme, over.Images, over.NoProbe)
	if err != nil {
		return m.wrong(l)
	}
	m.over = over
	cmd := m.resolve()
	var now string
	switch l.Name {
	case "theme":
		now = m.caps.Background.String()
	case "glyphs":
		now = m.caps.Glyphs.String()
	case "image":
		now = m.caps.Images.String()
	case "color":
		now = m.caps.Color.String()
		if cmd != nil {
			// The renderer is told the number of colours before it draws the
			// screen again, or it would bring the new ones down to the old.
			p := profile(m.caps.Color)
			cmd = tea.Sequence(func() tea.Msg { return tea.ColorProfileMsg{Profile: p} }, cmd)
		}
	}
	note := l.Name + ": " + value
	if value == "auto" || value != now {
		note += " (" + now + " now)"
	}
	m.chat.say(fmt.Sprintf("%s for this session; %s = %q in chat.toml keeps it", note, key, value), toneDone)
	return cmd
}

// cmdTop goes to the oldest message that has been loaded, and asks the
// server for the ones before it.
func cmdTop(m *model, _ commands.Line) tea.Cmd {
	m.chat.top()
	return nil
}

// cmdBottom goes to the newest line and follows it again.
func cmdBottom(m *model, _ commands.Line) tea.Cmd {
	c := m.chat
	c.win.Follow()
	c.dirty |= dirtyBody
	return nil
}

// cmdRedraw clears the screen and draws it again, as ctrl+l does.
func cmdRedraw(m *model, _ commands.Line) tea.Cmd { return m.clear() }

// cmdQuit leaves the client.
func cmdQuit(*model, commands.Line) tea.Cmd { return tea.Quit }

// reloadedMsg is the client's files as /reload read them: chat.toml and the
// status line's.
type reloadedMsg struct {
	chat     conf.Chat
	warnings []string
	err      error
	bar      barFile
	// mods is what the host lists after loading again; nil with no host.
	mods *modsState
}

// cmdReload reads the client's files again: the [glyph] table of chat.toml,
// and the status line. The other settings of chat.toml are for the start of
// a session, and a session's own choices stand. The mods are loaded again:
// the ones that are switched on and not running are started, and every one
// is asked again for what it shows. The files of pictures that could not be
// shown are fetched again.
func cmdReload(m *model, _ commands.Line) tea.Cmd {
	path, bar, safe := m.opt.Dirs.ChatFile(), m.opt.Dirs.StatusLineFile(), m.sh.safe
	host, ctx, dir := m.mods.host, m.ctx, m.opt.WorkDir
	// A picture that could not be shown is tried again: its file may have
	// been half written the first time, or not there yet.
	m.chat.imgs.store.Retry()
	if path == "" {
		m.chat.setNote("nothing to read again: no config directory is known")
		return nil
	}
	return func() tea.Msg {
		chat, warnings, err := conf.LoadChat(path)
		msg := reloadedMsg{chat: chat, warnings: warnings, err: err, bar: readBar(bar, safe)}
		if host != nil {
			host.Load(ctx)
			st := readMods(host, dir)
			msg.mods = &st
		}
		return msg
	}
}

// onReloaded takes the glyphs chat.toml asks for now and the status line
// its file describes now, and says what either file has that could not be
// used. A file that cannot be read at all changes nothing of its own; the
// other one still counts.
func (m *model) onReloaded(msg reloadedMsg) tea.Cmd {
	var (
		wrong []string
		cmd   tea.Cmd
	)
	if msg.err != nil {
		wrong = append(wrong, msg.err.Error())
	} else {
		if !maps.Equal(msg.chat.Glyph, m.opt.Chat.Glyph) {
			m.opt.Chat.Glyph = msg.chat.Glyph
			cmd = m.restyle()
		}
		if refused := append(theme.Check(m.themeOptions()), msg.warnings...); len(refused) > 0 {
			wrong = append(wrong, "chat.toml: "+strings.Join(refused, "; "))
		}
	}
	wrong = append(wrong, m.takeBar(msg.bar, "the status line stays as it was")...)
	cmd = tea.Batch(cmd, m.shellStep())
	loaded := ""
	if msg.mods != nil {
		m.setMods(*msg.mods)
		// Every segment is asked again, and every item a mod draws.
		m.mods.all = true
		if m.chat != nil {
			m.chat.setRenderers(m.mods.kinds, true)
		}
		loaded = "; mods on: " + strconv.Itoa(m.mods.modsOn())
	}
	if len(wrong) > 0 {
		m.say(strings.Join(wrong, "; "), "")
		return cmd
	}
	m.say(fmt.Sprintf("read chat.toml again; glyph overrides: %d; read %s again; segments on: %d%s",
		len(msg.chat.Glyph), barFileName, segmentsOn(m.bar), loaded), toneDone)
	return cmd
}

// segmentsOn counts the segments of a status line that are switched on.
func segmentsOn(cfg statusline.Config) int {
	n := 0
	for _, seg := range cfg.Segments {
		if seg.On {
			n++
		}
	}
	return n
}
