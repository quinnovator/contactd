// Package modscreen is the /mods screen of the terminal client: the mods that
// are installed, what each one adds, and what each one may read.
//
// The screen draws what it is given (a list of mods.Info) and nothing else.
// It never calls the mod host. A key that asks for something to be done
// (switch a mod on or off, install one, update one) comes back from Update as
// an Action; the root carries it out, hands the new list to SetInfos, and says
// what went wrong, if anything did, with SetNote.
package modscreen

import (
	"path/filepath"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/quinnovator/contactd/internal/tui/mods"
)

// ActionKind says what the screen asks the root to do.
type ActionKind uint8

const (
	// ActToggle switches the named mod on when it is off, and off when it
	// is on.
	ActToggle ActionKind = iota + 1
	// ActInstall installs a mod from the directory at Path.
	ActInstall
	// ActUpdate copies the named mod again from where it was installed.
	ActUpdate
)

// Action is one thing the screen asks the root to do. Name is the mod, for
// ActToggle and ActUpdate. Path is what was typed, for ActInstall, with the
// space around it removed and nothing else changed.
type Action struct {
	Kind       ActionKind
	Name, Path string
}

// maxPath is the longest path the install input takes, in characters. What
// is typed or pasted beyond it is dropped.
const maxPath = 1024

// Model is the screen's state: the list, the selected row, and the install
// input while it is open. It is not safe for concurrent use.
type Model struct {
	infos    []mods.Info
	sel      int
	dirLabel string
	tmuxNote bool
	budget   time.Duration

	// detail says the details have the focus. It matters only on a narrow
	// screen, where they are drawn in place of the list.
	detail bool
	// width is the width the screen was last drawn at; 0 before the first
	// draw.
	width int

	input  textinput.Model
	typing bool
	// installed is the path of the install that was last asked for. The
	// next list shows the mod it brought.
	installed string

	// note is a message from the root that stands in place of the key
	// hints until the next key press, and noteTone the tone it is drawn in.
	note, noteTone string
}

// New makes the screen. infos is what the host's Mods returned, in the order
// to list them. dirLabel is the mods directory as the header shows it, with
// "~" for the home directory. tmuxNote adds the line that says why images are
// half blocks under tmux.
func New(infos []mods.Info, dirLabel string, tmuxNote bool) *Model {
	in := textinput.New()
	in.Prompt = ""
	in.CharLimit = maxPath
	// The terminal's own cursor is used, so nothing blinks and nothing
	// wakes an idle client.
	in.SetVirtualCursor(false)
	// ctrl+v would run a command to read this machine's clipboard. A paste
	// arrives from the terminal as a paste message instead.
	in.KeyMap.Paste.SetEnabled(false)
	return &Model{
		infos:    slices.Clone(infos),
		dirLabel: dirLabel,
		tmuxNote: tmuxNote,
		budget:   mods.DefaultBudget,
		input:    in,
	}
}

// SetInfos replaces the list, after the root carried out an action or the
// host finished loading. The mod that was selected stays selected. After an
// install the mod that arrived is selected, and on a narrow screen its
// details take the list's place, so that what it may read is on the screen
// before anyone switches it on.
func (m *Model) SetInfos(infos []mods.Info) {
	was := ""
	if m.sel < len(m.infos) {
		was = m.infos[m.sel].Manifest.Name
	}
	old := make(map[string]bool, len(m.infos))
	for _, in := range m.infos {
		old[in.Manifest.Name] = true
	}
	m.infos = slices.Clone(infos)
	if len(m.infos) == 0 {
		// There are no details to keep the focus on.
		m.detail = false
	}

	want, arrived := was, false
	if m.installed != "" {
		base := filepath.Base(m.installed)
		m.installed = ""
		for _, in := range m.infos {
			name := in.Manifest.Name
			if !old[name] {
				want, arrived = name, true
				break
			}
			if name == base {
				want, arrived = name, true
			}
		}
	}
	for i, in := range m.infos {
		if in.Manifest.Name == want {
			m.sel = i
			if arrived && m.narrow() {
				m.detail = true
			}
			return
		}
	}
	m.sel = max(0, min(m.sel, len(m.infos)-1))
}

