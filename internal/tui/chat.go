package tui

import (
	"fmt"
	"hash/fnv"
	"math"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"

	"github.com/quinnovator/contactd/internal/client"
	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/transcript"
	"github.com/quinnovator/contactd/internal/tui/commands"
	"github.com/quinnovator/contactd/internal/tui/draw"
	"github.com/quinnovator/contactd/internal/tui/frame"
	"github.com/quinnovator/contactd/internal/tui/inputbox"
	"github.com/quinnovator/contactd/internal/tui/mods/wire"
	"github.com/quinnovator/contactd/internal/tui/palette"
	"github.com/quinnovator/contactd/internal/tui/safe"
	"github.com/quinnovator/contactd/internal/tui/scrollback"
	"github.com/quinnovator/contactd/internal/tui/stage"
	"github.com/quinnovator/contactd/internal/tui/statusline"
	"github.com/quinnovator/contactd/internal/tui/theme"
)

const (
	// maxMessageBytes is the server's limit on one message.
	maxMessageBytes = 64 << 10
	// typingEvery is how often, at most, the contact is told its human is
	// still writing.
	typingEvery = 400 * time.Millisecond
	// scrollStep is how far an arrow key moves the conversation.
	scrollStep = 3
	// cachedLines is how many drawn lines the transcript window keeps
	// besides the ones in view.
	cachedLines = 4000
)

// The kinds of block in the transcript, the Kind of a block's key.
const (
	blockBeginning uint8 = iota + 1
	blockDay
	blockItem
	blockFailure
	blockPending
	blockLocal
)

// What a draft that cannot be sent or run is answered with.
const (
	noteRefused = "not sent: a draft that starts with / is a command; start with // to send it"
	noteUnknown = "; /help lists them; start with // to send it"
)

// toneDone is the tone of a note that says something was done.
const toneDone = "ok"

// region names a part of the chat screen that has to be drawn again.
type region uint8

const (
	dirtyHeader region = 1 << iota
	// dirtyTail is the turn in flight, under the last block. dirtyBody is
	// the transcript's rows: what the window shows of the blocks and the
	// tail.
	dirtyTail
	dirtyBody
	dirtyPalette
	dirtyBar
	dirtyInput
	dirtyHint

	dirtyAll = dirtyHeader | dirtyTail | dirtyBody | dirtyPalette | dirtyBar | dirtyInput | dirtyHint
)

// block is one entry of the transcript's list: an item, a message still on
// its way, a rule, or what a command printed.
type block struct {
	key scrollback.Key
	rev uint64
	gap int
	// item is the index of the block's item among the transcript's, or of
	// what a command printed among the chat's local blocks.
	item int
	// ts is a time on a day rule's day, or the time of an item, in unix ms.
	ts int64
	// text is a pending message.
	text string
}

// blockList is the transcript as the window sees it: a list of blocks, of
// which it asks for the lines of the few in view. It is built again, without
// drawing anything, whenever the settled part of the conversation changes.
type blockList struct {
	blocks []block
	index  map[scrollback.Key]int
	// items is the transcript's own slice, as it was when the list was
	// built. locals is the chat's: what commands printed.
	items  []transcript.Item
	locals []local
	// rd draws a block. It is made for one width, theme and expansion.
	rd *draw.Renderer
}

// Len implements scrollback.Source.
func (l *blockList) Len() int { return len(l.blocks) }

// Key implements scrollback.Source.
func (l *blockList) Key(i int) scrollback.Key { return l.blocks[i].key }

// Rev implements scrollback.Source.
func (l *blockList) Rev(i int) uint64 { return l.blocks[i].rev }

// Gap implements scrollback.Source.
func (l *blockList) Gap(i int) int { return l.blocks[i].gap }

// Index implements scrollback.Source.
func (l *blockList) Index(k scrollback.Key) (int, bool) {
	i, ok := l.index[k]
	return i, ok
}

