// Package sledit is the status-line editor: the screen /statusline edit
// opens.
//
// It lists every segment of a statusline.Config in a table, shows the
// settings of the selected one beside it, and draws the bar as it would look
// at 100 columns, at 60 and without colour. Keys switch a segment on and off,
// move it to the other side or along the list, and change its settings one
// field at a time.
//
// The screen changes nothing but itself. It writes no file and runs no
// command: Update hands the config back in Result.Save, and whoever owns the
// screen writes it and applies it.
package sledit

import (
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/quinnovator/contactd/internal/tui/statusline"
	"github.com/quinnovator/contactd/internal/tui/theme"
)

// The tones the screen draws in. They are names theme.Style knows.
const (
	toneDim    = "dim"
	toneAccent = "accent"
	toneOK     = "ok"
	toneWarn   = "warn"
	toneBad    = "bad"
	toneMod    = "mod"
	toneRule   = "rule"
)

// fromMod starts the From of a mod segment; the mod's name follows.
const fromMod = "mod:"

// kind is where a segment comes from.
type kind uint8

const (
	kindCore kind = iota + 1
	kindMod
	kindShell
)

// kindOf reads a segment's From. Anything that is not a mod's or the shell's
// is drawn and edited as a core segment.
func kindOf(s statusline.Segment) kind {
	switch {
	case s.From == statusline.FromShell:
		return kindShell
	case strings.HasPrefix(s.From, fromMod):
		return kindMod
	}
	return kindCore
}

// Result is what one message did to the screen.
type Result struct {
	// Done says the screen is finished with and its owner should close it.
	Done bool
	// Save is the config to write to statusline.toml and to apply. It is
	// the caller's own copy. Nil means there is nothing to save.
	Save *statusline.Config
	// Note is one line about what just happened, or "". The screen shows it
	// itself while it is open; with Done it is for whatever screen comes
	// next.
	Note string
}

// Model is the editor screen. Make one with New, each time the screen is
// opened.
type Model struct {
	// cfg is the config being edited and saved the one last saved. The
	// two differ exactly when there are unsaved changes.
	cfg, saved statusline.Config
	// undo is what saved was before the latest save, for SaveFailed.
	undo *statusline.Config
	path string
	live map[string]statusline.Value
	// sel is the selected segment and top the first one the table shows.
	sel, top int
	// preset is the preset underlined at the bottom.
	preset string
	// asking says esc was pressed on unsaved changes: the next esc leaves.
	asking bool
	// note is shown in place of the key row until the next key.
	note, tone string
	// ed is the field being edited, or nil.
	ed *editing
	// plain is the theme of the "no colour" preview, kept between frames.
	plain *theme.Theme
}

// New makes the editor for a config. path is the file's path as the header
// shows it, with the home directory already written as ~. live holds each
// segment's current value, keyed by Segment.ID; a segment that has none is
// shown with a sample. The config is copied: the caller's is never changed.
//
// Every core segment is listed, as it is in a file that was read: the ones
// the config leaves out are added at the end, switched off.
func New(cfg statusline.Config, path string, live map[string]statusline.Value) *Model {
	segments := slices.Clone(cfg.Segments)
	for i := range segments {
		segments[i] = explicit(segments[i])
	}
	segments = withCore(segments)
	m := &Model{
		cfg:   statusline.Config{Segments: segments},
		saved: statusline.Config{Segments: slices.Clone(segments)},
		path:  path,
		live:  live,
	}
	m.preset = nearestPreset(m.cfg)
	return m
}

// explicit names what a segment may leave out: where it comes from and the
// side it sits at. The table shows both, and a saved file holds both.
func explicit(s statusline.Segment) statusline.Segment {
	if s.From == "" {
		s.From = statusline.FromCore
	}
	if s.Side != statusline.Left && s.Side != statusline.Right {
		s.Side = statusline.Right
		if def, ok := coreDefault(s); ok {
			s.Side = def.Side
		}
	}
	return s
}

// withCore adds the core segments a list leaves out, at its end and switched
// off, as statusline.Parse does for a file. So the table offers every one of
// them, and what the editor saves reads back as the same config.
func withCore(segments []statusline.Segment) []statusline.Segment {
	listed := make(map[string]bool, len(segments))
	for _, s := range segments {
		listed[s.ID()] = true
	}
	preset, _ := statusline.Preset(statusline.DefaultPreset)
	for _, def := range preset.Segments {
		if kindOf(def) == kindCore && !listed[def.ID()] {
			def.On = false
			segments = append(segments, def)
		}
	}
	return segments
}

