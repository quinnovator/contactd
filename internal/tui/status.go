package tui

import (
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/quinnovator/contactd/internal/client"
	"github.com/quinnovator/contactd/internal/tui/commands"
	"github.com/quinnovator/contactd/internal/tui/conf"
	"github.com/quinnovator/contactd/internal/tui/draw"
	"github.com/quinnovator/contactd/internal/tui/frame"
	"github.com/quinnovator/contactd/internal/tui/safe"
	"github.com/quinnovator/contactd/internal/tui/sledit"
	"github.com/quinnovator/contactd/internal/tui/statusline"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
	"github.com/quinnovator/contactd/internal/tui/theme"
)

// What the rows around the transcript say: the header above it, and the
// status bar and the hint line below. Each is worked out from the state of
// the conversation as a small value that can be compared, so a row is drawn
// again only when what it says has changed.

// snapshot is what the conversation knows that the status bar can show.
func (c *chat) snapshot(now time.Time) statusline.Snapshot {
	live := c.tr.Live()
	s := statusline.Snapshot{
		Conn:         conn(c.state),
		ConnErr:      c.stateErr,
		Contact:      c.label,
		Provider:     c.provider,
		Model:        c.model,
		Replying:     live.Typing,
		LoadingOlder: c.loadingOlder,
		Scrolled:     c.stage == nil && !c.win.Following(),
		Agents:       c.waiting,
		Helpers:      c.board.Count(),
		Clock:        now.In(c.loc).Format("15:04"),
	}
	if live.Running != "" {
		s.Running = safe.Line(live.Running, 64, false)
	}
	if a, ok := c.tr.Attempt(); ok {
		s.HasCall, s.CallCanceled = true, a.Error == "canceled"
		s.CallFailed = a.Error != "" && !s.CallCanceled
		s.Input, s.Cached, s.Output, s.MS = a.Input, a.Cached, a.Output, a.MS
		s.CacheReported = a.HasCacheReport()
	}
	return s
}

// conn names the state of the connection as the status line knows it.
func conn(s client.State) string {
	switch s {
	case client.StateConnecting:
		return statusline.ConnConnecting
	case client.StateLive:
		return statusline.ConnLive
	case client.StateDown:
		return statusline.ConnDown
	case client.StateGone:
		return statusline.ConnGone
	default:
		return ""
	}
}

// barRow draws the status bar from a snapshot: the row, and the rule above
// it where the bar has no fill. With no hint row on the screen a note takes
// the bar's row.
func (c *chat) barRow(snap statusline.Snapshot) (rule, row string) {
	th := c.th
	left, right := statusline.Layout(c.bar, c.values(snap), c.width, statusline.LayoutOptions{
		SepWidth: th.Width(th.G.Sep) + 2, MinGap: 2, Measure: th.Width,
	})
	rule, row = th.Bar(cells(left), cells(right), c.width)
	if c.note != "" && c.rows.Hint == 0 {
		row = c.hints()
	}
	return rule, row
}

// values is what every segment of the status line has to show: the core
// segments from a snapshot, and the shell and mod segments as the model last
// worked them out.
func (c *chat) values(snap statusline.Snapshot) map[string]statusline.Value {
	vals := statusline.CoreValues(snap, statusline.Marks{Dot: c.th.G.Dot, Newer: c.th.G.Newer})
	maps.Copy(vals, c.extra)
	return vals
}

// setBar gives the conversation another status line, or other values for
// its shell and mod segments. The map is read and never changed.
func (c *chat) setBar(bar statusline.Config, extra map[string]statusline.Value) {
	c.bar, c.extra = bar, extra
	c.dirty |= dirtyBar
}

// cells hands the status line's cells to the theme.
func cells(in []statusline.Cell) []theme.Cell {
	out := make([]theme.Cell, len(in))
	for i, cell := range in {
		out[i] = theme.Cell{Text: cell.Text, Tone: cell.Tone}
	}
	return out
}

