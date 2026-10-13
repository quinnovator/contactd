// Package ctxtop draws live context and cache diagnostics from the server.
package ctxtop

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/quinnovator/contactd/internal/client"
	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/diagnostic"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
	"github.com/quinnovator/contactd/internal/tui/theme"
)

// Options are the viewer's display and stream settings.
type Options struct {
	Contact string
	Caps    termcaps.Caps
}

type (
	snapshotMsg  diagnostic.Snapshot
	statusMsg    string
	connectedMsg struct{}
)

type model struct {
	client                                  *client.Client
	ctx                                     context.Context //nolint:containedctx // Bubble Tea commands inherit the viewer lifetime; Update has no context argument.
	screen                                  int
	row                                     [6]int
	collapsed                               map[int]bool
	captures                                map[uint64]diagnostic.Details
	loading                                 map[uint64]bool
	failed                                  map[uint64]bool
	finalized                               map[uint64]bool
	events                                  map[string][]core.Event
	eventLoading                            map[string]bool
	query                                   string
	searching                               bool
	raw, sizeSort, expanded, help, keywords bool
	scroll, spin                            int
	baseline                                uint64
	selectionSeq                            uint64
	selectionHash                           string
	notice                                  string
	history                                 []diagnostic.Snapshot
	run                                     string
	filter                                  core.ContactID
	sel                                     uint64
	follow                                  bool
	zoom                                    bool
	details                                 bool
	width                                   int
	height                                  int
	status                                  string
	theme                                   *theme.Theme
	fold                                    *strings.Replacer // set when only ASCII may be drawn
}

func newModel(caps termcaps.Caps) *model {
	m := &model{collapsed: make(map[int]bool), captures: make(map[uint64]diagnostic.Details), loading: make(map[uint64]bool), failed: make(map[uint64]bool), finalized: make(map[uint64]bool), events: make(map[string][]core.Event), eventLoading: make(map[string]bool), follow: true, width: 80, height: 24, status: "connecting to the server", theme: theme.New(caps, theme.Options{})}
	if caps.Glyphs == termcaps.GlyphsASCII {
		m.fold = asciiFold()
	}
	return m
}

type tickMsg time.Time

func tick() tea.Cmd {
	return tea.Tick(250*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}
func (m *model) Init() tea.Cmd { return tick() }

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tickMsg:
		m.spin++
		return m, tick()
	case detailMsg:
		delete(m.loading, msg.call)
		if msg.run != m.run {
			break
		}
		if msg.err != nil {
			m.notice = "Details unavailable or evicted: " + msg.err.Error()
			m.failed[msg.call] = true
		} else {
			if len(m.captures) >= 8 {
				for id := range m.captures {
					if id != m.sel {
						delete(m.captures, id)
						break
					}
				}
			}
			m.captures[msg.call] = msg.detail
			m.finalized[msg.call] = msg.done
		}
	case eventsMsg:
		delete(m.eventLoading, msg.key)
		if msg.err != nil {
			m.notice = msg.err.Error()
			m.events[msg.key] = []core.Event{}
		} else {
			if len(m.events) > 24 {
				m.events = make(map[string][]core.Event)
			}
			m.events[msg.key] = msg.events
			if m.events[msg.key] == nil {
				m.events[msg.key] = []core.Event{}
			}
		}

	case tea.WindowSizeMsg:
		m.width, m.height = max(1, msg.Width), max(1, msg.Height)
	case tea.KeyPressMsg:
		oldCall := m.sel
		m.notice = "" // a notice is read by the next key
		if m.searching {
			switch msg.String() {
			case "ctrl+c":
				return m, tea.Quit
			case "esc":
				m.query = ""
				m.searching = false
			case "enter":
				m.searching = false
			case "backspace":
				rr := []rune(m.query)
				if len(rr) > 0 {
					m.query = string(rr[:len(rr)-1])
				}
			default:
				if msg.Text != "" {
					m.query += msg.Text
				}
			}
			m.row[m.screen] = 0
			return m, m.load()
		}
		h := m.calls()
		i := m.selected(h)
		switch msg.String() {
		case "1", "2", "3", "4", "5", "6":
			m.screen = int(msg.String()[0] - '1')
			m.scroll, m.expanded = 0, false
			m.syncSelection()
		case "m":
			if m.screen == 4 {
				m.keywords = !m.keywords
			}
		case "up", "down", "j", "k":
			n := len(m.items())
			delta := 1
			if msg.String() == "up" || msg.String() == "k" {
				delta = -1
			}
			m.row[m.screen] = max(0, min(max(0, n-1), m.row[m.screen]+delta))
			m.scroll = 0
			m.rememberSelection()
		case "pgdown":
			m.scroll += 8
		case "pgup":
			m.scroll = max(0, m.scroll-8)
		case "r":
			m.raw = !m.raw
			m.scroll = 0
		case "s":
			m.sizeSort = !m.sizeSort
			m.row[m.screen] = 0
		case "/":
			m.searching = true
			m.query = ""
		case "esc":
			m.query = ""
			m.expanded = false
			m.help = false
		case "?":
			m.help = !m.help
		case "g":
			m.screen, m.expanded = 1, false
			m.syncSelection()
		case "[", "]":
			same := m.contactCalls()
			ix := 0
			for j, v := range same {
				if v.Call == m.baseline {
					ix = j
				}
			}
			if msg.String() == "[" {
				ix--
			} else {
				ix++
			}
			if len(same) > 0 {
				m.baseline = same[max(0, min(len(same)-1, ix))].Call
			}
		case "$":
			m.notice = "Cost overlay unavailable: routes do not expose price data."
		case "q", "ctrl+c":
			return m, tea.Quit
		case "left", "h":
			if len(h) > 0 {
				m.sel, m.follow = h[max(0, i-1)].Call, false
			}
		case "right", "l":
			if len(h) > 0 {
				i = min(len(h)-1, i+1)
				m.sel, m.follow = h[i].Call, i == len(h)-1
			}
		case "end", "f":
			m.follow = true
			if len(h) > 0 {
				m.sel = h[len(h)-1].Call
			}
		case "z":
			m.zoom = !m.zoom
		case "enter":
			rows := m.items()
			ix := m.row[m.screen]
			switch {
			case ix < len(rows) && rows[ix].tier >= 0:
				m.collapsed[rows[ix].tier] = !m.collapsed[rows[ix].tier]
			case m.screen == 0 || m.screen == 1 || m.screen == 3:
				m.expanded = !m.expanded
			}
			m.details = !m.details
		case "tab", "shift+tab":
			m.cycleContact(msg.String() == "shift+tab")
		}
		if m.sel != oldCall {
			m.scroll = 0
			m.syncSelection()
		}
	case snapshotMsg:
		delete(m.failed, msg.Call)
		m.accept(diagnostic.Snapshot(msg))
	case connectedMsg:
		m.status = "connected"
	case statusMsg:
		m.status = string(msg)
	}
	return m, m.load()
}

