package modscreen_test

import (
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/quinnovator/contactd/internal/tui/mods"
	"github.com/quinnovator/contactd/internal/tui/modscreen"
	"github.com/quinnovator/contactd/internal/tui/theme"
)

// The words of the spec for what a mod never reaches.
const (
	neverWithMessages    = "no network, no files"
	neverWithoutMessages = "no messages, no network, no files"
)

// TestDrawing asserts the rows of the drawing that the golden file also
// holds, so that an -update cannot change them unseen.
func TestDrawing(t *testing.T) {
	t.Parallel()
	th := darkTheme()
	m := modscreen.New(fiveMods(), dirLabel, false)
	rows := screen(m, th, 100, 30)
	if len(rows) != 30 {
		t.Fatalf("%d rows, want 30", len(rows))
	}
	want := map[int]string{
		0:  "Mods" + strings.Repeat(" ", 59) + "~/.config/contactd/mods · 4 on, 1 off",
		1:  strings.Repeat("─", 100),
		2:  "",
		3:  "       MOD      VERSION ADDS                      │  cache 0.3.1",
		4:  " ▸ [x] cache    0.3.1   segment · /stats          │  How much of each prompt the provider",
		5:  "   [x] diff     1.0.0   renderer for code:diff    │  served from its cache.",
		7:  "   [ ] mermaid  0.1.2   renderer for code:mermaid │  ADDS",
		10: " A MOD CAN ADD                                    │                    day",
		11: " segment   one cell group in the status line      │",
		12: " command   a /name run from the input box         │  MAY READ",
		13: " renderer  how one kind of item is drawn          │  model-call records of the open contact",
		29: "space on or off · enter details · i install from a path · u update" + strings.Repeat(" ", 26) + "esc back",
	}
	for i, w := range want {
		if got := strings.TrimRight(rows[i], " "); got != w {
			t.Errorf("row %d:\n got %q\nwant %q", i, got, w)
		}
	}
	// The details, block by block, as the drawing has them.
	pane := details(th, rows)
	blocks := map[string][]string{
		"ADDS": {
			"segment  cache    status line",
			"command  /stats   token use and cache share by",
			"                  day",
		},
		"MAY READ": {"model-call records of the open contact", neverWithoutMessages},
		"RUNS": {
			"sandboxed WebAssembly, in this process",
			"5 ms for each update",
			"over budget three times in a row and",
			"it is switched off, with a notice",
		},
		"LAST 100 UPDATES": {"median 0.4 ms · slowest 1.9 ms"},
	}
	for heading, lines := range blocks {
		if got := section(t, pane, heading); !slices.Equal(got, lines) {
			t.Errorf("%s:\n got %q\nwant %q", heading, got, lines)
		}
	}
}

// TestTones checks the tones of the drawing: what is on is in the ok tone,
// what a mod adds is named in the mod tone, and the rest is dim.
func TestTones(t *testing.T) {
	t.Parallel()
	th := darkTheme()
	m := modscreen.New(fiveMods(), dirLabel, false)
	content, _ := m.View(th, 100, 30)
	tone := func(name, s string) string { return th.Style(name).Render(s) }
	want := map[string]string{
		"the title is bold":                     th.Title().Render("Mods"),
		"the directory and the count are dim":   tone("dim", "~/.config/contactd/mods · 4 on, 1 off"),
		"the pointer is in the accent tone":     tone("accent", "▸"),
		"a mod that is on has an ok box":        tone("ok", "[x]") + " diff",
		"a mod that is off has a dim box":       tone("dim", "[ ]") + " " + tone("dim", "mermaid"),
		"an unselected version is dim":          tone("dim", "1.0.0"),
		"the column rule is in the rule colour": tone("rule", "│"),
		"the details are headed in the accent":  tone("accent", "cache") + tone("dim", " 0.3.1"),
		"a description is dim":                  tone("dim", "served from its cache."),
		"a kind is in the mod tone":             tone("mod", "command") + "  /stats   " + tone("dim", "token use and cache share by"),
		"the legend's kinds are in the mod":     tone("mod", "renderer") + "  " + tone("dim", "how one kind of item is drawn"),
		"a heading is dim":                      tone("dim", "MAY READ"),
		"what is never read is dim":             tone("dim", neverWithoutMessages),
		"the notice about the budget is dim":    tone("dim", "it is switched off, with a notice"),
		"the keys are dim":                      tone("dim", "esc back"),
	}
	for name, s := range want {
		if !strings.Contains(content, s) {
			t.Errorf("%s: %q is not on the screen", name, s)
		}
	}
	// What a mod may read is body text: no colour at all.
	for row := range strings.SplitSeq(content, "\n") {
		if strings.Contains(row, "model-call records of the open contact") &&
			!strings.HasSuffix(strings.TrimRight(row, " "), "  model-call records of the open contact") {
			t.Errorf("a grant is styled: %q", row)
		}
	}
}