// coreDefault returns a core segment as the presets write it: its usual
// side, formats and turn to give way.
func coreDefault(s statusline.Segment) (statusline.Segment, bool) {
	if kindOf(s) != kindCore {
		return statusline.Segment{}, false
	}
	preset, _ := statusline.Preset(statusline.DefaultPreset)
	for _, def := range preset.Segments {
		if def.ID() == s.ID() {
			return def, true
		}
	}
	return statusline.Segment{}, false
}

// nearestPreset names the preset a config is closest to: the one that
// differs in the fewest segments switched on or off. The default preset wins
// a tie.
func nearestPreset(c statusline.Config) string {
	on := make(map[string]bool, len(c.Segments))
	for _, s := range c.Segments {
		on[s.ID()] = s.On
	}
	best, least := statusline.DefaultPreset, -1
	for _, name := range append([]string{statusline.DefaultPreset}, statusline.Presets()...) {
		preset, ok := statusline.Preset(name)
		if !ok {
			continue
		}
		differ := 0
		for _, s := range preset.Segments {
			if on[s.ID()] != s.On {
				differ++
			}
		}
		if least < 0 || differ < least {
			best, least = name, differ
		}
	}
	return best
}

// SetLive replaces the segments' current values, keyed by Segment.ID. The
// map is read and never changed.
func (m *Model) SetLive(live map[string]statusline.Value) { m.live = live }

// SaveFailed tells the screen that the config it handed back in Result.Save
// could not be written. The screen says why and counts its changes as
// unsaved again, so that leaving asks first.
func (m *Model) SaveFailed(why string) {
	if m.undo != nil {
		m.saved, m.undo = *m.undo, nil
	}
	m.asking = false
	m.note, m.tone = "not saved: "+why, toneBad
}

// Update handles one message. Outside a field only key presses count: up and
// down select, space toggles, left and right set the side, J and K move the
// segment down and up, e edits its settings, a adds a shell segment, d
// deletes one, p takes the next preset, s saves and esc leaves. While a
// field is edited every message goes to its input: enter accepts the field
// and moves to the next, up and down accept it and move, and esc cancels.
func (m *Model) Update(msg tea.Msg) (Result, tea.Cmd) {
	if m.ed != nil {
		return m.updateField(msg)
	}
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return Result{}, nil
	}
	asked := m.asking
	m.asking, m.note, m.tone = false, "", ""
	m.clamp()
	switch keyName(key) {
	case "up":
		m.sel = max(m.sel-1, 0)
	case "down":
		m.sel = min(m.sel+1, max(len(m.cfg.Segments)-1, 0))
	case "space":
		return m.toggle(), nil
	case "left":
		m.setSide(statusline.Left)
	case "right":
		m.setSide(statusline.Right)
	case "J", "shift+j":
		m.move(1)
	case "K", "shift+k":
		m.move(-1)
	case "e":
		m.edit()
	case "a":
		return m.add(), nil
	case "d":
		return m.remove(), nil
	case "p":
		return m.nextPreset(), nil
	case "s":
		return m.save(), nil
	case "esc":
		return m.leave(asked), nil
	}
	return Result{}, nil
}

// keyName names a key press the way the screen matches it: "up", "esc",
// "space", or the character typed. Text of more than one character, which an
// input method can hand over in one press, is never a key: the word "esc"
// typed that way must not close the screen.
func keyName(key tea.KeyPressMsg) string {
	if utf8.RuneCountInString(key.Text) > 1 {
		return ""
	}
	return key.String()
}

// clamp keeps the selection on a segment.
func (m *Model) clamp() {
	m.sel = max(min(m.sel, len(m.cfg.Segments)-1), 0)
}

// selected returns the selected segment, or nil when there is none.
func (m *Model) selected() *statusline.Segment {
	if m.sel < 0 || m.sel >= len(m.cfg.Segments) {
		return nil
	}
	return &m.cfg.Segments[m.sel]
}

// dirty says there are changes that were not saved.
func (m *Model) dirty() bool {
	return !slices.Equal(m.cfg.Segments, m.saved.Segments)
}

// say sets the note the screen shows until the next key press.
func (m *Model) say(tone, text string) Result {
	m.note, m.tone = text, tone
	return Result{Note: text}
}

// toggle switches the selected segment on or off.
func (m *Model) toggle() Result {
	seg := m.selected()
	if seg == nil {
		return Result{}
	}
	seg.On = !seg.On
	if seg.On && kindOf(*seg) == kindShell && seg.Command == "" {
		return m.say(toneWarn, seg.Name+" has no command yet: e gives it one")
	}
	return Result{}
}

// setSide puts the selected segment at one edge of the bar.
func (m *Model) setSide(side statusline.Side) {
	if seg := m.selected(); seg != nil {
		seg.Side = side
	}
}

