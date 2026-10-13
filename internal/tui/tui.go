// Package tui is the terminal interface behind "contactd chat": a list of
// contacts, and a conversation with one of them.
//
// It is a client of the server like any other. It reaches it through the
// Backend interface, which internal/client satisfies, and keeps no state of
// its own between runs: the contacts, their names and every message live on
// the server. What is on screen is internal/transcript's reduction of the
// log and the live stream; this package only draws it and reads keys.
//
// The interface follows the platform's own shape. There are no sessions or
// threads to pick, only contacts, and opening one opens the end of its one
// continuous log. The input box never locks: a message sent while the agent
// is replying interrupts it, as it would a person.
//
// Everything drawn derives from one termcaps.Caps value, what the terminal
// can do, through the theme made from it. The value is worked out from the
// environment and the settings when the interface starts, and again each
// time the terminal answers one of the questions it is asked then. Nothing
// waits for the answers.
//
// The conversation is a full-screen single column: a header, the
// transcript, a status bar, the input and a line of key hints. Only the
// blocks of the transcript that are in view are ever drawn, and View hands
// back what was last drawn until something changes, so an idle client
// writes nothing.
//
// Where the terminal can draw one, the transcript's rows are a stage, and
// an avatar says the contact's replies aloud there: the words appear under
// it as they are said, and what a reply shows stands where the reply put it.
// Such a conversation has no thread and nothing to scroll. The rows around
// the stage, and everything a command does, are as they are for a thread.
// The stage is drawn again many times a second, from a timer that runs only
// while it is on the screen.
//
// A line in the input that starts with a slash is a command. It is run here
// and never sent to the contact, and neither is the sign that someone is
// writing. While one is being typed, the commands it could mean are listed
// between the transcript and the status bar. What a command has to say is
// shown in the transcript and is not part of the conversation.
//
// The status bar is a row of segments, read from statusline.toml: what the
// client knows, what a mod returned, and what a command printed. The
// commands run on their intervals away from the update loop. /statusline
// opens an editor for the file over the conversation, which goes on under
// it.
//
// Mods add to all of this: a segment, a command, or the drawing of one kind
// of item. They run in a host that the client is handed, and are loaded and
// called away from the update loop. /mods opens a screen that lists them and
// says what each may read.
//
// A reply can name a picture among its contact's files. It is shown as the
// terminal can: the picture itself, half-block characters, or a line of
// text. The file is fetched and decoded away from the update loop, and the
// terminal is never handed the file's own bytes. Where the picture itself is
// sent, it is sent once, beside the screen, and forgotten again when the
// conversation is left and when the interface ends.
package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/quinnovator/contactd/internal/client"
	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/tui/commands"
	"github.com/quinnovator/contactd/internal/tui/conf"
	"github.com/quinnovator/contactd/internal/tui/inputbox"
	"github.com/quinnovator/contactd/internal/tui/sledit"
	"github.com/quinnovator/contactd/internal/tui/sound"
	"github.com/quinnovator/contactd/internal/tui/statusline"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
	"github.com/quinnovator/contactd/internal/tui/theme"
)

// Backend is the server, as far as the interface needs it.
type Backend interface {
	Contacts(ctx context.Context) ([]client.Contact, error)
	Contact(ctx context.Context, id core.ContactID) (client.Contact, error)
	Create(ctx context.Context, nc client.NewContact) (core.ContactID, error)
	Rename(ctx context.Context, id core.ContactID, name string) (client.Contact, error)
	// Follow opens a session with a contact that runs until it is closed.
	Follow(ctx context.Context, id core.ContactID, visits ...core.Visit) Session
	// File fetches one file from a contact's working directory. etag, when
	// not empty, names the copy the caller holds.
	File(ctx context.Context, id core.ContactID, path, etag string) (client.File, error)
	// Where describes the server, for the screen and for error messages.
	Where() string
}

// Session is one contact being followed. *client.Session is the real one.
type Session interface {
	Updates() <-chan client.Update
	Say(text string)
	Typing()
	Interrupt()
	Older()
	Close()
}

// Remote adapts a *client.Client to Backend.
type Remote struct{ *client.Client }

// Follow implements Backend.
func (r Remote) Follow(ctx context.Context, id core.ContactID, visits ...core.Visit) Session {
	return r.Client.Follow(ctx, id, visits...)
}