func TestMayRead(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		read []mods.Grant
		// wording is the spec's sentence for the grant; lines is how the
		// pane breaks it and what follows.
		wording string
		lines   []string
	}{
		{
			name: "contact", read: []mods.Grant{mods.GrantContact},
			wording: "the open contact's name and id",
			lines:   []string{"the open contact's name and id", neverWithoutMessages},
		},
		{
			name: "route", read: []mods.Grant{mods.GrantRoute},
			wording: "its provider and model",
			lines:   []string{"its provider and model", neverWithoutMessages},
		},
		{
			name: "state", read: []mods.Grant{mods.GrantState},
			wording: "whether it is connected and replying",
			lines:   []string{"whether it is connected and replying", neverWithoutMessages},
		},
		{
			name: "model_calls", read: []mods.Grant{mods.GrantModelCalls},
			wording: "model-call records of the open contact",
			lines:   []string{"model-call records of the open contact", neverWithoutMessages},
		},
		{
			name: "messages", read: []mods.Grant{mods.GrantMessages},
			wording: "the last 20 messages, when you run its command, and the items it draws",
			lines: []string{
				"the last 20 messages, when you run",
				"  its command, and the items it draws",
				neverWithMessages,
			},
		},
		{
			name: "workspace", read: []mods.Grant{mods.GrantWorkspace},
			wording: "the directory name and git branch where the client started",
			lines: []string{
				"the directory name and git branch",
				"  where the client started",
				neverWithoutMessages,
			},
		},
		{
			name: "clock", read: []mods.Grant{mods.GrantClock},
			wording: "the time",
			lines:   []string{"the time", neverWithoutMessages},
		},
		{name: "no grant", lines: []string{neverWithoutMessages}},
		{
			name: "every grant", read: mods.Grants(),
			lines: []string{
				"the open contact's name and id",
				"its provider and model",
				"whether it is connected and replying",
				"model-call records of the open contact",
				"the last 20 messages, when you run",
				"  its command, and the items it draws",
				"the directory name and git branch",
				"  where the client started",
				"the time",
				neverWithMessages,
			},
		},
		{
			name: "every grant but messages",
			read: []mods.Grant{
				mods.GrantContact, mods.GrantRoute, mods.GrantState, mods.GrantModelCalls, mods.GrantWorkspace, mods.GrantClock,
			},
			lines: []string{
				"the open contact's name and id",
				"its provider and model",
				"whether it is connected and replying",
				"model-call records of the open contact",
				"the directory name and git branch",
				"  where the client started",
				"the time",
				neverWithoutMessages,
			},
		},
		{
			name: "a grant this client does not know", read: []mods.Grant{"screen"},
			lines: []string{"screen", neverWithoutMessages},
		},
	}
	th := darkTheme()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			in := info("probe", "1.0.0", "A mod to look at.", mods.StatusOn, tt.read...)
			check := func(where string, rows []string) {
				t.Helper()
				got := section(t, details(th, rows), "MAY READ")
				if !slices.Equal(got, tt.lines) {
					t.Errorf("%s:\n got %q\nwant %q", where, got, tt.lines)
				}
				if tt.wording == "" {
					return
				}
				// Put back together, the lines are the spec's sentence.
				parts := make([]string, 0, len(got))
				for _, line := range got[:len(got)-1] {
					parts = append(parts, strings.TrimSpace(line))
				}
				if joined := strings.Join(parts, " "); joined != tt.wording {
					t.Errorf("%s: the grant reads %q, want %q", where, joined, tt.wording)
				}
			}
			wide := modscreen.New([]mods.Info{in}, dirLabel, false)
			check("beside the list", screen(wide, th, 100, 40))

			narrow := modscreen.New([]mods.Info{in}, dirLabel, false)
			narrow.View(th, 60, 40)
			press(t, narrow, "enter")
			check("in place of the list", screen(narrow, th, 60, 40))
		})
	}
}

// A grant's words are never cut: a pane too narrow for a line wraps it.
func TestMayReadIsWrappedNotCut(t *testing.T) {
	t.Parallel()
	th := darkTheme()
	in := info("probe", "1.0.0", "", mods.StatusOn, mods.Grants()...)
	for _, width := range []int{100, 80, 79, 60, 40, 30, 20} {
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			t.Parallel()
			m := modscreen.New([]mods.Info{in}, dirLabel, false)
			m.View(th, width, 80)
			press(t, m, "enter")
			got := section(t, details(th, screen(m, th, width, 80)), "MAY READ")
			words := strings.Join(strings.Fields(strings.Join(got, " ")), " ")
			want := strings.Join([]string{
				"the open contact's name and id",
				"its provider and model",
				"whether it is connected and replying",
				"model-call records of the open contact",
				"the last 20 messages, when you run its command, and the items it draws",
				"the directory name and git branch where the client started",
				"the time",
				neverWithMessages,
			}, " ")
			if words != want {
				t.Errorf("at %d columns the block reads\n%q\nwant\n%q", width, words, want)
			}
		})
	}
}

