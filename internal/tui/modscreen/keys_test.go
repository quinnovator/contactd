package modscreen_test

import (
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/quinnovator/contactd/internal/tui/mods"
	"github.com/quinnovator/contactd/internal/tui/modscreen"
)

func TestKeysReturnActions(t *testing.T) {
	t.Parallel()
	toggle := func(name string) *modscreen.Action {
		return &modscreen.Action{Kind: modscreen.ActToggle, Name: name}
	}
	update := func(name string) *modscreen.Action {
		return &modscreen.Action{Kind: modscreen.ActUpdate, Name: name}
	}
	tests := []struct {
		name     string
		empty    bool
		before   []string
		key      string
		wantDone bool
		want     *modscreen.Action
	}{
		{name: "space toggles the first mod", key: "space", want: toggle("cache")},
		{name: "u updates the first mod", key: "u", want: update("cache")},
		{name: "down chooses the next mod", before: []string{"down"}, key: "space", want: toggle("diff")},
		{name: "j is down", before: []string{"j", "j"}, key: "u", want: update("git")},
		{name: "up goes back", before: []string{"down", "down", "up"}, key: "space", want: toggle("diff")},
		{name: "k is up", before: []string{"down", "down", "down", "k"}, key: "u", want: update("git")},
		{name: "up stops at the first mod", before: []string{"up", "up"}, key: "space", want: toggle("cache")},
		{
			name:   "down stops at the last mod",
			before: []string{"down", "down", "down", "down", "down", "down", "down"},
			key:    "space", want: toggle("standup"),
		},
		{name: "a mod that is off is toggled like any other", before: []string{"down", "down", "down"}, key: "space", want: toggle("mermaid")},
		{name: "esc leaves", key: "esc", wantDone: true},
		{name: "esc leaves after moving", before: []string{"down", "enter"}, key: "esc", wantDone: true},
		{name: "enter asks for nothing on a wide screen", key: "enter"},
		{name: "i asks for nothing yet", key: "i"},
		{name: "a key with no meaning", key: "x"},
		{name: "left has no meaning", key: "left"},
		{name: "space with no mods", empty: true, key: "space"},
		{name: "u with no mods", empty: true, key: "u"},
		{name: "down with no mods", empty: true, before: []string{"down", "up"}, key: "space"},
		{name: "enter with no mods", empty: true, key: "enter"},
		{name: "esc with no mods", empty: true, key: "esc", wantDone: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			infos := fiveMods()
			if tt.empty {
				infos = nil
			}
			m := modscreen.New(infos, dirLabel, false)
			m.View(darkTheme(), 100, 30)
			press(t, m, tt.before...)
			done, act, cmd := m.Update(key(tt.key))
			if done != tt.wantDone {
				t.Errorf("done = %v, want %v", done, tt.wantDone)
			}
			if cmd != nil {
				t.Error("a command was returned; nothing on this screen needs one")
			}
			switch {
			case tt.want == nil && act != nil:
				t.Errorf("action = %+v, want none", *act)
			case tt.want != nil && act == nil:
				t.Errorf("no action, want %+v", *tt.want)
			case tt.want != nil && *act != *tt.want:
				t.Errorf("action = %+v, want %+v", *act, *tt.want)
			}
		})
	}
}

func TestOtherMessagesChangeNothing(t *testing.T) {
	t.Parallel()
	msgs := map[string]tea.Msg{
		"a key release": tea.KeyReleaseMsg{Code: tea.KeySpace, Text: " "},
		"a paste":       tea.PasteMsg{Content: "u"},
		"focus":         tea.FocusMsg{},
		"nil":           nil,
	}
	for name, msg := range msgs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			m := modscreen.New(fiveMods(), dirLabel, false)
			before := text(m, darkTheme(), 100, 30)
			done, act, cmd := m.Update(msg)
			if done || act != nil || cmd != nil {
				t.Errorf("done %v, action %+v, command set %v", done, act, cmd != nil)
			}
			if after := text(m, darkTheme(), 100, 30); after != before {
				t.Errorf("the screen changed:\n%s", after)
			}
		})
	}
}

