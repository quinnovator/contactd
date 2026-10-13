package tui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/quinnovator/contactd/internal/transcript"
	"github.com/quinnovator/contactd/internal/tui/avatar"
	"github.com/quinnovator/contactd/internal/tui/canvas"
	"github.com/quinnovator/contactd/internal/tui/commands"
	"github.com/quinnovator/contactd/internal/tui/draw"
	"github.com/quinnovator/contactd/internal/tui/mods/wire"
	"github.com/quinnovator/contactd/internal/tui/safe"
	"github.com/quinnovator/contactd/internal/tui/script"
	"github.com/quinnovator/contactd/internal/tui/sound"
	"github.com/quinnovator/contactd/internal/tui/stage"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
)

// The avatar. Where the terminal can draw one, a conversation is not a
// thread of messages but a stage: an avatar says the contact's reply aloud,
// the words appear under it as they are said, and what the reply shows (a
// picture, a block of code, a table) stands on the stage where the reply
// asked for it. There is nothing to scroll. The stage holds the turn in
// flight, or the last one from this visit. Opening it leaves the avatar
// idle; the conversation's history is still available on a sheet.
//
// The stage takes the transcript's rows and nothing else. The header, the
// status bar, the input, the list of commands and the hint line are drawn as
// they are for a thread, and so is everything a command opens.
//
// What a command prints, and the turn as a thread would show it, have no
// place on a stage. They are read on a sheet, which lies over the stage
// until it is closed.

// The avatar's choices, as the settings name them.
const (
	avatarAuto = "auto"
	avatarOn   = "on"
	avatarOff  = "off"

	cellsQuadrant = "quadrant"
	cellsOctant   = "octant"
)

const (
	// frameEvery is how often the stage is drawn while the avatar speaks,
	// and idleEvery while it only breathes.
	frameEvery = time.Second / 60
	idleEvery  = time.Second / 30
	// maxSaid is the most of one reply, in bytes, that is read for the
	// avatar to say.
	maxSaid = 32 << 10
	// aboveMargin is the cells kept clear at the end of the line over the
	// stage, for the word that says a message is on its way.
	aboveMargin = 16
)

// The backgrounds the avatar is drawn against until the terminal has said
// what its own is.
const (
	darkPaper  = 0x121214
	lightPaper = 0xF6F3EC
)

// Avatar is how the avatar is set when the interface starts. The zero value
// is no avatar: a conversation is a thread.
type Avatar struct {
	// Show is "on", "off", or "auto" for an avatar where the terminal can
	// draw one and has said what its background is. "" is "off".
	Show string
	// Look names what the avatar wears, and Voice what it sounds like. ""
	// is the first of each.
	Look, Voice string
	// Rate is its pace in characters a second. Zero is the stage's own.
	Rate float64
	// Cells is how finely it is drawn: "quadrant", "octant", or "auto" and
	// "" for octants in a terminal known to have them.
	Cells string
	// Mute starts it silent.
	Mute bool
	// Synth is its voice, already sounding through a device. Nil is an
	// avatar that makes no sound.
	Synth *sound.Synth
}

// avatarState is the avatar as the session has it: the settings, and what
// the terminal answered about its background.
type avatarState struct {
	show        string
	look, voice int
	rate        float64
	cells       string
	mute        bool
	// paper is the background the terminal reported, and known says it has.
	paper canvas.Color
	dark  bool
	known bool
}

// newAvatarState reads the settings. A name that is no look or no voice is
// the first one: the settings are checked where they are read.
func newAvatarState(a Avatar) avatarState {
	st := avatarState{show: a.Show, rate: a.Rate, cells: a.Cells, mute: a.Mute}
	if st.show == "" {
		st.show = avatarOff
	}
	st.look = max(slices.IndexFunc(avatar.Palettes(), func(p avatar.Palette) bool { return p.Name == a.Look }), 0)
	st.voice = max(slices.IndexFunc(sound.Timbres(), func(t sound.Timbre) bool { return t.Name == a.Voice }), 0)
	return st
}