func TestReasons(t *testing.T) {
	t.Parallel()
	th := darkTheme()
	const overBudget = "over budget 3 times in a row, 2026-10-12 12:04"
	tests := []struct {
		name string
		in   mods.Info
		// heading and lines are the last block; tone is the tone of lines.
		heading string
		lines   []string
		tone    string
		// cell is the mod's last cell in the list.
		cell string
		// bare says the manifest could not be read, so nothing else is known.
		bare bool
	}{
		{
			name:    "failed to load",
			in:      withStatus(cacheMod(), mods.StatusFailed, "the module imports env.draw"),
			heading: "FAILED", lines: []string{"the module imports env.draw"}, tone: "bad", cell: "failed",
		},
		{
			name: "a manifest that could not be read",
			in: mods.Info{
				Manifest: mods.Manifest{Name: "cache"}, Status: mods.StatusFailed,
				Reason: `mod.toml: line 3: unknown grant "screen"`,
			},
			heading: "FAILED", lines: []string{"mod.toml: line 3: unknown grant", `"screen"`}, tone: "bad", cell: "failed",
			bare: true,
		},
		{
			name:    "failed with no reason",
			in:      withStatus(cacheMod(), mods.StatusFailed, ""),
			heading: "FAILED", lines: []string{"the host gave no reason"}, tone: "bad", cell: "failed",
		},
		{
			name:    "switched off by the host",
			in:      withStatus(cacheMod(), mods.StatusDisabled, overBudget),
			heading: "SWITCHED OFF", lines: []string{"over budget 3 times in a row,", "2026-10-12 12:04"}, tone: "bad",
			cell: "switched off",
		},
		{
			name:    "off because the module changed",
			in:      withStatus(cacheMod(), mods.StatusOff, "the module changed since it was switched on"),
			heading: "OFF", lines: []string{"the module changed since it was", "switched on"}, tone: "warn",
			cell: "segment · /stats",
		},
		{
			name:    "off",
			in:      withStatus(cacheMod(), mods.StatusOff, ""),
			heading: "LAST 100 UPDATES", lines: []string{"none: it is off"}, tone: "dim", cell: "segment · /stats",
		},
		{
			name:    "loading",
			in:      withStatus(cacheMod(), mods.StatusLoading, ""),
			heading: "LAST 100 UPDATES", lines: []string{"loading…"}, tone: "dim", cell: "loading…",
		},
		{
			name:    "on and never called",
			in:      withStatus(cacheMod(), mods.StatusOn, ""),
			heading: "LAST 100 UPDATES", lines: []string{"none yet"}, tone: "dim", cell: "segment · /stats",
		},
		{
			name:    "a status this client does not know",
			in:      withStatus(cacheMod(), mods.Status(99), ""),
			heading: "LAST 100 UPDATES", lines: []string{"none"}, tone: "dim", cell: "segment · /stats",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := modscreen.New([]mods.Info{tt.in}, dirLabel, false)
			content, _ := m.View(th, 100, 30)
			rows := strings.Split(ansi.Strip(content), "\n")
			pane := details(th, rows)
			if got := section(t, pane, tt.heading); !slices.Equal(got, tt.lines) {
				t.Errorf("%s:\n got %q\nwant %q", tt.heading, got, tt.lines)
			}
			for _, line := range tt.lines {
				if !strings.Contains(content, th.Style(tt.tone).Render(line)) {
					t.Errorf("%q is not drawn in the %s tone", line, tt.tone)
				}
			}
			// One last block only: a reason takes the place of the timings.
			for _, heading := range []string{"LAST 100 UPDATES", "FAILED", "SWITCHED OFF", "OFF"} {
				if has := slices.Contains(pane, heading); has != (heading == tt.heading) {
					t.Errorf("the details hold %q: %v", heading, has)
				}
			}
			if !strings.HasSuffix(strings.TrimRight(strings.Split(rows[4], th.G.Sep)[0], " "), " "+tt.cell) {
				t.Errorf("the mod's row is %q, want it to end %q", rows[4], tt.cell)
			}
			for _, heading := range []string{"ADDS", "MAY READ", "RUNS"} {
				if has := slices.Contains(pane, heading); has == tt.bare {
					t.Errorf("the details hold %q: %v", heading, has)
				}
			}
			if tt.bare && strings.Contains(content, neverWithoutMessages) {
				t.Error("an unread manifest is said to read nothing")
			}
		})
	}
}

// withStatus returns a mod in another state, with no calls behind it.
func withStatus(in mods.Info, status mods.Status, reason string) mods.Info {
	in.Status, in.Reason = status, reason
	in.Median, in.Slowest, in.Calls = 0, 0, 0
	return in
}

// The checkbox says whether a mod is switched on: a mod that failed or was
// switched off by the host shows an empty one, and is counted as off.
func TestCheckboxAndCount(t *testing.T) {
	t.Parallel()
	th := darkTheme()
	tests := []struct {
		status mods.Status
		on     bool
	}{
		{mods.StatusOff, false},
		{mods.StatusLoading, true},
		{mods.StatusOn, true},
		{mods.StatusFailed, false},
		{mods.StatusDisabled, false},
	}
	for _, tt := range tests {
		t.Run(tt.status.String(), func(t *testing.T) {
			t.Parallel()
			m := modscreen.New([]mods.Info{withStatus(cacheMod(), tt.status, "why")}, dirLabel, false)
			rows := screen(m, th, 100, 30)
			box, count := th.G.CheckOff, "0 on, 1 off"
			if tt.on {
				box, count = th.G.CheckOn, "1 on, 0 off"
			}
			if !strings.HasPrefix(rows[4], " "+th.G.Pointer+" "+box+" cache ") {
				t.Errorf("row = %q, want the box %q", rows[4], box)
			}
			if !strings.HasSuffix(rows[0], " "+th.G.Mid+" "+count) {
				t.Errorf("header = %q, want it to end %q", rows[0], count)
			}
		})
	}
}