// Lines implements scrollback.Source. The width is the renderer's own: the
// chat makes a renderer for the new width before the window hears of it.
func (l *blockList) Lines(i, _ int) []string {
	b := &l.blocks[i]
	switch b.key.Kind {
	case blockBeginning:
		return l.rd.Beginning()
	case blockDay:
		return l.rd.Day(b.ts)
	case blockItem, blockFailure:
		return l.rd.Item(&l.items[b.item])
	case blockPending:
		return l.rd.Pending(b.text)
	case blockLocal:
		return l.rd.Local(l.locals[b.item].title, l.locals[b.item].lines)
	default:
		return nil
	}
}

// add puts a block at the end of the list. A key is never used twice: a
// block that would repeat one takes the next free Sub.
func (l *blockList) add(b block) {
	for {
		if _, taken := l.index[b.key]; !taken {
			break
		}
		b.key.Sub++
	}
	l.index[b.key] = len(l.blocks)
	l.blocks = append(l.blocks, b)
}

// sent is a message on its way, with the number its block is known by. The
// number stays with the message, so the view does not move when an earlier
// message arrives.
type sent struct {
	id   uint64
	text string
}

// local is what a command printed into the conversation: the answer of
// /help or /status. It is the chat's own and never in the log. It stands
// after the item that was the newest when it was made, and stays there.
type local struct {
	id uint64
	// after is the sequence number of the newest item there was.
	after uint64
	title string
	lines []wire.Line
}

// chat is one open conversation: the transcript, the session that feeds it,
// the window on the transcript, and the box a message or a command is
// written in.
type chat struct {
	contact client.Contact
	sess    Session
	tr      transcript.Transcript

	th  *theme.Theme
	loc *time.Location
	// bar is the status line's configuration, and extra what its shell and
	// mod segments have to show, by Segment.ID. Both are the model's.
	bar   statusline.Config
	extra map[string]statusline.Value
	// label, provider and model are the contact's, safe to draw.
	label, provider, model string

	input *inputbox.Model
	// cmds is every command there is, and pal the list of them shown while
	// one is being typed.
	cmds *commands.Registry
	pal  *palette.Model
	win  *scrollback.Window
	list blockList
	// drawn is what the list's renderer was made with.
	drawn draw.Options

	state client.State
	// stateErr says why the connection is down, safe to draw.
	stateErr string
	// start means the beginning of the log has been read: there is nothing
	// older to load.
	start        bool
	loadingOlder bool
	expanded     bool
	// note is a line for the person, shown in place of the key hints until
	// their next keypress, and noteTone the tone it is drawn in.
	note, noteTone string
	lastTyping     time.Time

	// pending are the messages on their way, in the order they were sent.
	pending  []sent
	nextSent uint64
	// locals are what commands printed, in the order they were made.
	locals    []local
	nextLocal uint64
	// What the block list was last built from, and how often it was built.
	listRev    uint64
	listStart  bool
	listLocals int
	listBuilt  bool
	rebuilds   int
	// waiting is how many sub-agents the contact waits for.
	waiting int
	// turns counts the turns that ended while the conversation was open.
	turns uint64
	// renders has what the mods drew of this conversation, and imgs the
	// pictures its replies name.
	renders *renders
	imgs    *images

	// stage is the avatar that performs the conversation, where the
	// terminal can draw one. With it the transcript's rows are the stage's,
	// and there is no thread. sheet is what lies over the stage to be read,
	// and cards the renderers of what the stage shows, by their width.
	stage *stage.Stage
	sheet *sheet
	cards map[int]*draw.Renderer
	// primed says the log has been read once: what arrives from now on is
	// news, and is said aloud. framing says a frame of the stage is on its
	// way.
	primed, framing bool
	// openedSeq is the end of the log when this conversation was opened.
	// The stage starts idle; this history stays available to read explicitly.
	openedSeq uint64

	// toolSince is when the running tool started, and ticking says a tick
	// is on its way to draw its time again.
	toolSince time.Time
	ticking   bool

	width, height int
	rows          frame.Rows

	// What was last drawn. dirty names the regions that are stale; snap and
	// info are what the bar and the header were drawn from.
	dirty region
	reg   frame.Regions
	snap  statusline.Snapshot
	info  frame.HeaderInfo
	view  tea.View
}

