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
package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/quinnovator/contactd/internal/client"
	"github.com/quinnovator/contactd/internal/core"
)

// Backend is the server, as far as the interface needs it.
type Backend interface {
	Contacts(ctx context.Context) ([]client.Contact, error)
	Contact(ctx context.Context, id core.ContactID) (client.Contact, error)
	Create(ctx context.Context, nc client.NewContact) (core.ContactID, error)
	Rename(ctx context.Context, id core.ContactID, name string) (client.Contact, error)
	// Follow opens a session with a contact that runs until it is closed.
	Follow(ctx context.Context, id core.ContactID) Session
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
func (r Remote) Follow(ctx context.Context, id core.ContactID) Session {
	return r.Client.Follow(ctx, id)
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
}

// Run shows the interface until the person leaves it or ctx is done.
func Run(ctx context.Context, opt Options) error {
	m := newModel(ctx, opt)
	_, err := tea.NewProgram(m, tea.WithContext(ctx)).Run()
	m.closeChat()
	if err != nil && !errors.Is(err, tea.ErrProgramKilled) && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("run the terminal interface: %w", err)
	}
	return nil
}

type screen uint8

const (
	screenContacts screen = iota + 1
	screenChat
	screenNew
	screenRename
)

// model is the whole interface. Bubble Tea calls Update and View on one
// goroutine, so nothing here is locked.
type model struct {
	ctx  context.Context //nolint:containedctx // Bubble Tea's Update has no context to pass; commands need the program's
	opt  Options
	st   styles
	dark bool
	md   markdown

	width, height int
	screen        screen

	// The list of contacts.
	contacts []client.Contact
	cursor   int
	loading  bool
	// note is a line for the person on the list and its forms, shown until
	// their next keypress.
	note string

	// The open conversation.
	chat *chat

	// The new-contact form and the rename prompt.
	name   textinput.Model
	system textarea.Model
	field  int // 0 the name, 1 the system prompt
	target client.Contact
	busy   bool
}

func newModel(ctx context.Context, opt Options) *model {
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Location == nil {
		opt.Location = time.Local
	}
	m := &model{ctx: ctx, opt: opt, st: newStyles(), dark: true, md: glamourMarkdown(true), width: 80, height: 24}
	m.name = textinput.New()
	m.name.Prompt = "❯ "
	m.name.CharLimit = 64
	m.system = textarea.New()
	m.system.SetPromptFunc(2, promptMark)
	m.system.ShowLineNumbers = false
	m.system.DynamicHeight = true
	m.system.MinHeight = 3
	m.system.MaxHeight = 12
	// Enter moves on, as it does in the chat, so a new line needs another key.
	m.system.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("alt+enter", "shift+enter", "ctrl+j"))
	return m
}

// Messages from commands.
type (
	contactsMsg struct {
		contacts []client.Contact
		err      error
	}
	// openedMsg is a contact ready to be talked to: fetched, created or
	// renamed. back says to return to the list rather than open it.
	openedMsg struct {
		contact client.Contact
		err     error
		back    bool
	}
	// updatesMsg is what a session said since the last one. closed means
	// the session has ended.
	updatesMsg struct {
		sess   Session
		batch  []client.Update
		closed bool
	}
)

// Init implements tea.Model.
func (m *model) Init() tea.Cmd {
	cmds := []tea.Cmd{tea.RequestBackgroundColor}
	if m.opt.Open != nil {
		cmds = append(cmds, m.open(*m.opt.Open))
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
		m.width, m.height = msg.Width, msg.Height
	case tea.BackgroundColorMsg:
		if dark := msg.IsDark(); dark != m.dark {
			m.dark, m.md = dark, glamourMarkdown(dark)
		}
	case contactsMsg:
		m.loading = false
		if msg.err != nil {
			m.note = explain(msg.err)
		} else {
			m.contacts = msg.contacts
			m.cursor = min(m.cursor, max(len(m.contacts)-1, 0))
		}
	case openedMsg:
		m.busy = false
		switch {
		case msg.err != nil:
			m.note = explain(msg.err)
		case msg.back:
			cmd = m.showContacts()
		default:
			cmd = m.open(msg.contact)
		}
	case updatesMsg:
		cmd = m.onUpdates(msg)
	case tea.KeyPressMsg:
		cmd = m.onKey(msg)
	case tea.PasteMsg:
		cmd = m.toInput(msg)
	default:
		// Cursor blinks and the like belong to whichever box has the focus.
		cmd = m.toInput(msg)
	}
	if m.screen == screenChat && m.chat != nil {
		m.chat.layout(m.renderer(), m.dark, m.width, m.height)
	}
	return m, cmd
}