func TestSelectionIsDrawn(t *testing.T) {
	t.Parallel()
	th := darkTheme()
	m := modscreen.New(fiveMods(), dirLabel, false)
	names := []string{"cache", "diff", "git", "mermaid", "standup"}
	for i, name := range names {
		rows := screen(m, th, 100, 30)
		if !hasRow(rows, th.G.Pointer+" ", " "+name+" ") {
			t.Errorf("after %d downs the pointer is not on %s:\n%s", i, name, strings.Join(rows, "\n"))
		}
		if n := strings.Count(strings.Join(rows, "\n"), th.G.Pointer); n != 1 {
			t.Errorf("%d pointers on the screen, want 1", n)
		}
		// The details are the selected mod's.
		if got := details(th, rows)[3]; !strings.HasPrefix(got, name+" ") {
			t.Errorf("the details are headed %q, want %s", got, name)
		}
		press(t, m, "down")
	}
}

// The selected row is drawn the way theme.Selected draws a row, the full
// width of the list, and no other row is.
func TestSelectedRowIsFilled(t *testing.T) {
	t.Parallel()
	th := darkTheme()
	m := modscreen.New(fiveMods(), dirLabel, false)
	press(t, m, "down", "down")
	content, _ := m.View(th, 100, 30)
	fill := th.Selected("", 1)
	fill = fill[:strings.Index(fill, " ")]
	if fill == "" {
		t.Fatal("this theme has no fill to look for")
	}
	for i, row := range strings.Split(content, "\n") {
		selected := strings.Contains(row, th.G.Pointer)
		if got := strings.HasPrefix(row, fill); got != selected {
			t.Errorf("row %d starts with the fill: %v, want %v: %q", i, got, selected, row)
		}
		if selected && !strings.Contains(row, th.Style("accent").Render("git")) {
			t.Errorf("the selected row is not git's, in the accent tone: %q", row)
		}
		if selected && th.Width(row[:strings.Index(row, th.Style("rule").Render(th.G.Sep))]) != 50 {
			t.Errorf("the selected row is not 50 cells wide: %q", row)
		}
	}
}

func TestInstallInput(t *testing.T) {
	t.Parallel()
	th := darkTheme()
	install := func(path string) *modscreen.Action {
		return &modscreen.Action{Kind: modscreen.ActInstall, Path: path}
	}
	tests := []struct {
		name  string
		typed string
		paste string
		want  *modscreen.Action
	}{
		{name: "a path", typed: "dist/mods/cache", want: install("dist/mods/cache")},
		{name: "letters that are keys of the list are text", typed: "~/u i/k j", want: install("~/u i/k j")},
		{name: "space around the path is dropped", typed: "  ./mods/git  ", want: install("./mods/git")},
		{name: "a pasted path", paste: "/srv/mods/standup\n", want: install("/srv/mods/standup")},
		{name: "typed then pasted", typed: "~/", paste: "mods/git", want: install("~/mods/git")},
		{name: "nothing typed", typed: ""},
		{name: "only spaces", typed: "   "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := modscreen.New(fiveMods(), dirLabel, false)
			m.View(th, 100, 30)
			press(t, m, "down", "i")
			typeText(t, m, tt.typed)
			if tt.paste != "" {
				if done, act, cmd := m.Update(tea.PasteMsg{Content: tt.paste}); done || act != nil || cmd != nil {
					t.Fatalf("paste: done %v, action %+v, command set %v", done, act, cmd != nil)
				}
			}
			done, act, cmd := m.Update(key("enter"))
			if done || cmd != nil {
				t.Errorf("enter: done %v, command set %v", done, cmd != nil)
			}
			switch {
			case tt.want == nil && act != nil:
				t.Errorf("action = %+v, want none", *act)
			case tt.want != nil && act == nil:
				t.Errorf("no action, want %+v", *tt.want)
			case tt.want != nil && *act != *tt.want:
				t.Errorf("action = %+v, want %+v", *act, *tt.want)
			}
			// The input is closed: keys mean what they mean on the list, and
			// the selection is where it was.
			if _, cursor := m.View(th, 100, 30); cursor != nil {
				t.Errorf("cursor = %+v after the input closed", cursor)
			}
			rows := screen(m, th, 100, 30)
			if last := rows[len(rows)-1]; !strings.HasPrefix(last, "space on or off") {
				t.Errorf("the last row is not the list's keys: %q", last)
			}
			_, act, _ = m.Update(key("space"))
			if act == nil || *act != (modscreen.Action{Kind: modscreen.ActToggle, Name: "diff"}) {
				t.Errorf("space after the input = %+v, want a toggle of diff", act)
			}
		})
	}
}