func newChat(
	c client.Contact, sess Session, th *theme.Theme, loc *time.Location, bar statusline.Config, cmds *commands.Registry,
	imgs *images,
) *chat {
	ch := &chat{
		sess: sess, th: th, loc: loc, bar: bar,
		cmds: cmds, pal: palette.New(),
		state:   client.StateConnecting,
		win:     scrollback.New(cachedLines),
		list:    blockList{index: make(map[scrollback.Key]int)},
		renders: &renders{got: make(map[renderKey]*rendered)},
		imgs:    imgs,
		width:   80, height: 24,
		dirty: dirtyAll,
	}
	ch.name(c)
	ch.input = inputbox.New(th, "Message "+ch.label)
	ch.drawn = ch.drawOptions()
	ch.list.rd = draw.New(ch.drawn)
	ch.layout()
	return ch
}

// name takes what the server says about the contact: what it is called and
// what serves it, made safe to draw.
func (c *chat) name(contact client.Contact) {
	c.contact = contact
	c.label = safe.Line(contact.Label(), 256, false)
	c.provider, c.model = safe.Line(contact.Provider, 256, false), safe.Line(contact.Model, 256, false)
}

// renamed is called when the contact has a new name. The header, the title
// and the empty box all say it.
func (c *chat) renamed(contact client.Contact) {
	c.name(contact)
	draft := c.input.Value()
	c.input = inputbox.New(c.th, "Message "+c.label)
	c.input.SetValue(draft)
	c.inputChanged()
	c.dirty = dirtyAll
}

// drawOptions are what the blocks are drawn with right now.
func (c *chat) drawOptions() draw.Options {
	cols, rows := frame.ImageBox(c.width, c.height)
	return draw.Options{
		Theme: c.th, Width: max(c.width, 1), Expanded: c.expanded, Location: c.loc,
		Images: c.imgs, Mods: c.renders,
		MaxCols: cols, MaxRows: rows,
	}
}

// restyle makes the renderer again when what the blocks are drawn with has
// changed: the width, the theme, the expansion or the image box. Every
// block is then drawn again when it next comes into view.
func (c *chat) restyle() {
	o := c.drawOptions()
	if o == c.drawn {
		return
	}
	c.drawn = o
	c.list.rd = c.list.rd.With(o)
	c.win.InvalidateAll()
	c.restaged()
	c.dirty |= dirtyTail | dirtyBody
}

// restaged has everything the stage shows drawn again: what it is drawn
// with has changed, or what it draws.
func (c *chat) restaged() {
	c.cards = nil
	if c.stage != nil {
		c.stage.Refresh()
	}
	if c.sheet != nil {
		c.sheet.drawn = false
	}
}

// resize gives the conversation the terminal's size.
func (c *chat) resize(width, height int) {
	if width == c.width && height == c.height {
		return
	}
	c.width, c.height = width, height
	c.restyle()
	c.layout()
	c.dirty = dirtyAll
}

// setTheme draws the conversation with another theme from now on.
func (c *chat) setTheme(th *theme.Theme) {
	c.th = th
	c.input.SetTheme(th)
	if c.stage != nil {
		c.stage.SetTheme(th)
	}
	c.restyle()
	c.layout()
	c.dirty = dirtyAll
}

// layout shares out the rows of the screen. It is called after anything
// that could change the plan: the terminal's size, the theme, and the
// number of rows the draft takes.
func (c *chat) layout() {
	most := frame.Layout(c.width, c.height, 8, 0, c.th.BarFilled).InputText
	c.input.SetSize(c.width, most, frame.BoxedAt(c.width, c.height))
	rows := frame.Layout(c.width, c.height, c.input.TextRows(), c.pal.Want(c.width), c.th.BarFilled)
	// The window hears of every size, also a width that leaves the rows as
	// they were. It does nothing when told the size it has.
	c.win.Resize(max(c.width, 1), rows.Body)
	if rows == c.rows {
		return
	}
	c.rows = rows
	c.dirty = dirtyAll
}

