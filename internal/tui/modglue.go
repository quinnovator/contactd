package tui

import (
	"cmp"
	"context"
	"errors"
	"hash/fnv"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/quinnovator/contactd/internal/transcript"
	"github.com/quinnovator/contactd/internal/tui/commands"
	"github.com/quinnovator/contactd/internal/tui/conf"
	"github.com/quinnovator/contactd/internal/tui/draw"
	"github.com/quinnovator/contactd/internal/tui/mods"
	"github.com/quinnovator/contactd/internal/tui/mods/wire"
	"github.com/quinnovator/contactd/internal/tui/modscreen"
	"github.com/quinnovator/contactd/internal/tui/safe"
	"github.com/quinnovator/contactd/internal/tui/scrollback"
	"github.com/quinnovator/contactd/internal/tui/statusline"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
)

// The mods in the client.
//
// A mod adds a segment to the status line, a command to the input box, or a
// way to draw one kind of item. The host runs them; this file is where what
// they add meets the interface. The host is slow by the measure of a key
// press: loading compiles a module, and a call waits for the mod. So nothing
// here calls the host from Update. Every call is a command of the program's,
// and its answer comes back as a message.
//
// A mod is sent what its manifest asks for and nothing else. What it sends
// back is text and the names of tones; the host makes it safe to draw.

// ModHost is the mod host, as far as the interface needs it. *mods.Host is
// the real one. Its methods wait: for a module to be compiled, or for a mod
// to answer. The interface calls them from commands, never from Update, but
// for Mods, which only lists and is also read once before the program starts.
type ModHost interface {
	Load(ctx context.Context) []mods.Info
	Mods() []mods.Info
	Needs() []mods.Grant
	Call(ctx context.Context, mod, op, name string, in mods.Input) (wire.Reply, error)
	SetEnabled(ctx context.Context, name string, on bool) (mods.Info, error)
	Install(ctx context.Context, dir string) (mods.Info, error)
	Update(ctx context.Context, name string) (mods.Info, error)
	Close(ctx context.Context) error
}

const (
	// maxRendered is how many answers of renderers a conversation keeps.
	maxRendered = 1024
	// modMessages is how many messages a mod's command is handed.
	modMessages = mods.MaxMessages
	// modsOffDefault says why there are no mods when nothing else does.
	modsOffDefault = "this client was started without them"
)

// modSeg is one segment a mod that is on adds to the status line.
type modSeg struct {
	mod, name string
	// id is the segment's Segment.ID in the status line.
	id string
	// refresh is what the segment is asked again on, as the manifest has it,
	// and every the interval of one that is asked on a timer.
	refresh string
	every   time.Duration
	man     mods.Manifest
}

// modCommand is a command of the input box that a mod runs: the mod, and the
// mod's own name for the command.
type modCommand struct{ mod, name string }

// modKind is a kind of item a mod draws, as its manifest spells it.
type modKind struct {
	mod, kind string
	man       mods.Manifest
}

// modVal is the latest answer of a segment. failed says the mod could not be
// asked, or answered with an error.
type modVal struct {
	text, short, tone string
	failed            bool
}

// modSeen is what the segments were last asked about: the conversation, its
// newest model call, how many of its turns have ended, and its state.
type modSeen struct {
	sess  Session
	call  uint64
	turns uint64
	state wire.State
}

// modGlue is what the interface keeps about the mods.
type modGlue struct {
	host ModHost
	// infos is every installed mod as the host last listed them, and needs
	// the grants the mods that are on hold between them.
	infos []mods.Info
	needs map[mods.Grant]bool

	// What the mods that are on add. owners are the mods with commands in
	// the registry, commands those commands by the name they are registered
	// under, and added what all of them were made from. kinds is by the
	// kind in lower case.
	owners   []string
	commands map[string]modCommand
	added    string
	segs     []modSeg
	kinds    map[string]modKind

	// busy holds the segments with a call on its way, and again those that
	// are to be asked once more when it is back. Both are by segment id, as
	// are vals, the latest answers, and last, when each came.
	busy, again map[string]bool
	vals        map[string]modVal
	last        map[string]time.Time
	// wake is when the timer for the segments asked on an interval goes off;
	// zero means none is set. gen numbers the timers.
	wake time.Time
	gen  uint64
	// all says every segment is to be asked again, and seen what they were
	// last asked about. term is what the mods were last told of the
	// terminal.
	all  bool
	seen modSeen
	term wire.Term

	// ws is where the client started, read for the mods that may know.
	// wsBusy says it is being read, and wsAgain that it is to be read once
	// more after that.
	ws              *wire.Workspace
	wsBusy, wsAgain bool

	// screen is the /mods screen while it is open.
	screen *modscreen.Model
}

// newModGlue takes the host the client was started with. The mods it lists
// are not loaded yet.
func newModGlue(host ModHost) modGlue {
	g := modGlue{
		host: host, needs: map[mods.Grant]bool{}, commands: map[string]modCommand{},
		busy: map[string]bool{}, again: map[string]bool{},
		vals: map[string]modVal{}, last: map[string]time.Time{},
	}
	if host != nil {
		g.infos = host.Mods()
	}
	return g
}