func TestInstallInputEsc(t *testing.T) {
	t.Parallel()
	th := darkTheme()
	m := modscreen.New(fiveMods(), dirLabel, false)
	m.View(th, 100, 30)
	press(t, m, "i")
	typeText(t, m, "dist/mods/cache")
	// esc closes the input and stays on the screen.
	press(t, m, "esc")
	if got := text(m, th, 100, 30); strings.Contains(got, "dist/mods/cache") {
		t.Errorf("the path is still on the screen:\n%s", got)
	}
	// Opened again, the input is empty.
	press(t, m, "i")
	rows := screen(m, th, 100, 30)
	if last := rows[len(rows)-1]; !strings.HasPrefix(last, "install from a directory that holds a mod.toml") {
		t.Errorf("a reopened input is not empty: %q", last)
	}
	if _, act, _ := m.Update(key("enter")); act != nil {
		t.Errorf("enter on the reopened input = %+v, want no action", *act)
	}
	// A second esc, with no input open, leaves.
	if done, _, _ := m.Update(key("esc")); !done {
		t.Error("esc with no input open did not leave the screen")
	}
}

func TestInstallInputIsDrawn(t *testing.T) {
	t.Parallel()
	th := darkTheme()
	tests := []struct {
		name          string
		tmux          bool
		typed         string
		after         []string
		width, height int
		wantRow       string
		wantX, wantY  int
	}{
		{
			name: "empty", width: 100, height: 30,
			wantRow: "install from a directory that holds a mod.toml", wantX: 13, wantY: 29,
		},
		{
			name: "a path", typed: "dist/mods/cache", width: 100, height: 30,
			wantRow: "install from dist/mods/cache", wantX: 28, wantY: 29,
		},
		{
			name: "the cursor moved left", typed: "dist/mods/cache", after: []string{"left", "left"}, width: 100, height: 30,
			wantRow: "install from dist/mods/cache", wantX: 26, wantY: 29,
		},
		{
			name: "a character deleted", typed: "dist/mods/cachw", after: []string{"backspace", "e"}, width: 100, height: 30,
			wantRow: "install from dist/mods/cache", wantX: 28, wantY: 29,
		},
		{
			name: "above the tmux line", tmux: true, typed: "x", width: 100, height: 30,
			wantRow: "install from x", wantX: 14, wantY: 28,
		},
		{
			name: "no room for the keys", typed: "x", width: 60, height: 16,
			wantRow: "install from x", wantX: 14, wantY: 15,
		},
		{
			name: "no room for the label", typed: "abc", width: 20, height: 8,
			wantRow: "abc", wantX: 3, wantY: 7,
		},
		{
			name: "a path longer than the field ends at the cursor", typed: strings.Repeat("a/", 20) + "end", width: 20, height: 8,
			wantRow: "a/a/a/a/a/a/a/a/end", wantX: 19, wantY: 7,
		},
		{
			name: "one row is the input's", typed: "x", width: 40, height: 1,
			wantRow: "install from x", wantX: 14, wantY: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := modscreen.New(fiveMods(), dirLabel, tt.tmux)
			m.View(th, tt.width, tt.height)
			press(t, m, "i")
			typeText(t, m, tt.typed)
			press(t, m, tt.after...)
			content, cursor := m.View(th, tt.width, tt.height)
			rows := screen(m, th, tt.width, tt.height)
			if cursor == nil {
				t.Fatalf("no cursor with the input open:\n%s", content)
			}
			if cursor.X != tt.wantX || cursor.Y != tt.wantY {
				t.Errorf("cursor at %d,%d, want %d,%d", cursor.X, cursor.Y, tt.wantX, tt.wantY)
			}
			if cursor.Blink {
				t.Error("the cursor blinks")
			}
			if got := strings.TrimRight(rows[tt.wantY], " "); !strings.HasPrefix(got, tt.wantRow) {
				t.Errorf("row %d = %q, want it to start %q", tt.wantY, got, tt.wantRow)
			}
			if tt.width >= 100 && !strings.HasSuffix(rows[tt.wantY], "enter install "+th.G.Mid+" esc cancel") {
				t.Errorf("row %d = %q, want the input's keys at the right", tt.wantY, rows[tt.wantY])
			}
		})
	}
}