// inputChanged is called after anything that changed the draft. The list of
// commands follows the draft: it opens on a line that starts with a slash,
// narrows as the word is typed, and closes when the draft is something else.
func (c *chat) inputChanged() {
	c.dirty |= dirtyInput
	was := c.pal.Open()
	open := c.pal.Set(c.cmds, c.input.Value())
	if was || open {
		c.dirty |= dirtyPalette
	}
	if was != open {
		c.dirty |= dirtyHint
	}
	c.input.SetGhost(c.ghost())
	c.layout()
}

// ghost is what is drawn dim after a command that is being typed: the rest
// of the chosen command's name and what it takes, or what it takes alone
// once the name and a space are there.
func (c *chat) ghost() string {
	draft := c.input.Value()
	if spec, ok := c.pal.Selected(); ok {
		return commands.Ghost(draft, spec)
	}
	// The list is closed once a space is typed. It is also closed after esc,
	// and then nothing is offered.
	if !strings.HasPrefix(draft, "/") || strings.IndexFunc(draft, unicode.IsSpace) < 0 {
		return ""
	}
	line := commands.Parse(draft)
	if line.Kind != commands.LineCommand {
		return ""
	}
	spec, ok := c.cmds.Lookup(line.Name)
	if !ok {
		return ""
	}
	return commands.Ghost(draft, spec)
}

// choose moves the choice in the list of commands.
func (c *chat) choose(delta int) {
	c.pal.Move(delta)
	c.input.SetGhost(c.ghost())
	c.dirty |= dirtyPalette | dirtyInput
}

// print puts what a command has to say into the conversation, after the
// newest item there is, and returns the key of its block.
func (c *chat) print(title string, lines []wire.Line) scrollback.Key {
	c.nextLocal++
	c.locals = append(c.locals, local{id: c.nextLocal, after: c.tr.Newest(), title: title, lines: lines})
	c.dirty |= dirtyBody
	return scrollback.Key{Kind: blockLocal, ID: c.nextLocal}
}

// show brings a block near the end of the conversation into view. The view
// goes to the newest line; when the block and what is under it are more
// than the view holds, it is moved up so that the block's first row is the
// first in view, where reading starts.
func (c *chat) show(k scrollback.Key, now time.Time) {
	// A stage has no thread to put it in: it is read on a sheet.
	if c.stage != nil {
		c.printed(k.ID)
		return
	}
	c.sync()
	c.win.Follow()
	c.dirty |= dirtyBody
	at, ok := c.list.Index(k)
	if !ok {
		return
	}
	below := len(c.tail(now))
	for i := at; i < c.list.Len(); i++ {
		below += len(c.list.Lines(i, c.width))
		if i > at {
			below += c.list.Gap(i)
		}
	}
	if over := below - c.rows.Body; over > 0 {
		c.win.Scroll(&c.list, -over)
	}
}

// clearInput empties the box.
func (c *chat) clearInput() {
	c.input.Reset()
	c.inputChanged()
}

// setNote shows a line in place of the key hints, or with "" the hints
// again. Where there is no hint row the note takes the bar's. A note can
// quote the server, so it is made safe to draw.
func (c *chat) setNote(note string) { c.say(note, "") }

// say shows a note in a tone: "" is the tone of a warning, which is what
// most notes are. toneDone is for one that says something was done.
func (c *chat) say(note, tone string) {
	note = safe.Line(note, 512, false)
	if note == c.note && tone == c.noteTone {
		return
	}
	c.note, c.noteTone = note, tone
	c.dirty |= dirtyHint | dirtyBar
}

// apply folds one update from the session into the conversation, and counts
// the turn it ended, if it ended one.
func (c *chat) apply(u client.Update, now time.Time) {
	replying := c.tr.Live().Typing
	c.fold(u, now)
	if replying && !c.tr.Live().Typing {
		c.turns++
	}
}