// Looks returns the names of what an avatar can wear, and Voices of what it
// can sound like, for whoever reads the settings.
func Looks() []string {
	looks := avatar.Palettes()
	names := make([]string, 0, len(looks))
	for _, p := range looks {
		names = append(names, p.Name)
	}
	return names
}

// Voices returns the names of the avatar's voices.
func Voices() []string {
	voices := sound.Timbres()
	names := make([]string, 0, len(voices))
	for _, t := range voices {
		names = append(names, t.Name)
	}
	return names
}

// staged reports whether a conversation is shown by the avatar right now,
// and says why not when it is not.
func (m *model) staged() (on bool, why string) {
	switch {
	case m.av.show == avatarOff:
		return false, "switched off"
	case m.caps.Glyphs != termcaps.GlyphsUnicode:
		return false, "it is drawn with block characters, and the glyphs are ASCII"
	case m.caps.Color < termcaps.Color256:
		return false, "it needs 256 colours or more"
	case m.av.show == avatarAuto && m.caps.Background == termcaps.BgUnknown:
		return false, "the terminal has not said what its background is; /theme or /avatar on settles it"
	}
	return true, ""
}

// paper is the background the avatar is drawn against: what the terminal
// reported, when that agrees with the background the client draws for, and a
// usual one otherwise.
func (m *model) paper() (canvas.Color, bool) {
	dark := m.caps.Background != termcaps.BgLight
	if m.av.known && m.av.dark == dark {
		return m.av.paper, dark
	}
	if dark {
		return canvas.Hex(darkPaper), true
	}
	return canvas.Hex(lightPaper), false
}

// grid is how finely the avatar is drawn. Octants are new enough that most
// fonts lack them, so without a setting they are used only in a terminal
// that draws block characters itself and is known to have them.
func (m *model) grid() canvas.Grid {
	switch m.av.cells {
	case cellsOctant:
		return canvas.Octants
	case cellsQuadrant:
		return canvas.Quadrants
	}
	if strings.HasPrefix(m.caps.Terminal, "ghostty") {
		return canvas.Octants
	}
	return canvas.Quadrants
}

// restage gives the open conversation its stage, or takes it away, as the
// settings and the terminal have it now. A stage that is new has nothing to
// say that was not on the screen already: what the conversation holds is
// shown as said.
func (m *model) restage() {
	c := m.chat
	if c == nil {
		return
	}
	on, _ := m.staged()
	paper, dark := m.paper()
	switch {
	case on && c.stage == nil:
		c.stage = stage.New(stage.Options{
			Theme: c.th, Rate: m.av.rate, Look: m.av.look, Grid: m.grid(),
			Synth: m.opt.Avatar.Synth, Muted: m.av.mute,
			Seed:  uint64(m.opt.Now().UnixNano()),
			Paper: paper, Dark: dark,
		})
		c.perform(true)
		c.dirty = dirtyAll
	case !on && c.stage != nil:
		c.stage.Hush()
		c.stage, c.sheet, c.cards = nil, nil, nil
		c.win.InvalidateAll()
		c.dirty = dirtyAll
	case on:
		c.stage.SetPaper(paper, dark)
		c.stage.SetGrid(m.grid())
		c.dirty |= dirtyBody
	}
}

// frameMsg says it is time for the next frame of the avatar. sess is the
// conversation whose stage it is for.
type frameMsg struct{ sess Session }

// frames asks for the next frame of the open conversation's stage, unless
// one is on its way. The stage is drawn only while it is on the screen:
// under another screen the avatar holds still, and its voice with it.
func (m *model) frames() tea.Cmd {
	c := m.chat
	if c == nil || c.stage == nil || c.framing {
		return nil
	}
	if m.screen != screenChat {
		c.stage.Hush()
		return nil
	}
	c.framing = true
	every := idleEvery
	if c.stage.Speaking() {
		every = frameEvery
	}
	return m.after(every, frameMsg{sess: c.sess})
}