func (m *model) renderer() *renderer {
	return &renderer{st: m.st, md: m.md, width: m.width, expanded: m.chat != nil && m.chat.expanded, loc: m.opt.Location}
}

func (m *model) onUpdates(msg updatesMsg) tea.Cmd {
	if m.chat == nil || msg.sess != m.chat.sess {
		return nil // from a conversation that has since been left
	}
	for _, u := range msg.batch {
		m.chat.apply(u, m.opt.Now())
	}
	if msg.closed {
		return nil
	}
	return listen(msg.sess)
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
	default:
		return nil
	}
}

func (m *model) chatKey(msg tea.KeyPressMsg) tea.Cmd {
	c := m.chat
	switch msg.String() {
	case "ctrl+c":
		// First it clears what has been typed; with nothing typed it leaves.
		if c.input.Value() != "" {
			c.input.Reset()
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
	}
	if c.key(msg) {
		return nil
	}
	cmd := m.toInput(msg)
	c.typed(m.opt.Now())
	return cmd
}

func (m *model) contactsKey(msg tea.KeyPressMsg) tea.Cmd {
	m.note = ""
	switch msg.String() {
	case "ctrl+c", "q", "esc":
		return tea.Quit
	case "up", "k":
		m.cursor = max(m.cursor-1, 0)
	case "down", "j":
		m.cursor = min(m.cursor+1, max(len(m.contacts)-1, 0))
	case "enter":
		if m.cursor < len(m.contacts) {
			return m.open(m.contacts[m.cursor])
		}
	case "n":
		m.screen, m.field = screenNew, 0
		m.name.SetValue("")
		m.system.SetValue("")
		m.system.Blur()
		return m.name.Focus()
	case "r":
		if m.cursor < len(m.contacts) {
			m.screen, m.target = screenRename, m.contacts[m.cursor]
			m.name.SetValue(m.target.Name)
			m.name.CursorEnd()
			return m.name.Focus()
		}
	case "g":
		return m.showContacts()
	}
	return nil
}

func (m *model) formKey(msg tea.KeyPressMsg) tea.Cmd {
	m.note = ""
	if m.busy {
		return nil
	}
	switch msg.String() {
	case "ctrl+c":
		return tea.Quit
	case "esc":
		return m.showContacts()
	case "tab", "shift+tab":
		if m.screen == screenNew {
			return m.focusField(1 - m.field)
		}
		return nil
	case "enter":
		name := strings.TrimSpace(m.name.Value())
		switch {
		case m.screen == screenRename:
			m.busy = true
			return m.rename(m.target.ID, name)
		case m.field == 0:
			return m.focusField(1)
		case strings.TrimSpace(m.system.Value()) == "":
			m.note = "say what this contact is: a system prompt"
			return nil
		default:
			m.busy = true
			return m.create(client.NewContact{Name: name, Agent: core.AgentConfig{System: strings.TrimSpace(m.system.Value())}})
		}
	}
	return m.toInput(msg)
}

func (m *model) focusField(i int) tea.Cmd {
	m.field = i
	if i == 0 {
		m.system.Blur()
		return m.name.Focus()
	}
	m.name.Blur()
	return m.system.Focus()
}

// toInput hands a message to whichever box is being typed in.
func (m *model) toInput(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	switch {
	case m.screen == screenChat && m.chat != nil:
		m.chat.input, cmd = m.chat.input.Update(msg)
	case m.screen == screenRename, m.screen == screenNew && m.field == 0:
		m.name, cmd = m.name.Update(msg)
	case m.screen == screenNew:
		m.system, cmd = m.system.Update(msg)
	}
	return cmd
}

// showContacts goes to the list and reloads it.
func (m *model) showContacts() tea.Cmd {
	m.screen, m.loading, m.busy = screenContacts, true, false
	b, ctx := m.opt.Backend, m.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		contacts, err := b.Contacts(ctx)
		return contactsMsg{contacts: contacts, err: err}
	}
}

// open starts a conversation with a contact.
func (m *model) open(c client.Contact) tea.Cmd {
	m.closeChat()
	sess := m.opt.Backend.Follow(m.ctx, c.ID)
	m.chat, m.screen = newChat(c, sess, m.dark), screenChat
	return tea.Batch(listen(sess), m.chat.input.Focus())
}