// modsOff says why there are no mods.
func (m *model) modsOff() string {
	if m.opt.ModsOff != "" {
		return m.opt.ModsOff
	}
	return modsOffDefault
}

// modsState is what the host says about the mods at one moment, and where
// the client started when a mod that is on may know.
type modsState struct {
	infos []mods.Info
	needs []mods.Grant
	ws    *wire.Workspace
}

// readMods asks the host what there is. It is called from commands.
func readMods(host ModHost, workDir string) modsState {
	st := modsState{infos: host.Mods(), needs: host.Needs()}
	if workDir != "" && slices.Contains(st.needs, mods.GrantWorkspace) {
		ws := mods.ReadWorkspace(workDir)
		st.ws = &ws
	}
	return st
}

// modAnswer is what a call to a mod came to. state is set when the call
// switched the mod off: what the host lists since.
type modAnswer struct {
	reply wire.Reply
	err   error
	state *modsState
}

// callMod sends one request to a mod. It waits for the mod, so it is called
// from commands.
func callMod(ctx context.Context, host ModHost, workDir, mod, op, name string, in mods.Input) modAnswer {
	a := modAnswer{}
	a.reply, a.err = host.Call(ctx, mod, op, name, in)
	if errors.Is(a.err, mods.ErrDisabled) {
		st := readMods(host, workDir)
		a.state = &st
	}
	return a
}

// modVerb is one thing done to a mod.
type modVerb uint8

const (
	modOn modVerb = iota + 1
	modOff
	modInstall
	modUpdate
)

// Messages of the mods.
type (
	// modsMsg is what the host lists: after it loaded the mods, or when it
	// was asked.
	modsMsg struct{ state modsState }
	// modSegmentMsg is a segment's answer. sess is the conversation it was
	// asked about.
	modSegmentMsg struct {
		modAnswer

		sess Session
		seg  modSeg
	}
	// modCommandMsg is the answer to a mod's command. draft is the line that
	// ran it.
	modCommandMsg struct {
		modAnswer

		sess       Session
		mod, draft string
	}
	// modRenderedMsg is a mod's drawing of one item.
	modRenderedMsg struct {
		modAnswer

		sess Session
		mod  string
		key  renderKey
		sum  uint64
	}
	// modActedMsg is a mod switched on or off, installed or updated. screen
	// says the /mods screen asked for it; otherwise a command did, in the
	// conversation of sess, with the line draft.
	modActedMsg struct {
		verb   modVerb
		arg    string
		screen bool
		sess   Session
		draft  string
		info   mods.Info
		err    error
		state  modsState
	}
	// modTickMsg says the moment the mods' timer was set for has come.
	modTickMsg struct{ gen uint64 }
	// workspaceMsg is where the client started, read again.
	workspaceMsg struct{ ws wire.Workspace }
)

// loadMods has the host load the mods that are switched on. It is slow the
// first time a module is seen, and runs beside the first frames.
func (m *model) loadMods() tea.Cmd {
	host, ctx, dir := m.mods.host, m.ctx, m.opt.WorkDir
	if host == nil {
		return nil
	}
	return func() tea.Msg {
		host.Load(ctx)
		return modsMsg{state: readMods(host, dir)}
	}
}

// listMods asks the host for the mods as they are now: their timings change
// with every call.
func (m *model) listMods() tea.Cmd {
	host, dir := m.mods.host, m.opt.WorkDir
	return func() tea.Msg { return modsMsg{state: readMods(host, dir)} }
}

// setMods takes what the host lists. The commands of the mods that are on go
// into the registry, their segments into the status line, and the kinds of
// item they draw to the open conversation; what a mod that is no longer on
// added is taken away.
func (m *model) setMods(st modsState) {
	g := &m.mods
	g.infos = st.infos
	g.needs = make(map[mods.Grant]bool, len(st.needs))
	for _, grant := range st.needs {
		g.needs[grant] = true
	}
	if st.ws != nil && (g.ws == nil || *g.ws != *st.ws) {
		g.ws, g.all = st.ws, true
	}
	on := make([]mods.Manifest, 0, len(st.infos))
	for _, info := range st.infos {
		if info.Status == mods.StatusOn {
			on = append(on, info.Manifest)
		}
	}
	slices.SortFunc(on, func(a, b mods.Manifest) int { return cmp.Compare(a.Name, b.Name) })

	m.setModCommands(on)

	// The segments. One that is new has nothing to show until it is asked.
	var segs []modSeg
	m.modSegs = nil
	for _, man := range on {
		for _, decl := range man.Segments {
			id := statusline.Segment{Name: decl.Name, From: "mod:" + man.Name}.ID()
			segs = append(segs, modSeg{mod: man.Name, name: decl.Name, id: id, refresh: decl.Refresh, every: decl.Every(), man: man})
			m.modSegs = append(m.modSegs, statusline.ModSegment{Mod: man.Name, Name: decl.Name})
		}
	}
	g.segs = segs
	for id := range g.vals {
		if !slices.ContainsFunc(segs, func(seg modSeg) bool { return seg.id == id }) {
			delete(g.vals, id)
			delete(g.last, id)
		}
	}

	// The renderers. The first mod by name that draws a kind draws it.
	kinds := make(map[string]modKind)
	for _, man := range on {
		for _, decl := range man.Renderers {
			if kind := strings.ToLower(decl.Kind); kinds[kind].mod == "" {
				kinds[kind] = modKind{mod: man.Name, kind: decl.Kind, man: man}
			}
		}
	}
	changed := len(kinds) != len(g.kinds)
	for kind, by := range kinds {
		changed = changed || g.kinds[kind].mod != by.mod || g.kinds[kind].kind != by.kind
	}
	g.kinds = kinds
	if c := m.chat; c != nil {
		c.setRenderers(kinds, changed)
	}
	m.revalue()
	if g.screen != nil {
		g.screen.SetInfos(st.infos)
		m.stale = true
	}
}

