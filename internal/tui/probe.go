package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/quinnovator/contactd/internal/tui/canvas"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
)

// What the terminal is asked when the client starts, and what it answers.
//
// The questions go out once, from Init, and nothing waits for the answers:
// keys are read and the screen is drawn at once, for the terminal the
// environment describes. Each answer is folded into what is known, and when
// that changes what the terminal can do, the screen is drawn again for it.
// After the deadline an answer to one of the questions is no longer
// believed, and no question is ever asked later.
//
// This is the only file that knows the terminal library under Bubble Tea:
// some answers reach Update as its events, untranslated.

// probeDeadline is how long the terminal has to answer.
const probeDeadline = 2 * time.Second

// probeDeadlineMsg says the time for answers is over.
type probeDeadlineMsg struct{}

// probe asks the terminal what it can do, as the plan says: its background
// colour through Bubble Tea, then the plan's own questions in one write. It
// also starts the wait for the deadline. It is nil when nothing is asked.
func (m *model) probe() tea.Cmd {
	if m.plan == termcaps.ProbeOff {
		return nil
	}
	ask := []tea.Cmd{tea.RequestBackgroundColor}
	if questions := termcaps.ProbeBytes(m.plan); questions != "" {
		ask = append(ask, tea.Raw(questions))
	}
	return tea.Batch(
		tea.Sequence(ask...),
		tea.Tick(m.probeWait, func(time.Time) tea.Msg { return probeDeadlineMsg{} }),
	)
}

// onTerminal takes a message that is none of the interface's own. An answer
// from the terminal is folded into what is known, and the screen follows
// when that changes what the terminal can do. Anything else is dropped.
func (m *model) onTerminal(msg tea.Msg) tea.Cmd {
	reply, ok := terminalReply(msg)
	if !ok {
		return nil
	}
	// The avatar is drawn against the very colour, not only its kind.
	if bg, ok := msg.(tea.BackgroundColorMsg); ok && bg.Color != nil {
		m.av.paper, m.av.dark, m.av.known = canvas.FromColor(bg.Color), bg.IsDark(), true
		defer m.restage()
	}
	// A picture is sent in a way that asks for no answer. A terminal that
	// answers all the same does so in the words of its answer to the question
	// of the start, and its "no" to one picture is not "no pictures here".
	if reply.Kind == termcaps.ReplyKitty && m.raw.used {
		return nil
	}
	m.replies = termcaps.Fold(m.replies, reply)
	cmd := m.resolve()
	// A tmux that only its own answer gave away was not asked about the
	// terminal behind it. It is asked now, once.
	if m.plan == termcaps.ProbeFull && m.replies.TmuxSeen && !m.tmuxAsked {
		m.tmuxAsked = true
		cmd = tea.Batch(cmd, tea.Raw(termcaps.TmuxRoundBytes()))
	}
	return cmd
}

// terminalReply turns a message into the answer it carries. ok is false for
// a message that answers nothing the client reads: the sequences the
// terminal library could not name, and the answers to questions nobody here
// asked. None of them is ever typed into the box.
func terminalReply(msg tea.Msg) (reply termcaps.Reply, ok bool) {
	switch msg := msg.(type) {
	case probeDeadlineMsg:
		return termcaps.Reply{Kind: termcaps.ReplyDeadline}, true
	case tea.BackgroundColorMsg:
		return termcaps.Reply{Kind: termcaps.ReplyBackground, Dark: msg.IsDark()}, true
	case tea.TerminalVersionMsg:
		return termcaps.Reply{Kind: termcaps.ReplyVersion, Text: msg.Name}, true
	case tea.ModeReportMsg:
		// Bubble Tea asks about grapheme clustering itself, and its renderer
		// starts counting by cluster on the same three answers.
		if msg.Mode != ansi.ModeUnicodeCore {
			return reply, false
		}
		recognised := msg.Value == ansi.ModeSet || msg.Value == ansi.ModeReset || msg.Value == ansi.ModePermanentlySet
		return termcaps.Reply{Kind: termcaps.ReplyMode2027, OK: recognised}, true
	case tea.KeyboardEnhancementsMsg:
		return termcaps.Reply{Kind: termcaps.ReplyKeyboard, Flags: msg.Flags}, true
	case uv.KittyGraphicsEvent:
		return termcaps.Reply{Kind: termcaps.ReplyKitty, ID: msg.Options.ID, OK: string(msg.Payload) == "OK"}, true
	case uv.PrimaryDeviceAttributesEvent:
		return termcaps.Reply{Kind: termcaps.ReplyDA1}, true
	case uv.CellSizeEvent:
		// The terminal sends the height first; the library has put the two
		// in their fields.
		return termcaps.Reply{Kind: termcaps.ReplyCell, W: msg.Width, H: msg.Height}, true
	case uv.UnknownEvent, uv.UnknownCsiEvent, uv.UnknownSs3Event, uv.UnknownOscEvent,
		uv.UnknownDcsEvent, uv.UnknownSosEvent, uv.UnknownPmEvent, uv.UnknownApcEvent,
		uv.SecondaryDeviceAttributesEvent, uv.TertiaryDeviceAttributesEvent,
		uv.PixelSizeEvent, uv.WindowOpEvent, uv.ModifyOtherKeysEvent:
		return reply, false
	default:
		return reply, false
	}
}
