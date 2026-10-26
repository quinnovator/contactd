package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/quinnovator/contactd/internal/client"
	"github.com/quinnovator/contactd/internal/tui/commands"
	"github.com/quinnovator/contactd/internal/tui/conf"
	"github.com/quinnovator/contactd/internal/tui/frame"
	"github.com/quinnovator/contactd/internal/tui/safe"
)

type helperPeek struct {
	chat   *chat
	handle uint64
}

func cmdHelpers(m *model, _ commands.Line) tea.Cmd {
	c := m.chat
	c.hideHelpers = !c.hideHelpers
	value := "on"
	if c.hideHelpers {
		value = "off"
	}
	m.opt.Chat.Helpers = value
	c.layout()
	c.dirty = dirtyAll
	if path := m.opt.Dirs.ChatFile(); path != "" {
		if err := conf.SaveHelpers(path, value); err != nil {
			c.setNote("could not save helper strip setting: " + err.Error())
		}
	}
	return nil
}

func cmdA2A(m *model, _ commands.Line) tea.Cmd {
	c := m.chat
	c.closeHelperOverlay()
	c.wireOpen = true
	c.wireSelected = max(0, len(c.board.Wire())-1)
	c.dirty = dirtyAll
	return nil
}

func cmdHelper(m *model, l commands.Line) tea.Cmd {
	handle, err := strconv.ParseUint(one(l), 10, 64)
	if err != nil {
		return m.wrong(l)
	}
	c := m.chat
	h, ok := c.board.Get(handle)
	if !ok {
		return m.refuse(fmt.Sprintf("you have no helper %d", handle))
	}
	if h.Contact == 0 {
		return m.refuse(fmt.Sprintf("helper %d runs on another server; its own log is not available here", handle))
	}
	c.closeHelperOverlay()
	sess := m.opt.Backend.Follow(m.ctx, h.Contact)
	p := newChat(client.Contact{ID: h.Contact, Name: fmt.Sprintf("helper %d", handle)}, sess, c.th, c.loc, c.bar, c.cmds, m.newImages(h.Contact))
	p.tr.ShowChecks = true
	p.resize(c.width, c.height)
	c.peek = &helperPeek{chat: p, handle: handle}
	c.dirty = dirtyAll
	return listen(sess)
}

// closeHelperOverlay closes /a2a or /helper. Closing a peek ends its session
// and lets go of its pictures; what the terminal must be told to forget them
// is left in c.raw for the model to write.
func (c *chat) closeHelperOverlay() bool {
	had := c.peek != nil || c.wireOpen
	if c.peek != nil {
		c.peek.chat.sess.Close()
		c.raw += c.peek.chat.imgs.leave()
		c.peek = nil
	}
	c.wireOpen = false
	c.wireExpanded = false
	if had {
		c.dirty = dirtyAll
	}
	return had
}

func (c *chat) helperKey(msg tea.KeyPressMsg) bool {
	if c.peek == nil && !c.wireOpen {
		return false
	}
	switch msg.String() {
	case "esc":
		c.closeHelperOverlay()
	case "up", "pgup":
		if c.wireOpen {
			c.wireSelected = max(0, c.wireSelected-1)
		} else {
			c.peek.chat.scroll(-1)
		}
	case "down", "pgdown":
		if c.wireOpen {
			c.wireSelected = min(max(0, len(c.board.Wire())-1), c.wireSelected+1)
		} else {
			c.peek.chat.scroll(1)
		}
	case "enter":
		if c.wireOpen {
			c.wireExpanded = !c.wireExpanded
		}
	}
	c.dirty = dirtyAll
	return true
}

// helperRows draws the strip in the rows the plan gives it. A plan cut short
// lists fewer helpers and keeps the row that counts the rest.
func (c *chat) helperRows(now time.Time) []string {
	if c.rows.Helpers == 0 {
		return nil
	}
	hs := c.board.Active()
	n, more := frame.StripRows(c.rows.Helpers, len(hs))
	rows := make([]string, 0, n+1)
	for _, h := range hs[:n] {
		rows = append(rows, c.list.rd.HelperRow(h, now.UnixMilli(), c.width))
	}
	if more > 0 {
		rows = append(rows, c.th.Truncate(fmt.Sprintf("+%d more %s /helpers", more, c.th.G.Mid), c.width))
	}
	return rows
}