func TestNarrowScreenFocus(t *testing.T) {
	t.Parallel()
	th := darkTheme()
	const w, h = 60, 30
	m := modscreen.New(fiveMods(), dirLabel, false)

	list := func(when string) {
		t.Helper()
		got := text(m, th, w, h)
		if !strings.Contains(got, "VERSION") || !strings.Contains(got, "A MOD CAN ADD") || strings.Contains(got, "MAY READ") {
			t.Errorf("%s: want the list alone:\n%s", when, got)
		}
		if !strings.Contains(got, "enter details") || !strings.HasSuffix(got, "esc back") {
			t.Errorf("%s: want the list's keys:\n%s", when, got)
		}
	}
	detail := func(when, name string) {
		t.Helper()
		got := text(m, th, w, h)
		if strings.Contains(got, "VERSION") || strings.Contains(got, "A MOD CAN ADD") || !strings.Contains(got, "MAY READ") {
			t.Errorf("%s: want the details alone:\n%s", when, got)
		}
		if !strings.Contains(got, "\n "+name+" ") {
			t.Errorf("%s: want the details of %s:\n%s", when, name, got)
		}
		if strings.Contains(got, "enter details") || !strings.HasSuffix(got, "esc list") {
			t.Errorf("%s: want the details' keys:\n%s", when, got)
		}
	}

	list("at first")
	press(t, m, "enter")
	detail("after enter", "cache")
	press(t, m, "down")
	detail("after down", "diff")
	if _, act, _ := m.Update(key("space")); act == nil || *act != (modscreen.Action{Kind: modscreen.ActToggle, Name: "diff"}) {
		t.Errorf("space in the details = %+v, want a toggle of diff", act)
	}
	if _, act, _ := m.Update(key("u")); act == nil || *act != (modscreen.Action{Kind: modscreen.ActUpdate, Name: "diff"}) {
		t.Errorf("u in the details = %+v, want an update of diff", act)
	}
	press(t, m, "esc") // back to the list, not out of the screen
	list("after esc")
	press(t, m, "enter")
	detail("after a second enter", "diff")
	press(t, m, "enter")
	list("after enter in the details")
	if done, _, _ := m.Update(key("esc")); !done {
		t.Error("esc on the list did not leave the screen")
	}
}

// On a wide screen both panes are drawn, so esc always leaves at once: a
// focus taken while the screen was narrow does not outlive the resize.
func TestFocusEndsWhenTheScreenWidens(t *testing.T) {
	t.Parallel()
	th := darkTheme()
	resize := map[string]func(m *modscreen.Model){
		"by a draw":           func(m *modscreen.Model) { m.View(th, 100, 30) },
		"by a window message": func(m *modscreen.Model) { m.Update(tea.WindowSizeMsg{Width: 100, Height: 30}) },
	}
	for name, widen := range resize {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			m := modscreen.New(fiveMods(), dirLabel, false)
			m.View(th, 60, 30)
			press(t, m, "enter")
			widen(m)
			if got := text(m, th, 100, 30); !strings.Contains(got, "VERSION") || !strings.Contains(got, "MAY READ") {
				t.Errorf("want both panes at 100 columns:\n%s", got)
			}
			if done, _, _ := m.Update(key("esc")); !done {
				t.Error("esc on a wide screen did not leave")
			}
			// Narrow again: the list is back, not the details.
			if got := text(m, th, 60, 30); !strings.Contains(got, "VERSION") {
				t.Errorf("want the list after narrowing again:\n%s", got)
			}
		})
	}
}

func TestWidthFromWindowMessage(t *testing.T) {
	t.Parallel()
	th := darkTheme()
	m := modscreen.New(fiveMods(), dirLabel, false)
	// Before anything says how wide the screen is, enter has no meaning.
	press(t, m, "enter")
	if done, _, _ := m.Update(key("esc")); !done {
		t.Error("esc before the first draw did not leave")
	}
	if done, act, cmd := m.Update(tea.WindowSizeMsg{Width: 60, Height: 20}); done || act != nil || cmd != nil {
		t.Errorf("a window message: done %v, action %+v, command set %v", done, act, cmd != nil)
	}
	press(t, m, "enter")
	if got := text(m, th, 60, 20); !strings.Contains(got, "MAY READ") {
		t.Errorf("enter after a window message of 60 columns did not show the details:\n%s", got)
	}
}