// Options configures a run of the interface.
type Options struct {
	Backend Backend
	// Open, when set, is the contact to open straight away. Without it the
	// interface starts at the list of contacts.
	Open *client.Contact
	// Now and Location are for tests; they default to the wall clock and
	// the local time zone.
	Now      func() time.Time
	Location *time.Location
	// Env is what the environment says about the terminal, and Overrides
	// what the settings say. What the terminal can do is worked out from the
	// two, and again each time the terminal answers a question; every glyph
	// and colour drawn follows from that. The zero values are the safest
	// terminal: ASCII, no colour, and nothing asked.
	Env       termcaps.Env
	Overrides termcaps.Overrides
	// Dirs are where the client's own files are. Home is the home directory,
	// which a screen writes as "~" in the paths it shows.
	Dirs conf.Dirs
	Home string
	// Chat is chat.toml. Its [glyph] table is taken from here; its other
	// settings arrive merged into Overrides.
	Chat conf.Chat
	// WorkDir is the directory the client was started in.
	WorkDir string
	// Mods runs the installed mods. Nil means there are none, and ModsOff
	// then says why, for /mods and /status. Run closes the host when the
	// interface ends.
	Mods    ModHost
	ModsOff string
	// Shell runs the commands of the status line's shell segments. It is
	// statusline.NewShell() when nil.
	Shell statusline.Runner
	// Output is where what the client writes after the screen has gone is
	// written. It is os.Stdout when nil.
	Output io.Writer
	// Avatar is how a conversation is shown: by an avatar that says the
	// replies aloud, or as a thread. The zero value is a thread.
	Avatar Avatar
	// Program is added to the program's own options. Tests use it to give
	// the program a pipe to read, a recorder to write to, and a size.
	Program []tea.ProgramOption
}

// Run shows the interface until the person leaves it or ctx is done.
func Run(ctx context.Context, opt Options) error {
	// What the interface started ends with it: its context is done when it
	// is left, whichever way that is.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	m, p := newProgram(ctx, opt)
	_, err := p.Run()
	m.closeChat()
	// A shell segment's command that is still running is stopped, and is
	// gone before Run returns.
	cancel()
	m.sh.stop()
	if opt.Mods != nil {
		// The mods are stopped once nothing calls them any more. What that
		// comes to is nobody's to act on: the interface is gone.
		closing, done := context.WithTimeout(context.WithoutCancel(ctx), stopWait)
		_ = opt.Mods.Close(closing)
		done()
	}
	// The screen is gone and the terminal is the shell's again, but it still
	// holds the pictures it was sent. It is told to forget them, which the
	// program can no longer do.
	if left := strings.Join(m.raw.queue, ""); left != "" {
		out := opt.Output
		if out == nil {
			out = os.Stdout
		}
		_, _ = io.WriteString(out, left) // nothing can be done about a terminal that is gone
	}
	return ended(err)
}

// ended turns what the program ended with into what Run returns. Leaving is
// no error, whichever way it was done: by a key, by the context ending, or
// by a signal. A panic is one, although the program reports it as being
// killed: the terminal was put back, and the caller must not call it success.
func ended(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, tea.ErrProgramPanic):
		return fmt.Errorf("the terminal interface stopped: %w", err)
	case errors.Is(err, tea.ErrProgramKilled), errors.Is(err, tea.ErrInterrupted), errors.Is(err, context.Canceled):
		return nil
	default:
		return fmt.Errorf("run the terminal interface: %w", err)
	}
}

// newProgram makes the interface and the program that runs it. The colours
// the program may write are the ones the theme picks from: they are chosen
// by hand for each number of colours, so the renderer is told that number
// and never left to detect it, or it would bring the chosen colours down
// again by its own rule.
func newProgram(ctx context.Context, opt Options) (*model, *tea.Program) {
	m := newModel(ctx, opt)
	options := make([]tea.ProgramOption, 0, len(opt.Program)+2)
	options = append(options, tea.WithContext(ctx))
	options = append(options, opt.Program...)
	options = append(options, tea.WithColorProfile(profile(m.caps.Color)))
	return m, tea.NewProgram(m, options...)
}