// onFrame advances the stage to now and has it drawn again.
func (m *model) onFrame(msg frameMsg) {
	c := m.chat
	if c == nil || msg.sess != c.sess {
		return
	}
	c.framing = false
	if c.stage == nil {
		return
	}
	c.stage.Tick(m.opt.Now())
	if c.sheet == nil {
		c.dirty |= dirtyBody
	}
}

// perform hands the stage the turn as the transcript has it now. said marks
// words already seen, such as when switching from a thread to the avatar.
func (c *chat) perform(said bool) {
	if c.stage != nil {
		c.stage.Perform(c.turn(), said)
	}
}

// turnStart is the index of the first item of the newest turn: the one after
// the newest thing that was said to the contact, by its human or by the
// runtime.
func turnStart(items []transcript.Item) int {
	for i, it := range slices.Backward(items) {
		if it.Kind == transcript.User || it.Kind == transcript.Notice {
			return i + 1
		}
	}
	return 0
}

// stageStart leaves the opening history off the stage, even when a reply
// arriving during this visit continues the same turn. The transcript itself
// stays whole for the thread and the turn's sheet.
func (c *chat) stageStart() int {
	items := c.tr.Items()
	if !c.primed {
		return len(items)
	}
	from := turnStart(items)
	for i := len(items) - 1; i >= from; i-- {
		if items[i].Seq != 0 && items[i].Seq <= c.openedSeq {
			return i + 1
		}
	}
	return from
}

// turn is the newest turn as the avatar performs it: what the contact has
// said in it, in the order it was said, and what that shows. What it said
// before a tool call and after it are one speech. The reply in flight comes
// last, and is read as far as it can no longer change.
func (c *chat) turn() stage.Turn {
	if !c.primed {
		return stage.Turn{Over: true}
	}
	items, live := c.tr.Items(), c.tr.Live()
	from := c.stageStart()
	t := stage.Turn{Over: !live.Typing, Busy: live.Typing || len(c.tr.Pending()) > 0}
	if start := turnStart(items); start > 0 {
		t.ID = items[start-1].Seq
	}
	var text strings.Builder
	runes := 0
	say := func(seq uint64, ts int64, src string, whole bool) {
		sc := script.Read(c.sayable(src), whole, draw.MaxImages)
		if sc.Text != "" {
			if runes > 0 {
				text.WriteString("\n\n")
				runes += 2
			}
			for _, sh := range sc.Shows {
				t.Shows = append(t.Shows, c.exhibit(len(t.Shows), runes+sh.At, seq, ts, sh))
			}
			text.WriteString(sc.Text)
			runes += utf8.RuneCountInString(sc.Text)
			return
		}
		for _, sh := range sc.Shows {
			t.Shows = append(t.Shows, c.exhibit(len(t.Shows), runes, seq, ts, sh))
		}
	}
	for i := from; i < len(items); i++ {
		switch it := &items[i]; it.Kind {
		case transcript.Agent:
			say(it.Seq, it.TS, it.Text, true)
		case transcript.ToolCall:
			say(it.Seq, it.TS, it.Lead, true)
		case transcript.User, transcript.ToolResult, transcript.Notice, transcript.Checkpoint, transcript.Failure:
		}
	}
	say(0, 0, live.Text, false)
	t.Text = text.String()
	return t
}

// sayable makes a reply safe to draw and no longer than the avatar reads.
func (c *chat) sayable(src string) string {
	if len(src) > maxSaid {
		src = strings.ToValidUTF8(src[:maxSaid], "")
	}
	return safe.Clean(src, c.th.Caps.Width != termcaps.WidthGrapheme)
}