func TestSetInfos(t *testing.T) {
	t.Parallel()
	th := darkTheme()
	extra := func(name string) mods.Info {
		return info(name, "0.0.1", "A mod that was just installed.", mods.StatusOff, mods.GrantClock)
	}
	without := func(infos []mods.Info, name string) []mods.Info {
		var out []mods.Info
		for _, in := range infos {
			if in.Manifest.Name != name {
				out = append(out, in)
			}
		}
		return out
	}
	tests := []struct {
		name    string
		before  []string
		install string
		next    []mods.Info
		want    string
	}{
		{name: "the same list", before: []string{"down", "down"}, next: fiveMods(), want: "git"},
		{name: "a mod before the selected one", before: []string{"down", "down"}, next: append([]mods.Info{extra("aaa")}, fiveMods()...), want: "git"},
		{name: "a mod before the selected one is gone", before: []string{"down", "down"}, next: without(fiveMods(), "cache"), want: "git"},
		{name: "the selected mod is gone", before: []string{"down", "down"}, next: without(fiveMods(), "git"), want: "mermaid"},
		{name: "the last mod was selected and is gone", before: []string{"down", "down", "down", "down"}, next: without(fiveMods(), "standup"), want: "mermaid"},
		{name: "a new mod without an install", next: append([]mods.Info{extra("aaa")}, fiveMods()...), want: "cache"},
		{name: "an installed mod is selected", install: "dist/mods/zebra", next: append(fiveMods(), extra("zebra")), want: "zebra"},
		{name: "an installed mod under another directory name", install: "~/src/new", next: append(fiveMods(), extra("zebra")), want: "zebra"},
		{name: "a mod installed again", install: "dist/mods/standup/", next: fiveMods(), want: "standup"},
		{name: "an install that brought nothing", before: []string{"down"}, install: "nowhere", next: fiveMods(), want: "diff"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := modscreen.New(fiveMods(), dirLabel, false)
			m.View(th, 100, 30)
			press(t, m, tt.before...)
			if tt.install != "" {
				press(t, m, "i")
				typeText(t, m, tt.install)
				if _, act, _ := m.Update(key("enter")); act == nil || act.Kind != modscreen.ActInstall {
					t.Fatalf("enter = %+v, want an install", act)
				}
			}
			m.SetInfos(tt.next)
			_, act, _ := m.Update(key("space"))
			if act == nil || act.Name != tt.want {
				t.Errorf("selected after SetInfos = %+v, want %s", act, tt.want)
			}
			if rows := screen(m, th, 100, 30); !hasRow(rows, th.G.Pointer+" ", " "+tt.want+" ") {
				t.Errorf("the pointer is not on %s:\n%s", tt.want, strings.Join(rows, "\n"))
			}
		})
	}
}

// An install is remembered for one list only: a later list with a new mod in
// it leaves the selection alone.
func TestInstallSelectsOnce(t *testing.T) {
	t.Parallel()
	m := modscreen.New(fiveMods(), dirLabel, false)
	press(t, m, "i")
	typeText(t, m, "nowhere")
	if _, act, _ := m.Update(key("enter")); act == nil {
		t.Fatal("enter returned no action")
	}
	m.SetInfos(fiveMods())
	m.SetInfos(append(fiveMods(), info("zebra", "1.0.0", "", mods.StatusOff)))
	if _, act, _ := m.Update(key("space")); act == nil || act.Name != "cache" {
		t.Errorf("selected = %+v, want cache", act)
	}
}

// The screen keeps its own copy of the list.
func TestInfosAreCopied(t *testing.T) {
	t.Parallel()
	infos := fiveMods()
	m := modscreen.New(infos, dirLabel, false)
	infos[0].Manifest.Name = "changed"
	if _, act, _ := m.Update(key("space")); act == nil || act.Name != "cache" {
		t.Errorf("after New: selected = %+v, want cache", act)
	}
	next := fiveMods()
	m.SetInfos(next)
	next[0].Manifest.Name = "changed"
	if _, act, _ := m.Update(key("space")); act == nil || act.Name != "cache" {
		t.Errorf("after SetInfos: selected = %+v, want cache", act)
	}
}