// headerInfo is what the header shows: who this is, what serves it, the
// days of the blocks in view, and how much of the last call's prompt came
// from the provider's cache.
func (c *chat) headerInfo() frame.HeaderInfo {
	h := frame.HeaderInfo{Label: c.label, Provider: c.provider, Model: c.model, Range: c.dateRange()}
	if a, ok := c.tr.Attempt(); ok && a.Error == "" && a.Input > 0 && a.HasCacheReport() {
		h.HasCall, h.CachePct = true, 100*a.Cached/a.Input
	}
	return h
}

// dateRange names the days of the blocks the window last showed.
func (c *chat) dateRange() string {
	// A stage shows one turn: the day is that of the newest thing in it.
	if c.stage != nil {
		for _, it := range slices.Backward(c.tr.Items()) {
			if it.TS != 0 {
				return draw.DateRange(it.TS, it.TS, c.loc, c.th.G.Dash)
			}
		}
		return ""
	}
	first, last, ok := c.win.Visible()
	if !ok || last >= len(c.list.blocks) {
		return ""
	}
	var from, to int64
	for i := first; i <= last && from == 0; i++ {
		from = c.list.blocks[i].ts
	}
	for i := last; i >= first && to == 0; i-- {
		to = c.list.blocks[i].ts
	}
	return draw.DateRange(from, to, c.loc, c.th.G.Dash)
}

// hints draws the hint line: the keys, what a command is while the list of
// them is open, or the note that stands in for either until the next
// keypress.
func (c *chat) hints() string {
	return frame.Hints(c.th, frame.HintState{
		PaletteOpen: c.pal.Open(), ShiftEnter: c.th.Caps.ShiftEnter, Note: c.note, NoteTone: c.noteTone,
	}, c.width)
}

// The status line's file, its shell segments and its editor.
//
// The bar is what statusline.toml says, a preset when there is no file. A
// shell segment shows what a command printed. Each command runs away from
// the update loop, one run of a segment at a time, and the next one starts
// an interval after the last one finished. The one timer for that exists
// only while a conversation is open and a shell segment is on.

// barFileName is how notes name the status line's file.
const barFileName = "statusline.toml"

// stopWait is how long leaving waits for the commands of shell segments
// that are still running to have been stopped.
const stopWait = 2 * time.Second

// barFile is statusline.toml as it was read.
type barFile struct {
	path     string
	cfg      statusline.Config
	warnings []string
	// err says the file could not be used at all. unsafe says why its
	// commands may not be run; nil means they may.
	err, unsafe error
}

// readBar reads the status line's file. Who may write to the file is looked
// at before it is read, and only what this one read holds is ever run. safe
// is that look: conf.SafeToExecuteFrom.
func readBar(path string, safe func(string) error) barFile {
	f := barFile{path: path, unsafe: safe(path)}
	f.cfg, f.warnings, f.err = statusline.Load(path)
	return f
}

// says is an error of the file as a note has it: with the file's name in
// place of its whole path.
func (f barFile) says(err error) string {
	if f.path == "" {
		return err.Error()
	}
	return strings.ReplaceAll(err.Error(), f.path, barFileName)
}

// shellRun is the latest finished run of a shell segment's command.
type shellRun struct {
	command, out string
	err          error
}

// shells is the running of the status line's shell segments. The maps are
// keyed by Segment.ID.
type shells struct {
	run statusline.Runner
	// safe says whether a file may be trusted with commands to run: nil, or
	// why not. It is conf.SafeToExecuteFrom.
	safe func(path string) error
	// busy holds the segments whose command is running, last when each
	// segment's latest run finished, and done what that run printed.
	busy map[string]bool
	last map[string]time.Time
	done map[string]shellRun
	// wake is when the timer that is set goes off; zero means none is set.
	// gen numbers the timers: the tick of an older one is dropped.
	wake time.Time
	gen  uint64
	// flights counts the commands that are running right now.
	flights *flights
}

// flights counts the commands of shell segments that are running, on
// whichever goroutines the program runs them, so that leaving the client can
// wait until they have been stopped.
type flights struct {
	mu     sync.Mutex
	closed bool
	wg     sync.WaitGroup
}

// enter is called before a command is run. It reports false once the client
// is leaving: nothing is started then.
func (f *flights) enter() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return false
	}
	f.wg.Add(1)
	return true
}