// A failure is the last block, and the last block is never the one cut off
// by a short screen.
func TestReasonSurvivesShortScreens(t *testing.T) {
	t.Parallel()
	th := darkTheme()
	in := withStatus(cacheMod(), mods.StatusFailed, "the module imports env.draw")
	for _, size := range [][2]int{{100, 30}, {100, 16}, {100, 12}, {100, 8}, {100, 5}, {60, 16}, {60, 8}, {60, 5}, {30, 8}} {
		t.Run(strconv.Itoa(size[0])+"x"+strconv.Itoa(size[1]), func(t *testing.T) {
			t.Parallel()
			m := modscreen.New([]mods.Info{in}, dirLabel, false)
			m.View(th, size[0], size[1])
			press(t, m, "enter")
			got := text(m, th, size[0], size[1])
			if !strings.Contains(got, "FAILED") || !strings.Contains(strings.Join(strings.Fields(got), " "), "the module imports env.draw") {
				t.Errorf("the reason is not on the screen:\n%s", got)
			}
			if !strings.Contains(got, "cache") {
				t.Errorf("the mod's name is not on the screen:\n%s", got)
			}
		})
	}
}

// Blocks go whole, in a fixed order, as the pane gets shorter.
func TestDetailsDropOrder(t *testing.T) {
	t.Parallel()
	th := darkTheme()
	tests := []struct {
		height int
		want   []string
	}{
		// The whole pane is 21 rows, and the screen's own rows are 4.
		{30, []string{"How much", "ADDS", "MAY READ", "RUNS", "LAST 100 UPDATES"}},
		{25, []string{"How much", "ADDS", "MAY READ", "RUNS", "LAST 100 UPDATES"}},
		{24, []string{"How much", "ADDS", "MAY READ", "LAST 100 UPDATES"}},
		{19, []string{"How much", "ADDS", "MAY READ", "LAST 100 UPDATES"}},
		{18, []string{"ADDS", "MAY READ", "LAST 100 UPDATES"}},
		{17, []string{"ADDS", "MAY READ", "LAST 100 UPDATES"}},
		{16, []string{"MAY READ", "LAST 100 UPDATES"}},
		{11, []string{"MAY READ", "LAST 100 UPDATES"}},
		{10, []string{"LAST 100 UPDATES"}},
		{5, []string{"LAST 100 UPDATES"}},
	}
	for _, tt := range tests {
		t.Run(strconv.Itoa(tt.height), func(t *testing.T) {
			t.Parallel()
			m := modscreen.New(fiveMods(), dirLabel, false)
			pane := strings.Join(details(th, screen(m, th, 100, tt.height)), "\n")
			var got []string
			for _, mark := range []string{"How much", "ADDS", "MAY READ", "RUNS", "LAST 100 UPDATES"} {
				if strings.Contains(pane, "\n"+mark) {
					got = append(got, mark)
				}
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("at %d rows the details hold %q, want %q:\n%s", tt.height, got, tt.want, pane)
			}
			if !strings.Contains(pane, "median 0.4 ms") {
				t.Errorf("at %d rows the timings are gone:\n%s", tt.height, pane)
			}
		})
	}
}