// The two panes stand side by side from 80 columns, and share the screen
// under that.
func TestEightyColumns(t *testing.T) {
	t.Parallel()
	th := darkTheme()
	tests := []struct {
		width int
		wide  bool
	}{{200, true}, {100, true}, {80, true}, {79, false}, {60, false}, {30, false}}
	for _, tt := range tests {
		t.Run(strconv.Itoa(tt.width), func(t *testing.T) {
			t.Parallel()
			m := modscreen.New(fiveMods(), dirLabel, false)
			first := text(m, th, tt.width, 40)
			if list, both := strings.Contains(first, "ADDS "), strings.Contains(first, "MAY READ"); !list || both != tt.wide {
				t.Errorf("at %d columns: the list is drawn: %v, the details too: %v:\n%s", tt.width, list, both, first)
			}
			if has := strings.Contains(first, th.G.Sep); has != tt.wide {
				t.Errorf("at %d columns: a column rule is drawn: %v", tt.width, has)
			}
			// enter changes the screen only where the panes share it.
			press(t, m, "enter")
			second := text(m, th, tt.width, 40)
			if changed := second != first; changed == tt.wide {
				t.Errorf("at %d columns: enter changed the screen: %v:\n%s", tt.width, changed, second)
			}
			if !strings.Contains(second, "MAY READ") {
				t.Errorf("at %d columns: no details after enter:\n%s", tt.width, second)
			}
			done, _, _ := m.Update(key("esc"))
			if done != tt.wide {
				t.Errorf("at %d columns: the first esc left the screen: %v", tt.width, done)
			}
		})
	}
}

// ctrl+v is not a paste key here: the input never runs a command to read a
// clipboard, and a paste comes from the terminal.
func TestInputRunsNoCommand(t *testing.T) {
	t.Parallel()
	m := modscreen.New(fiveMods(), dirLabel, false)
	press(t, m, "i")
	typeText(t, m, "dist/")
	for _, msg := range []tea.Msg{
		tea.KeyPressMsg{Code: 'v', Mod: tea.ModCtrl},
		tea.KeyPressMsg{Code: 'a', Mod: tea.ModCtrl},
		tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl},
		tea.KeyPressMsg{Code: tea.KeyTab},
		tea.KeyPressMsg{Code: tea.KeyUp},
		tea.PasteMsg{Content: "mods/"},
		tea.FocusMsg{},
	} {
		if done, act, cmd := m.Update(msg); done || act != nil || cmd != nil {
			t.Errorf("%#v: done %v, action %+v, command set %v", msg, done, act, cmd != nil)
		}
	}
	typeText(t, m, "git")
	_, act, _ := m.Update(key("enter"))
	if act == nil || *act != (modscreen.Action{Kind: modscreen.ActInstall, Path: "dist/mods/git"}) {
		t.Errorf("action = %+v, want an install of dist/mods/git", act)
	}
}

// The input takes a path of at most 1024 characters, however much is pasted.
func TestInputHasALimit(t *testing.T) {
	t.Parallel()
	th := darkTheme()
	m := modscreen.New(fiveMods(), dirLabel, false)
	press(t, m, "i")
	m.Update(tea.PasteMsg{Content: strings.Repeat("é/", 4000)})
	// The field is the 59 cells between the label and the input's keys.
	if _, cursor := m.View(th, 100, 30); cursor == nil || cursor.X != 13+58 || cursor.Y != 29 {
		t.Errorf("cursor = %+v, want it at the right edge of the field on the last row", cursor)
	}
	_, act, _ := m.Update(key("enter"))
	if act == nil || act.Kind != modscreen.ActInstall || act.Path != strings.Repeat("é/", 512) {
		t.Errorf("action = %+v, want an install of the first 1024 characters", act)
	}
}