// leave is called when a command that entered has ended.
func (f *flights) leave() { f.wg.Done() }

// close lets no further command start, and returns a channel that is closed
// when every one that had started has ended.
func (f *flights) close() <-chan struct{} {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	over := make(chan struct{})
	go func() {
		f.wg.Wait()
		close(over)
	}()
	return over
}

// stop is called on leaving, after the context the commands run under has
// ended: a command is killed for that, and stop waits until it is gone, so
// that nothing the client started outlives it. The wait is given up after a
// moment all the same: a Runner that does not look at its context must not
// keep the person from leaving.
func (s *shells) stop() {
	select {
	case <-s.flights.close():
	case <-time.After(stopWait):
	}
}

// Messages of the status line.
type (
	// shellDoneMsg is one run of a shell segment's command, finished.
	shellDoneMsg struct {
		id, command, out string
		err              error
	}
	// shellTickMsg says the moment the timer was set for has come.
	shellTickMsg struct{ gen uint64 }
)

// after gives a message once a time has passed.
func after(d time.Duration, msg tea.Msg) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return msg })
}

// runnable is the shell segments of a config that are on and have a command
// to run.
func runnable(cfg statusline.Config) []statusline.Segment {
	return statusline.Due(cfg, nil, time.Time{})
}

// withoutShell is a config with every shell segment switched off.
func withoutShell(cfg statusline.Config) statusline.Config {
	out := statusline.Config{Segments: slices.Clone(cfg.Segments)}
	for i := range out.Segments {
		if out.Segments[i].From == statusline.FromShell {
			out.Segments[i].On = false
		}
	}
	return out
}

// trusted returns the config whose commands may be run, given what
// conf.SafeToExecuteFrom said about the file it is in. A file that someone
// else could have written has its shell segments switched off, so that none
// runs, and the note says why. Switching one on again in the editor and
// saving is the person's own word for its command.
func trusted(f barFile, cfg statusline.Config) (statusline.Config, string) {
	if f.unsafe == nil || len(runnable(cfg)) == 0 {
		return cfg, ""
	}
	return withoutShell(cfg), "shell segments are off: " + f.says(f.unsafe)
}

// takeBar makes the status line what the file says, and returns what is
// wrong with the file, a line for each thing. A file that could not be used
// changes nothing: the line names where it broke and kept says what the
// status line is instead.
func (m *model) takeBar(f barFile, kept string) []string {
	if f.err != nil {
		return []string{f.says(f.err) + "; " + kept}
	}
	var wrong []string
	cfg, refused := trusted(f, f.cfg)
	if refused != "" {
		wrong = append(wrong, refused)
	}
	if len(f.warnings) > 0 {
		wrong = append(wrong, barFileName+": "+strings.Join(f.warnings, "; "))
	}
	m.setBar(cfg)
	return wrong
}

// setBar makes cfg the status line. What a shell segment's command printed
// is kept only while the segment still has that command.
func (m *model) setBar(cfg statusline.Config) {
	m.bar = cfg
	commands := make(map[string]string, len(cfg.Segments))
	for _, seg := range cfg.Segments {
		if seg.From == statusline.FromShell {
			commands[seg.ID()] = seg.Command
		}
	}
	for id, run := range m.sh.done {
		if command, listed := commands[id]; !listed || command != run.command {
			delete(m.sh.done, id)
			delete(m.sh.last, id)
		}
	}
	m.revalue()
}

// shownBar is the status line as it is drawn and edited: the configured one
// and a segment for everything the mods offer that it does not list.
func (m *model) shownBar() statusline.Config { return m.bar.WithMods(m.modSegs) }

// revalue works out again what the shell and mod segments have to show, and
// hands the bar and the values to the open conversation. It is called after
// the configuration, a value, or the glyph set changed.
func (m *model) revalue() {
	extra := make(map[string]statusline.Value, len(m.sh.done)+len(m.mods.vals))
	m.modValues(extra)
	ascii := m.caps.Glyphs == termcaps.GlyphsASCII
	for _, seg := range m.bar.Segments {
		if run, ok := m.sh.done[seg.ID()]; ok && seg.From == statusline.FromShell {
			extra[seg.ID()] = statusline.ShellValue(seg, run.out, run.err, ascii)
		}
	}
	m.extra = extra
	m.extraRev++
	if m.chat != nil {
		m.chat.setBar(m.shownBar(), extra)
	}
}