// setModCommands puts the commands of the mods that are on into the
// registry. Two mods may want one name, and the second to be set is then
// registered under its mod's name as well. So that which one that is depends
// only on which mods are on, and not on the order they were switched on in,
// every mod's commands are taken away and set again, in the order of the
// mods' names, whenever what they would add has changed.
func (m *model) setModCommands(on []mods.Manifest) {
	g := &m.mods
	var sig strings.Builder
	for _, man := range on {
		for _, decl := range man.Commands {
			sig.WriteString(strconv.Quote(man.Name) + strconv.Quote(decl.Name) + strconv.Quote(decl.Usage) + strconv.Quote(decl.Summary))
		}
	}
	if sig.String() == g.added {
		return
	}
	g.added = sig.String()
	for _, name := range g.owners {
		m.cmds.RemoveMod(name)
	}
	g.owners, g.commands = g.owners[:0], make(map[string]modCommand)
	for _, man := range on {
		if len(man.Commands) == 0 {
			continue
		}
		specs := make([]commands.Spec, len(man.Commands))
		for i, decl := range man.Commands {
			specs[i] = commands.Spec{
				Name: decl.Name, Usage: safe.Line(decl.Usage, 64, false), Summary: safe.Line(decl.Summary, 120, false),
			}
		}
		renamed := m.cmds.SetMod(man.Name, specs)
		g.owners = append(g.owners, man.Name)
		for _, decl := range man.Commands {
			name := strings.ToLower(decl.Name)
			if other := strings.ToLower(man.Name) + "." + name; slices.Contains(renamed, other) {
				name = other
			}
			if spec, ok := m.cmds.Lookup(name); ok && spec.Source == "mod "+man.Name {
				g.commands[name] = modCommand{mod: man.Name, name: decl.Name}
			}
		}
	}
	if c := m.chat; c != nil {
		c.inputChanged() // the list of commands, if it is open
	}
}

// modsOn counts the mods that are on.
func (g *modGlue) modsOn() int {
	n := 0
	for _, info := range g.infos {
		if info.Status == mods.StatusOn {
			n++
		}
	}
	return n
}

// has reports whether a mod of this name is installed.
func (g *modGlue) has(name string) bool {
	_, ok := g.info(name)
	return ok
}

// info is an installed mod by its name.
func (g *modGlue) info(name string) (mods.Info, bool) {
	for _, info := range g.infos {
		if info.Manifest.Name == name {
			return info, true
		}
	}
	return mods.Info{}, false
}

// modInput is what a request to a mod could carry, built from the open
// conversation: of everything there is, what the mod's manifest asks for.
// The host checks the grants again. The lists handed over are never changed
// afterwards, so the command that sends them may read them.
func (m *model) modInput(c *chat, man mods.Manifest, op string) mods.Input {
	g := &m.mods
	may := func(grant mods.Grant) bool { return g.needs[grant] && man.Has(grant) }
	in := mods.Input{Term: m.modTerm()}
	if may(mods.GrantContact) {
		in.Data.Contact = &wire.Contact{ID: uint64(c.contact.ID), Name: c.contact.Name}
	}
	if may(mods.GrantRoute) {
		in.Data.Route = &wire.Route{Provider: c.contact.Provider, Model: c.contact.Model}
	}
	if may(mods.GrantState) {
		state := c.modState()
		in.Data.State = &state
	}
	if may(mods.GrantModelCalls) {
		in.Data.ModelCalls = c.modCalls()
	}
	if may(mods.GrantMessages) && op == mods.OpCommand {
		in.Data.Messages = c.modMessages()
	}
	if may(mods.GrantWorkspace) {
		in.Data.Workspace = g.ws
	}
	if may(mods.GrantClock) {
		in.Data.Now = m.opt.Now().UnixMilli()
	}
	return in
}

// modTerm is what a mod is told of the terminal: enough to leave out a
// glyph or pick a tone.
func (m *model) modTerm() wire.Term {
	return wire.Term{
		Color: m.caps.Color.String(), Glyphs: m.caps.Glyphs.String(), Background: m.caps.Background.String(),
	}
}

// modState is the state of the conversation as a mod is told it.
func (c *chat) modState() wire.State {
	live := c.tr.Live()
	return wire.State{Conn: conn(c.state), Replying: live.Typing, Running: live.Running}
}