// exhibit is one thing a reply shows, as the stage is handed it. i counts
// the turn's exhibits and at is where in the turn's words it stands. seq is
// the item that holds it, zero while the reply is still being written: a
// picture is fetched, and a mod asked, once the reply is in the log.
func (c *chat) exhibit(i, at int, seq uint64, ts int64, sh script.Show) stage.Exhibit {
	ex := stage.Exhibit{Key: strconv.Itoa(i), Rev: seq, At: at}
	switch sh.Kind {
	case script.Image:
		ex.Where, _ = stage.Place(sh.Place)
		ref := draw.ImageRef{Seq: seq, N: sh.N, Path: sh.Path, Alt: sh.Alt, TS: ts}
		ex.Draw = func(cols, _ int) []string { return c.card(cols).Image(ref) }
	case script.Code:
		ex.Draw = func(cols, rows int) []string {
			rd := c.card(cols)
			return rd.Clip(rd.Code(seq, sh.N, sh.Lang, sh.Text), rows)
		}
	case script.Table:
		ex.Draw = func(cols, rows int) []string {
			rd := c.card(cols)
			return rd.Clip(rd.Markdown(sh.Text), rows)
		}
	}
	return ex
}

// card is the renderer that draws an exhibit a given number of cells wide.
// One is kept for each width that was asked for, until the look changes.
func (c *chat) card(cols int) *draw.Renderer {
	if rd := c.cards[cols]; rd != nil {
		return rd
	}
	o := c.drawOptions()
	o.Width, o.Expanded = max(cols, 1), true
	rd := c.list.rd.With(o)
	if c.cards == nil {
		c.cards = make(map[int]*draw.Renderer)
	}
	c.cards[cols] = rd
	return rd
}

// above is the line over the stage: the last thing that was said to the
// contact, which is what it is answering. A message that is still on its way
// says so.
func (c *chat) above() string {
	one := func(text string) string {
		return c.th.Truncate(safe.Line(text, 4*c.width, false), max(c.width-aboveMargin, 1))
	}
	if pending := c.tr.Pending(); len(pending) > 0 {
		return first(c.list.rd.Pending(one(pending[len(pending)-1])))
	}
	items := c.tr.Items()
	from := turnStart(items)
	if from == 0 {
		return ""
	}
	it := items[from-1]
	if !c.primed || it.Seq <= c.openedSeq {
		return ""
	}
	it.Text = one(it.Text)
	return first(c.list.rd.Item(&it))
}

// first is the first of some drawn lines, or "".
func first(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return lines[0]
}

// below is the lines between the avatar and its words: the tool the turn is
// busy with, and how the turn ended when it did not end with a reply.
func (c *chat) below(now time.Time) []string {
	var lines []string
	rd, live := c.list.rd, c.tr.Live()
	var running time.Duration
	if live.Running != "" {
		running = max(now.Sub(c.toolSince), 0)
	}
	if doing := rd.Doing(live, running); doing != "" {
		lines = append(lines, doing)
	}
	items := c.tr.Items()
	from := c.stageStart()
	for i := len(items) - 1; i >= from && len(lines) == 0; i-- {
		switch it := items[i]; {
		case it.Kind == transcript.Failure:
			it.Text = safe.Line(it.Text, 4*c.width, false)
			lines = append(lines, first(rd.Item(&it)))
		case it.Kind == transcript.Agent && it.Interrupted:
			lines = append(lines, rd.Stopped())
		}
	}
	return lines
}

// stageRows draws the transcript's rows for a conversation that is shown by
// the avatar: the stage, or the sheet that lies over it. told says the
// transcript may have changed since the stage was last handed the turn.
func (c *chat) stageRows(now time.Time, told bool) []string {
	if told {
		c.perform(!c.primed)
	}
	if c.sheet != nil {
		return c.sheetRows(now)
	}
	c.stage.Resize(c.width, c.rows.Body)
	c.stage.SetAround(c.above(), c.below(now))
	return c.stage.View()
}

// sheet is something to read that lies over the stage: what a command
// printed, or the turn as a thread would show it. It is the one thing on a
// stage that scrolls.
type sheet struct {
	title string
	lines []string
	// top is the first line in view, and follow keeps the last one there.
	top    int
	follow bool
	// turn says the sheet shows the turn; otherwise it shows what commands
	// printed, from the one numbered from on.
	turn bool
	from uint64
	// What the lines were drawn from.
	rev    uint64
	locals int
	width  int
	drawn  bool
}