// shellStep starts every shell segment whose turn it is, and sets the timer
// for the next turn. A segment that is still running is not started again,
// and nothing runs while no conversation, and so no bar, is open.
func (m *model) shellStep() tea.Cmd {
	if m.chat == nil {
		return nil
	}
	now := m.opt.Now()
	var cmds []tea.Cmd
	for _, seg := range statusline.Due(m.bar, m.sh.last, now) {
		if id := seg.ID(); !m.sh.busy[id] {
			m.sh.busy[id] = true
			cmds = append(cmds, m.shellRun(seg))
		}
	}
	// The timer is for the first segment that waits for its interval to
	// pass. One that runs sets it when it is done.
	var next time.Time
	for _, seg := range runnable(m.bar) {
		at, ran := m.sh.last[seg.ID()]
		if !ran || m.sh.busy[seg.ID()] {
			continue
		}
		if at = at.Add(max(seg.Interval, statusline.MinInterval)); next.IsZero() || at.Before(next) {
			next = at
		}
	}
	if !next.IsZero() && (m.sh.wake.IsZero() || next.Before(m.sh.wake)) {
		m.sh.gen++
		m.sh.wake = next
		cmds = append(cmds, m.after(max(next.Sub(now), 0), shellTickMsg{gen: m.sh.gen}))
	}
	return tea.Batch(cmds...)
}

// shellRun is the command that runs a shell segment's command once. It
// blocks for as long as the command takes, which is why it is a command of
// the program's and never called from Update.
func (m *model) shellRun(seg statusline.Segment) tea.Cmd {
	run, flights, ctx, id := m.sh.run, m.sh.flights, m.ctx, seg.ID()
	return func() tea.Msg {
		if !flights.enter() {
			return nil // the client is being left
		}
		defer flights.leave()
		out, err := run.Run(ctx, seg.Command, seg.Timeout)
		return shellDoneMsg{id: id, command: seg.Command, out: out, err: err}
	}
}

// onShellDone takes what a shell segment's command printed. A run of a
// command the segment no longer has says nothing about the segment.
func (m *model) onShellDone(msg shellDoneMsg) tea.Cmd {
	delete(m.sh.busy, msg.id)
	for _, seg := range m.bar.Segments {
		if seg.From == statusline.FromShell && seg.ID() == msg.id && seg.Command == msg.command {
			m.sh.last[msg.id] = m.opt.Now()
			m.sh.done[msg.id] = shellRun{command: msg.command, out: msg.out, err: msg.err}
			m.revalue()
			break
		}
	}
	return m.shellStep()
}

// onShellTick is the timer going off.
func (m *model) onShellTick(msg shellTickMsg) tea.Cmd {
	if msg.gen != m.sh.gen {
		return nil // an older timer; a newer one is set
	}
	m.sh.wake = time.Time{}
	return m.shellStep()
}

// cmdStatusLine is /statusline: the editor, or a preset for this session.
func cmdStatusLine(m *model, l commands.Line) tea.Cmd {
	const usage = "usage: /statusline preset <name> | edit"
	switch {
	case len(l.Args) == 0, len(l.Args) == 1 && strings.EqualFold(l.Args[0], "edit"):
		m.editBar()
		return nil
	case len(l.Args) != 2 || !strings.EqualFold(l.Args[0], "preset"):
		return m.refuse(usage)
	}
	name := strings.ToLower(l.Args[1])
	preset, ok := statusline.Preset(name)
	if !ok {
		return m.refuse("no preset " + strconv.Quote(safe.Line(name, nameInNote, false)) +
			"; there are " + strings.Join(statusline.Presets(), ", "))
	}
	m.setBar(withPreset(m.bar, preset))
	m.chat.say("status line: preset "+name+" for this session; /statusline edit, then s, keeps it", toneDone)
	return m.shellStep()
}