// modCalls is the records of the contact's model calls as a mod is sent
// them, oldest first. The list is made for the one request and never changed
// after.
func (c *chat) modCalls() []wire.ModelCall {
	attempts := c.tr.Attempts()
	calls := make([]wire.ModelCall, len(attempts))
	for i, a := range attempts {
		calls[i] = wire.ModelCall{
			Seq: a.Seq, TS: a.TS, Step: a.Step, Route: a.Route,
			Input: a.Input, Cached: a.Cached, Output: a.Output, Reasoning: a.Reasoning,
			MS: a.MS, Error: a.Error,
		}
	}
	return calls
}

// modMessages is the newest messages of the conversation, oldest first: what
// was said to the contact and what it replied.
func (c *chat) modMessages() []wire.Message {
	items := c.tr.Items()
	var out []wire.Message
	for i := len(items) - 1; i >= 0 && len(out) < modMessages; i-- {
		it := &items[i]
		role := "user"
		if it.Kind == transcript.Agent {
			role = "agent"
		} else if it.Kind != transcript.User {
			continue
		}
		out = append(out, wire.Message{Seq: it.Seq, TS: it.TS, Role: role, Text: it.Text})
	}
	slices.Reverse(out)
	return out
}

// newestCall is the sequence number of the contact's newest model call, or
// zero.
func (c *chat) newestCall() uint64 {
	if a := c.tr.Attempts(); len(a) > 0 {
		return a[len(a)-1].Seq
	}
	return 0
}

// modWatch looks at the conversation after a message was handled, and asks
// the segments again whose trigger it finds: a new model call, a turn that
// ended, another state, or an interval that has passed. In a conversation
// that was just opened every segment is asked. Nothing is asked while no
// conversation, and so no bar, is open.
func (m *model) modWatch() tea.Cmd {
	g, c := &m.mods, m.chat
	if g.host == nil || c == nil {
		return nil
	}
	now := modSeen{sess: c.sess, call: c.newestCall(), turns: c.turns, state: c.modState()}
	was := g.seen
	g.seen = now
	var cmds []tea.Cmd
	if was.sess != now.sess {
		// What the segments said was about another contact.
		clear(g.vals)
		clear(g.last)
		m.revalue()
	}
	turn := was.sess == now.sess && now.turns != was.turns
	if turn {
		cmds = append(cmds, m.readWorkspace())
	}
	all := g.all
	g.all = false
	at := m.opt.Now()
	var next time.Time
	for _, seg := range g.segs {
		// A segment with no answer about this conversation is asked for
		// one: a mod that was just switched on, a conversation just opened.
		last, answered := g.last[seg.id]
		due := all || !answered && !g.busy[seg.id]
		switch {
		case seg.every > 0:
			// Asked when its interval has passed since its last answer.
			due = due || !g.busy[seg.id] && !at.Before(last.Add(seg.every))
			if wake := last.Add(seg.every); !due && !g.busy[seg.id] && (next.IsZero() || wake.Before(next)) {
				next = wake
			}
		case seg.refresh == mods.RefreshModelCall:
			due = due || now.call != was.call
		case seg.refresh == mods.RefreshTurn:
			due = due || turn
		case seg.refresh == mods.RefreshState:
			due = due || now.state != was.state
		}
		if due {
			cmds = append(cmds, m.modRefresh(seg))
		}
	}
	// One timer, for the first segment whose interval is still running.
	if !next.IsZero() && (g.wake.IsZero() || next.Before(g.wake)) {
		g.gen++
		g.wake = next
		cmds = append(cmds, m.after(max(next.Sub(at), 0), modTickMsg{gen: g.gen}))
	}
	return tea.Batch(cmds...)
}

// onModTick is the mods' timer going off. The segments whose interval has
// passed are asked by the look that follows every message.
func (m *model) onModTick(msg modTickMsg) {
	if msg.gen == m.mods.gen {
		m.mods.wake = time.Time{}
	}
}

// modRefresh asks a segment for its value again. One call for a segment is
// on its way at a time: asked for while one is, the segment is asked once
// more when that one is back.
func (m *model) modRefresh(seg modSeg) tea.Cmd {
	g, c := &m.mods, m.chat
	if c == nil {
		return nil
	}
	if g.busy[seg.id] {
		g.again[seg.id] = true
		return nil
	}
	g.busy[seg.id] = true
	in := m.modInput(c, seg.man, mods.OpSegment)
	in.Width = c.width
	host, ctx, dir, sess := g.host, m.ctx, m.opt.WorkDir, c.sess
	return func() tea.Msg {
		return modSegmentMsg{sess: sess, seg: seg, modAnswer: callMod(ctx, host, dir, seg.mod, mods.OpSegment, seg.name, in)}
	}
}

