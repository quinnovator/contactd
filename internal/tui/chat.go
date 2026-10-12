package tui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/quinnovator/contactd/internal/client"
	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/transcript"
)

const (
	// maxMessageBytes is the server's limit on one message.
	maxMessageBytes = 64 << 10
	// typingEvery is how often, at most, the contact is told its human is
	// still writing.
	typingEvery = 400 * time.Millisecond
	// scrollStep is how far an arrow key moves the conversation.
	scrollStep = 3
)

// chat is one open conversation: the transcript, the session that feeds it,
// and the box a message is written in.
type chat struct {
	contact client.Contact
	sess    Session
	tr      transcript.Transcript

	input textarea.Model
	view  viewport.Model

	state    client.State
	stateErr error
	// start means the beginning of the log has been read: there is nothing
	// older to load.
	start        bool
	loadingOlder bool
	expanded     bool
	// note is a line for the person, shown until their next keypress.
	note       string
	lastTyping time.Time

	// What was last drawn, so that a reply streaming in redraws only itself.
	settled     []string
	settledRev  uint64
	settledFor  drawnFor
	drawnOldest uint64
	blocks      map[blockKey]string
}

// drawnFor is everything besides the transcript that the drawn lines depend
// on.
type drawnFor struct {
	width    int
	expanded bool
	dark     bool
}

// blockKey identifies one drawn item. An item's text is replaced only by
// the log's account of the same event, so its length stands in for it.
type blockKey struct {
	seq, ts  uint64
	kind     transcript.Kind
	size     int
	isError  bool
	cutShort bool
}

func newChat(c client.Contact, sess Session, dark bool) *chat {
	in := textarea.New()
	in.SetPromptFunc(2, promptMark)
	in.Placeholder = "Message " + c.Label()
	in.ShowLineNumbers = false
	in.DynamicHeight = true
	in.MinHeight = 1
	in.MaxHeight = 8
	in.CharLimit = 0
	// Enter sends, so a new line needs another key.
	in.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("alt+enter", "shift+enter", "ctrl+j"))
	st := textarea.DefaultStyles(dark)
	st.Focused.CursorLine = lipgloss.NewStyle()
	st.Focused.Base = lipgloss.NewStyle()
	st.Focused.Prompt = newStyles().you
	in.SetStyles(st)
	in.Focus()

	vp := viewport.New()
	vp.SoftWrap = false
	vp.KeyMap = viewport.KeyMap{} // keys are routed by hand: the input box has first call on them
	return &chat{contact: c, sess: sess, input: in, view: vp, state: client.StateConnecting, blocks: make(map[blockKey]string)}
}

// promptMark marks the first line of a box, and only the first.
func promptMark(info textarea.PromptInfo) string {
	if info.LineNumber == 0 {
		return "❯ "
	}
	return "  "
}

// apply folds one update from the session into the conversation.
func (c *chat) apply(u client.Update, now time.Time) {
	switch u.Kind {
	case client.UpdateEvents:
		c.tr.Events(u.Events)
		c.start = c.start || u.Start
	case client.UpdateOlder:
		c.loadingOlder = false
		if u.Err != nil {
			c.note = "could not load earlier messages: " + u.Err.Error()
			return
		}
		c.tr.Events(u.Events)
		c.start = u.Start
	case client.UpdateDelta:
		c.tr.Delta(u.Delta, now.UnixMilli())
	case client.UpdateAck:
		c.tr.Acked(u.Text, u.Seq, now.UnixMilli())
	case client.UpdateRefused:
		c.tr.Refused(u.Text)
		c.giveBack(u.Text)
		c.note = "not sent: " + u.Reason
	case client.UpdateSynced:
		if lost := c.tr.DropPending(); len(lost) > 0 {
			c.giveBack(strings.Join(lost, "\n"))
			c.note = "not sent: the connection dropped first"
		}
	case client.UpdateState:
		if u.State == client.StateLive && c.state != client.StateLive && strings.HasSuffix(c.note, "not connected") {
			c.note = "" // no longer true
		}
		c.state, c.stateErr = u.State, u.Err
	}
}

// giveBack returns a message that did not arrive to the input box, ahead of
// whatever has been typed since.
func (c *chat) giveBack(text string) {
	if rest := c.input.Value(); strings.TrimSpace(rest) != "" {
		text += "\n" + rest
	}
	c.input.SetValue(text)
}