// fold is what one update does to the conversation.
func (c *chat) fold(u client.Update, now time.Time) {
	c.dirty |= dirtyTail | dirtyBody
	switch u.Kind {
	case client.UpdateEvents:
		c.tr.Events(u.Events)
		c.start = c.start || u.Start
	case client.UpdateOlder:
		c.loadingOlder = false
		if u.Err != nil {
			c.setNote("could not load earlier messages: " + u.Err.Error())
			return
		}
		c.tr.Events(u.Events)
		c.start = u.Start
	case client.UpdateDelta:
		c.tr.Delta(u.Delta, now.UnixMilli())
		if u.Delta.Type == core.DeltaToolCall {
			c.toolSince = now
		}
	case client.UpdateAck:
		c.tr.Acked(u.Text, u.Seq, now.UnixMilli())
	case client.UpdateRefused:
		c.tr.Refused(u.Text)
		// A refusal that names no message has none to give back.
		if u.Text != "" {
			c.giveBack(u.Text)
		}
		c.setNote("not sent: " + u.Reason)
	case client.UpdateSynced:
		if lost := c.tr.DropPending(); len(lost) > 0 {
			c.giveBack(strings.Join(lost, "\n"))
			c.setNote("not sent: the connection dropped first")
		}
	case client.UpdateState:
		// Remember the opening history without putting it on the stage.
		// Reconnecting must not hide replies from this visit.
		if u.State == client.StateLive && !c.primed {
			c.openedSeq = c.tr.Newest()
			c.primed = true
			c.perform(true)
		}
		c.setState(u.State, u.Err, now)
	}
}

// setState records the state of the connection. The bar says what it is;
// why it went down is a note.
//
// The turn in flight is known from the stream alone, so it goes with the
// connection: nothing says any more that a reply is being written or a tool
// is running. What the turn committed is in the log, which is read again on
// the way back, and a turn that is still going then is described again by
// the new stream. Without this a reply that ended while the connection was
// down would stay half written under the log's account of it for good.
func (c *chat) setState(state client.State, err error, now time.Time) {
	if c.state == client.StateLive && state != client.StateLive {
		c.tr.Delta(core.Delta{Type: core.DeltaTyping}, now.UnixMilli())
	}
	if state == client.StateLive && c.state != client.StateLive &&
		(strings.HasSuffix(c.note, "not connected") || strings.HasPrefix(c.note, "disconnected")) {
		c.setNote("") // no longer true
	}
	c.state, c.stateErr = state, ""
	if err != nil {
		c.stateErr = safe.Line(err.Error(), 256, false)
	}
	if state == client.StateDown && c.stateErr != "" {
		c.setNote("disconnected: " + c.stateErr)
	}
}

// giveBack returns a message that did not arrive to the input box, ahead of
// whatever has been typed since.
func (c *chat) giveBack(text string) {
	if rest := c.input.Value(); strings.TrimSpace(rest) != "" {
		text += "\n" + rest
	}
	c.input.SetValue(text)
	c.inputChanged()
}

// key handles a keypress in the conversation. What it does not use is
// typing, and goes to the input box. The note of the key before is gone by
// now: the model clears it for every key.
//
// When the key was enter on a command, the command is returned for the
// model to run: what a command does reaches beyond the conversation.
func (c *chat) key(msg tea.KeyPressMsg, now time.Time) (tea.Cmd, *commands.Line) {
	// While the list of commands is open, four keys are its own.
	if c.pal.Open() {
		switch msg.String() {
		case "up":
			c.choose(-1)
			return nil, nil
		case "down":
			c.choose(1)
			return nil, nil
		case "tab":
			if spec, ok := c.pal.Selected(); ok {
				c.input.SetValue(commands.Complete(spec))
				c.inputChanged()
			}
			return nil, nil
		case "esc":
			c.pal.Dismiss()
			c.dirty |= dirtyPalette | dirtyHint
			c.inputChanged()
			return nil, nil
		}
	}
	switch msg.String() {
	case "enter":
		// A backslash before the cursor makes enter a new line.
		text, done := c.input.Enter()
		c.inputChanged()
		if done {
			return nil, c.submit(text)
		}
	case "esc":
		if c.closeSheet() {
			break
		}
		// Only while there is a reply to stop: an interrupt with nothing in
		// flight does nothing, and esc should not feel like it did.
		if c.tr.Live().Typing {
			c.sess.Interrupt()
		}
		// The avatar stops talking with it: what there was to say is all
		// there to read.
		if c.stage != nil {
			c.stage.Skip()
		}
	case "ctrl+o":
		c.unfold()
	case "pgup":
		c.scroll(-c.page())
	case "pgdown":
		c.scroll(c.page())
	case "up", "down":
		// With one row in the box there is nowhere for the cursor to go, so
		// the arrows move the conversation. Many terminals send them for the
		// mouse wheel, too.
		if c.input.TextRows() > 1 {
			return c.typing(msg, now), nil
		}
		// Never past rows that were not shown: a transcript of a row or two
		// moves by less.
		step := min(scrollStep, max(c.rows.Body, 1))
		if msg.String() == "up" {
			step = -step
		}
		c.scroll(step)
	case "tab":
		// Nothing to complete.
	default:
		return c.typing(msg, now), nil
	}
	return nil, nil
}