// onModSegment takes a segment's answer. The bar shows it until the next
// one. An answer about a conversation that has been left says nothing about
// the one that is open.
func (m *model) onModSegment(msg modSegmentMsg) tea.Cmd {
	g, id := &m.mods, msg.seg.id
	delete(g.busy, id)
	if m.modSwitchedOff(msg.seg.mod, msg.modAnswer) {
		delete(g.again, id)
		return nil
	}
	at := slices.IndexFunc(g.segs, func(seg modSeg) bool { return seg.id == id })
	if at < 0 {
		delete(g.again, id)
		return nil
	}
	if c := m.chat; c != nil && c.sess == msg.sess {
		g.last[id] = m.opt.Now()
		g.vals[id] = modVal{
			text: msg.reply.Text, short: msg.reply.Short, tone: msg.reply.Tone,
			failed: msg.err != nil || msg.reply.Error != "",
		}
		m.revalue()
	}
	if g.again[id] {
		delete(g.again, id)
		return m.modRefresh(g.segs[at])
	}
	return nil
}

// modValues is what the mods' segments have to show, by Segment.ID. A
// segment whose mod could not be asked shows its name and a question mark,
// as a shell segment whose command failed does.
func (m *model) modValues(into map[string]statusline.Value) {
	ascii := m.caps.Glyphs == termcaps.GlyphsASCII
	for _, seg := range m.mods.segs {
		v, ok := m.mods.vals[seg.id]
		switch {
		case !ok:
		case v.failed:
			into[seg.id] = statusline.Value{Text: safe.Line(seg.name, 64, ascii) + " ?", Tone: "dim"}
		default:
			into[seg.id] = statusline.ModValue(safe.Line(v.text, 120, ascii), safe.Line(v.short, 120, ascii), modTone(v.tone))
		}
	}
}

// modTone is a tone a mod named, when it is one there is, and the plain one
// otherwise.
func modTone(name string) string {
	switch name {
	case "dim", "accent", "tool", "ok", "warn", "bad", "mod":
		return name
	}
	return ""
}

// modSwitchedOff reports whether a call ended with the host switching the
// mod off. It says so in the conversation, once, and takes away what the mod
// added.
func (m *model) modSwitchedOff(mod string, a modAnswer) bool {
	if !errors.Is(a.err, mods.ErrDisabled) {
		return false
	}
	notice := "mod " + mod + " was switched off"
	if _, why, found := strings.Cut(a.err.Error(), mods.ErrDisabled.Error()+": "); found && why != "" {
		notice += ": " + why
	}
	if c := m.chat; c != nil {
		c.print("", []wire.Line{{{T: notice, Tone: "warn"}}})
	} else {
		m.say(notice, "")
	}
	var st modsState
	if a.state != nil {
		st = *a.state
	} else {
		st = modsState{infos: slices.Clone(m.mods.infos)}
		for grant := range m.mods.needs {
			st.needs = append(st.needs, grant)
		}
	}
	// Whatever the host lists, the mod is off.
	st.infos = slices.Clone(st.infos)
	for i := range st.infos {
		if st.infos[i].Manifest.Name == mod && st.infos[i].Status == mods.StatusOn {
			st.infos[i].Status = mods.StatusDisabled
		}
	}
	m.setMods(st)
	return true
}

// readWorkspace reads where the client started again, for the mods that may
// know: the branch may have changed under a turn.
func (m *model) readWorkspace() tea.Cmd {
	g, dir := &m.mods, m.opt.WorkDir
	if dir == "" || !g.needs[mods.GrantWorkspace] {
		return nil
	}
	if g.wsBusy {
		g.wsAgain = true
		return nil
	}
	g.wsBusy = true
	return func() tea.Msg { return workspaceMsg{ws: mods.ReadWorkspace(dir)} }
}

// onWorkspace takes where the client started. When it changed, the segments
// of the mods that are told it are asked again.
func (m *model) onWorkspace(msg workspaceMsg) tea.Cmd {
	g := &m.mods
	g.wsBusy = false
	var cmds []tea.Cmd
	if g.ws == nil || *g.ws != msg.ws {
		g.ws = &msg.ws
		for _, seg := range g.segs {
			if seg.man.Has(mods.GrantWorkspace) {
				cmds = append(cmds, m.modRefresh(seg))
			}
		}
	}
	if g.wsAgain {
		g.wsAgain = false
		cmds = append(cmds, m.readWorkspace())
	}
	return tea.Batch(cmds...)
}

// runModCommand runs a command a mod added. The mod is asked away from the
// update loop, and its answer is shown when it is back.
func (m *model) runModCommand(spec commands.Spec, l commands.Line) tea.Cmd {
	c, g := m.chat, &m.mods
	by, ok := g.commands[spec.Name]
	info, on := g.info(by.mod)
	if g.host == nil || !ok || !on || info.Status != mods.StatusOn {
		c.setNote("/" + spec.Name + " is a mod's command, and that mod is not running")
		return nil
	}
	draft := c.input.Value()
	c.clearInput()
	in := m.modInput(c, info.Manifest, mods.OpCommand)
	in.Args, in.Width = l.Args, max(c.width-2, 1)
	host, ctx, dir, sess := g.host, m.ctx, m.opt.WorkDir, c.sess
	return func() tea.Msg {
		return modCommandMsg{
			sess: sess, mod: by.mod, draft: draft,
			modAnswer: callMod(ctx, host, dir, by.mod, mods.OpCommand, by.name, in),
		}
	}
}