// move takes the selected segment one place down the list, or up, and the
// selection with it.
func (m *Model) move(by int) {
	to := m.sel + by
	if m.selected() == nil || to < 0 || to >= len(m.cfg.Segments) {
		return
	}
	segments := m.cfg.Segments
	segments[m.sel], segments[to] = segments[to], segments[m.sel]
	m.sel = to
}

// beforeClock is where a new segment goes: before a clock on the right, as
// WithMods places a mod's, or at the end.
func beforeClock(segments []statusline.Segment) int {
	at := slices.IndexFunc(segments, func(s statusline.Segment) bool {
		return s.ID() == "core:clock" && s.Side == statusline.Right
	})
	if at < 0 {
		return len(segments)
	}
	return at
}

// add puts a new shell segment in the list and opens its fields, the name
// first.
func (m *Model) add() Result {
	extra := 0
	for _, s := range m.cfg.Segments {
		if kindOf(s) != kindCore {
			extra++
		}
	}
	if extra >= statusline.MaxSegments {
		return m.say(toneBad, "a status line holds at most "+strconv.Itoa(statusline.MaxSegments)+" mod and shell segments")
	}
	seg := statusline.Segment{
		From: statusline.FromShell, On: true, Side: statusline.Right, Drop: 1,
		Interval: statusline.DefaultInterval, Timeout: statusline.DefaultTimeout,
	}
	at, back := beforeClock(m.cfg.Segments), m.sel
	m.cfg.Segments = slices.Insert(m.cfg.Segments, at, seg)
	m.sel = at
	m.open(0)
	m.ed.added, m.ed.back = true, back
	return Result{}
}

// remove deletes the selected segment, when it is a shell segment. Core and
// mod segments stay listed: they can only be switched off.
func (m *Model) remove() Result {
	seg := m.selected()
	if seg == nil {
		return Result{}
	}
	if kindOf(*seg) != kindShell {
		return m.say(toneWarn, "only a shell segment can be deleted: space switches "+seg.Name+" off")
	}
	name := seg.Name
	m.cfg.Segments = slices.Delete(m.cfg.Segments, m.sel, m.sel+1)
	m.clamp()
	return m.say("", "deleted "+name)
}

// nextPreset replaces the config with the preset after the current one. The
// segments a preset does not list, those of mods and the shell segments
// added here, are kept as they are, before the clock.
func (m *Model) nextPreset() Result {
	names := statusline.Presets()
	name := names[(slices.Index(names, m.preset)+1)%len(names)]
	preset, ok := statusline.Preset(name)
	if !ok {
		return Result{}
	}
	listed := make(map[string]bool, len(preset.Segments))
	for _, s := range preset.Segments {
		listed[s.ID()] = true
	}
	var kept []statusline.Segment
	for _, s := range m.cfg.Segments {
		if !listed[s.ID()] {
			kept = append(kept, s)
		}
	}
	id := ""
	if seg := m.selected(); seg != nil {
		id = seg.ID()
	}
	at := beforeClock(preset.Segments)
	m.cfg.Segments = slices.Concat(preset.Segments[:at], kept, preset.Segments[at:])
	m.preset = name
	if i := slices.IndexFunc(m.cfg.Segments, func(s statusline.Segment) bool { return s.ID() == id }); i >= 0 {
		m.sel = i
	}
	m.clamp()
	return m.say("", "preset "+name)
}

// save hands the config back for writing, once every segment in it is one
// the file can hold.
func (m *Model) save() Result {
	seen := make(map[string]bool, len(m.cfg.Segments))
	extra := 0
	for i, s := range m.cfg.Segments {
		if err := s.Validate(); err != nil {
			m.sel = i
			return m.say(toneBad, "not saved: "+s.Name+": "+err.Error())
		}
		if seen[s.ID()] {
			m.sel = i
			return m.say(toneBad, "not saved: "+s.Name+" is listed twice")
		}
		seen[s.ID()] = true
		if kindOf(s) != kindCore {
			extra++
		}
	}
	out := statusline.Config{Segments: slices.Clone(m.cfg.Segments)}
	before := m.saved
	m.undo = &before
	m.saved = statusline.Config{Segments: slices.Clone(m.cfg.Segments)}
	res := m.say("", "saved")
	if extra > statusline.MaxSegments {
		res = m.say(toneWarn, "saved, but only the first "+strconv.Itoa(statusline.MaxSegments)+" mod and shell segments are read back")
	}
	res.Save = &out
	return res
}

// leave is esc: it is done at once when everything is saved, and asks once
// when it is not.
func (m *Model) leave(asked bool) Result {
	switch {
	case !m.dirty():
		return Result{Done: true}
	case !asked:
		m.asking = true
		return m.say(toneWarn, "not saved: s saves, esc again leaves without saving")
	}
	return Result{Done: true, Note: "the status line changes were not saved"}
}