// unfold shows all of every tool call and its output, or folds them again.
func (c *chat) unfold() {
	// A stage shows no tool calls to unfold. The turn is read on a sheet,
	// with everything in it.
	if c.stage != nil {
		c.readTurn()
		return
	}
	c.expanded = !c.expanded
	c.restyle()
}

// top goes to the oldest message that has been loaded, and asks for the
// ones before it when there are any.
func (c *chat) top() {
	if c.stage != nil {
		c.scroll(-len(c.tr.Items()) - cachedLines)
		return
	}
	c.sync()
	c.win.Top(&c.list)
	c.dirty |= dirtyBody
	if !c.start && !c.loadingOlder {
		c.loadingOlder = true
		c.sess.Older()
	}
}

// page is how far page up and page down move the conversation: the rows of
// the transcript less one, so that a row of what was in view stays, and one
// row where the transcript has no more than that.
func (c *chat) page() int { return max(c.rows.Body-1, 1) }

// typing hands a key or pasted text to the input box, and tells the contact
// that its human is writing. A command is not written to the contact, so
// nothing is said about a draft that starts with one slash.
func (c *chat) typing(msg tea.Msg, now time.Time) tea.Cmd {
	before := c.input.Value()
	cmd, changed := c.input.Update(msg)
	if changed {
		c.inputChanged()
		// The avatar looks down at what is being written to it.
		if c.stage != nil {
			c.stage.Glance(0, 1)
		}
	}
	after := c.input.Value()
	if after == before || strings.TrimSpace(after) == "" || now.Sub(c.lastTyping) < typingEvery {
		return cmd
	}
	if commands.Parse(after).Kind != commands.LineMessage {
		return cmd
	}
	// A pending reply waits a moment longer while its human is still
	// writing, as a person would.
	c.lastTyping = now
	c.sess.Typing()
	return cmd
}

// submit takes the draft as enter left it, trimmed of trailing space. It is
// the one place a finished draft goes: a message is sent, a command is
// handed back to be run, and a draft that is neither is refused with a note.
func (c *chat) submit(text string) *commands.Line {
	line := commands.Parse(text)
	switch line.Kind {
	case commands.LineCommand:
		return c.command(line)
	case commands.LineRefused:
		c.setNote(noteRefused)
	case commands.LineMessage:
		c.send(line.Message)
	}
	return nil
}

// command decides what enter means on a command line. The name as typed is
// run when it is a command's. Otherwise, with the list open, the draft is
// completed to the chosen command, which is then run unless it needs an
// argument first. A slash alone is only completed: nothing was asked for
// yet. Any other name is returned as it is, for the model to say there is
// no such command.
func (c *chat) command(line commands.Line) *commands.Line {
	if _, typed := c.cmds.Lookup(line.Name); typed {
		return &line
	}
	spec, ok := c.pal.Selected()
	if !ok {
		return &line
	}
	c.input.SetValue(commands.Complete(spec))
	c.inputChanged()
	if line.Name == "" || spec.NeedsArg {
		return nil
	}
	return &commands.Line{Kind: commands.LineCommand, Name: spec.Name}
}