// onModCommand shows what a mod's command answered: its lines as a block of
// the conversation under the mod's name, its text in the input box, and its
// note where the key hints are. Nothing of it is ever sent to the contact.
// A command that failed leaves its line in the box.
func (m *model) onModCommand(msg modCommandMsg) {
	off := m.modSwitchedOff(msg.mod, msg.modAnswer)
	c := m.chat
	if c == nil || c.sess != msg.sess || off {
		return
	}
	now := m.opt.Now()
	switch {
	case msg.err != nil:
		c.restore(msg.draft)
		c.setNote(msg.err.Error())
		return
	case msg.reply.Error != "":
		c.restore(msg.draft)
		c.setNote(msg.reply.Error)
		return
	}
	// The host has made the answer safe to draw and cut it to size. That
	// is done again here for what is not drawn by a renderer, which does it
	// itself: the box takes one line, and a block so many.
	if lines := msg.reply.Lines; len(lines) > 0 {
		c.show(c.print(msg.mod, lines[:min(len(lines), mods.MaxLines)]), now)
	}
	if insert := safe.Line(msg.reply.Insert, mods.MaxSpanRunes, false); insert != "" {
		c.giveBack(insert)
	}
	if msg.reply.Note != "" {
		c.say(msg.reply.Note, modTone(msg.reply.Tone))
	}
}

// restore puts the line of a command that did nothing back into the box,
// unless something has been typed since.
func (c *chat) restore(draft string) {
	if draft == "" || c.input.Value() != "" {
		return
	}
	c.input.SetValue(draft)
	c.inputChanged()
}

// renderKey names one drawing a mod was asked for: the item, the part of it,
// and the width it is for.
type renderKey struct {
	seq      uint64
	n, width int
}

// rendered is a mod's drawing of one part of an item, or the wait for it.
type rendered struct {
	// kind and sum say what was asked: the kind in lower case and a hash of
	// the text.
	kind string
	sum  uint64
	// done says the mod has answered. lines is what it drew; none means the
	// client draws the part itself.
	done  bool
	lines []wire.Line
}

// renderAsk is a drawing still to be asked for.
type renderAsk struct {
	key  renderKey
	sum  uint64
	by   modKind
	text string
}

// renders has what the mods drew of one conversation. It is the
// draw.RenderSource of the conversation's renderer: it answers at once from
// what it has, and writes down what it was asked and does not have, for the
// model to ask the mod.
type renders struct {
	kinds map[string]modKind
	got   map[renderKey]*rendered
	asks  []renderAsk
}

// Rendered implements draw.RenderSource.
func (r *renders) Rendered(ref draw.RenderRef, width int) ([]wire.Line, bool) {
	if len(r.kinds) == 0 || ref.Seq == 0 || len(ref.Text) > mods.MaxItem {
		return nil, false
	}
	kind := strings.ToLower(ref.Kind)
	by, ok := r.kinds[kind]
	if !ok {
		return nil, false
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(ref.Text)) // a hash's Write never fails
	key, sum := renderKey{seq: ref.Seq, n: ref.N, width: width}, h.Sum64()
	if e := r.got[key]; e != nil && e.kind == kind && e.sum == sum {
		return e.lines, e.done && len(e.lines) > 0
	}
	if len(r.got) >= maxRendered {
		// Drawings for other widths are the ones least likely to be wanted.
		for k := range r.got {
			if k.width != width {
				delete(r.got, k)
			}
		}
		if len(r.got) >= maxRendered {
			clear(r.got)
		}
	}
	r.got[key] = &rendered{kind: kind, sum: sum}
	r.asks = append(r.asks, renderAsk{key: key, sum: sum, by: by, text: ref.Text})
	return nil, false
}

// take returns the drawings to ask for, and forgets them.
func (r *renders) take() []renderAsk {
	asks := r.asks
	r.asks = nil
	return asks
}

// setRenderers gives the conversation the kinds of item the mods draw.
// forget drops what they drew so far, and has every block drawn again: by
// the client, until the mods have answered again.
func (c *chat) setRenderers(kinds map[string]modKind, forget bool) {
	c.renders.kinds = kinds
	if !forget {
		return
	}
	clear(c.renders.got)
	c.renders.asks = nil
	c.win.InvalidateAll()
	c.restaged()
	c.dirty |= dirtyTail | dirtyBody
}

// modRender asks the mods for the drawings the conversation's blocks wanted
// when they were last drawn. The blocks are drawn here when they are stale,
// so that what they want is known now and not after the next message; the
// view the program then asks for is the one drawn here.
func (m *model) modRender() tea.Cmd {
	c := m.chat
	if c == nil || len(c.renders.kinds) == 0 {
		return nil
	}
	if m.screen == screenChat && c.dirty != 0 {
		c.draw(m.opt.Now())
	}
	asks := c.renders.take()
	cmds := make([]tea.Cmd, 0, len(asks))
	host, ctx, dir, sess := m.mods.host, m.ctx, m.opt.WorkDir, c.sess
	for _, ask := range asks {
		in := m.modInput(c, ask.by.man, mods.OpRender)
		in.Width, in.Item = ask.key.width, &wire.Item{Kind: ask.by.kind, Text: ask.text}
		cmds = append(cmds, func() tea.Msg {
			return modRenderedMsg{
				sess: sess, mod: ask.by.mod, key: ask.key, sum: ask.sum,
				modAnswer: callMod(ctx, host, dir, ask.by.mod, mods.OpRender, ask.by.kind, in),
			}
		})
	}
	return tea.Batch(cmds...)
}

