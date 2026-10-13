package sledit

import (
	"maps"
	"slices"
	"strings"
	"unicode"

	"github.com/quinnovator/contactd/internal/tui/statusline"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
	"github.com/quinnovator/contactd/internal/tui/theme"
)

// sampleOut is what a sample shell segment's command printed, and the first
// group its expression found.
const sampleOut = "0.42"

// sampleSnapshot is a moment in which every core segment has something to
// show: the one the mock-ups draw.
func sampleSnapshot() statusline.Snapshot {
	return statusline.Snapshot{
		Conn: statusline.ConnLive, Contact: "ada", Provider: "deepseek", Model: "deepseek-flash",
		HasCall: true, CacheReported: true, Input: 3700, Cached: 3034, Output: 210, MS: 7200,
		Replying: true, Running: "python", Scrolled: true, Agents: 1,
		Clock: "12:04",
	}
}

// coreSamples returns a sample value for every core segment, drawn with a
// theme's glyphs.
func coreSamples(th *theme.Theme) map[string]statusline.Value {
	return statusline.CoreValues(sampleSnapshot(), statusline.Marks{Dot: th.G.Dot, Newer: th.G.Newer})
}

// sampleOf returns a value to show for a segment that has none right now.
// There is none for a mod's segment: only the mod knows what it shows.
func sampleOf(seg statusline.Segment, core map[string]statusline.Value) (statusline.Value, bool) {
	switch kindOf(seg) {
	case kindCore:
		v, ok := core[seg.ID()]
		return v, ok
	case kindShell:
		return statusline.Value{Vars: map[string]string{"out": sampleOut, "1": sampleOut}}, true
	case kindMod:
	}
	return statusline.Value{}, false
}

// current returns a segment's live value, when it has one with something to
// show.
func current(seg statusline.Segment, live map[string]statusline.Value) (statusline.Value, bool) {
	v, ok := live[seg.ID()]
	if !ok {
		return statusline.Value{}, false
	}
	if full, _ := seg.Text(v); full == "" {
		return statusline.Value{}, false
	}
	return v, true
}

// steady says a segment shows something most of the time, so that a preview
// should draw it even when it has no value yet. The segments that come and
// go with a reply are not steady, and neither is a mod's: one whose mod is
// off or missing is not drawn.
func steady(seg statusline.Segment) bool {
	switch kindOf(seg) {
	case kindCore:
		return !slices.Contains([]string{"activity", "stop", "scroll", "agents"}, seg.Name)
	case kindShell:
		return true
	case kindMod:
	}
	return false
}

// barValues returns the values a preview is drawn from: each segment's live
// value, or a sample for a steady segment that has none. So the preview is
// the bar as it is now, filled in where a first model call or a first run of
// a command is still to come.
func barValues(c statusline.Config, live, core map[string]statusline.Value) map[string]statusline.Value {
	vals := make(map[string]statusline.Value, len(c.Segments))
	for _, seg := range c.Segments {
		if v, ok := current(seg, live); ok {
			vals[seg.ID()] = v
			continue
		}
		if !steady(seg) {
			continue
		}
		if v, ok := sampleOf(seg, core); ok {
			vals[seg.ID()] = v
		}
	}
	return vals
}

// plainTheme returns the theme of the "no colour" preview: no colour, ASCII
// glyphs, and cells counted the way the screen's theme counts them.
func (m *Model) plainTheme(th *theme.Theme) *theme.Theme {
	if m.plain == nil || m.plain.Caps.Width != th.Caps.Width {
		m.plain = theme.New(termcaps.Caps{
			Color:      termcaps.ColorNone,
			Background: termcaps.BgUnknown,
			Glyphs:     termcaps.GlyphsASCII,
			Images:     termcaps.ImagesText,
			Width:      th.Caps.Width,
			CellW:      8,
			CellH:      16,
			Settled:    true,
		}, theme.Options{})
	}
	return m.plain
}

// folder rewrites text for a terminal that shows ASCII only. The "no colour"
// preview cannot ask a mod or a command for its value again, so it folds the
// values it has: a glyph of the theme becomes its ASCII stand-in, a letter
// or a digit with none becomes a question mark, and any other symbol is left
// out.
type folder struct{ glyphs *strings.Replacer }

// newFolder makes the folder from one glyph set to another.
func newFolder(from, to theme.Glyphs) folder {
	pairs := [][2]string{
		{from.Dot, to.Dot},
		{from.Newer, to.Newer},
		{from.Sep, to.Sep},
		{from.Ellipsis, to.Ellipsis},
		{from.Mid, to.Mid},
		{from.Bullet, to.Bullet},
		{from.Dash, to.Dash},
		{from.Times, to.Times},
		{from.Arrow, to.Arrow},
		{from.UpDown, to.UpDown},
		{from.LeftRight, to.LeftRight},
		{from.Pointer, to.Pointer},
		{from.Prompt, to.Prompt},
	}
	oldnew := make([]string, 0, 2*len(pairs))
	for _, p := range pairs {
		if p[0] != "" && p[0] != p[1] {
			oldnew = append(oldnew, p[0], p[1])
		}
	}
	return folder{glyphs: strings.NewReplacer(oldnew...)}
}

// text folds one piece of text. Text that is ASCII already comes back as it
// is. Text with nothing left in it becomes a question mark, so that it does
// not vanish.
func (f folder) text(s string) string {
	if isASCII(s) {
		return s
	}
	var b strings.Builder
	for _, r := range f.glyphs.Replace(s) {
		switch {
		case r < 0x80:
			b.WriteRune(r)
		case unicode.IsLetter(r) || unicode.IsNumber(r):
			b.WriteByte('?')
		}
	}
	folded := strings.Join(strings.Fields(b.String()), " ")
	if folded == "" {
		return "?"
	}
	return folded
}

// value folds everything a value can show.
func (f folder) value(v statusline.Value) statusline.Value {
	v.Text = f.text(v.Text)
	v.Vars = maps.Clone(v.Vars)
	for name, text := range v.Vars {
		v.Vars[name] = f.text(text)
	}
	return v
}

// values folds every value of a map into a new one.
func (f folder) values(vals map[string]statusline.Value) map[string]statusline.Value {
	out := make(map[string]statusline.Value, len(vals))
	for id, v := range vals {
		out[id] = f.value(v)
	}
	return out
}

// config folds the formats of every segment into a new config.
func (f folder) config(c statusline.Config) statusline.Config {
	segments := slices.Clone(c.Segments)
	for i := range segments {
		segments[i].Format = f.text(segments[i].Format)
		segments[i].Short = f.text(segments[i].Short)
	}
	return statusline.Config{Segments: segments}
}

func isASCII(s string) bool {
	for i := range len(s) {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}