// profile is the renderer's name for a number of colours. With no colour it
// still lets bold, faint and reverse video through.
func profile(c termcaps.Color) colorprofile.Profile {
	switch c {
	case termcaps.ColorTrue:
		return colorprofile.TrueColor
	case termcaps.Color256:
		return colorprofile.ANSI256
	case termcaps.Color16:
		return colorprofile.ANSI
	case termcaps.ColorNone:
		return colorprofile.ASCII
	default:
		return colorprofile.ASCII
	}
}

type screen uint8

const (
	screenContacts screen = iota + 1
	screenChat
	screenNew
	screenRename
	// screenStatusLine is the status line's editor, over a conversation.
	screenStatusLine
	// screenMods is the list of the mods, over a conversation.
	screenMods
)

// toolTickEvery is how often the time a running tool has taken is drawn
// again.
const toolTickEvery = 200 * time.Millisecond

// model is the whole interface. Bubble Tea calls Update and View on one
// goroutine, so nothing here is locked.
type model struct {
	ctx context.Context //nolint:containedctx // Bubble Tea's Update has no context to pass; commands need the program's
	opt Options

	// caps is what the terminal can do and th the look made from it. Both
	// change only in setCaps.
	caps termcaps.Caps
	th   *theme.Theme
	// What caps is worked out from, besides the environment: the settings,
	// and what the terminal has answered so far. plan is what it was asked
	// at the start, and tmuxAsked says the terminal behind a tmux that only
	// its answer gave away has been asked too.
	over      termcaps.Overrides
	replies   termcaps.Replies
	plan      termcaps.Plan
	tmuxAsked bool
	// probeWait is how long the terminal has to answer.
	probeWait time.Duration
	// bar is the status line's configuration: what the file says, or a
	// preset. modSegs are the segments the mods offer, which a bar is drawn
	// with whether it lists them or not.
	bar     statusline.Config
	modSegs []statusline.ModSegment
	// mods is the mods: what they add, and the calls to them.
	mods modGlue
	// extra is what the shell and mod segments have to show, by Segment.ID.
	// It is made anew for every change and never changed after, and extraRev
	// counts the changes.
	extra    map[string]statusline.Value
	extraRev uint64
	// sh runs the shell segments, and after sets its timer.
	sh    shells
	after func(time.Duration, tea.Msg) tea.Cmd
	// edit is the status line's editor while it is open, seen what its live
	// values were last made from, and editNote what there is to say when it
	// is left.
	edit     *sledit.Model
	seen     editSeen
	editNote string
	// cmds is every command the input box takes, and run what each core
	// command does. kept is set by a command that did nothing, so that its
	// draft stays in the box.
	cmds *commands.Registry
	run  map[string]func(m *model, l commands.Line) tea.Cmd
	kept bool

	width, height int
	screen        screen

	// The list of contacts.
	contacts []client.Contact
	cursor   int
	loading  bool
	// note is a line for the person on the list and its forms, shown until
	// their next keypress.
	note string

	// The open conversation, and how the avatar that shows it is set.
	chat *chat
	av   avatarState
	// raw is what is written to the terminal beside the screen: the pictures
	// of the conversation, and the word to forget them.
	raw rawOut

	// The new-contact form and the rename prompt.
	name   *inputbox.Model
	system *inputbox.Model
	field  int // 0 the name, 1 the system prompt
	target client.Contact
	busy   bool

	// view is the list or the form as last drawn, and stale says it has to
	// be drawn again. The conversation keeps its own.
	view  tea.View
	stale bool
}