// The tmux line is the last thing on the screen, dim, and only when asked
// for. Where one row does not hold it, it takes up to three and is not cut.
func TestTmuxLine(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		th            *theme.Theme
		asked         bool
		width, height int
		// want is the rows the line takes; none when it is not drawn.
		want []string
	}{
		{name: "asked for", th: darkTheme(), asked: true, width: 100, height: 30, want: []string{tmuxLine}},
		{name: "not asked for", th: darkTheme(), asked: false, width: 100, height: 30},
		{name: "asked for, ascii", th: asciiTheme(), asked: true, width: 100, height: 30, want: []string{tmuxLine}},
		{name: "asked for on a narrow screen", th: darkTheme(), asked: true, width: 60, height: 16, want: []string{tmuxLine}},
		{name: "not asked for on a narrow screen", th: darkTheme(), asked: false, width: 60, height: 16},
		{name: "asked for at nine rows", th: darkTheme(), asked: true, width: 100, height: 9, want: []string{tmuxLine}},
		{name: "no room under nine rows", th: darkTheme(), asked: true, width: 100, height: 8},
		{name: "it just fits one row", th: darkTheme(), asked: true, width: 58, height: 9, want: []string{tmuxLine}},
		{
			name: "one cell less and it takes two", th: darkTheme(), asked: true, width: 57, height: 16,
			want: []string{"images are half blocks here: tmux allow-passthrough is", "off"},
		},
		{
			name: "two rows at 40 columns", th: darkTheme(), asked: true, width: 40, height: 16,
			want: []string{"images are half blocks here: tmux", "allow-passthrough is off"},
		},
		{
			name: "two rows at 30 columns", th: darkTheme(), asked: true, width: 30, height: 10,
			want: []string{"images are half blocks here:", "tmux allow-passthrough is off"},
		},
		{name: "two rows need ten", th: darkTheme(), asked: true, width: 30, height: 9},
		{
			name: "three rows at most", th: darkTheme(), asked: true, width: 20, height: 16,
			want: []string{"images are half", "blocks here: tmux", "allow-passthrough i…"},
		},
		{name: "three rows need eleven", th: darkTheme(), asked: true, width: 20, height: 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := modscreen.New(fiveMods(), dirLabel, tt.asked)
			content, _ := m.View(tt.th, tt.width, tt.height)
			rows := strings.Split(ansi.Strip(content), "\n")
			if got := strings.Contains(ansi.Strip(content), "images are half"); got != (tt.want != nil) {
				t.Fatalf("the tmux line is on the screen: %v, want %v", got, tt.want != nil)
			}
			// It is the last thing on the screen, dim, under the keys.
			last := rows[len(rows)-len(tt.want):]
			for i, want := range tt.want {
				if got := strings.TrimRight(last[i], " "); got != want {
					t.Errorf("row %d of the tmux line = %q, want %q", i, got, want)
				}
				if !strings.Contains(content, tt.th.Style("dim").Render(want)) {
					t.Errorf("%q is not dim", want)
				}
			}
			if keys := rows[len(rows)-len(tt.want)-1]; !strings.HasSuffix(keys, "esc back") {
				t.Errorf("the key row is %q", keys)
			}
		})
	}
}

// The root can say later that the tmux line is wanted, or no longer wanted:
// the terminal may answer while the screen is open.
func TestSetTmuxNote(t *testing.T) {
	t.Parallel()
	th := darkTheme()
	m := modscreen.New(fiveMods(), dirLabel, false)
	press(t, m, "down")
	for _, on := range []bool{true, false, true} {
		m.SetTmuxNote(on)
		rows := screen(m, th, 100, 30)
		if got := strings.TrimRight(rows[29], " ") == tmuxLine; got != on {
			t.Errorf("after SetTmuxNote(%v) the last row is %q", on, rows[29])
		}
		if !hasRow(rows, th.G.Pointer+" ", " diff ") {
			t.Errorf("after SetTmuxNote(%v) the selection moved:\n%s", on, strings.Join(rows, "\n"))
		}
	}
}

func TestHeader(t *testing.T) {
	t.Parallel()
	th := darkTheme()
	long := "~/Library/Application Support/contactd/a rather long directory name/mods"
	tests := []struct {
		name  string
		label string
		width int
		want  string
	}{
		{"as drawn", dirLabel, 100, "Mods" + strings.Repeat(" ", 59) + "~/.config/contactd/mods · 4 on, 1 off"},
		{"just fits", dirLabel, 43, "Mods  ~/.config/contactd/mods · 4 on, 1 off"},
		{"the directory goes first", dirLabel, 42, "Mods" + strings.Repeat(" ", 27) + "4 on, 1 off"},
		{"the count just fits", dirLabel, 17, "Mods  4 on, 1 off"},
		{"then the count goes", dirLabel, 16, "Mods"},
		{"a long directory gives way to the count", long, 60, "Mods" + strings.Repeat(" ", 45) + "4 on, 1 off"},
		{"no directory", "", 100, "Mods" + strings.Repeat(" ", 85) + "4 on, 1 off"},
		{"narrower than the title", dirLabel, 3, "Mo…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := modscreen.New(fiveMods(), tt.label, false)
			if got := strings.TrimRight(screen(m, th, tt.width, 30)[0], " "); got != tt.want {
				t.Errorf("header at %d columns:\n got %q\nwant %q", tt.width, got, tt.want)
			}
		})
	}
}