// key handles a keypress. It reports whether the chat used it; what it does
// not use is typing, and goes to the input box.
func (c *chat) key(msg tea.KeyPressMsg) (used bool) {
	c.note = ""
	switch msg.String() {
	case "enter":
		c.send()
	case "esc":
		// Only while there is a reply to stop: an interrupt with nothing in
		// flight does nothing, and esc should not feel like it did.
		if c.tr.Live().Typing {
			c.sess.Interrupt()
		}
	case "ctrl+o":
		c.expanded = !c.expanded
	case "pgup":
		c.scroll(-c.view.Height() + 1)
	case "pgdown":
		c.scroll(c.view.Height() - 1)
	case "up":
		// With one line in the box there is nowhere for the cursor to go, so
		// the arrows move the conversation. Many terminals send them for the
		// mouse wheel, too.
		if c.input.LineCount() > 1 {
			return false
		}
		c.scroll(-scrollStep)
	case "down":
		if c.input.LineCount() > 1 {
			return false
		}
		c.scroll(scrollStep)
	default:
		return false
	}
	return true
}

// typed is called after the input box has taken a keypress.
func (c *chat) typed(now time.Time) {
	if strings.TrimSpace(c.input.Value()) == "" || now.Sub(c.lastTyping) < typingEvery {
		return
	}
	// A pending reply waits a moment longer while its human is still
	// writing, as a person would.
	c.lastTyping = now
	c.sess.Typing()
}

func (c *chat) send() {
	text := strings.TrimSpace(c.input.Value())
	switch {
	case text == "":
	case len(text) > maxMessageBytes:
		c.note = fmt.Sprintf("not sent: the message is %d bytes and the limit is %d", len(text), maxMessageBytes)
	case c.state != client.StateLive:
		c.note = "not sent: not connected"
	default:
		c.tr.Sent(text)
		c.sess.Say(text)
		c.input.Reset()
		c.view.GotoBottom()
	}
}

// scroll moves the conversation, and asks for earlier history on reaching
// the top of what has been loaded.
func (c *chat) scroll(lines int) {
	if lines < 0 {
		if c.view.AtTop() && !c.start && !c.loadingOlder {
			c.loadingOlder = true
			c.sess.Older()
		}
		c.view.ScrollUp(-lines)
		return
	}
	c.view.ScrollDown(lines)
}

// layout sizes the parts for the window and redraws the conversation. It is
// called after anything that could have changed either.
func (c *chat) layout(r *renderer, dark bool, width, height int) {
	// Whether the newest line is in view is asked before anything is
	// resized: a box that grows by a line must not count as scrolling away.
	atBottom := c.view.AtBottom() || c.view.TotalLineCount() <= c.view.Height()
	before := len(c.settled)
	offset := c.view.YOffset()

	c.input.SetWidth(width)
	c.view.SetWidth(width)
	c.view.SetHeight(max(height-2-c.input.Height(), 1)) // a header line and a status line

	want := drawnFor{width: width, expanded: c.expanded, dark: dark}
	if want != c.settledFor {
		clear(c.blocks)
	}
	if want != c.settledFor || c.tr.SettledRev() != c.settledRev || c.settled == nil {
		grewAtTop := c.grewAtTop()
		c.settled = c.drawSettled(r)
		c.settledFor, c.settledRev = want, c.tr.SettledRev()
		if grewAtTop && !atBottom {
			// Earlier history arrived above: keep the same lines in view.
			offset += len(c.settled) - before
		}
	}
	lines := c.settled
	if live := r.live(c.tr.Live()); live != "" {
		lines = append(slices.Clip(lines), "")
		lines = append(lines, strings.Split(live, "\n")...)
	}
	c.view.SetContentLines(lines)
	if atBottom {
		c.view.GotoBottom()
	} else {
		c.view.SetYOffset(offset)
	}
}

// grewAtTop reports whether the transcript's oldest item is older than the
// oldest one last drawn.
func (c *chat) grewAtTop() bool {
	return c.drawnOldest != 0 && c.tr.Oldest() != 0 && c.tr.Oldest() < c.drawnOldest
}