func newModel(ctx context.Context, opt Options) *model {
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Location == nil {
		opt.Location = time.Local
	}
	bar, _ := statusline.Preset(statusline.DefaultPreset)
	m := &model{ctx: ctx, opt: opt, bar: bar, width: 80, height: 24, stale: true, probeWait: probeDeadline, after: after}
	m.av = newAvatarState(opt.Avatar)
	if opt.Avatar.Synth != nil {
		opt.Avatar.Synth.Use(sound.Timbres()[m.av.voice])
	}
	m.sh = shells{
		run: opt.Shell, safe: conf.SafeToExecuteFrom, flights: &flights{},
		busy: map[string]bool{}, last: map[string]time.Time{}, done: map[string]shellRun{},
	}
	if m.sh.run == nil {
		m.sh.run = statusline.NewShell()
	}
	m.mods = newModGlue(opt.Mods)
	m.cmds, m.run = commands.NewRegistry(commands.Core()), handlers()
	m.over, m.plan = opt.Overrides, termcaps.PlanFor(opt.Env, opt.Overrides)
	// A terminal that is asked nothing has nothing to answer: what it says
	// anyway is not taken for an answer.
	if m.plan == termcaps.ProbeOff {
		m.replies = termcaps.Fold(m.replies, termcaps.Reply{Kind: termcaps.ReplyDeadline})
	}
	m.resolve()
	m.name = inputbox.New(m.th, "")
	m.system = inputbox.New(m.th, "")
	// What is wrong with the client's files is said once, at the start: the
	// glyphs chat.toml asks for that cannot be drawn, and what the status
	// line's file has that could not be used.
	var wrong []string
	if refused := theme.Check(m.themeOptions()); len(refused) > 0 {
		wrong = append(wrong, "chat.toml: "+strings.Join(refused, "; "))
	}
	wrong = append(wrong, m.takeBar(readBar(opt.Dirs.StatusLineFile(), m.sh.safe), "the default status line is used")...)
	m.note = strings.Join(wrong, "; ")
	return m
}

// themeOptions are the settings the theme takes besides what the terminal
// can do.
func (m *model) themeOptions() theme.Options {
	return theme.Options{Glyph: m.opt.Chat.Glyph}
}

// resolve works out again what the terminal can do, from the environment,
// the settings and the terminal's answers, and makes it so. It is called
// after any of the three has changed.
func (m *model) resolve() tea.Cmd {
	return m.setCaps(termcaps.Resolve(m.opt.Env, m.over, m.replies))
}

// setCaps makes c what the terminal can do. It is the one place the look
// changes: the theme is made again, and everything that was drawn with the
// old one is drawn again. The command it returns clears the screen, which
// the renderer does not repaint by itself after such a change; it is nil
// when nothing changed.
//
// One change is not a change of look: that the terminal has had its time to
// answer. Nothing is drawn from it but one line of the list of mods, so the
// screen is not cleared for it. It is the only change that comes late, two
// seconds after the start, in a terminal that left a question unanswered.
func (m *model) setCaps(c termcaps.Caps) tea.Cmd {
	if m.th != nil && c == m.caps {
		return nil
	}
	first := m.th == nil
	was := m.caps
	m.caps = c
	if was.Settled = c.Settled; !first && was == c {
		// The theme is still made for what the terminal can do, to the last
		// field, and what is drawn with it comes out as it was.
		m.retheme()
		if m.chat != nil {
			m.chat.setTheme(m.th)
		}
		if m.mods.screen != nil {
			m.mods.screen.SetTmuxNote(m.tmuxNote())
		}
		return nil
	}
	defer m.restage()
	cmd := m.restyle()
	if first {
		return nil
	}
	return cmd
}

// restyle makes the theme again, from what the terminal can do and the
// glyphs chat.toml asks for, and has everything drawn with it. The command
// it returns clears the screen.
func (m *model) restyle() tea.Cmd {
	m.retheme()
	// A mod is told what the terminal can do. When that changed, every
	// segment is asked again, and so is what a mod drew.
	if term := m.modTerm(); term != m.mods.term {
		m.mods.term, m.mods.all = term, true
		if m.chat != nil {
			m.chat.setRenderers(m.mods.kinds, true)
		}
	}
	if m.chat != nil {
		// The pictures are drawn as the terminal can now, and the ones it was
		// sent another way are forgotten. The rest are sent again: the screen
		// is about to be cleared.
		m.write(m.chat.setCaps(m.caps))
		m.resend()
		m.chat.setTheme(m.th)
	}
	if m.mods.screen != nil {
		m.mods.screen.SetTmuxNote(m.tmuxNote())
	}
	// What a command printed is folded to ASCII for a terminal that has
	// only that, and the editor shows the theme's own marks.
	m.revalue()
	m.followBar()
	return tea.ClearScreen
}

// retheme makes the theme again and hands it to the two forms' boxes. The
// screens that are kept are drawn again with it.
func (m *model) retheme() {
	m.th, m.stale = theme.New(m.caps, m.themeOptions()), true
	// The boxes are made after the first theme.
	if m.name != nil {
		m.name.SetTheme(m.th)
		m.system.SetTheme(m.th)
	}
}