func TestKeyRow(t *testing.T) {
	t.Parallel()
	th := darkTheme()
	const all = "space on or off · enter details · i install from a path · u update"
	tests := []struct {
		name  string
		empty bool
		// detail says the details have the focus, on a narrow screen.
		detail bool
		width  int
		left   string
		right  string
	}{
		{name: "as drawn", width: 100, left: all, right: "esc back"},
		{name: "all four just fit", width: 76, left: all, right: "esc back"},
		{name: "update goes first", width: 75, left: "space on or off · enter details · i install from a path", right: "esc back"},
		{name: "three just fit", width: 65, left: "space on or off · enter details · i install from a path", right: "esc back"},
		{name: "then install", width: 64, left: "space on or off · enter details", right: "esc back"},
		{name: "two just fit", width: 41, left: "space on or off · enter details", right: "esc back"},
		// On a narrow screen enter is the only way to what a mod may read,
		// so its hint outlives the toggle's.
		{name: "then the toggle", width: 40, left: "enter details", right: "esc back"},
		{name: "details just fits", width: 23, left: "enter details", right: "esc back"},
		{name: "esc is the last standing", width: 22, left: "", right: "esc back"},
		{name: "esc alone", width: 8, left: "", right: "esc back"},
		{name: "not even esc", width: 5, left: "", right: "esc …"},
		{name: "no mods, nothing to toggle", empty: true, width: 100, left: "i install from a path", right: "esc back"},
		{name: "no mods, install just fits", empty: true, width: 31, left: "i install from a path", right: "esc back"},
		{name: "no mods, install in short", empty: true, width: 30, left: "i install", right: "esc back"},
		{name: "no mods, the short one just fits", empty: true, width: 19, left: "i install", right: "esc back"},
		{name: "no mods, esc alone", empty: true, width: 18, left: "", right: "esc back"},
		{name: "details, both keys", detail: true, width: 60, left: "space on or off · u update", right: "esc list"},
		{name: "details, both just fit", detail: true, width: 36, left: "space on or off · u update", right: "esc list"},
		{name: "details, update goes", detail: true, width: 35, left: "space on or off", right: "esc list"},
		{name: "details, the toggle just fits", detail: true, width: 25, left: "space on or off", right: "esc list"},
		{name: "details, esc alone", detail: true, width: 24, left: "", right: "esc list"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			infos := fiveMods()
			if tt.empty {
				infos = nil
			}
			m := modscreen.New(infos, dirLabel, false)
			m.View(th, tt.width, 30)
			if tt.detail {
				press(t, m, "enter")
			}
			rows := screen(m, th, tt.width, 30)
			got := rows[len(rows)-1]
			want := tt.left + strings.Repeat(" ", max(0, tt.width-th.Width(tt.left)-th.Width(tt.right))) + tt.right
			if got != want {
				t.Errorf("key row at %d columns:\n got %q\nwant %q", tt.width, got, want)
			}
		})
	}
}

// With no mod installed the screen says how to install one, at every size
// that has room for the words: in the details beside the list, or in the
// list where it stands alone.
func TestNoModsSaysHowToInstall(t *testing.T) {
	t.Parallel()
	th := darkTheme()
	tests := []struct {
		width, height int
		// sentence says the whole sentence is there; otherwise the key hint
		// alone says it.
		sentence bool
		hint     string
	}{
		{100, 30, true, "i install from a path"},
		{100, 8, true, "i install from a path"},
		{80, 16, true, "i install from a path"},
		{79, 30, true, "i install from a path"},
		{60, 16, true, "i install from a path"},
		{60, 8, true, "i install from a path"},
		{40, 16, true, "i install from a path"},
		{30, 30, true, "i install"},
		{30, 8, true, "i install"},
		{30, 5, false, "i install"},
		{30, 3, false, "i install"},
		{20, 16, true, "i install"},
		{20, 8, false, "i install"},
	}
	for _, tt := range tests {
		t.Run(strconv.Itoa(tt.width)+"x"+strconv.Itoa(tt.height), func(t *testing.T) {
			t.Parallel()
			m := modscreen.New(nil, dirLabel, false)
			rows := screen(m, th, tt.width, tt.height)
			if !hasRow(rows, "no mods installed") {
				t.Errorf("the list does not say it is empty:\n%s", strings.Join(rows, "\n"))
			}
			body := strings.Join(rows[:len(rows)-1], "\n")
			if tt.width >= 80 {
				body = strings.Join(details(th, rows[:len(rows)-1]), "\n")
			}
			if got := strings.Contains(words(body), howInstall); got != tt.sentence {
				t.Errorf("the screen says how to install a mod: %v, want %v:\n%s", got, tt.sentence, strings.Join(rows, "\n"))
			}
			if last := rows[len(rows)-1]; !strings.HasPrefix(last, tt.hint+" ") || !strings.HasSuffix(last, "esc back") {
				t.Errorf("the key row is %q, want it to start %q", last, tt.hint)
			}
			// i opens the input, with no mod to stand on.
			press(t, m, "i")
			if _, cursor := m.View(th, tt.width, tt.height); cursor == nil {
				t.Error("i did not open the install input")
			}
		})
	}
}

// Beside the list the empty details say it once: the list does not repeat it.
func TestNoModsSaysItOnce(t *testing.T) {
	t.Parallel()
	m := modscreen.New(nil, dirLabel, false)
	if n := strings.Count(text(m, darkTheme(), 100, 30), "press i"); n != 1 {
		t.Errorf("the sentence is on the screen %d times, want 1", n)
	}
}