// sheetClose is what a sheet's title says about leaving it.
const sheetClose = "esc closes"

// printed opens the sheet on what commands printed, from the block with the
// given number on. The sheet is called what that block is.
func (c *chat) printed(from uint64) {
	c.sheet = &sheet{title: "printed", from: from}
	for _, l := range c.locals {
		if l.id == from && l.title != "" {
			c.sheet.title = safe.Line(l.title, nameInNote, false)
		}
	}
	c.dirty |= dirtyBody
}

// readTurn opens the sheet on the turn, or closes it when it is open there.
func (c *chat) readTurn() {
	if c.sheet != nil && c.sheet.turn {
		c.sheet = nil
	} else {
		c.sheet = &sheet{title: "this turn", turn: true, follow: true}
	}
	c.dirty |= dirtyBody
}

// closeSheet takes the sheet away, and reports whether there was one.
func (c *chat) closeSheet() bool {
	if c.sheet == nil {
		return false
	}
	c.sheet = nil
	c.dirty |= dirtyBody
	return true
}

// sheetRows draws the sheet into the transcript's rows: a rule that names it
// and says how to leave, and under it as many of its lines as fit.
func (c *chat) sheetRows(now time.Time) []string {
	sh := c.sheet
	if rev := c.tr.Rev(); !sh.drawn || sh.rev != rev || sh.locals != len(c.locals) || sh.width != c.width {
		sh.rev, sh.locals, sh.width, sh.drawn = rev, len(c.locals), c.width, true
		sh.lines = c.sheetLines(now)
	}
	room := max(c.rows.Body-1, 0)
	most := max(len(sh.lines)-room, 0)
	if sh.follow {
		sh.top = most
	}
	sh.top = min(max(sh.top, 0), most)
	sh.follow = sh.turn && sh.top == most

	title := sh.title + " " + c.th.G.Mid + " " + sheetClose
	if most > 0 {
		title += " " + c.th.G.Mid + " " + c.th.G.UpDown + " scroll"
	}
	out := make([]string, 0, c.rows.Body)
	if c.rows.Body > 0 {
		out = append(out, c.th.Rule(c.width, title))
	}
	return append(out, sh.lines[sh.top:min(sh.top+room, len(sh.lines))]...)
}

// sheetLines draws what the sheet shows, as a thread would: the turn's
// items with everything unfolded, or the blocks commands printed.
func (c *chat) sheetLines(now time.Time) []string {
	var out []string
	block := func(lines []string, gap bool) {
		if len(lines) == 0 {
			return
		}
		if gap && len(out) > 0 {
			out = append(out, "")
		}
		out = append(out, lines...)
	}
	if !c.sheet.turn {
		for _, l := range c.locals {
			switch {
			case l.id == c.sheet.from:
				// The sheet's own title says what this one is.
				block(c.list.rd.Local("", l.lines), true)
			case l.id > c.sheet.from:
				block(c.list.rd.Local(l.title, l.lines), true)
			}
		}
		return out
	}
	o := c.drawOptions()
	o.Expanded = true
	rd := c.list.rd.With(o)
	items := c.tr.Items()
	call := false
	for i := max(turnStart(items)-1, 0); i < len(items); i++ {
		it := &items[i]
		// A tool's result belongs to the call above it.
		block(rd.Item(it), it.Kind != transcript.ToolResult || !call)
		call = it.Kind == transcript.ToolCall
	}
	block(c.tail(now), false)
	if len(out) == 0 {
		out = rd.Local("", []wire.Line{{{T: "nothing has been said yet", Tone: "dim"}}})
	}
	return out
}