// SetNote shows a message in place of the key hints until the next key
// press: what became of an action, such as why an install failed. The screen
// has no other place for one. tone is a name theme.Theme.Style takes, and ""
// is the warn tone. The text is made safe to draw and takes up to three
// rows. An empty text takes the note away.
func (m *Model) SetNote(text, tone string) {
	if tone == "" {
		tone = toneWarn
	}
	m.note, m.noteTone = text, tone
}

// SetTmuxNote shows or hides the line that says why images are half blocks
// under tmux. New takes the same value; this is for a terminal that answers
// while the screen is open.
func (m *Model) SetTmuxNote(on bool) { m.tmuxNote = on }

// SetBudget tells the screen how long the host lets a mod take for one call,
// which the details state. Until it is called the screen states
// mods.DefaultBudget.
func (m *Model) SetBudget(d time.Duration) {
	if d > 0 {
		m.budget = d
	}
}

// Update handles one message. done is true when the user left the screen.
// act is not nil when the user asked for something the root must do. cmd is
// whatever the install input wants run.
//
// Any key press takes away a note set with SetNote, and then does what it
// does.
//
// Keys: up and down (or k and j) choose a mod; space returns ActToggle and u
// returns ActUpdate for it; i opens a one-line input for a path, where enter
// returns ActInstall and esc closes the input; esc leaves the screen. On a
// screen under 80 columns enter shows the details in place of the list, and
// enter or esc brings the list back. The width is the one View last drew at,
// or the one a tea.WindowSizeMsg gave since.
func (m *Model) Update(msg tea.Msg) (done bool, act *Action, cmd tea.Cmd) {
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		m.setWidth(size.Width)
		return false, nil, nil
	}
	key, ok := msg.(tea.KeyPressMsg)
	if ok {
		// A note is read by the time a key is pressed.
		m.note = ""
	}
	if m.typing {
		act, cmd = m.updateInput(msg)
		return false, act, cmd
	}
	if !ok {
		return false, nil, nil
	}
	switch key.String() {
	case "up", "k":
		m.move(-1)
	case "down", "j":
		m.move(1)
	case "space":
		if name, ok := m.selected(); ok {
			act = &Action{Kind: ActToggle, Name: name}
		}
	case "u":
		if name, ok := m.selected(); ok {
			act = &Action{Kind: ActUpdate, Name: name}
		}
	case "i":
		m.typing = true
		cmd = m.input.Focus()
	case "enter":
		if m.narrow() && len(m.infos) > 0 {
			m.detail = !m.detail
		}
	case "esc":
		if m.narrow() && m.detail {
			m.detail = false
		} else {
			done = true
		}
	}
	return done, act, cmd
}

// updateInput handles a message while the install input is open. Every key
// but enter and esc is typing.
func (m *Model) updateInput(msg tea.Msg) (*Action, tea.Cmd) {
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "esc":
			m.closeInput()
			return nil, nil
		case "enter":
			path := strings.TrimSpace(m.input.Value())
			m.closeInput()
			if path == "" {
				return nil, nil
			}
			m.installed = path
			return &Action{Kind: ActInstall, Path: path}, nil
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return nil, cmd
}

// closeInput closes the install input and forgets what was typed.
func (m *Model) closeInput() {
	m.typing = false
	m.input.Reset()
	m.input.Blur()
}

// move moves the selection and stops at both ends of the list.
func (m *Model) move(delta int) {
	m.sel = max(0, min(m.sel+delta, len(m.infos)-1))
}

// selected returns the name of the selected mod; ok is false when the list
// is empty.
func (m *Model) selected() (name string, ok bool) {
	if m.sel < 0 || m.sel >= len(m.infos) {
		return "", false
	}
	return m.infos[m.sel].Manifest.Name, true
}

// setWidth records the width of the screen. The details can only have the
// focus on a narrow one.
func (m *Model) setWidth(w int) {
	m.width = w
	if !m.narrow() {
		m.detail = false
	}
}

// narrow reports whether the list and the details share one column. Before
// the first draw the width is not known and the screen counts as wide.
func (m *Model) narrow() bool {
	return m.width > 0 && m.width < wideFrom
}