// onModRendered takes a mod's drawing of an item. The block is drawn again
// from the mod's lines; a mod that passed, failed or drew nothing leaves it
// as the client drew it.
func (m *model) onModRendered(msg modRenderedMsg) {
	off := m.modSwitchedOff(msg.mod, msg.modAnswer)
	c := m.chat
	if c == nil || c.sess != msg.sess || off {
		return
	}
	e := c.renders.got[msg.key]
	if e == nil || e.sum != msg.sum || e.done {
		return // the answer to a question that is no longer asked
	}
	e.done = true
	if msg.err != nil || msg.reply.Pass || msg.reply.Error != "" || len(msg.reply.Lines) == 0 {
		return
	}
	e.lines = msg.reply.Lines
	c.list.rd.Forget(msg.key.seq)
	c.win.Invalidate(scrollback.Key{Kind: blockItem, ID: msg.key.seq})
	c.restaged()
	c.dirty |= dirtyBody
}

// cmdMods is /mods: the screen of the mods, or one thing done to one mod.
func cmdMods(m *model, l commands.Line) tea.Cmd {
	g := &m.mods
	if g.host == nil {
		return m.refuse("mods are off: " + m.modsOff())
	}
	if len(l.Args) == 0 {
		return m.openMods()
	}
	if len(l.Args) != 2 || l.Args[1] == "" {
		return m.wrong(l)
	}
	var verb modVerb
	switch strings.ToLower(l.Args[0]) {
	case "on":
		verb = modOn
	case "off":
		verb = modOff
	case "install":
		verb = modInstall
	case "update":
		verb = modUpdate
	default:
		return m.wrong(l)
	}
	arg := l.Args[1]
	if verb == modInstall {
		arg = m.typedPath(arg)
	} else if arg = strings.ToLower(arg); !g.has(arg) {
		// A mod's name is in lower case, however it was typed.
		return m.refuse("no mod is called " + safe.Line(strconv.Quote(arg), nameInNote, false) + "; /mods lists them")
	}
	return m.modAct(verb, arg, false, "/"+l.Name+" "+l.Rest)
}

// typedPath is a path as a person typed it, made whole: "~" is the home
// directory, and a path that does not start at the root starts where the
// client did. It is the directory a mod is installed from, and where
// /image save writes.
func (m *model) typedPath(typed string) string {
	home := m.opt.Home
	switch {
	case typed == "~" && home != "":
		return home
	case strings.HasPrefix(typed, "~/") && home != "":
		return filepath.Join(home, typed[2:])
	case !filepath.IsAbs(typed) && m.opt.WorkDir != "":
		return filepath.Join(m.opt.WorkDir, typed)
	}
	return typed
}

// modAct switches a mod on or off, installs one from a directory, or copies
// one again from where it was installed. arg is the mod, or the directory.
func (m *model) modAct(verb modVerb, arg string, screen bool, draft string) tea.Cmd {
	host, ctx, dir := m.mods.host, m.ctx, m.opt.WorkDir
	var sess Session
	if m.chat != nil {
		sess = m.chat.sess
	}
	return func() tea.Msg {
		msg := modActedMsg{verb: verb, arg: arg, screen: screen, sess: sess, draft: draft}
		switch verb {
		case modOn:
			msg.info, msg.err = host.SetEnabled(ctx, arg, true)
		case modOff:
			msg.info, msg.err = host.SetEnabled(ctx, arg, false)
		case modInstall:
			msg.info, msg.err = host.Install(ctx, arg)
		case modUpdate:
			msg.info, msg.err = host.Update(ctx, arg)
		}
		msg.state = readMods(host, dir)
		return msg
	}
}

// onModActed takes what became of a mod, and says so: on the /mods screen
// only when it went wrong, since the screen shows the rest; after a command,
// in the conversation that ran it.
func (m *model) onModActed(msg modActedMsg) {
	m.setMods(msg.state)
	if msg.screen {
		switch scr := m.mods.screen; {
		case msg.err == nil:
		case scr != nil:
			scr.SetNote(msg.err.Error(), "bad")
			m.stale = true
		default:
			// The screen was left before the answer came.
			m.say(msg.err.Error(), "")
		}
		return
	}
	c := m.chat
	if c == nil || c.sess != msg.sess {
		return
	}
	if msg.err != nil {
		c.restore(msg.draft)
		c.say(msg.err.Error(), "")
		return
	}
	name := msg.info.Manifest.Name
	reason := msg.info.Reason
	switch msg.verb {
	case modOn:
		switch {
		case msg.info.Status == mods.StatusOn:
			c.say("mod "+name+" is on", toneDone)
		case reason != "":
			c.say("mod "+name+" could not be switched on: "+reason, "")
		default:
			c.say("mod "+name+" is "+msg.info.Status.String(), "")
		}
	case modOff:
		c.say("mod "+name+" is off", toneDone)
	case modInstall:
		installed := "installed " + name
		if v := msg.info.Manifest.Version; v != "" {
			installed += " " + v
		}
		c.say(installed+"; it is off until /mods on "+name+", and /mods shows what it may read", toneDone)
	case modUpdate:
		switch {
		case msg.info.Status == mods.StatusOn:
			c.say("mod "+name+" is as it was installed: nothing changed", toneDone)
		case reason != "":
			c.say("mod "+name+": "+reason, "")
		default:
			c.say("mod "+name+" was copied again, and is "+msg.info.Status.String(), toneDone)
		}
	}
}