// When the last mod goes while its details have the focus, the list is back
// and one esc leaves.
func TestFocusEndsWithTheLastMod(t *testing.T) {
	t.Parallel()
	th := darkTheme()
	m := modscreen.New(fiveMods()[:1], dirLabel, false)
	m.View(th, 60, 20)
	press(t, m, "enter")
	m.SetInfos(nil)
	if got := text(m, th, 60, 20); !strings.Contains(got, "no mods installed") {
		t.Errorf("want the empty list:\n%s", got)
	}
	if done, _, _ := m.Update(key("esc")); !done {
		t.Error("esc on the empty list did not leave the screen")
	}
}

// A note from the root stands where the key hints stand, in its tone, until
// the next key press. It is how the screen says what became of an action.
func TestNote(t *testing.T) {
	t.Parallel()
	th := darkTheme()
	const keys = "space on or off"
	tests := []struct {
		name          string
		text, tone    string
		tmux          bool
		width, height int
		// want is the rows the note takes, and wantTone the tone they are in.
		// With no rows the key hints are there.
		want     []string
		wantTone string
	}{
		{
			name: "a failed install", text: "install: dist/mods/cach holds no mod.toml", tone: "bad", width: 100, height: 30,
			want: []string{"install: dist/mods/cach holds no mod.toml"}, wantTone: "bad",
		},
		{
			name: "no tone is the warn tone", text: "git has no recorded source to update from", width: 100, height: 30,
			want: []string{"git has no recorded source to update from"}, wantTone: "warn",
		},
		{
			name: "a note of good news", text: "installed zebra: it is off", tone: "ok", width: 100, height: 30,
			want: []string{"installed zebra: it is off"}, wantTone: "ok",
		},
		{
			name: "above the tmux line", text: "install: not a directory", tone: "bad", tmux: true, width: 100, height: 30,
			want: []string{"install: not a directory"}, wantTone: "bad",
		},
		{
			name: "wrapped where a row does not hold it", text: "install: dist/mods/cach holds no mod.toml", tone: "bad",
			width: 30, height: 16,
			want: []string{"install: dist/mods/cach holds", "no mod.toml"}, wantTone: "bad",
		},
		{
			name: "three rows at most", text: strings.Repeat("no such file or directory ", 6), tone: "bad", width: 30, height: 16,
			want: []string{"no such file or directory no", "such file or directory no such", "file or directory no such fil…"}, wantTone: "bad",
		},
		{
			name: "kept on a screen too short for hints", text: "install: not a directory", tone: "bad", width: 60, height: 2,
			want: []string{"install: not a directory"}, wantTone: "bad",
		},
		{
			name: "made safe to draw", text: "install: \x1b[2J\x1b]0;owned\x07 failed\r\nagain", tone: "bad", width: 100, height: 30,
			want: []string{"install: [2J]0;owned failed again"}, wantTone: "bad",
		},
		{name: "no text is no note", text: "", tone: "bad", width: 100, height: 30},
		{name: "nothing to draw is no note", text: "\x1b\x07", tone: "bad", width: 100, height: 30},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := modscreen.New(fiveMods(), dirLabel, tt.tmux)
			m.View(th, tt.width, tt.height)
			m.SetNote(tt.text, tt.tone)
			content, cursor := m.View(th, tt.width, tt.height)
			if cursor != nil {
				t.Errorf("cursor = %+v with no input open", cursor)
			}
			rows := screen(m, th, tt.width, tt.height)
			if tt.tmux {
				rows = rows[:len(rows)-1]
			}
			if len(tt.want) == 0 {
				if last := rows[len(rows)-1]; !strings.HasPrefix(last, keys) {
					t.Errorf("the last row is %q, want the key hints", last)
				}
				return
			}
			if got := strings.Join(rows, "\n"); strings.Contains(got, keys) || strings.Contains(got, "esc back") {
				t.Errorf("the key hints are drawn beside a note:\n%s", got)
			}
			for i, want := range tt.want {
				if got := strings.TrimRight(rows[len(rows)-len(tt.want)+i], " "); got != want {
					t.Errorf("row %d of the note = %q, want %q", i, got, want)
				}
				if !strings.Contains(content, th.Style(tt.wantTone).Render(want)) {
					t.Errorf("%q is not in the %s tone", want, tt.wantTone)
				}
			}
			// The next key takes the note away, and still does what it does.
			press(t, m, "down")
			rows = screen(m, th, tt.width, tt.height)
			if got := strings.Join(rows, "\n"); strings.Contains(got, tt.want[0]) {
				t.Errorf("the note outlived a key press:\n%s", got)
			}
			if _, act, _ := m.Update(key("space")); act == nil || act.Name != "diff" {
				t.Errorf("the key that took the note away did not move the selection: %+v", act)
			}
		})
	}
}