// withPreset is a config after a preset was applied to it, as the editor's
// p does it: the segments the preset lists are the preset's, and the others,
// those of mods and the shell segments a person added, stay as they are,
// before the clock.
func withPreset(cfg, preset statusline.Config) statusline.Config {
	listed := make(map[string]bool, len(preset.Segments))
	for _, seg := range preset.Segments {
		listed[seg.ID()] = true
	}
	var kept []statusline.Segment
	for _, seg := range cfg.Segments {
		if !listed[seg.ID()] {
			kept = append(kept, seg)
		}
	}
	at := slices.IndexFunc(preset.Segments, func(seg statusline.Segment) bool {
		return seg.ID() == "core:clock" && seg.Side == statusline.Right
	})
	if at < 0 {
		at = len(preset.Segments)
	}
	return statusline.Config{Segments: slices.Concat(preset.Segments[:at], kept, preset.Segments[at:])}
}

// editSeen is what the editor's live values were last made from.
type editSeen struct {
	snap statusline.Snapshot
	rev  uint64
	th   *theme.Theme
}

// editBar opens the status line's editor over the conversation, which stays
// open under it.
func (m *model) editBar() {
	m.edit = sledit.New(m.shownBar(), conf.Short(m.opt.Dirs.StatusLineFile(), m.opt.Home), nil)
	m.seen, m.editNote = editSeen{}, ""
	m.screen, m.stale = screenStatusLine, true
	m.followBar()
}

// followBar keeps the values the editor shows the conversation's own, which
// goes on under it. It is called after every message while the editor is
// open, and tells the editor only when something it shows has changed.
func (m *model) followBar() {
	c := m.chat
	if m.edit == nil || c == nil {
		return
	}
	// The conversation is not drawn while the editor is, so nothing else
	// brings what it counts up to date.
	c.sync()
	now := editSeen{snap: c.snapshot(m.opt.Now()), rev: m.extraRev, th: m.th}
	if now == m.seen {
		return
	}
	m.seen, m.stale = now, true
	m.edit.SetLive(c.values(now.snap))
}

// editMsg hands a key or pasted text to the editor, and does what the
// editor asks for: write the file, or close the screen. Two keys are not the
// editor's: ctrl+c leaves the client, as on every screen, and ctrl+l draws
// the screen again.
func (m *model) editMsg(msg tea.Msg) tea.Cmd {
	if m.edit == nil {
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
	res, cmd := m.edit.Update(msg)
	if res.Save != nil {
		cmd = tea.Batch(cmd, m.saveBar(*res.Save))
	}
	if res.Done {
		m.closeEditor(res.Note)
	}
	return cmd
}

// saveBar writes the status line the editor handed back and makes it the
// one in use. A status line that could not be written is not used: the
// editor is told, and says so.
func (m *model) saveBar(cfg statusline.Config) tea.Cmd {
	f := barFile{path: m.opt.Dirs.StatusLineFile()}
	if err := conf.WriteFile(f.path, cfg.Encode()); err != nil {
		m.edit.SaveFailed(f.says(err))
		return nil
	}
	// The file is the client's own now. Where it still is not safe to run
	// commands from, the note waits until the editor is left: the editor
	// has the screen.
	f.unsafe = m.sh.safe(f.path)
	cfg, m.editNote = trusted(f, cfg)
	m.setBar(cfg)
	return m.shellStep()
}

// closeEditor goes back to the conversation. note is what the editor has to
// say on leaving. With nothing to say, a note the conversation got in the
// meantime stands.
func (m *model) closeEditor(note string) {
	if note == "" {
		note = m.editNote
	}
	m.edit, m.editNote = nil, ""
	m.screen = screenChat
	if note != "" {
		m.say(note, "")
	}
}

// editView draws the editor.
func (m *model) editView() tea.View {
	content, cursor := m.edit.View(m.th, m.width, m.height)
	v := tea.NewView(content)
	v.AltScreen = true
	v.WindowTitle = "contactd: status line"
	v.Cursor = cursor
	return v
}