// openMods opens the /mods screen over the conversation, which stays open
// under it. The list is the one the host last gave; a newer one, with the
// timings of the calls made since, is asked for.
func (m *model) openMods() tea.Cmd {
	g := &m.mods
	g.screen = modscreen.New(g.infos, conf.Short(m.opt.Dirs.ModsDir(), m.opt.Home), m.tmuxNote())
	g.screen.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
	m.screen, m.stale = screenMods, true
	return m.listMods()
}

// tmuxNote reports whether images are half blocks only because tmux does
// not pass the terminal's answer through: what the /mods screen then says.
// While the terminal may still answer, nothing is known about that.
func (m *model) tmuxNote() bool {
	if !m.caps.Settled {
		return false
	}
	for _, line := range termcaps.Explain(m.opt.Env, m.over, m.replies) {
		if strings.HasPrefix(line, "images: ") {
			return strings.Contains(line, "allow-passthrough")
		}
	}
	return false
}

// modsInput hands a key or pasted text to the /mods screen, and does what
// the screen asks for. Two keys are not the screen's, whatever is being
// typed on it: ctrl+c leaves the client, as on every screen, and ctrl+l
// draws the screen again.
func (m *model) modsInput(msg tea.Msg) tea.Cmd {
	g := &m.mods
	if g.screen == nil {
		return nil
	}
	// Text an input method hands over in one press is never one of the two.
	if key, ok := msg.(tea.KeyPressMsg); ok && key.Mod == tea.ModCtrl && key.Text == "" {
		switch key.Code {
		case 'c':
			return tea.Quit
		case 'l':
			return m.clear()
		}
	}
	m.stale = true
	done, act, cmd := g.screen.Update(msg)
	if act != nil {
		cmd = tea.Batch(cmd, m.modScreenAct(*act))
	}
	if done {
		g.screen, m.screen = nil, screenChat
	}
	return cmd
}

// modScreenAct carries out what the /mods screen asked for.
func (m *model) modScreenAct(act modscreen.Action) tea.Cmd {
	switch act.Kind {
	case modscreen.ActToggle:
		// The two states the screen's box shows as on are switched off.
		verb := modOn
		if info, ok := m.mods.info(act.Name); ok && (info.Status == mods.StatusOn || info.Status == mods.StatusLoading) {
			verb = modOff
		}
		return m.modAct(verb, act.Name, true, "")
	case modscreen.ActInstall:
		return m.modAct(modInstall, m.typedPath(act.Path), true, "")
	case modscreen.ActUpdate:
		return m.modAct(modUpdate, act.Name, true, "")
	}
	return nil
}

// modsView draws the /mods screen.
func (m *model) modsView() tea.View {
	content, cursor := m.mods.screen.View(m.th, m.width, m.height)
	v := tea.NewView(content)
	v.AltScreen = true
	v.WindowTitle = "contactd: mods"
	v.Cursor = cursor
	return v
}

// modLines is the mods in /status: a line for each, with what it adds, or
// why it is not on.
func (m *model) modLines(row func(name, value, tone string) wire.Line) []wire.Line {
	g := &m.mods
	switch {
	case g.host == nil:
		return []wire.Line{row("mods", "off: "+m.modsOff(), "dim")}
	case len(g.infos) == 0:
		return []wire.Line{row("mods", "none installed", "dim")}
	}
	lines := make([]wire.Line, 0, len(g.infos))
	for _, info := range g.infos {
		man := info.Manifest
		value, tone := info.Status.String(), ""
		if man.Version != "" {
			value += ", " + man.Version
		}
		switch info.Status {
		case mods.StatusOn:
			if adds := modAdds(man); adds != "" {
				value += ": " + adds
			}
		case mods.StatusFailed, mods.StatusDisabled:
			tone = "bad"
			if info.Reason != "" {
				value += ": " + info.Reason
			}
		case mods.StatusOff, mods.StatusLoading:
			tone = "dim"
			if info.Reason != "" {
				value += ": " + info.Reason
			}
		}
		lines = append(lines, row("mod "+man.Name, value, tone))
	}
	return lines
}

// modAdds names what a mod adds, in a few words.
func modAdds(man mods.Manifest) string {
	adds := make([]string, 0, len(man.Segments)+len(man.Commands)+len(man.Renderers))
	for _, seg := range man.Segments {
		adds = append(adds, "segment "+seg.Name)
	}
	for _, c := range man.Commands {
		adds = append(adds, "/"+c.Name)
	}
	for _, r := range man.Renderers {
		adds = append(adds, "draws "+r.Kind)
	}
	return strings.Join(adds, ", ")
}