// Only a key press takes a note away, and an open input stands in front of
// it.
func TestNoteStaysUntilAKey(t *testing.T) {
	t.Parallel()
	th := darkTheme()
	const note = "install: not a directory"
	m := modscreen.New(fiveMods(), dirLabel, false)
	m.SetNote(note, "bad")
	for _, msg := range []tea.Msg{
		tea.WindowSizeMsg{Width: 100, Height: 30},
		tea.PasteMsg{Content: "x"},
		tea.FocusMsg{},
		tea.KeyReleaseMsg{Code: tea.KeyDown},
		nil,
	} {
		m.Update(msg)
		if got := text(m, th, 100, 30); !strings.Contains(got, note) {
			t.Errorf("after %#v the note is gone:\n%s", msg, got)
		}
	}
	m.SetInfos(fiveMods())
	if got := text(m, th, 100, 30); !strings.Contains(got, note) {
		t.Errorf("after SetInfos the note is gone:\n%s", got)
	}
	m.SetNote("", "")
	if got := text(m, th, 100, 30); strings.Contains(got, note) || !strings.HasSuffix(got, "esc back") {
		t.Errorf("an empty note did not take the note away:\n%s", got)
	}
	// A note that arrives while the input is open waits behind it, and the
	// next key is typed into the input and takes the note away.
	press(t, m, "i")
	m.SetNote(note, "bad")
	got, cursor := m.View(th, 100, 30)
	if cursor == nil || strings.Contains(got, note) || !strings.Contains(got, "install from") {
		t.Errorf("want the open input in front of the note, cursor %+v:\n%s", cursor, got)
	}
	press(t, m, "x", "esc")
	if got := text(m, th, 100, 30); strings.Contains(got, note) || !strings.HasSuffix(got, "esc back") {
		t.Errorf("want the key hints after the input closed:\n%s", got)
	}
}

// After an install on a narrow screen the new mod's details take the list's
// place: what it may read is on the screen before anyone switches it on.
func TestInstallShowsDetailsOnANarrowScreen(t *testing.T) {
	t.Parallel()
	th := darkTheme()
	zebra := info("zebra", "0.0.1", "A mod that was just installed.", mods.StatusOff, mods.GrantMessages)
	tests := []struct {
		name    string
		width   int
		next    []mods.Info
		details bool
		list    bool
	}{
		{name: "narrow", width: 60, next: append(fiveMods(), zebra), details: true},
		{name: "narrow, and nothing arrived", width: 60, next: fiveMods(), list: true},
		{name: "wide", width: 100, next: append(fiveMods(), zebra), details: true, list: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := modscreen.New(fiveMods(), dirLabel, false)
			m.View(th, tt.width, 30)
			press(t, m, "i")
			typeText(t, m, "dist/mods/zebra")
			if _, act, _ := m.Update(key("enter")); act == nil || act.Kind != modscreen.ActInstall {
				t.Fatalf("enter = %+v, want an install", act)
			}
			m.SetInfos(tt.next)
			got := text(m, th, tt.width, 30)
			if has := strings.Contains(got, "zebra 0.0.1") && strings.Contains(got, "the last 20 messages"); has != tt.details {
				t.Errorf("the details of the new mod are on the screen: %v, want %v:\n%s", has, tt.details, got)
			}
			if has := strings.Contains(got, "VERSION"); has != tt.list {
				t.Errorf("the list is on the screen: %v, want %v:\n%s", has, tt.list, got)
			}
			if tt.width >= 80 || !tt.details {
				return
			}
			// esc goes back to the list, with the new mod selected.
			press(t, m, "esc")
			if rows := screen(m, th, tt.width, 30); !hasRow(rows, th.G.Pointer+" ", " zebra ") {
				t.Errorf("after esc the pointer is not on zebra:\n%s", strings.Join(rows, "\n"))
			}
		})
	}
}