func (c *chat) helperOverlayRows(now time.Time) []string {
	height := max(0, c.rows.Body-1)
	if c.peek != nil {
		p := c.peek.chat
		// The peek is drawn with what the conversation is drawn with; its
		// blocks are drawn again only when that changes.
		if p.th != c.th || p.width != c.width || p.height != c.height {
			p.th, p.width, p.height = c.th, c.width, c.height
			p.restyle()
		}
		p.win.Resize(max(1, c.width), height)
		p.sync()
		p.win.SetTail(p.tail(now))
		h, _ := c.board.Get(c.peek.handle)
		title := fmt.Sprintf("helper %d %s %s %s %s %s esc returns", h.Handle, c.th.G.Mid, h.Title, c.th.G.Mid, h.State, c.th.G.Mid)
		return append([]string{c.th.Rule(c.width, safe.Line(title, 512, false))}, p.win.View(&p.list)...)
	}
	title := "A2A " + c.th.G.Mid + " esc closes " + c.th.G.Mid + " " + c.th.G.UpDown + " scroll " + c.th.G.Mid + " enter expands"
	rows := []string{c.th.Rule(c.width, title)}
	history := c.board.Wire()
	if len(history) == 0 {
		return append(rows, c.th.Truncate("No A2A traffic in this session.", c.width))
	}
	selected := min(c.wireSelected, len(history)-1)
	start := max(0, selected-height+1)
	for i := start; i <= selected; i++ {
		entry := history[i]
		dir := "<-"
		if entry.Dir == "out" {
			dir = "->"
		}
		mark := " "
		if i == selected {
			mark = c.th.G.Pointer
		}
		line := fmt.Sprintf("%s %s %s %-18s helper %d %s %s", mark, time.UnixMilli(entry.At).In(c.loc).Format("15:04:05"), dir, entry.Method, entry.Handle, entry.State, entry.Text)
		rows = append(rows, c.th.Truncate(safe.Line(line, 1024, false), c.width))
	}
	if c.wireExpanded {
		payload := history[selected].Payload
		var pretty bytes.Buffer
		if json.Indent(&pretty, []byte(payload), "", "  ") == nil {
			payload = pretty.String()
		}
		detail := c.th.Wrap(safe.Clean(payload, true), max(1, c.width-2))
		if len(detail) > height-1 {
			detail = detail[:max(0, height-1)]
		}
		rows = append([]string{rows[0], rows[len(rows)-1]}, detail...)
	}
	return rows
}

// helperCount is the helpers the strip would show: none while it is hidden.
func (c *chat) helperCount() int {
	if c.hideHelpers {
		return 0
	}
	return len(c.board.Active())
}

// helperTick reports whether the strip shows a countdown, which is drawn
// again every second.
func (c *chat) helperTick() bool {
	if c.rows.Helpers == 0 {
		return false
	}
	hs := c.board.Active()
	n, _ := frame.StripRows(c.rows.Helpers, len(hs))
	for _, h := range hs[:n] {
		if h.NextCheck > 0 {
			return true
		}
	}
	return false
}

// helpersMoved lays the screen out again when the number of helpers the
// strip shows has changed since the last layout: a finished helper drops out
// of it with nothing else happening.
func (c *chat) helpersMoved() {
	if c.helperCount() == c.helperShown {
		return
	}
	c.layout()
	c.dirty |= dirtyHelpers
}

// Preserve the person's draft while either overlay owns the input region.
func (c *chat) helperInput() string {
	if c.peek != nil {
		return fmt.Sprintf("read-only: helper %d works for %s", c.peek.handle, c.label)
	}
	return strings.Join([]string{"A2A traffic", "esc returns"}, " "+c.th.G.Mid+" ")
}
