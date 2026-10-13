package modscreen_test

import (
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/quinnovator/contactd/internal/tui/mods"
	"github.com/quinnovator/contactd/internal/tui/modscreen"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
	"github.com/quinnovator/contactd/internal/tui/theme"
)

// state is one thing the screen can be showing.
type state struct {
	name  string
	infos []mods.Info
	label string
	// keys are pressed after the first draw, typed goes into the input they
	// open, and pasted is pasted there.
	keys   []string
	typed  string
	pasted string
	// note is a note the root sets after the keys.
	note string
	// typing says the input is open, so there is a cursor.
	typing bool
	// uneven says the fixture holds text the two ways of counting cells
	// disagree about, so only the theme's own count is checked.
	uneven bool
}

// states are the screens the matrix draws.
func states() []state {
	troubled := fiveMods()
	troubled[0] = withStatus(troubled[0], mods.StatusDisabled, "over budget 3 times in a row, 2026-10-12 12:04")
	troubled[1] = mods.Info{
		Manifest: mods.Manifest{Name: "diff"}, Status: mods.StatusFailed,
		Reason: "mod.toml: line 9: a renderer needs \"messages\" in read",
	}
	troubled[2] = withStatus(troubled[2], mods.StatusLoading, "")
	troubled[3] = withStatus(troubled[3], mods.StatusOff, "updated: switch it on to use the new version")

	return []state{
		{name: "list", infos: fiveMods(), label: dirLabel},
		{name: "details", infos: fiveMods(), label: dirLabel, keys: []string{"down", "enter"}},
		{name: "empty", label: dirLabel},
		{name: "forty mods", infos: manyMods(40), label: dirLabel, keys: []string{"down", "down", "down", "down", "down", "down", "down"}},
		{name: "a disabled mod", infos: troubled, label: dirLabel, keys: []string{"enter"}},
		{name: "an unread manifest", infos: troubled, label: dirLabel, keys: []string{"down", "enter"}},
		{name: "a loading mod", infos: troubled, label: dirLabel, keys: []string{"down", "down"}},
		{name: "an updated mod", infos: troubled, label: dirLabel, keys: []string{"down", "down", "down", "enter"}},
		{name: "input, empty", infos: fiveMods(), label: dirLabel, keys: []string{"i"}, typing: true},
		{name: "input, a path", infos: fiveMods(), label: dirLabel, keys: []string{"i"}, typed: "dist/mods/cache", typing: true},
		{
			name: "input, a long path", infos: fiveMods(), label: dirLabel, keys: []string{"i"},
			typed: strings.Repeat("a/very/long/path/", 12), typing: true,
		},
		{
			name: "input, the cursor in the middle of a long path", infos: fiveMods(), label: dirLabel,
			keys: []string{"i"}, pasted: strings.Repeat("src/", 60), typing: true,
		},
		{
			name: "input, with no mods", label: dirLabel, keys: []string{"i"}, typed: "~/mods/git", typing: true,
		},
		{name: "every grant", infos: []mods.Info{info("all", "9.9.9", "Reads all a mod may.", mods.StatusOn, mods.Grants()...)}, label: dirLabel, keys: []string{"enter"}},
		{name: "wide and hostile text", infos: hostileMods(), label: hostileLabel, keys: []string{"enter"}, uneven: true},
		{name: "a note", infos: fiveMods(), label: dirLabel, note: "install: no mod.toml in dist/mods/cach"},
		{
			name: "a long and hostile note", infos: fiveMods(), label: dirLabel, keys: []string{"enter"}, uneven: true,
			note: "install: \x1b[2J" + hostileLabel + ": " + strings.Repeat("no such file or directory, ", 12),
		},
		{name: "a note with no mods", label: dirLabel, note: "the mods directory cannot be read"},
		{name: "hostile names", infos: hostileNames(), label: hostileLabel, keys: []string{"down", "down", "enter"}, uneven: true},
		{
			name: "wide and hostile text, typed", infos: hostileMods(), label: hostileLabel,
			keys: []string{"i"}, pasted: "~/模组/👨\u200d👩\u200d👧/\u202egnp.x\x1b]0;t\x07/👍🏽", typing: true, uneven: true,
		},
	}
}

// hostileLabel is a directory label with an escape sequence, a bidi override
// and wide characters in it.
const hostileLabel = "~/\x1b[2J配置/\u202econtactd/mods\x1b]0;owned\x07"