// The list's last cell says what a mod adds.
func TestAddsCell(t *testing.T) {
	t.Parallel()
	th := darkTheme()
	seg := func(names ...string) []mods.SegmentDecl {
		out := make([]mods.SegmentDecl, 0, len(names))
		for _, n := range names {
			out = append(out, mods.SegmentDecl{Name: n, Refresh: mods.RefreshTurn})
		}
		return out
	}
	cmd := func(names ...string) []mods.CommandDecl {
		out := make([]mods.CommandDecl, 0, len(names))
		for _, n := range names {
			out = append(out, mods.CommandDecl{Name: n, Summary: "what " + n + " does"})
		}
		return out
	}
	tests := []struct {
		name      string
		segments  []mods.SegmentDecl
		commands  []mods.CommandDecl
		renderers []mods.RendererDecl
		cell      string
		adds      []string
	}{
		{
			name: "a segment", segments: seg("branch"),
			cell: "segment", adds: []string{"segment  branch   status line"},
		},
		{
			name: "a command", commands: cmd("standup"),
			cell: "/standup", adds: []string{"command  /standup   what standup does"},
		},
		{
			name: "a renderer", renderers: []mods.RendererDecl{{Kind: "tool:python"}},
			cell: "renderer for tool:python", adds: []string{"renderer tool:python   transcript"},
		},
		{
			name: "a segment and a command", segments: seg("cache"), commands: cmd("stats"),
			cell: "segment · /stats",
			adds: []string{"segment  cache    status line", "command  /stats   what stats does"},
		},
		{
			name: "two segments and two commands", segments: seg("in", "out"), commands: cmd("a", "b"),
			cell: "2 segments · /a · /b",
			adds: []string{
				"segment  in    status line", "segment  out   status line",
				"command  /a    what a does", "command  /b    what b does",
			},
		},
		{name: "nothing", cell: "nothing", adds: []string{"nothing"}},
		{
			name:     "more than the cell holds",
			segments: seg("one"), commands: cmd("first", "second", "third"),
			cell: "segment · /first · /seco…",
			adds: []string{
				"segment  one       status line", "command  /first    what first does",
				"command  /second   what second does", "command  /third    what third does",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			in := info("probe", "1.0.0", "", mods.StatusOn)
			in.Manifest.Segments, in.Manifest.Commands, in.Manifest.Renderers = tt.segments, tt.commands, tt.renderers
			m := modscreen.New([]mods.Info{in}, dirLabel, false)
			rows := screen(m, th, 100, 30)
			if got := strings.TrimRight(strings.Split(rows[4], th.G.Sep)[0], " "); got != " ▸ [x] probe    1.0.0   "+tt.cell {
				t.Errorf("row = %q, want it to end %q", got, tt.cell)
			}
			if got := section(t, details(th, rows), "ADDS"); !slices.Equal(got, tt.adds) {
				t.Errorf("ADDS:\n got %q\nwant %q", got, tt.adds)
			}
		})
	}
}

// Nothing in the ADDS block is cut: a note that does not fit beside its name
// goes under it, and so does the note of a name too long for the name column.
func TestAddsLayout(t *testing.T) {
	t.Parallel()
	th := darkTheme()
	const longName = "a-command-with-a-name-of-32-char"
	const longKind = "code:a-language-whose-name-is-as-long-as-a-renderer-kind-may-be-64ch"
	long := cacheMod()
	long.Manifest.Commands = append(long.Manifest.Commands, mods.CommandDecl{Name: longName, Summary: "what it does"})
	long.Manifest.Renderers = []mods.RendererDecl{{Kind: longKind}}
	diff := fiveMods()[1]
	tests := []struct {
		name  string
		in    mods.Info
		width int
		want  []string
	}{
		{
			name: "as drawn", in: cacheMod(), width: 100,
			want: []string{
				"segment  cache    status line",
				"command  /stats   token use and cache share by",
				"                  day",
			},
		},
		{
			name: "a pane too narrow for three columns", in: cacheMod(), width: 80,
			want: []string{
				"segment  cache",
				"         status line",
				"command  /stats",
				"         token use and",
				"         cache share by day",
			},
		},
		{
			name: "in place of the list at 30 columns", in: diff, width: 30,
			want: []string{"renderer code:diff", "         transcript"},
		},
		{
			name: "in place of the list at 60 columns", in: diff, width: 60,
			want: []string{"renderer code:diff   transcript"},
		},
		{
			name: "long names have rows of their own", in: long, width: 100,
			want: []string{
				"segment  cache    status line",
				"command  /stats   token use and cache share by",
				"                  day",
				"command  /" + longName,
				"         what it does",
				"renderer " + longKind[:38],
				"         " + longKind[38:],
				"         transcript",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := modscreen.New([]mods.Info{tt.in}, dirLabel, false)
			m.View(th, tt.width, 60)
			press(t, m, "enter")
			got := section(t, details(th, screen(m, th, tt.width, 60)), "ADDS")
			if !slices.Equal(got, tt.want) {
				t.Errorf("ADDS at %d columns:\n got %q\nwant %q", tt.width, got, tt.want)
			}
		})
	}
}