// cmdAvatar is /avatar: whether a conversation is shown by the avatar, and
// what the avatar looks and sounds like, for this session. With no argument
// it says how things are.
func cmdAvatar(m *model, l commands.Line) tea.Cmd {
	c := m.chat
	arg := func(i int) string {
		if i < len(l.Args) {
			return strings.ToLower(l.Args[i])
		}
		return ""
	}
	keep := func(what, line string) tea.Cmd {
		m.restage()
		c.say(fmt.Sprintf("avatar: %s for this session; %s under [avatar] in chat.toml keeps it", what, line), toneDone)
		return nil
	}
	switch word := arg(0); {
	case len(l.Args) == 0:
		c.say("avatar: "+m.avatarLine(), toneDone)
	case len(l.Args) == 1 && (word == avatarOn || word == avatarOff || word == avatarAuto):
		m.av.show = word
		m.restage()
		what := word
		if on, why := m.staged(); !on && word != avatarOff {
			what += ", and a thread all the same: " + why
		}
		c.say(fmt.Sprintf("avatar: %s for this session; show = %q under [avatar] in chat.toml keeps it", what, word), toneDone)
	case len(l.Args) == 1 && (word == "mute" || word == "unmute"):
		m.av.mute = word == "mute"
		if c.stage != nil {
			c.stage.SetMuted(m.av.mute)
		}
		what := "sound on"
		switch {
		case m.av.mute:
			what = "muted"
		case m.opt.Avatar.Synth == nil:
			what = "sound on, but there is no audio device to play it"
		}
		return keep(what, "mute = "+strconv.FormatBool(m.av.mute))
	case len(l.Args) == 2 && word == "look":
		i := slices.Index(Looks(), arg(1))
		if i < 0 {
			return m.refuse("a look is one of " + strings.Join(Looks(), ", "))
		}
		m.av.look = i
		if c.stage != nil {
			c.stage.Wear(i)
		}
		return keep("look "+arg(1), fmt.Sprintf("look = %q", arg(1)))
	case len(l.Args) == 2 && word == "voice":
		i := slices.Index(Voices(), arg(1))
		if i < 0 {
			return m.refuse("a voice is one of " + strings.Join(Voices(), ", "))
		}
		m.av.voice = i
		if m.opt.Avatar.Synth != nil {
			m.opt.Avatar.Synth.Use(sound.Timbres()[i])
		}
		return keep("voice "+arg(1), fmt.Sprintf("voice = %q", arg(1)))
	case len(l.Args) == 2 && word == "rate":
		n, err := strconv.Atoi(arg(1))
		if err != nil || float64(n) < stage.MinRate || float64(n) > stage.MaxRate {
			return m.refuse(fmt.Sprintf("a rate is from %.0f to %.0f characters a second", stage.MinRate, stage.MaxRate))
		}
		m.av.rate = float64(n)
		if c.stage != nil {
			c.stage.SetRate(m.av.rate)
		}
		return keep("rate "+arg(1), "rate = "+arg(1))
	case len(l.Args) == 2 && word == "cells" && (arg(1) == avatarAuto || arg(1) == cellsQuadrant || arg(1) == cellsOctant):
		m.av.cells = arg(1)
		return keep("cells "+arg(1), fmt.Sprintf("cells = %q", arg(1)))
	default:
		return m.wrong(l)
	}
	return nil
}

// avatarLine says how the avatar is right now, for /avatar and /status.
func (m *model) avatarLine() string {
	on, why := m.staged()
	if !on {
		if m.av.show == avatarOff {
			return "off; a conversation is a thread"
		}
		return m.av.show + ", but off: " + why
	}
	cells := cellsQuadrant + "s"
	if m.grid() == canvas.Octants {
		cells = cellsOctant + "s"
	}
	voice := "voice " + Voices()[m.av.voice]
	switch {
	case m.av.mute:
		voice += ", muted"
	case m.opt.Avatar.Synth == nil:
		voice += ", silent: no audio device"
	}
	rate := m.av.rate
	if rate == 0 {
		rate = stage.DefaultRate
	}
	return fmt.Sprintf("%s; look %s, %s, %.0f characters a second, drawn in %s",
		m.av.show, Looks()[m.av.look], voice, rate, cells)
}