// send sends a message to the contact.
func (c *chat) send(text string) {
	text = strings.TrimSpace(text)
	switch {
	case text == "":
	case len(text) > maxMessageBytes:
		c.setNote(fmt.Sprintf("not sent: the message is %d bytes and the limit is %d", len(text), maxMessageBytes))
	case c.state != client.StateLive:
		c.setNote("not sent: not connected")
	default:
		c.tr.Sent(text)
		c.sess.Say(text)
		c.clearInput()
		c.win.Follow()
		// The avatar stops talking and listens, as a person would.
		if c.stage != nil {
			c.stage.Skip()
			c.closeSheet()
		}
		c.dirty |= dirtyBody
	}
}

// scroll moves the conversation, and asks for earlier history on reaching
// the top of what has been loaded.
func (c *chat) scroll(lines int) {
	if lines == 0 {
		return
	}
	// A stage has nothing to scroll but the sheet over it.
	if c.stage != nil {
		if c.sheet != nil {
			c.sheet.top, c.sheet.follow = c.sheet.top+lines, false
			c.dirty |= dirtyBody
		}
		return
	}
	c.sync()
	top := c.win.Scroll(&c.list, lines)
	c.dirty |= dirtyBody
	if top && !c.start && !c.loadingOlder {
		c.loadingOlder = true
		c.sess.Older()
	}
}

// sync brings the block list up to date with the transcript. It draws
// nothing, and does nothing while the settled part of the conversation is
// what the list was built from.
func (c *chat) sync() {
	if c.listBuilt && c.tr.SettledRev() == c.listRev && c.start == c.listStart && len(c.locals) == c.listLocals {
		return
	}
	c.listBuilt, c.listRev, c.listStart, c.listLocals = true, c.tr.SettledRev(), c.start, len(c.locals)
	c.rebuild()
	c.waiting = c.tr.Waiting()
	c.win.Sync(&c.list)
}

// rebuild makes the block list from the transcript: one block for each
// item, a rule above the first thing ever said and one where a day begins,
// what commands printed, and the messages still on their way. It is one
// pass over the items. Every block has a blank row above it but a tool's
// result, which belongs to the call above it.
//
// What a command printed stands before the first item that is newer than
// the newest there was when it was made, and so before that item's day rule
// and before every message still on its way. Nothing that arrives later
// moves it.
func (c *chat) rebuild() {
	c.rebuilds++
	l := &c.list
	l.items, l.locals = c.tr.Items(), c.locals
	l.blocks = l.blocks[:0]
	clear(l.index)
	if c.start && len(l.items) > 0 {
		l.add(block{key: scrollback.Key{Kind: blockBeginning}, gap: 1})
	}
	// dayFrom and dayTo bound the day of the item before, in unix ms, so
	// that the calendar is asked only when an item falls outside it.
	var dayFrom, dayTo int64
	call := false // the block before is a tool call
	printed := 0  // the local blocks placed so far
	locals := func(before uint64) {
		for ; printed < len(l.locals) && l.locals[printed].after < before; printed++ {
			l.add(block{key: scrollback.Key{Kind: blockLocal, ID: l.locals[printed].id}, gap: 1, item: printed})
			call = false
		}
	}
	for i := range l.items {
		it := &l.items[i]
		// An item with no sequence number, a failure, stands with the item
		// it follows.
		if it.Seq != 0 {
			locals(it.Seq)
		}
		if it.TS != 0 && (it.TS < dayFrom || it.TS >= dayTo) {
			y, m, d := time.UnixMilli(it.TS).In(c.loc).Date()
			from := time.Date(y, m, d, 0, 0, 0, 0, c.loc)
			dayFrom, dayTo = from.UnixMilli(), from.AddDate(0, 0, 1).UnixMilli()
			day := uint64(y)*10000 + uint64(m)*100 + uint64(d) //nolint:gosec // a calendar date is never negative
			l.add(block{key: scrollback.Key{Kind: blockDay, ID: day}, gap: 1, ts: it.TS})
			call = false
		}
		b := block{key: scrollback.Key{Kind: blockItem, ID: it.Seq}, rev: it.Rev, gap: 1, item: i, ts: it.TS}
		if it.Kind == transcript.Failure {
			b.key = scrollback.Key{Kind: blockFailure, ID: it.After}
		}
		if it.Kind == transcript.ToolResult && call {
			b.gap = 0
		}
		l.add(b)
		call = it.Kind == transcript.ToolCall
	}
	locals(math.MaxUint64)
	c.pending = renumber(c.pending, c.tr.Pending(), &c.nextSent)
	for _, p := range c.pending {
		h := fnv.New64a()
		_, _ = h.Write([]byte(p.text)) // a hash's Write never fails
		l.add(block{key: scrollback.Key{Kind: blockPending, ID: p.id}, rev: h.Sum64(), gap: 1, text: p.text})
	}
}