// hostileMods are mods whose text tries to reach the terminal, or is wide.
// A real host has already made this text safe; the screen does not count on
// that.
func hostileMods() []mods.Info {
	evil := info("evil", "1.0\x1b[31m.0", "Clears \x1b[2J the screen\r\nand sets \x1b]0;a title\x07 and \x9b31m too.",
		mods.StatusFailed, mods.GrantMessages, mods.Grant("\x1b]52;c;AAAA\x07"))
	evil.Reason = "trap: \x1b[?1049h\x1bP1$r\x1b\\ unreachable \u202e txet desrever"
	evil.Manifest.Segments = []mods.SegmentDecl{{Name: "seg\x1b[5m", Refresh: mods.RefreshTurn}}
	evil.Manifest.Commands = []mods.CommandDecl{{Name: "cmd\x07", Summary: "rings \x07 the bell\tand tabs"}}
	evil.Manifest.Renderers = []mods.RendererDecl{{Kind: "code:\x1b_Gi=1;AAAA\x1b\\"}}

	wide := info("wide", "二点零", "这个模组把每一行都画成很宽的字符，用来测试换行和截断是否正确。 👨\u200d👩\u200d👧 👍🏽 e\u0301",
		mods.StatusOn, mods.GrantClock)
	wide.Manifest.Commands = []mods.CommandDecl{{Name: "宽", Summary: "一个很长很长很长很长很长很长很长很长很长的说明 👍🏽👍🏽👍🏽"}}
	wide.Manifest.Segments = []mods.SegmentDecl{{Name: "👨\u200d👩\u200d👧", Refresh: mods.RefreshTurn}}
	wide.Median, wide.Slowest, wide.Calls = 1, 2, 3
	return []mods.Info{evil, wide}
}

// hostileNames are mods whose names are not names a host would list: empty,
// with an escape sequence, a line break, a bidi override, wide characters and
// characters that take no cell. Each name is also the mod's version, what it
// adds, a grant and the reason it failed.
func hostileNames() []mods.Info {
	names := []string{
		"", " ", "\x1b[2J", "a\nb\tc", string(rune(0x202e)) + "evil", strings.Repeat(string(rune(0x5bbd)), 600),
		string([]rune{0x1f468, 0x200d, 0x1f469, 0x200d, 0x1f467}), string([]rune{'e', 0x301, 0x301, 0x301}),
		string([]rune{'a', 0x200b, 'b', 0xfeff, 'c', 0xad, 'd'}), string([]rune{'a', 0x2028, 'b', 0x2066, 'c'}), "\xff\x00",
	}
	out := make([]mods.Info, 0, len(names))
	for i, name := range names {
		status := mods.StatusOn
		if i%2 == 1 {
			status = mods.StatusFailed
		}
		in := info(name, name, name, status, mods.Grant(name))
		in.Reason = strings.Repeat(name+" ", 20)
		in.Manifest.Segments = []mods.SegmentDecl{{Name: name, Refresh: mods.RefreshTurn}}
		in.Manifest.Commands = []mods.CommandDecl{{Name: name, Usage: name, Summary: name}}
		in.Manifest.Renderers = []mods.RendererDecl{{Kind: name}}
		out = append(out, in)
	}
	return out
}

// themes are the four terminals of the matrix, and a fifth whose glyphs were
// replaced by wider ones.
func themes() map[string]*theme.Theme {
	out := make(map[string]*theme.Theme, 5)
	for _, c := range fourCaps() {
		out[c.name] = theme.New(c.caps, theme.Options{})
	}
	out["wide glyph overrides"] = theme.New(fourCaps()[0].caps, theme.Options{Glyph: map[string]string{
		"pointer": "==>", "check_on": "(x)", "check_off": "( )", "sep": "||", "mid": "::", "ellipsis": "~~", "hrule": "=",
	}})
	return out
}

// TestMatrix draws every state at every size on every terminal. Each screen
// is exactly as many rows as it was given, each row exactly as wide, and
// nothing but styles is in it.
func TestMatrix(t *testing.T) {
	t.Parallel()
	widths := []int{100, 80, 79, 60, 40, 30, 20, 10, 1, 200}
	heights := []int{30, 16, 12, 8, 5, 2, 1}
	for name, th := range themes() {
		for _, st := range states() {
			t.Run(name+"/"+st.name, func(t *testing.T) {
				t.Parallel()
				for _, w := range widths {
					for _, h := range heights {
						checkScreen(t, th, st, w, h)
					}
				}
			})
		}
	}
}