func TestTimings(t *testing.T) {
	t.Parallel()
	th := darkTheme()
	us := time.Microsecond
	tests := []struct {
		median, slowest time.Duration
		want            string
	}{
		{400 * us, 1900 * us, "median 0.4 ms · slowest 1.9 ms"},
		{70 * us, 110 * us, "median 0.07 ms · slowest 0.1 ms"},
		{5 * time.Millisecond, 24 * time.Millisecond, "median 5 ms · slowest 24 ms"},
		{2040 * us, 9960 * us, "median 2 ms · slowest 10 ms"},
		{650 * us, 49500 * us, "median 0.7 ms · slowest 50 ms"},
		{3 * us, 400 * time.Millisecond, "median 0 ms · slowest 400 ms"},
		{0, 0, "median 0 ms · slowest 0 ms"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()
			in := cacheMod()
			in.Median, in.Slowest = tt.median, tt.slowest
			m := modscreen.New([]mods.Info{in}, dirLabel, false)
			got := section(t, details(th, screen(m, th, 100, 30)), "LAST 100 UPDATES")
			if !slices.Equal(got, []string{tt.want}) {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// The details state the time a mod gets for a call: the host's budget, or the
// mod's own where its manifest asks for less.
func TestBudget(t *testing.T) {
	t.Parallel()
	th := darkTheme()
	ms := time.Millisecond
	tests := []struct {
		name     string
		host     time.Duration
		manifest time.Duration
		want     string
	}{
		{name: "the default", want: "5 ms for each update"},
		{name: "the manifest names the default", manifest: 5 * ms, want: "5 ms for each update"},
		{name: "the manifest asks for less", manifest: 2 * ms, want: "2 ms for each update"},
		{name: "a manifest cannot raise it", manifest: 20 * ms, want: "5 ms for each update"},
		{name: "the host allows more", host: 10 * ms, want: "10 ms for each update"},
		{name: "the host allows more, the manifest less", host: 40 * ms, manifest: 2500 * time.Microsecond, want: "2.5 ms for each update"},
		{name: "a budget of nothing is not one", host: -1, want: "5 ms for each update"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			in := cacheMod()
			in.Manifest.Budget = tt.manifest
			m := modscreen.New([]mods.Info{in}, dirLabel, false)
			if tt.host != 0 {
				m.SetBudget(tt.host)
			}
			if got := section(t, details(th, screen(m, th, 100, 30)), "RUNS"); len(got) != 4 || got[1] != tt.want {
				t.Errorf("RUNS = %q, want its second row to be %q", got, tt.want)
			}
		})
	}
}

// With more mods than rows the list follows the selected one.
func TestListFollowsSelection(t *testing.T) {
	t.Parallel()
	th := darkTheme()
	for _, size := range [][2]int{{100, 30}, {100, 12}, {60, 8}, {100, 5}, {100, 2}, {100, 1}} {
		t.Run(strconv.Itoa(size[0])+"x"+strconv.Itoa(size[1]), func(t *testing.T) {
			t.Parallel()
			infos := manyMods(40)
			m := modscreen.New(infos, dirLabel, false)
			for i, in := range infos {
				rows := screen(m, th, size[0], size[1])
				if len(rows) != size[1] {
					t.Fatalf("%d rows, want %d", len(rows), size[1])
				}
				if !hasRow(rows, th.G.Pointer+" ", " "+in.Manifest.Name+" ") {
					t.Fatalf("after %d downs the selected mod %s is not on the screen:\n%s", i, in.Manifest.Name, strings.Join(rows, "\n"))
				}
				press(t, m, "down")
			}
			for _, in := range slices.Backward(infos) {
				rows := screen(m, th, size[0], size[1])
				if !hasRow(rows, th.G.Pointer+" ", " "+in.Manifest.Name+" ") {
					t.Fatalf("going up, the selected mod %s is not on the screen:\n%s", in.Manifest.Name, strings.Join(rows, "\n"))
				}
				press(t, m, "up")
			}
		})
	}
}

// The list's columns make room for a long name, and say so only as far as
// 18 cells.
func TestLongNames(t *testing.T) {
	t.Parallel()
	th := darkTheme()
	infos := []mods.Info{
		info("a-name-of-14ch", "1.0.0", "", mods.StatusOn),
		info("a-mod-with-the-longest-name-of-32", "2.0.0", "", mods.StatusOff),
	}
	m := modscreen.New(infos, dirLabel, false)
	rows := screen(m, th, 100, 30)
	want := []string{
		"       MOD               VERSION ADDS             │  a-name-of-14ch 1.0.0",
		" ▸ [x] a-name-of-14ch    1.0.0   nothing          │",
		"   [ ] a-mod-with-the-l… 2.0.0   nothing          │  ADDS",
	}
	for i, w := range want {
		if got := strings.TrimRight(rows[3+i], " "); got != w {
			t.Errorf("row %d:\n got %q\nwant %q", 3+i, got, w)
		}
	}
	// The details have the name whole.
	press(t, m, "down")
	if got := details(th, screen(m, th, 100, 30))[3]; got != "a-mod-with-the-longest-name-of-32 2.0.0" {
		t.Errorf("details headed %q", got)
	}
}

func TestNoSize(t *testing.T) {
	t.Parallel()
	for _, size := range [][2]int{{0, 30}, {100, 0}, {-1, 30}, {100, -4}, {0, 0}} {
		m := modscreen.New(fiveMods(), dirLabel, true)
		press(t, m, "i")
		if content, cursor := m.View(darkTheme(), size[0], size[1]); content != "" || cursor != nil {
			t.Errorf("View(%d, %d) = %q, %+v; want nothing", size[0], size[1], content, cursor)
		}
	}
}