// say shows a note where the person is looking: in the open conversation,
// or on the list and its forms. tone is for the conversation, whose notes
// have one. Under the status line's editor and the list of mods the
// conversation keeps the note until that screen is left.
func (m *model) say(note, tone string) {
	if (m.screen == screenChat || m.screen == screenStatusLine || m.screen == screenMods) && m.chat != nil {
		m.chat.say(note, tone)
		return
	}
	m.note, m.stale = note, true
}

// Messages from commands.
type (
	contactsMsg struct {
		contacts []client.Contact
		err      error
	}
	// openedMsg is a contact ready to be talked to: fetched, created or
	// renamed. back says it was renamed: return to where the rename was
	// asked for rather than open it. That is the conversation of sess, or
	// the list when sess is nil.
	openedMsg struct {
		contact client.Contact
		err     error
		back    bool
		sess    Session
	}
	// updatesMsg is what a session said since the last one. closed means
	// the session has ended.
	updatesMsg struct {
		sess   Session
		batch  []client.Update
		closed bool
	}
	// toolTickMsg says a moment has passed while a tool of this session's
	// contact was running.
	toolTickMsg struct{ sess Session }
	// clockMsg says a new minute has begun.
	clockMsg struct{}
)

// Init implements tea.Model.
func (m *model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.clockTick(), m.probe(), m.loadMods()}
	if m.opt.Open != nil {
		cmds = append(cmds, m.open(*m.opt.Open))
		// What the list would have said at the start, the conversation says.
		m.chat.setNote(m.note)
		m.note = ""
	} else {
		cmds = append(cmds, m.showContacts())
	}
	return tea.Batch(cmds...)
}

// Update implements tea.Model.
func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height, m.stale = msg.Width, msg.Height, true
		if m.chat != nil {
			m.chat.resize(m.width, m.height)
		}
		if m.mods.screen != nil {
			m.mods.screen.Update(msg) // the screen wants to know how wide it is
		}
	case contactsMsg:
		m.loading, m.stale = false, true
		if msg.err != nil {
			m.note = explain(msg.err)
		} else {
			m.contacts = msg.contacts
			m.cursor = min(m.cursor, max(len(m.contacts)-1, 0))
		}
	case openedMsg:
		if msg.sess != nil && (m.chat == nil || m.chat.sess != msg.sess) {
			break // the conversation that asked for the rename has been left
		}
		m.busy, m.stale = false, true
		switch {
		case msg.err != nil:
			m.say(explain(msg.err), "")
		case msg.back:
			cmd = m.renamed(msg.contact)
		default:
			cmd = m.open(msg.contact)
		}
	case foundMsg:
		cmd = m.onFound(msg)
	case reloadedMsg:
		cmd = m.onReloaded(msg)
	case shellDoneMsg:
		cmd = m.onShellDone(msg)
	case shellTickMsg:
		cmd = m.onShellTick(msg)
	case modsMsg:
		m.setMods(msg.state)
	case modSegmentMsg:
		cmd = m.onModSegment(msg)
	case modCommandMsg:
		m.onModCommand(msg)
	case modRenderedMsg:
		m.onModRendered(msg)
	case modActedMsg:
		m.onModActed(msg)
	case modTickMsg:
		m.onModTick(msg)
	case workspaceMsg:
		cmd = m.onWorkspace(msg)
	case imageDoneMsg:
		m.onImageDone(msg)
	case imageSavedMsg:
		m.onImageSaved(msg)
	case rawWrittenMsg:
		m.onRawWritten()
	case frameMsg:
		m.onFrame(msg)
	case updatesMsg:
		cmd = m.onUpdates(msg)
	case toolTickMsg:
		cmd = m.onToolTick(msg)
	case clockMsg:
		// The clock in the bar, and the "3m ago" of the list.
		m.stale = true
		if m.chat != nil {
			m.chat.dirty |= dirtyBar
		}
		cmd = m.clockTick()
	case tea.KeyPressMsg:
		cmd = m.onKey(msg)
	case tea.PasteMsg:
		cmd = m.toInput(msg)
	default:
		cmd = m.onTerminal(msg)
	}
	if m.screen == screenStatusLine {
		m.followBar()
	}
	// What the message changed may be something a mod's segment follows, or
	// have brought an item into view that a mod draws or a picture that has
	// to be read. What it left to be written to the terminal goes out last.
	return m, tea.Batch(cmd, m.modWatch(), m.modRender(), m.imageJob(), m.flush(), m.frames())
}