// checkScreen draws one state at one size and checks the frame.
func checkScreen(t *testing.T, th *theme.Theme, st state, w, h int) {
	t.Helper()
	at := strconv.Itoa(w) + "x" + strconv.Itoa(h)
	m := modscreen.New(st.infos, st.label, true)
	m.View(th, w, h)
	press(t, m, st.keys...)
	if st.typed != "" {
		typeText(t, m, st.typed)
	}
	if st.pasted != "" {
		m.Update(tea.PasteMsg{Content: st.pasted})
		for range 40 {
			press(t, m, "left")
		}
	}
	if st.note != "" {
		m.SetNote(st.note, "bad")
	}
	content, cursor := m.View(th, w, h)
	rows := strings.Split(content, "\n")
	if len(rows) != h {
		t.Errorf("%s: %d rows, want %d", at, len(rows), h)
	}
	for i, row := range rows {
		if got := th.Width(row); got != w {
			t.Errorf("%s: row %d is %d cells wide: %q", at, i, got, row)
		}
		if !st.uneven {
			if wc, gr := ansi.StringWidthWc(row), ansi.StringWidth(row); wc > w || gr > w {
				t.Errorf("%s: row %d is %d cells by wcwidth and %d by grapheme: %q", at, i, wc, gr, row)
			}
		}
	}
	// Styles are the only escape sequences, and no other control character
	// is there at all.
	bare := sgr().ReplaceAllString(content, "")
	for _, r := range bare {
		if r != '\n' && isControl(r) {
			t.Errorf("%s: the screen holds the control character %U:\n%q", at, r, content)
			break
		}
	}
	if th.Caps.Glyphs == termcaps.GlyphsASCII {
		for i := range len(content) {
			if content[i] >= 0x80 {
				t.Errorf("%s: with ASCII glyphs the screen holds byte %#x:\n%q", at, content[i], content)
				break
			}
		}
	}
	switch {
	case !st.typing && cursor != nil:
		t.Errorf("%s: cursor = %+v with no input open", at, cursor)
	case st.typing && cursor == nil:
		t.Errorf("%s: no cursor with the input open", at)
	case st.typing && (cursor.X < 0 || cursor.X >= w || cursor.Y < 0 || cursor.Y >= h):
		t.Errorf("%s: the cursor is at %d,%d, off the screen", at, cursor.X, cursor.Y)
	case st.typing && cursor.Y != inputRow(th, w, h):
		t.Errorf("%s: the cursor is on row %d, and the input on row %d", at, cursor.Y, inputRow(th, w, h))
	}
}

// isControl reports whether a terminal would act on r and not draw it: a
// control character, a line separator, or a character that changes the
// direction of text.
func isControl(r rune) bool {
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) ||
		r == 0x2028 || r == 0x2029 || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069)
}

// inputRow is the row the input is on: the last one, or the one above the
// tmux line where the screen is tall enough to have that line.
func inputRow(th *theme.Theme, w, h int) int {
	if note := noteRows(th, w); h >= 8+note {
		return h - 1 - note
	}
	return h - 1
}

// TestHostileText looks at the hostile fixture in particular: what its text
// says is still on the screen, as text, and nothing of what it tried to do.
func TestHostileText(t *testing.T) {
	t.Parallel()
	th := darkTheme()
	m := modscreen.New(hostileMods(), hostileLabel, false)
	content, _ := m.View(th, 100, 40)
	if bare := sgr().ReplaceAllString(content, ""); strings.ContainsAny(bare, "\x1b\x07\u009b\u202e\r\t") {
		t.Errorf("an escape or control character reached the screen:\n%q", content)
	}
	rows := strings.Split(ansi.Strip(content), "\n")
	if want := "~/[2J配置/contactd/mods]0;owned · 1 on, 1 off"; !strings.HasSuffix(rows[0], want) {
		t.Errorf("header = %q, want it to end %q", rows[0], want)
	}
	pane := strings.Join(strings.Fields(strings.Join(details(th, rows), " ")), " ")
	for _, want := range []string{
		"evil 1.0[31m.0 Clears [2J the screen and sets ]0;a title and",
		"segment seg[5m status line",
		"command /cmd rings the bell and tabs",
		"renderer code:_Gi=1;AAAA\\ transcript",
		"its command, and the items it draws ]52;c;AAAA no network, no files",
		"FAILED trap: [?1049hP1$r\\ unreachable txet desrever",
	} {
		if !strings.Contains(pane, want) {
			t.Errorf("%q is not in the details:\n%s", want, pane)
		}
	}
}