func (m *model) accept(s diagnostic.Snapshot) {
	if s.Run == "" || s.Call == 0 {
		return
	}
	oldCall := m.sel
	if m.run != s.Run {
		m.run, m.history, m.sel, m.filter, m.follow = s.Run, nil, 0, 0, true
		m.captures = make(map[uint64]diagnostic.Details)
		m.loading = make(map[uint64]bool)
		m.events = make(map[string][]core.Event)
		m.eventLoading = make(map[string]bool)
		m.baseline = 0
		m.failed = make(map[uint64]bool)
		m.finalized = make(map[uint64]bool)
	}
	m.status = "live"
	i, found := slices.BinarySearchFunc(m.history, s.Call, func(v diagnostic.Snapshot, id uint64) int {
		if v.Call < id {
			return -1
		}
		if v.Call > id {
			return 1
		}
		return 0
	})
	if found {
		if m.history[i].Done && !s.Done {
			return // a replayed pending half cannot undo a completion
		}
		m.history[i] = s
	} else {
		m.history = slices.Insert(m.history, i, s)
	}
	if len(m.history) > 500 {
		m.history = slices.Delete(m.history, 0, len(m.history)-500)
	}
	h := m.calls()
	if len(h) > 0 && (m.follow || m.selected(h) < 0) {
		if m.follow {
			m.sel = h[len(h)-1].Call
		} else {
			m.sel = h[0].Call
		}
	}
	if oldCall != m.sel {
		m.scroll = 0
		m.syncSelection()
	}
}

func (m *model) calls() []diagnostic.Snapshot {
	if m.filter == 0 {
		return m.history
	}
	var out []diagnostic.Snapshot
	for _, s := range m.history {
		if s.Contact == m.filter {
			out = append(out, s)
		}
	}
	return out
}

func (m *model) selected(h []diagnostic.Snapshot) int {
	for i, s := range h {
		if s.Call == m.sel {
			return i
		}
	}
	return -1
}

func (m *model) cycleContact(back bool) {
	ids := []core.ContactID{0}
	for _, s := range m.history {
		if !slices.Contains(ids, s.Contact) {
			ids = append(ids, s.Contact)
		}
	}
	i := max(0, slices.Index(ids, m.filter))
	if back {
		i = (i + len(ids) - 1) % len(ids)
	} else {
		i = (i + 1) % len(ids)
	}
	m.filter, m.follow = ids[i], true
	h := m.calls()
	if len(h) > 0 {
		m.sel = h[len(h)-1].Call
	}
}

// Run starts the viewer and cancels its reconnecting stream when it exits.
func Run(ctx context.Context, c *client.Client, opt Options) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	profile := colorprofile.ANSI256
	if opt.Caps.Color == termcaps.ColorNone {
		profile = colorprofile.ASCII
	}
	m := newModel(opt.Caps)
	m.client, m.ctx = c, ctx
	p := tea.NewProgram(m, tea.WithContext(ctx), tea.WithColorProfile(profile))
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ctx.Err() == nil {
			err := c.WatchPlans(ctx, opt.Contact, client.PlanObserver{
				Connected: func() { p.Send(connectedMsg{}) },
				Snapshot:  func(s diagnostic.Snapshot) { p.Send(snapshotMsg(s)) },
			})
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				p.Send(statusMsg("reconnecting: " + err.Error()))
			} else {
				p.Send(statusMsg("disconnected; reconnecting"))
			}
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
	_, err := p.Run()
	cancel()
	<-done
	if err == nil || errors.Is(err, tea.ErrProgramKilled) || errors.Is(err, tea.ErrInterrupted) || errors.Is(err, context.Canceled) {
		return nil
	}
	return fmt.Errorf("run context viewer: %w", err)
}