// renumber matches the messages on their way against the numbers they were
// given when they were sent. A message that is gone loses its number, one
// that is new takes the next, and the rest keep theirs.
func renumber(old []sent, texts []string, next *uint64) []sent {
	out := make([]sent, 0, len(texts))
	for _, text := range texts {
		for len(old) > 0 && old[0].text != text {
			old = old[1:]
		}
		if len(old) > 0 {
			out, old = append(out, old[0]), old[1:]
			continue
		}
		*next++
		out = append(out, sent{id: *next, text: text})
	}
	return out
}

// tail draws the turn in flight: what goes under the last block.
func (c *chat) tail(now time.Time) []string {
	live := c.tr.Live()
	var running time.Duration
	if live.Running != "" {
		running = max(now.Sub(c.toolSince), 0)
	}
	lines := c.list.rd.Live(live, running)
	if len(lines) == 0 {
		return nil
	}
	// The running row belongs directly under its tool call. Anything else
	// stands apart from the block above it.
	if strings.TrimSpace(live.Text) == "" && live.Starting == "" {
		return lines
	}
	return append([]string{""}, lines...)
}

// draw returns the screen. Only the regions that are stale are drawn again,
// and when none is, the view is the one returned last time.
func (c *chat) draw(now time.Time) tea.View {
	if c.dirty == 0 {
		return c.view
	}
	dirty := c.dirty
	c.dirty = 0
	// The transcript first: the header says which days are in view.
	if dirty&(dirtyTail|dirtyBody) != 0 {
		c.sync()
		if c.stage != nil {
			c.reg.Body = c.stageRows(now, dirty&dirtyTail != 0)
		} else {
			if dirty&dirtyTail != 0 {
				c.win.SetTail(c.tail(now))
			}
			c.reg.Body = c.win.View(&c.list)
		}
	}
	if info := c.headerInfo(); dirty&dirtyHeader != 0 || info != c.info {
		c.info = info
		c.reg.Header = frame.Header(c.th, info, c.width)
		c.reg.HeaderRule = c.th.Rule(c.width, "")
	}
	if dirty&dirtyPalette != 0 {
		c.reg.Palette = c.pal.Lines(c.th, c.width, c.rows.Palette)
	}
	if dirty&dirtyHint != 0 {
		c.reg.Hint = c.hints()
	}
	if snap := c.snapshot(now); dirty&dirtyBar != 0 || snap != c.snap {
		c.snap = snap
		c.reg.BarRule, c.reg.Bar = c.barRow(snap)
	}
	if dirty&dirtyInput != 0 {
		c.reg.Input = c.input.Lines()
	}
	v := tea.NewView(frame.Compose(c.rows, c.reg))
	v.AltScreen = true
	v.WindowTitle = "contactd: " + c.label
	if c.rows.Input > 0 {
		x, y := c.input.Cursor()
		v.Cursor = tea.NewCursor(x, c.rows.InputTop()+y)
		v.Cursor.Blink = false
	}
	c.view = v
	return v
}