func (m *model) onUpdates(msg updatesMsg) tea.Cmd {
	c := m.chat
	if c == nil || msg.sess != c.sess {
		return nil // from a conversation that has since been left
	}
	now := m.opt.Now()
	for _, u := range msg.batch {
		c.apply(u, now)
	}
	var cmds []tea.Cmd
	if !msg.closed {
		cmds = append(cmds, listen(msg.sess))
	}
	// The time a tool has run is drawn from a tick, and only while one runs.
	if c.tr.Live().Running != "" && !c.ticking {
		c.ticking = true
		cmds = append(cmds, toolTick(c.sess))
	}
	return tea.Batch(cmds...)
}

// onToolTick draws the running tool's time again and asks for the next
// tick. The ticks stop with the tool.
func (m *model) onToolTick(msg toolTickMsg) tea.Cmd {
	c := m.chat
	if c == nil || msg.sess != c.sess {
		return nil
	}
	if c.tr.Live().Running == "" {
		c.ticking = false
		return nil
	}
	c.dirty |= dirtyTail | dirtyBody
	return toolTick(c.sess)
}

func toolTick(sess Session) tea.Cmd {
	return tea.Tick(toolTickEvery, func(time.Time) tea.Msg { return toolTickMsg{sess: sess} })
}

// clockTick waits for the next minute to begin. It is the only thing that
// wakes a client with nothing going on.
func (m *model) clockTick() tea.Cmd {
	now := m.opt.Now()
	wait := now.Truncate(time.Minute).Add(time.Minute).Sub(now)
	return tea.Tick(wait, func(time.Time) tea.Msg { return clockMsg{} })
}

// listen waits for a session to say something, then takes whatever else is
// already waiting, so that a fast stream costs one redraw and not one each.
func listen(sess Session) tea.Cmd {
	return func() tea.Msg {
		updates := sess.Updates()
		u, ok := <-updates
		if !ok {
			return updatesMsg{sess: sess, closed: true}
		}
		batch := []client.Update{u}
		for len(batch) < 128 {
			select {
			case u, ok := <-updates:
				if !ok {
					return updatesMsg{sess: sess, batch: batch, closed: true}
				}
				batch = append(batch, u)
			default:
				return updatesMsg{sess: sess, batch: batch}
			}
		}
		return updatesMsg{sess: sess, batch: batch}
	}
}

func (m *model) onKey(msg tea.KeyPressMsg) tea.Cmd {
	switch m.screen {
	case screenChat:
		return m.chatKey(msg)
	case screenContacts:
		return m.contactsKey(msg)
	case screenNew, screenRename:
		return m.formKey(msg)
	case screenStatusLine:
		return m.editMsg(msg)
	case screenMods:
		return m.modsInput(msg)
	default:
		return nil
	}
}

// chatKey handles a key in a conversation. The keys that leave it or act on
// the whole screen are taken here; the rest are the conversation's.
func (m *model) chatKey(msg tea.KeyPressMsg) tea.Cmd {
	c := m.chat
	if c == nil {
		return nil
	}
	// A note is shown until the next key, whichever key that is.
	c.setNote("")
	switch msg.String() {
	case "ctrl+c":
		// First it clears what has been typed; with nothing typed it leaves.
		if c.input.Value() != "" {
			c.clearInput()
			return nil
		}
		return tea.Quit
	case "ctrl+d":
		if c.input.Value() == "" {
			return tea.Quit
		}
	case "ctrl+p":
		m.closeChat()
		return m.showContacts()
	case "ctrl+l":
		return m.clear()
	}
	cmd, line := c.key(msg, m.opt.Now())
	if line != nil {
		return m.runCommand(*line)
	}
	return cmd
}

// toInput hands a message to whichever box is being typed in.
func (m *model) toInput(msg tea.Msg) tea.Cmd {
	switch {
	case m.screen == screenStatusLine:
		return m.editMsg(msg)
	case m.screen == screenMods:
		return m.modsInput(msg)
	case m.screen == screenChat && m.chat != nil:
		return m.chat.typing(msg, m.opt.Now())
	case m.screen == screenRename, m.screen == screenNew && m.field == 0:
		return m.nameInput(msg)
	case m.screen == screenNew:
		cmd, _ := m.system.Update(msg)
		m.stale = true
		return cmd
	}
	return nil
}