// closeChat ends the open conversation's session, if there is one.
func (m *model) closeChat() {
	if m.chat != nil {
		m.chat.sess.Close()
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

func (m *model) rename(id core.ContactID, name string) tea.Cmd {
	b, ctx := m.opt.Backend, m.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		c, err := b.Rename(ctx, id, name)
		return openedMsg{contact: c, err: err, back: true}
	}
}

// explain turns an error into a line for the person.
func explain(err error) string {
	if errors.Is(err, client.ErrUnavailable) {
		return err.Error() + " (is contactd running?)"
	}
	return err.Error()
}

// View implements tea.Model.
func (m *model) View() tea.View {
	var body string
	switch m.screen {
	case screenChat:
		if m.chat != nil {
			body = m.chat.render(m.st, m.width)
		}
	case screenContacts:
		body = m.contactsView()
	case screenNew:
		body = m.newView()
	case screenRename:
		body = m.renameView()
	}
	v := tea.NewView(body)
	v.AltScreen = true
	v.WindowTitle = "contactd"
	if m.chat != nil {
		v.WindowTitle = "contactd: " + clean(m.chat.contact.Label())
	}
	return v
}

func (m *model) contactsView() string {
	var b strings.Builder
	b.WriteString(spread(m.st.title.Render("Contacts"), m.st.dim.Render(clean(m.opt.Backend.Where())), m.width))
	b.WriteString("\n\n")
	switch {
	case len(m.contacts) == 0 && m.loading:
		b.WriteString(m.st.dim.Render("  loading…"))
	case len(m.contacts) == 0 && m.note == "":
		b.WriteString(m.st.dim.Render("  No contacts yet. Press n to make one."))
	}
	// Scroll the list so the cursor stays on screen.
	rows := max(m.height-5, 1)
	first := max(min(m.cursor-rows/2, len(m.contacts)-rows), 0)
	now := m.opt.Now()
	for i := first; i < len(m.contacts) && i < first+rows; i++ {
		c := &m.contacts[i]
		mark, st := "  ", m.st.title.Bold(false)
		if i == m.cursor {
			mark, st = "▸ ", m.st.selected
		}
		line := mark + pad(clean(c.Label()), 24) + "  "
		route := pad(clean(c.Provider+"/"+c.Model), 34)
		b.WriteString(ansi.Truncate(st.Render(line)+m.st.dim.Render(route+"  "+ago(now, c.LastActive)), m.width, "…"))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	if m.note != "" {
		b.WriteString(m.st.warn.Render(wrap(clean(m.note), m.width)) + "\n")
	}
	b.WriteString(m.st.dim.Render("enter open · n new · r rename · g reload · q quit"))
	return b.String()
}

func (m *model) newView() string {
	var b strings.Builder
	b.WriteString(m.st.title.Render("New contact") + "\n\n")
	b.WriteString("Name " + m.st.dim.Render("(optional; what you will call it)") + "\n")
	b.WriteString(m.name.View() + "\n\n")
	b.WriteString("System prompt " + m.st.dim.Render("(who this contact is)") + "\n")
	b.WriteString(m.system.View() + "\n\n")
	if m.note != "" {
		b.WriteString(m.st.warn.Render(wrap(clean(m.note), m.width)) + "\n")
	}
	if m.busy {
		b.WriteString(m.st.dim.Render("creating…") + "\n")
	}
	b.WriteString(m.st.dim.Render("tab switch field · enter next / create · alt+enter new line · esc cancel") + "\n")
	b.WriteString(m.st.dim.Render("Tools, model and effort are set with: contactd chat -new NAME -system ... -tools python"))
	return b.String()
}

func (m *model) renameView() string {
	var b strings.Builder
	b.WriteString(m.st.title.Render("Rename "+clean(m.target.Label())) + "\n\n")
	b.WriteString(m.name.View() + "\n\n")
	if m.note != "" {
		b.WriteString(m.st.warn.Render(wrap(clean(m.note), m.width)) + "\n")
	}
	b.WriteString(m.st.dim.Render("enter rename · esc cancel · an empty name removes it"))
	return b.String()
}

// pad cuts or pads s to exactly width columns.
func pad(s string, width int) string {
	s = ansi.Truncate(s, width, "…")
	return s + strings.Repeat(" ", max(width-ansi.StringWidth(s), 0))
}