// drawSettled draws everything but the turn in flight: the items, with a
// line between days, and the messages still being sent.
func (c *chat) drawSettled(r *renderer) []string {
	items := c.tr.Items()
	lines := make([]string, 0, 4*len(items))
	// Only the blocks still in use are kept, so the cache cannot outgrow
	// the conversation.
	kept := make(map[blockKey]string, len(items))
	add := func(block string) {
		if block == "" {
			return
		}
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, strings.Split(block, "\n")...)
	}
	if c.start && len(items) > 0 {
		add(r.rule("the beginning"))
	}
	var prev int64
	for i := range items {
		it := &items[i]
		if it.TS != 0 && (prev == 0 || !r.sameDay(prev, it.TS)) {
			add(r.day(it.TS))
		}
		if it.TS != 0 {
			prev = it.TS
		}
		k := blockKey{
			seq: it.Seq, ts: uint64(max(it.TS, 0)), kind: it.Kind, size: len(it.Text) + len(it.Lead),
			isError: it.IsError, cutShort: it.Interrupted,
		}
		block, ok := c.blocks[k]
		if !ok {
			block = r.item(it)
		}
		kept[k] = block
		// A result belongs to the call above it: no blank line between.
		if it.Kind == transcript.ToolResult && len(lines) > 0 {
			lines = append(lines, strings.Split(block, "\n")...)
			continue
		}
		add(block)
	}
	for _, text := range c.tr.Pending() {
		add(r.pending(text))
	}
	c.blocks, c.drawnOldest = kept, c.tr.Oldest()
	return lines
}

// header is the line above the conversation: who this is, what serves it,
// and how its cache is doing.
func (c *chat) header(st styles, width int) string {
	left := st.title.Render(clean(c.contact.Label()))
	if c.contact.Provider != "" {
		left += st.dim.Render("  " + clean(c.contact.Provider+"/"+c.contact.Model))
	}
	var right string
	if a, ok := c.tr.Attempt(); ok {
		right = st.dim.Render(usage(a))
	}
	return spread(left, right, width)
}

// usage sums up a model call for the header: how much of the prompt came
// from the provider's cache, and how long the call took.
func usage(a core.AttemptBody) string {
	if a.Error != "" {
		return "last call failed: " + clean(a.Error)
	}
	if a.Input <= 0 {
		return ""
	}
	return fmt.Sprintf("%d%% of %s tokens cached · %s", 100*a.Cached/a.Input, thousands(a.Input), seconds(a.MS))
}

func thousands(n int) string {
	if n < 1000 {
		return strconv.Itoa(n)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}

func seconds(ms int64) string {
	return fmt.Sprintf("%.1fs", float64(ms)/1000)
}

// status is the line between the conversation and the input box: what is
// happening now on the left, and what the keys do on the right.
func (c *chat) status(st styles, width int) string {
	live := c.tr.Live()
	var left string
	switch {
	case c.note != "":
		left = st.warn.Render(clean(c.note))
	case c.state == client.StateGone:
		left = st.bad.Render("this contact no longer exists")
	case c.state == client.StateDown:
		left = st.warn.Render("disconnected; trying again")
		if c.stateErr != nil {
			left += st.dim.Render("  " + clean(c.stateErr.Error()))
		}
	case c.state == client.StateConnecting:
		left = st.dim.Render("connecting…")
	case live.Running != "":
		left = st.tool.Render("running " + toolName(live.Running) + "…")
	case live.Typing:
		left = st.dim.Render(clean(c.contact.Label()) + " is replying…")
	case c.loadingOlder:
		left = st.dim.Render("loading earlier messages…")
	case !c.view.AtBottom():
		left = st.dim.Render("↓ pgdn for newer")
	}
	hints := []string{"enter send", "alt+enter new line", "ctrl+o details", "ctrl+p contacts"}
	if live.Typing {
		hints = append([]string{"esc stop"}, hints...)
	}
	return spread(left, st.dim.Render(fit(hints, width-ansi.StringWidth(left)-2)), width)
}

// fit joins as many of the hints as there is room for, the first ones first.
func fit(hints []string, room int) string {
	var out string
	for _, h := range hints {
		next := h
		if out != "" {
			next = out + " · " + h
		}
		if ansi.StringWidth(next) > room {
			break
		}
		out = next
	}
	return out
}

// spread puts one string at the left edge and one at the right. When both do
// not fit, the right one gives way.
func spread(left, right string, width int) string {
	lw, rw := ansi.StringWidth(left), ansi.StringWidth(right)
	switch {
	case lw >= width:
		return ansi.Truncate(left, width, "…")
	case lw+rw+2 > width:
		return left
	default:
		return left + strings.Repeat(" ", width-lw-rw) + right
	}
}

func (c *chat) render(st styles, width int) string {
	return strings.Join([]string{c.header(st, width), c.view.View(), c.status(st, width), c.input.View()}, "\n")
}