// showContacts goes to the list and reloads it.
func (m *model) showContacts() tea.Cmd {
	m.screen, m.loading, m.busy, m.stale = screenContacts, true, false, true
	b, ctx := m.opt.Backend, m.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		contacts, err := b.Contacts(ctx)
		return contactsMsg{contacts: contacts, err: err}
	}
}

// open starts a conversation with a contact. The shell segments of its
// status line run from now on.
func (m *model) open(c client.Contact) tea.Cmd {
	m.closeChat()
	presentation := "text"
	if on, _ := m.staged(); on {
		presentation = "avatar"
	}
	visit := core.Visit{Presentation: presentation}
	if m.opt.Location != nil && m.opt.Location.String() != "Local" {
		visit.TimeZone = m.opt.Location.String()
	}
	sess := m.opt.Backend.Follow(m.ctx, c.ID, visit)
	m.chat, m.screen = newChat(c, sess, m.th, m.opt.Location, m.shownBar(), m.cmds, m.newImages(c.ID)), screenChat
	m.chat.extra = m.extra
	m.chat.setRenderers(m.mods.kinds, false)
	m.chat.resize(m.width, m.height)
	m.restage()
	return tea.Batch(listen(sess), m.shellStep())
}

// closeChat ends the open conversation's session, if there is one. The
// terminal is told to forget the pictures it was sent for it.
func (m *model) closeChat() {
	if m.chat != nil {
		if m.chat.stage != nil {
			m.chat.stage.Hush()
		}
		m.chat.sess.Close()
		m.write(m.chat.imgs.leave())
		m.chat = nil
	}
}

func (m *model) create(nc client.NewContact) tea.Cmd {
	b, ctx := m.opt.Backend, m.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		id, err := b.Create(ctx, nc)
		if err != nil {
			return openedMsg{err: err}
		}
		c, err := b.Contact(ctx, id)
		return openedMsg{contact: c, err: err}
	}
}

// rename asks the server to give a contact a name. While a conversation is
// open the contact is its own, and the answer is for that conversation.
func (m *model) rename(id core.ContactID, name string) tea.Cmd {
	b, ctx := m.opt.Backend, m.ctx
	var sess Session
	if m.chat != nil {
		sess = m.chat.sess
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		c, err := b.Rename(ctx, id, name)
		return openedMsg{contact: c, err: err, back: true, sess: sess}
	}
}

// renamed is called when the server has given a contact a new name. A
// rename asked for in a conversation goes back to it, and the conversation
// says the new name; one asked for on the list goes back to the list.
func (m *model) renamed(c client.Contact) tea.Cmd {
	if m.chat == nil {
		return m.showContacts()
	}
	// The answer to a name typed after /rename can find another screen over
	// the conversation, which stays.
	if m.screen == screenRename {
		m.screen = screenChat
	}
	m.chat.renamed(c)
	m.chat.say("renamed to "+c.Label(), toneDone)
	// A mod that is told the contact's name is told the new one.
	m.mods.all = true
	return nil
}

// explain turns an error into a line for the person.
func explain(err error) string {
	if errors.Is(err, client.ErrUnavailable) {
		return err.Error() + " (is contactd running?)"
	}
	return err.Error()
}

// View implements tea.Model. It returns what was last drawn unless
// something has changed since.
func (m *model) View() tea.View {
	if m.screen == screenChat && m.chat != nil {
		return m.chat.draw(m.opt.Now())
	}
	if !m.stale {
		return m.view
	}
	var (
		lines  []line
		cursor *inputbox.Model
	)
	switch m.screen {
	case screenContacts:
		lines = m.contactsView()
	case screenNew:
		lines, cursor = m.newView()
	case screenRename:
		lines, cursor = m.renameView()
	case screenStatusLine:
		m.view, m.stale = m.editView(), false
		return m.view
	case screenMods:
		m.view, m.stale = m.modsView(), false
		return m.view
	case screenChat:
	}
	m.view, m.stale = m.listView(lines, cursor), false
	return m.view
}
