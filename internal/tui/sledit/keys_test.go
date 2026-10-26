package sledit_test

import (
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/quinnovator/contactd/internal/tui/sledit"
	"github.com/quinnovator/contactd/internal/tui/statusline"
	"github.com/quinnovator/contactd/internal/tui/theme"
)

// inputRow returns the row a field is edited in, on a screen where nothing
// had to give way.
func inputRow(t *testing.T, m *sledit.Model) string {
	t.Helper()
	th := themeOf(trueDark())
	content, cursor := m.View(th, 100, 40)
	if cursor == nil {
		t.Fatal("no cursor: no field is being edited")
	}
	return strings.Split(plainText(content), "\n")[cursor.Y]
}

// openField starts editing the selected segment and moves to a field.
func openField(t *testing.T, m *sledit.Model, label string) {
	t.Helper()
	send(t, m, char('e'))
	for range 12 {
		send(t, m, up())
	}
	for range 12 {
		if strings.HasPrefix(inputRow(t, m), label+" ") {
			return
		}
		send(t, m, down())
	}
	t.Fatalf("no field %q; the input row is %q", label, inputRow(t, m))
}

func TestSelect(t *testing.T) {
	t.Parallel()
	m := sledit.New(drawing(t), path, nil)
	if got := m.Selected(); got != "core:state" {
		t.Fatalf("a new editor selects %q, want the first segment", got)
	}
	send(t, m, up())
	if got := m.Selected(); got != "core:state" {
		t.Errorf("up at the top selects %q, want core:state", got)
	}
	send(t, m, down(), down())
	if got := m.Selected(); got != "core:route" {
		t.Errorf("two downs select %q, want core:route", got)
	}
	send(t, m, up())
	if got := m.Selected(); got != "core:contact" {
		t.Errorf("then up selects %q, want core:contact", got)
	}
	for range 40 {
		send(t, m, down())
	}
	if got := m.Selected(); got != "core:helpers" {
		t.Errorf("down past the end selects %q, want the last segment", got)
	}
	if m.Config().Segments[0].ID() != "core:state" || len(m.Config().Segments) != len(drawing(t).Segments) {
		t.Error("selecting changed the config")
	}
}

// One key each, on the mock-up's segments.
func TestKeys(t *testing.T) {
	t.Parallel()
	order := ids(drawing(t))
	at := func(id string) int { return slices.Index(order, id) }
	tests := []struct {
		name  string
		start string
		keys  []tea.Msg
		check func(t *testing.T, m *sledit.Model)
	}{
		{"space switches a segment off", "core:cache", []tea.Msg{space()}, func(t *testing.T, m *sledit.Model) {
			t.Helper()
			if segment(t, m, "core:cache").On {
				t.Error("cache is still on")
			}
		}},
		{"space switches a segment on", "core:tokens", []tea.Msg{space()}, func(t *testing.T, m *sledit.Model) {
			t.Helper()
			if !segment(t, m, "core:tokens").On {
				t.Error("tokens is still off")
			}
		}},
		{"space twice changes nothing", "core:cache", []tea.Msg{space(), space()}, func(t *testing.T, m *sledit.Model) {
			t.Helper()
			if !slices.Equal(m.Config().Segments, drawing(t).Segments) {
				t.Error("the config changed")
			}
		}},
		{"right moves a segment to the right side", "core:cache", []tea.Msg{right()}, func(t *testing.T, m *sledit.Model) {
			t.Helper()
			if got := segment(t, m, "core:cache").Side; got != statusline.Right {
				t.Errorf("side is %v, want right", got)
			}
		}},
		{"left moves a segment to the left side", "core:clock", []tea.Msg{left()}, func(t *testing.T, m *sledit.Model) {
			t.Helper()
			if got := segment(t, m, "core:clock").Side; got != statusline.Left {
				t.Errorf("side is %v, want left", got)
			}
		}},
		{"left on the left side changes nothing", "core:cache", []tea.Msg{left()}, func(t *testing.T, m *sledit.Model) {
			t.Helper()
			if !slices.Equal(m.Config().Segments, drawing(t).Segments) {
				t.Error("the config changed")
			}
		}},
		{"J moves a segment down", "core:cache", []tea.Msg{shift('j')}, func(t *testing.T, m *sledit.Model) {
			t.Helper()
			got := ids(m.Config())
			if got[at("core:cache")] != "core:tokens" || got[at("core:cache")+1] != "core:cache" {
				t.Errorf("order is %v, want cache after tokens", got)
			}
			if m.Selected() != "core:cache" {
				t.Errorf("selection is %q, want it to follow cache", m.Selected())
			}
		}},
		{"K moves a segment up", "core:cache", []tea.Msg{shift('k')}, func(t *testing.T, m *sledit.Model) {
			t.Helper()
			got := ids(m.Config())
			if got[at("core:cache")] != "core:route" || got[at("core:cache")-1] != "core:cache" {
				t.Errorf("order is %v, want cache before route", got)
			}
			if m.Selected() != "core:cache" {
				t.Errorf("selection is %q, want it to follow cache", m.Selected())
			}
		}},
		{"a capital without the shift flag moves too", "core:cache", []tea.Msg{char('J')}, func(t *testing.T, m *sledit.Model) {
			t.Helper()
			if got := ids(m.Config()); got[at("core:cache")+1] != "core:cache" {
				t.Errorf("order is %v, want cache one place down", got)
			}
		}},
		{"K at the top changes nothing", "core:state", []tea.Msg{shift('k')}, func(t *testing.T, m *sledit.Model) {
			t.Helper()
			if !slices.Equal(ids(m.Config()), order) {
				t.Errorf("order is %v", ids(m.Config()))
			}
		}},
		{"J at the bottom changes nothing", "core:helpers", []tea.Msg{shift('j')}, func(t *testing.T, m *sledit.Model) {
			t.Helper()
			if !slices.Equal(ids(m.Config()), order) {
				t.Errorf("order is %v", ids(m.Config()))
			}
		}},
		{"J then K puts a segment back", "core:cache", []tea.Msg{shift('j'), shift('k')}, func(t *testing.T, m *sledit.Model) {
			t.Helper()
			if !slices.Equal(ids(m.Config()), order) {
				t.Errorf("order is %v", ids(m.Config()))
			}
		}},
		{"lower-case j and k are not keys", "core:cache", []tea.Msg{char('j'), char('k'), char('x')}, func(t *testing.T, m *sledit.Model) {
			t.Helper()
			if !slices.Equal(m.Config().Segments, drawing(t).Segments) || m.Selected() != "core:cache" {
				t.Error("the config or the selection changed")
			}
		}},
		{"d deletes a shell segment", "shell:load", []tea.Msg{char('d')}, func(t *testing.T, m *sledit.Model) {
			t.Helper()
			if slices.Contains(ids(m.Config()), "shell:load") {
				t.Error("load is still listed")
			}
			if len(m.Config().Segments) != len(order)-1 {
				t.Errorf("%d segments are left, want %d", len(m.Config().Segments), len(order)-1)
			}
		}},
		{"d leaves a core segment", "core:cache", []tea.Msg{char('d')}, func(t *testing.T, m *sledit.Model) {
			t.Helper()
			if !slices.Equal(m.Config().Segments, drawing(t).Segments) {
				t.Error("the config changed")
			}
		}},
		{"d leaves a mod segment", "mod:git/branch", []tea.Msg{char('d')}, func(t *testing.T, m *sledit.Model) {
			t.Helper()
			if !slices.Equal(m.Config().Segments, drawing(t).Segments) {
				t.Error("the config changed")
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := sledit.New(drawing(t), path, nil)
			sel(t, m, tt.start)
			if res := send(t, m, tt.keys...); res.Done || res.Save != nil {
				t.Errorf("result is %+v, want neither done nor a save", res)
			}
			tt.check(t, m)
		})
	}
}

func TestDeleteSaysWhyNot(t *testing.T) {
	t.Parallel()
	th := themeOf(trueDark())
	m := newDrawing(t, th)
	res := send(t, m, char('d'))
	want := "only a shell segment can be deleted: space switches cache off"
	if res.Note != want {
		t.Errorf("note is %q, want %q", res.Note, want)
	}
	rows := screen(t, m, th, 100, 30)
	if got := strings.TrimRight(rows[len(rows)-1], " "); got != want {
		t.Errorf("the bottom row is %q, want the note", got)
	}
	send(t, m, down())
	rows = screen(t, m, th, 100, 30)
	if got := rows[len(rows)-1]; !strings.Contains(got, "esc back") {
		t.Errorf("after the next key the bottom row is %q, want the keys", got)
	}
}

// The caller's config is its own: the editor works on a copy.
func TestNewCopies(t *testing.T) {
	t.Parallel()
	c := drawing(t)
	m := sledit.New(c, path, nil)
	send(t, m, space(), shift('j'), right())
	if !slices.Equal(c.Segments, drawing(t).Segments) {
		t.Error("the caller's config changed")
	}
}

// A segment that leaves its side or its source out gets both named, since
// the table shows them and the file holds them.
func TestNewNamesSideAndFrom(t *testing.T) {
	t.Parallel()
	m := sledit.New(statusline.Config{Segments: []statusline.Segment{
		{Name: "cache", On: true},
		{Name: "clock", On: true},
		{Name: "branch", From: "mod:git", On: true},
		{Name: "load", From: statusline.FromShell, Command: "uptime", Interval: time.Minute, Timeout: time.Second},
	}}, path, nil)
	want := []struct {
		id   string
		from string
		side statusline.Side
	}{
		{"core:cache", "core", statusline.Left},
		{"core:clock", "core", statusline.Right},
		{"mod:git/branch", "mod:git", statusline.Right},
		{"shell:load", "shell", statusline.Right},
	}
	for _, w := range want {
		got := segment(t, m, w.id)
		if got.From != w.from || got.Side != w.side {
			t.Errorf("%s is from %q on the %v, want from %q on the %v", w.id, got.From, got.Side, w.from, w.side)
		}
	}
	// Naming them is not a change to save.
	if res := send(t, m, esc()); !res.Done {
		t.Errorf("esc gave %+v, want done: nothing was changed", res)
	}
}

func TestEditAccept(t *testing.T) {
	t.Parallel()
	m := sledit.New(drawing(t), path, nil)
	sel(t, m, "core:cache")
	send(t, m, char('e'))
	if !m.Editing() {
		t.Fatal("e did not open a field")
	}
	if got := inputRow(t, m); got != "format ❯ cache $pct%" {
		t.Fatalf("the input row is %q, want the format", got)
	}
	// Keys that mean something in the table are text here.
	send(t, m, typed(" J s p")...)
	if got := inputRow(t, m); got != "format ❯ cache $pct% J s p" {
		t.Fatalf("the input row is %q after typing", got)
	}
	if got := segment(t, m, "core:cache").Format; got != "cache $pct%" {
		t.Errorf("format is %q before enter, want it unchanged", got)
	}
	send(t, m, enter())
	if got := segment(t, m, "core:cache").Format; got != "cache $pct% J s p" {
		t.Errorf("format is %q after enter", got)
	}
	// Enter went on to the next field, and on through the rest.
	if got := inputRow(t, m); got != "short ❯ $pct%" {
		t.Fatalf("the input row is %q, want the short form", got)
	}
	send(t, m, enter())
	if got := inputRow(t, m); got != "warn below ❯ 50" {
		t.Fatalf("the input row is %q, want warn below", got)
	}
	send(t, m, enter())
	if got := inputRow(t, m); got != "drop ❯ 4" {
		t.Fatalf("the input row is %q, want drop", got)
	}
	send(t, m, enter())
	if m.Editing() {
		t.Error("enter on the last field did not end the editing")
	}
	want := drawing(t)
	for i, s := range want.Segments {
		if s.Name == "cache" {
			want.Segments[i].Format = "cache $pct% J s p"
		}
	}
	if !slices.Equal(m.Config().Segments, want.Segments) {
		t.Errorf("config is %+v, want only the format changed", m.Config().Segments)
	}
}

func TestEditCancel(t *testing.T) {
	t.Parallel()
	m := sledit.New(drawing(t), path, nil)
	sel(t, m, "core:cache")
	send(t, m, char('e'))
	send(t, m, typed("xyz")...)
	res := send(t, m, esc())
	if res.Done {
		t.Error("esc in a field closed the screen")
	}
	if m.Editing() {
		t.Error("esc did not end the editing")
	}
	if !slices.Equal(m.Config().Segments, drawing(t).Segments) {
		t.Error("a cancelled edit changed the config")
	}
	// Nothing changed, so the next esc leaves without asking.
	if res := send(t, m, esc()); !res.Done {
		t.Errorf("esc after a cancelled edit gave %+v, want done", res)
	}
}

// Esc cancels the field it is pressed in. The fields accepted before it stay.
func TestEditCancelKeepsAccepted(t *testing.T) {
	t.Parallel()
	m := sledit.New(drawing(t), path, nil)
	sel(t, m, "core:cache")
	send(t, m, char('e'))
	retype(t, m, "c $pct")
	send(t, m, enter())
	retype(t, m, "never kept")
	send(t, m, esc())
	got := segment(t, m, "core:cache")
	if got.Format != "c $pct" || got.Short != "$pct%" {
		t.Errorf("format %q and short %q, want the format kept and the short form as it was", got.Format, got.Short)
	}
}

// Up and down accept a field and move to its neighbour.
func TestEditMoveBetweenFields(t *testing.T) {
	t.Parallel()
	m := sledit.New(drawing(t), path, nil)
	sel(t, m, "core:cache")
	send(t, m, char('e'), down(), down())
	if got := inputRow(t, m); got != "warn below ❯ 50" {
		t.Fatalf("the input row is %q, want warn below", got)
	}
	retype(t, m, "30")
	send(t, m, up())
	if got := inputRow(t, m); got != "short ❯ $pct%" {
		t.Fatalf("the input row is %q, want the short form", got)
	}
	if got := segment(t, m, "core:cache").WarnBelow; got != 30 {
		t.Errorf("warn below is %d, want the 30 that up accepted", got)
	}
	// Down on the last field stays there; only enter ends the editing.
	send(t, m, down(), down(), down(), down())
	if got := inputRow(t, m); got != "drop ❯ 4" || !m.Editing() {
		t.Errorf("the input row is %q, want to stay on drop", got)
	}
	send(t, m, up(), up(), up(), up(), up())
	if got := inputRow(t, m); got != "format ❯ cache $pct%" {
		t.Errorf("the input row is %q, want to stay on the format", got)
	}
}

// What each field takes, and what it turns into.
func TestEditFields(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		segment      string
		field, typed string
		check        func(s statusline.Segment) bool
	}{
		{"format", "core:cache", "format", "$pct% cached", func(s statusline.Segment) bool { return s.Format == "$pct% cached" }},
		{"an empty format is the default", "core:cache", "format", "", func(s statusline.Segment) bool { return s.Format == "" }},
		{"short", "core:route", "short", "$model", func(s statusline.Segment) bool { return s.Short == "$model" }},
		{"warn below", "core:cache", "warn below", "25", func(s statusline.Segment) bool { return s.HasWarn && s.WarnBelow == 25 }},
		{"warn below, negative", "core:cache", "warn below", "-5", func(s statusline.Segment) bool { return s.HasWarn && s.WarnBelow == -5 }},
		{"no warn below", "core:cache", "warn below", "", func(s statusline.Segment) bool { return !s.HasWarn && s.WarnBelow == 0 }},
		{"warn below where there was none", "core:route", "warn below", " 7 ", func(s statusline.Segment) bool { return s.HasWarn && s.WarnBelow == 7 }},
		{"drop", "core:cache", "drop", "9", func(s statusline.Segment) bool { return s.Drop == 9 }},
		{"drop never", "core:cache", "drop", "0", func(s statusline.Segment) bool { return s.Drop == 0 }},
		{"a mod's format", "mod:git/branch", "format", "on $text", func(s statusline.Segment) bool { return s.Format == "on $text" }},
		{"command", "shell:load", "command", "sysctl -n vm.loadavg", func(s statusline.Segment) bool { return s.Command == "sysctl -n vm.loadavg" }},
		{"match", "shell:load", "match", `(\d+\.\d+)`, func(s statusline.Segment) bool { return s.Match == `(\d+\.\d+)` }},
		{"no match", "shell:load", "match", "", func(s statusline.Segment) bool { return s.Match == "" }},
		{"a shell segment's name", "shell:load", "name", "uptime", func(s statusline.Segment) bool { return s.Name == "uptime" }},
		{"interval", "shell:load", "interval", "1m30s", func(s statusline.Segment) bool {
			return s.Interval == 90*time.Second && s.Timeout == time.Second
		}},
		{"interval as bare seconds", "shell:load", "interval", "45", func(s statusline.Segment) bool { return s.Interval == 45*time.Second }},
		{"timeout", "shell:load", "timeout", "500ms", func(s statusline.Segment) bool { return s.Timeout == 500*time.Millisecond }},
		{"timeout up to the interval", "shell:load", "timeout", "10s", func(s statusline.Segment) bool { return s.Timeout == 10*time.Second }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := sledit.New(drawing(t), path, nil)
			sel(t, m, tt.segment)
			before := m.Selected()
			openField(t, m, tt.field)
			retype(t, m, tt.typed)
			if res := send(t, m, enter()); res.Note != "" {
				t.Fatalf("%q was refused: %s", tt.typed, res.Note)
			}
			got := m.Config().Segments[slices.Index(ids(drawing(t)), before)]
			if !tt.check(got) {
				t.Errorf("after typing %q in %s the segment is %+v", tt.typed, tt.field, got)
			}
			if err := got.Validate(); err != nil {
				t.Errorf("the segment no longer validates: %v", err)
			}
		})
	}
}

// A shorter interval takes the timeout down with it, since a run may not
// take longer than the wait between two runs.
func TestEditIntervalLowersTimeout(t *testing.T) {
	t.Parallel()
	m := sledit.New(drawing(t), path, nil)
	sel(t, m, "shell:load")
	openField(t, m, "timeout")
	retype(t, m, "8s")
	send(t, m, enter())
	openField(t, m, "interval")
	retype(t, m, "2s")
	if res := send(t, m, enter()); res.Note != "" {
		t.Fatalf("the interval was refused: %s", res.Note)
	}
	got := segment(t, m, "shell:load")
	if got.Interval != 2*time.Second || got.Timeout != 2*time.Second {
		t.Errorf("interval %v and timeout %v, want 2s and 2s", got.Interval, got.Timeout)
	}
}

// Text a field cannot take keeps the field open and says why.
func TestEditRefused(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		segment      string
		field, typed string
		want         string
	}{
		{"warn below is a number", "core:cache", "warn below", "low", "warn below takes a whole number"},
		{"drop is a number", "core:cache", "drop", "first", "drop takes a whole number"},
		{"drop is not empty", "core:cache", "drop", "", "drop takes a whole number"},
		{"drop is not negative", "core:cache", "drop", "-1", "drop is -1"},
		{"interval is a time", "shell:load", "interval", "soon", "interval takes a time"},
		{"interval is at least a second", "shell:load", "interval", "200ms", "the least is 1s"},
		{"interval is not negative", "shell:load", "interval", "-5", "interval takes a time"},
		{"interval is not endless", "shell:load", "interval", "99999999999999", "interval takes a time"},
		{"timeout is a time", "shell:load", "timeout", "1 parsec", "timeout takes a time"},
		{"timeout is not endless", "shell:load", "timeout", "99999999999999", "timeout takes a time"},
		{"timeout is more than nothing", "shell:load", "timeout", "0s", "it must be more than 0"},
		{"timeout is at most the interval", "shell:load", "timeout", "11s", "the most is the interval"},
		{"match is an expression", "shell:load", "match", "([0-9", "match is not an expression"},
		{"a name is not empty", "shell:load", "name", "", "a segment needs a name"},
		{"a name has no space", "shell:load", "name", "sys load", "has a space or a control character"},
		{"a format is not endless", "core:cache", "format", strings.Repeat("x", 300), "format is longer than 256 bytes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			th := themeOf(trueDark())
			m := sledit.New(drawing(t), path, nil)
			sel(t, m, tt.segment)
			openField(t, m, tt.field)
			retype(t, m, tt.typed)
			res := send(t, m, enter())
			if !strings.Contains(res.Note, tt.want) {
				t.Errorf("note is %q, want it to hold %q", res.Note, tt.want)
			}
			if !m.Editing() {
				t.Fatal("the field was closed")
			}
			if !slices.Equal(m.Config().Segments, drawing(t).Segments) {
				t.Error("refused text changed the config")
			}
			// The reason is on the screen, under the input, and goes with
			// the next character typed.
			content, cursor := m.View(th, 100, 40)
			rows := strings.Split(plainText(content), "\n")
			if got := rows[cursor.Y+1]; !strings.Contains(got, tt.want) {
				t.Errorf("the row under the input is %q, want the reason", got)
			}
			send(t, m, char('1'))
			content, cursor = m.View(th, 100, 40)
			rows = strings.Split(plainText(content), "\n")
			if got := rows[cursor.Y+1]; !strings.Contains(got, "enter accept") {
				t.Errorf("after typing, the row under the input is %q, want the keys", got)
			}
			// Down and up are refused the same way.
			retype(t, m, tt.typed)
			if res := send(t, m, down()); !strings.Contains(res.Note, tt.want) {
				t.Errorf("down gave the note %q, want %q", res.Note, tt.want)
			}
			if res := send(t, m, up()); !strings.Contains(res.Note, tt.want) {
				t.Errorf("up gave the note %q, want %q", res.Note, tt.want)
			}
		})
	}
}

// A field that was only looked at stays as it was, even when the input
// could not show all of it.
func TestEditUntouchedFieldIsKept(t *testing.T) {
	t.Parallel()
	c := drawing(t)
	const format = "a\tb\x1b[31m$pct"
	for i, s := range c.Segments {
		if s.Name == "cache" {
			c.Segments[i].Format = format
		}
	}
	m := sledit.New(c, path, nil)
	sel(t, m, "core:cache")
	send(t, m, char('e'), enter(), enter(), enter(), enter())
	if m.Editing() {
		t.Fatal("four enters did not pass the four fields")
	}
	if got := segment(t, m, "core:cache").Format; got != format {
		t.Errorf("format is %q, want %q untouched", got, format)
	}
}

func TestAddShell(t *testing.T) {
	t.Parallel()
	m := sledit.New(drawing(t), path, nil)
	sel(t, m, "core:cache")
	send(t, m, char('a'))
	if !m.Editing() {
		t.Fatal("a did not open the new segment's fields")
	}
	if got := inputRow(t, m); got != "name ❯ " {
		t.Fatalf("the input row is %q, want an empty name", got)
	}
	send(t, m, typed("disk")...)
	send(t, m, enter())
	if got := inputRow(t, m); got != "command ❯ " {
		t.Fatalf("the input row is %q, want an empty command", got)
	}
	send(t, m, typed("df -h / | tail -1")...)
	send(t, m, enter())
	if got := inputRow(t, m); got != "match ❯ " {
		t.Fatalf("the input row is %q, want an empty match", got)
	}
	send(t, m, typed(`(\d+)%`)...)
	send(t, m, enter())
	send(t, m, typed("disk $1%")...)
	// The rest keep their defaults.
	send(t, m, enter(), enter(), enter(), enter(), enter(), enter())
	if m.Editing() {
		t.Fatalf("still editing: %q", inputRow(t, m))
	}
	want := statusline.Segment{
		Name: "disk", From: statusline.FromShell, On: true, Side: statusline.Right, Drop: 1,
		Command: "df -h / | tail -1", Match: `(\d+)%`, Format: "disk $1%",
		Interval: statusline.DefaultInterval, Timeout: statusline.DefaultTimeout,
	}
	if got := segment(t, m, "shell:disk"); got != want {
		t.Errorf("the new segment is %+v, want %+v", got, want)
	}
	if err := want.Validate(); err != nil {
		t.Errorf("the new segment does not validate: %v", err)
	}
	// It goes before the clock, as a mod's segment does, and is selected.
	got := ids(m.Config())
	if n := len(got); got[n-3] != "shell:disk" || got[n-2] != "core:clock" {
		t.Errorf("order is %v, want the new segment before the clock", got)
	}
	if m.Selected() != "shell:disk" {
		t.Errorf("selection is %q, want the new segment", m.Selected())
	}
}

// With no clock at the right to stand before, a new segment goes last.
func TestAddShellWithoutARightClock(t *testing.T) {
	t.Parallel()
	c := drawing(t)
	for i, s := range c.Segments {
		if s.Name == "clock" {
			c.Segments[i].Side = statusline.Left
		}
	}
	m := sledit.New(c, path, nil)
	send(t, m, char('a'))
	send(t, m, typed("disk")...)
	send(t, m, enter(), esc())
	got := ids(m.Config())
	if n := len(got); n != len(c.Segments)+1 || got[n-1] != "shell:disk" || got[n-2] != "core:helpers" {
		t.Errorf("order is %v, want the new segment last", got)
	}
	if m.Selected() != "shell:disk" {
		t.Errorf("selection is %q, want the new segment", m.Selected())
	}
}

// Esc at the name takes the new segment out again; after the name it stays.
func TestAddCancel(t *testing.T) {
	t.Parallel()
	m := sledit.New(drawing(t), path, nil)
	sel(t, m, "core:cache")
	send(t, m, char('a'))
	send(t, m, typed("disk")...)
	send(t, m, esc())
	if m.Editing() {
		t.Error("esc did not end the editing")
	}
	if !slices.Equal(m.Config().Segments, drawing(t).Segments) {
		t.Errorf("segments are %v, want the new one gone", ids(m.Config()))
	}
	if m.Selected() != "core:cache" {
		t.Errorf("selection is %q, want it back on cache", m.Selected())
	}
	if res := send(t, m, esc()); !res.Done {
		t.Errorf("esc gave %+v, want done: an add that was cancelled is no change", res)
	}

	send(t, m, char('a'))
	send(t, m, typed("disk")...)
	send(t, m, enter(), esc())
	if !slices.Contains(ids(m.Config()), "shell:disk") {
		t.Errorf("segments are %v, want the named segment kept", ids(m.Config()))
	}
}

// A new segment cannot be left without a name, and not with another's.
func TestAddNeedsItsOwnName(t *testing.T) {
	t.Parallel()
	m := sledit.New(drawing(t), path, nil)
	send(t, m, char('a'))
	for _, k := range []tea.Msg{enter(), down(), up()} {
		if res := send(t, m, k); !strings.Contains(res.Note, "a segment needs a name") {
			t.Errorf("note is %q, want a name asked for", res.Note)
		}
		if got := inputRow(t, m); got != "name ❯ " {
			t.Fatalf("the input row is %q, want to stay on the name", got)
		}
	}
	send(t, m, typed("load")...)
	if res := send(t, m, enter()); !strings.Contains(res.Note, "there is a shell segment named load already") {
		t.Errorf("note is %q, want the clash named", res.Note)
	}
	// A core segment's name is free: the two have different sources.
	retype(t, m, "cache")
	if res := send(t, m, enter()); res.Note != "" {
		t.Errorf("the name cache was refused: %s", res.Note)
	}
	if !slices.Contains(ids(m.Config()), "shell:cache") {
		t.Errorf("segments are %v, want shell:cache", ids(m.Config()))
	}
}

// A file holds at most 32 mod and shell segments, so no more are added.
func TestAddStopsAtTheLimit(t *testing.T) {
	t.Parallel()
	c := preset(t, statusline.DefaultPreset) // one shell segment: load
	for i := 1; i < statusline.MaxSegments-1; i++ {
		c.Segments = append(c.Segments, statusline.Segment{
			Name: "s" + strconv.Itoa(i), From: statusline.FromShell, Side: statusline.Right, Command: "true",
			Interval: statusline.DefaultInterval, Timeout: statusline.DefaultTimeout,
		})
	}
	m := sledit.New(c, path, nil)
	send(t, m, char('a'))
	send(t, m, typed("last")...)
	send(t, m, enter(), esc())
	if n := len(m.Config().Segments); n != len(c.Segments)+1 {
		t.Fatalf("%d segments, want the 32nd added", n)
	}
	before := m.Config()
	res := send(t, m, char('a'))
	if !strings.Contains(res.Note, "at most 32 mod and shell segments") {
		t.Errorf("note is %q, want the limit", res.Note)
	}
	if m.Editing() || !slices.Equal(m.Config().Segments, before.Segments) {
		t.Error("a 33rd segment was added")
	}
}

func TestPreset(t *testing.T) {
	t.Parallel()
	th := themeOf(trueDark())
	m := sledit.New(preset(t, "minimal"), path, nil)
	underlined := func() string {
		t.Helper()
		content, _ := m.View(th, 100, 30)
		rows := strings.Split(content, "\n")
		for _, name := range statusline.Presets() {
			if strings.Contains(rows[len(rows)-2], th.Style("accent").Underline(true).Render(name)) {
				return name
			}
		}
		t.Fatalf("no preset is underlined in %q", rows[len(rows)-2])
		return ""
	}
	if got := underlined(); got != "minimal" {
		t.Errorf("%q is underlined at first, want minimal", got)
	}
	for _, want := range []string{"default", "ops", "minimal", "default"} {
		res := send(t, m, char('p'))
		if res.Note != "preset "+want {
			t.Errorf("note is %q, want preset %s", res.Note, want)
		}
		if !slices.Equal(m.Config().Segments, preset(t, want).Segments) {
			t.Errorf("after p the config is not the %s preset", want)
		}
		if got := underlined(); got != want {
			t.Errorf("%q is underlined, want %s", got, want)
		}
	}
}

// A preset lists the core segments and load. What else the config holds, a
// mod's segments and the shell segments added here, is kept.
func TestPresetKeepsOtherSegments(t *testing.T) {
	t.Parallel()
	c := drawing(t)
	disk := statusline.Segment{
		Name: "disk", From: statusline.FromShell, On: true, Side: statusline.Left, Drop: 3, Command: "df",
		Interval: statusline.DefaultInterval, Timeout: statusline.DefaultTimeout,
	}
	c.Segments = slices.Insert(c.Segments, 2, disk)
	m := sledit.New(c, path, nil)
	sel(t, m, "mod:git/branch")
	send(t, m, char('p'))
	got := m.Config()
	want := preset(t, "ops")
	branch := segment(t, sledit.New(drawing(t), path, nil), "mod:git/branch")
	n := len(want.Segments)
	want.Segments = slices.Concat(want.Segments[:n-2], []statusline.Segment{disk, branch}, want.Segments[n-2:])
	if !slices.Equal(got.Segments, want.Segments) {
		t.Errorf("segments are %v, want %v", ids(got), ids(want))
	}
	if m.Selected() != "mod:git/branch" {
		t.Errorf("selection is %q, want it to stay on the branch", m.Selected())
	}
	// A preset switches no shell segment on, load least of all.
	if segment(t, m, "shell:load").On {
		t.Error("the preset switched load on")
	}
}

// The preset underlined at first is the one the config is closest to. The
// mock-up's segments are the default with two changes, and so are they the
// ops preset: the default wins the tie, as drawn.
func TestPresetNearest(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		cfg  func(t *testing.T) statusline.Config
		next string
	}{
		{"minimal", func(t *testing.T) statusline.Config { t.Helper(); return preset(t, "minimal") }, "default"},
		{"default", func(t *testing.T) statusline.Config { t.Helper(); return preset(t, "default") }, "ops"},
		{"ops", func(t *testing.T) statusline.Config { t.Helper(); return preset(t, "ops") }, "minimal"},
		{"the drawing", drawing, "ops"},
		// Two segments from minimal and two from default: the default wins.
		{"between minimal and default", func(t *testing.T) statusline.Config {
			t.Helper()
			c := preset(t, "minimal")
			for i, s := range c.Segments {
				if s.Name == "route" || s.Name == "tokens" || s.Name == "helpers" {
					c.Segments[i].On = true
				}
			}
			return c
		}, "ops"},
		{"nothing", func(*testing.T) statusline.Config { return statusline.Config{} }, "default"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := sledit.New(tt.cfg(t), path, nil)
			if res := send(t, m, char('p')); res.Note != "preset "+tt.next {
				t.Errorf("the first p gave %q, want preset %s", res.Note, tt.next)
			}
		})
	}
}

// The config s hands back is written by Encode as a file Parse reads back as
// the same config.
func TestSaveRoundTrip(t *testing.T) {
	t.Parallel()
	m := sledit.New(drawing(t), path, nil)
	// A little of everything: a toggle, a side, a move, an edit, a new
	// shell segment, a deleted one.
	sel(t, m, "core:tokens")
	send(t, m, space(), right(), shift('j'))
	sel(t, m, "core:cache")
	openField(t, m, "warn below")
	retype(t, m, "")
	send(t, m, enter(), esc())
	openField(t, m, "format")
	retype(t, m, `c "$pct" \ é ●`)
	send(t, m, enter(), esc())
	sel(t, m, "shell:load")
	send(t, m, char('d'))
	send(t, m, char('a'))
	send(t, m, typed("disk")...)
	send(t, m, enter())
	send(t, m, typed(`df -h / | awk 'END {print $5}'`)...)
	send(t, m, enter())
	send(t, m, typed(`(\d+)%`)...)
	send(t, m, enter(), esc())

	res := send(t, m, char('s'))
	if res.Save == nil {
		t.Fatalf("s gave %+v, want a config to save", res)
	}
	if res.Done {
		t.Error("s closed the screen")
	}
	if res.Note != "saved" {
		t.Errorf("note is %q, want saved", res.Note)
	}
	if !slices.Equal(res.Save.Segments, m.Config().Segments) {
		t.Error("the saved config is not the one being edited")
	}
	back, warnings, err := statusline.Parse(res.Save.Encode())
	if err != nil || len(warnings) != 0 {
		t.Fatalf("the saved file does not read back: %v, warnings %q\n%s", err, warnings, res.Save.Encode())
	}
	if !slices.Equal(back.Segments, res.Save.Segments) {
		t.Errorf("read back as\n%+v\nwant\n%+v", back.Segments, res.Save.Segments)
	}
	// What was changed is in it.
	saved := sledit.New(*res.Save, path, nil)
	if s := segment(t, saved, "core:tokens"); !s.On || s.Side != statusline.Right {
		t.Errorf("tokens was saved as %+v", s)
	}
	if s := segment(t, saved, "core:cache"); s.HasWarn || s.Format != `c "$pct" \ é ●` {
		t.Errorf("cache was saved as %+v", s)
	}
	if got := ids(*res.Save); slices.Contains(got, "shell:load") || !slices.Contains(got, "shell:disk") {
		t.Errorf("saved segments are %v", got)
	}
}

// Every preset, untouched, saves and reads back too.
func TestSavePresets(t *testing.T) {
	t.Parallel()
	for _, name := range statusline.Presets() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			m := sledit.New(preset(t, name), path, nil)
			res := send(t, m, char('s'))
			if res.Save == nil {
				t.Fatalf("s gave %+v", res)
			}
			back, warnings, err := statusline.Parse(res.Save.Encode())
			if err != nil || len(warnings) != 0 {
				t.Fatalf("does not read back: %v, %q", err, warnings)
			}
			if !slices.Equal(back.Segments, preset(t, name).Segments) {
				t.Error("read back as another config")
			}
		})
	}
}

// The config handed back is the caller's: later edits do not reach it.
func TestSaveHandsBackACopy(t *testing.T) {
	t.Parallel()
	m := sledit.New(drawing(t), path, nil)
	res := send(t, m, char('s'))
	if res.Save == nil {
		t.Fatal("no config to save")
	}
	send(t, m, space(), shift('j'))
	if !slices.Equal(res.Save.Segments, drawing(t).Segments) {
		t.Error("an edit after the save changed the saved config")
	}
	res.Save.Segments[0].On = false
	if !m.Config().Segments[0].On && m.Config().Segments[0].Name == "state" {
		t.Error("changing the saved config changed the editor's")
	}
}

// A config the file could not hold is not handed back.
func TestSaveRefused(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		bad  statusline.Segment
		want string
	}{
		{
			"a segment that does not validate",
			statusline.Segment{Name: "slow", From: statusline.FromShell, Side: statusline.Right, Command: "x", Interval: time.Second, Timeout: time.Minute},
			"not saved: slow: timeout is 1m0s",
		},
		{
			"a segment listed twice",
			statusline.Segment{Name: "clock", From: statusline.FromCore, Side: statusline.Right, Format: "$time"},
			"not saved: clock is listed twice",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := drawing(t)
			c.Segments = append(c.Segments, tt.bad)
			m := sledit.New(c, path, nil)
			res := send(t, m, char('s'))
			if res.Save != nil {
				t.Error("a config was handed back")
			}
			if !strings.Contains(res.Note, tt.want) {
				t.Errorf("note is %q, want %q", res.Note, tt.want)
			}
			if m.Selected() != tt.bad.ID() {
				t.Errorf("selection is %q, want the segment at fault", m.Selected())
			}
		})
	}
}

func TestEscAsksOnce(t *testing.T) {
	t.Parallel()
	th := themeOf(trueDark())
	header := func(m *sledit.Model) string {
		return strings.TrimRight(screen(t, m, th, 100, 30)[0], " ")
	}

	t.Run("nothing changed", func(t *testing.T) {
		t.Parallel()
		m := newDrawing(t, th)
		if strings.Contains(header(m), "not saved") {
			t.Errorf("the header is %q with nothing changed", header(m))
		}
		res := send(t, m, esc())
		if !res.Done || res.Save != nil || res.Note != "" {
			t.Errorf("esc gave %+v, want done and nothing else", res)
		}
	})

	t.Run("unsaved changes", func(t *testing.T) {
		t.Parallel()
		m := newDrawing(t, th)
		send(t, m, space())
		if !strings.Contains(header(m), "Status line  not saved") {
			t.Errorf("the header is %q, want it to say not saved", header(m))
		}
		res := send(t, m, esc())
		if res.Done {
			t.Fatal("the first esc left with unsaved changes")
		}
		const ask = "not saved: s saves, esc again leaves without saving"
		if res.Note != ask {
			t.Errorf("note is %q, want %q", res.Note, ask)
		}
		rows := screen(t, m, th, 100, 30)
		if got := strings.TrimRight(rows[len(rows)-1], " "); got != ask {
			t.Errorf("the bottom row is %q, want the question", got)
		}
		res = send(t, m, esc())
		if !res.Done || res.Save != nil {
			t.Errorf("the second esc gave %+v, want done and no save", res)
		}
		if res.Note != "the status line changes were not saved" {
			t.Errorf("note is %q", res.Note)
		}
	})

	t.Run("a key in between asks again", func(t *testing.T) {
		t.Parallel()
		m := newDrawing(t, th)
		send(t, m, space())
		send(t, m, esc())
		send(t, m, down())
		if res := send(t, m, esc()); res.Done {
			t.Error("esc left, though another key came after the question")
		}
		if res := send(t, m, esc()); !res.Done {
			t.Error("esc twice in a row did not leave")
		}
	})

	t.Run("a change undone is no change", func(t *testing.T) {
		t.Parallel()
		m := newDrawing(t, th)
		send(t, m, space(), space(), shift('j'), shift('k'), right(), left())
		if res := send(t, m, esc()); !res.Done {
			t.Errorf("esc gave %+v, want done", res)
		}
	})

	t.Run("saved", func(t *testing.T) {
		t.Parallel()
		m := newDrawing(t, th)
		send(t, m, space())
		if res := send(t, m, esc(), char('s')); res.Save == nil {
			t.Fatalf("s after the question gave %+v, want a save", res)
		}
		if strings.Contains(header(m), "not saved") {
			t.Errorf("the header is %q after a save", header(m))
		}
		if res := send(t, m, esc()); !res.Done || res.Note != "" {
			t.Errorf("esc after a save gave %+v, want done at once", res)
		}
	})

	t.Run("changed again after a save", func(t *testing.T) {
		t.Parallel()
		m := newDrawing(t, th)
		send(t, m, space(), char('s'), space())
		if res := send(t, m, esc()); res.Done {
			t.Error("esc left with a change made after the save")
		}
	})
}

// When the file could not be written, the changes count as unsaved again.
func TestSaveFailed(t *testing.T) {
	t.Parallel()
	th := themeOf(trueDark())
	m := newDrawing(t, th)
	send(t, m, space())
	if res := send(t, m, char('s')); res.Save == nil {
		t.Fatal("no config to save")
	}
	m.SaveFailed("the disk is full")
	rows := screen(t, m, th, 100, 30)
	if got := strings.TrimRight(rows[len(rows)-1], " "); got != "not saved: the disk is full" {
		t.Errorf("the bottom row is %q, want the reason", got)
	}
	if !strings.Contains(rows[0], "not saved") {
		t.Errorf("the header is %q, want it to say not saved", rows[0])
	}
	if res := send(t, m, esc()); res.Done {
		t.Error("esc left though the save had failed")
	}
	// A second try that works is a save like any other.
	if res := send(t, m, char('s')); res.Save == nil {
		t.Fatal("no config to save the second time")
	}
	if res := send(t, m, esc()); !res.Done {
		t.Error("esc did not leave after the second save")
	}
}

// Only key presses reach the table; nothing else changes it or fails.
func TestOtherMessages(t *testing.T) {
	t.Parallel()
	m := sledit.New(drawing(t), path, nil)
	msgs := []tea.Msg{
		nil,
		tea.WindowSizeMsg{Width: 80, Height: 24},
		tea.PasteMsg{Content: "s"},
		tea.KeyReleaseMsg{Code: tea.KeySpace, Text: " "},
		tea.FocusMsg{},
		"text", 7,
	}
	for _, msg := range msgs {
		res, cmd := m.Update(msg)
		if res != (sledit.Result{}) || cmd != nil {
			t.Errorf("%T gave %+v and a command %v", msg, res, cmd != nil)
		}
	}
	if !slices.Equal(m.Config().Segments, drawing(t).Segments) || m.Selected() != "core:state" {
		t.Error("a message that is not a key press changed the editor")
	}
}

// A paste goes into the field being edited, as one line.
func TestPasteIntoField(t *testing.T) {
	t.Parallel()
	m := sledit.New(drawing(t), path, nil)
	sel(t, m, "shell:load")
	openField(t, m, "command")
	retype(t, m, "")
	send(t, m, tea.PasteMsg{Content: "uptime |\n\ttail -1\x1b[2J"})
	send(t, m, enter())
	if got := segment(t, m, "shell:load").Command; got != "uptime |  tail -1[2J" {
		t.Errorf("command is %q", got)
	}
}

// A config that leaves core segments out is shown with every one of them,
// the missing ones at the end and switched off, as a file is read. So what
// the editor saves reads back as the same config, whatever it was given.
func TestMissingCoreSegments(t *testing.T) {
	t.Parallel()
	core := func() []string {
		var out []string
		for _, s := range preset(t, statusline.DefaultPreset).Segments {
			if s.From == statusline.FromCore {
				out = append(out, s.ID())
			}
		}
		return out
	}
	tests := []struct {
		name string
		cfg  statusline.Config
		want []string
	}{
		{"no segments at all", statusline.Config{}, core()},
		{"two of them, and a shell segment", statusline.Config{Segments: []statusline.Segment{
			{Name: "clock", On: true},
			{Name: "disk", From: statusline.FromShell, On: true, Side: statusline.Left, Command: "df", Interval: time.Minute, Timeout: time.Second},
			{Name: "cache", On: true, Format: "c $pct"},
		}}, slices.Concat([]string{"core:clock", "shell:disk", "core:cache"}, slices.DeleteFunc(core(), func(id string) bool {
			return id == "core:clock" || id == "core:cache"
		}))},
		{"all of them", preset(t, "ops"), ids(preset(t, "ops"))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			given := slices.Clone(tt.cfg.Segments)
			m := sledit.New(tt.cfg, path, nil)
			got := m.Config()
			if !slices.Equal(ids(got), tt.want) {
				t.Fatalf("segments are %v, want %v", ids(got), tt.want)
			}
			for i, s := range got.Segments {
				if i >= len(given) && s.On {
					t.Errorf("%s was added switched on", s.ID())
				}
				if i < len(given) && s.On != given[i].On {
					t.Errorf("%s was switched", s.ID())
				}
			}
			if !slices.Equal(tt.cfg.Segments, given) {
				t.Error("the caller's config changed")
			}
			// Adding them is not a change to save.
			if res := send(t, sledit.New(tt.cfg, path, nil), esc()); !res.Done {
				t.Errorf("esc gave %+v, want done: nothing was changed", res)
			}
			res := send(t, m, char('s'))
			if res.Save == nil {
				t.Fatalf("s gave %+v, want a config to save", res)
			}
			back, warnings, err := statusline.Parse(res.Save.Encode())
			if err != nil || len(warnings) != 0 {
				t.Fatalf("the saved file does not read back: %v, warnings %q", err, warnings)
			}
			if !slices.Equal(back.Segments, res.Save.Segments) {
				t.Errorf("read back as\n%+v\nwant\n%+v", back.Segments, res.Save.Segments)
			}
		})
	}
}

// An editor opened on no config at all takes every key without failing,
// on every terminal, and draws as many rows as the screen has.
func TestEmptyConfig(t *testing.T) {
	t.Parallel()
	for _, term := range terminals() {
		t.Run(term.name, func(t *testing.T) {
			t.Parallel()
			th := theme.New(term.caps, theme.Options{})
			m := sledit.New(statusline.Config{}, "", nil)
			for _, size := range [][2]int{{100, 30}, {80, 24}, {79, 30}, {60, 12}, {40, 16}, {30, 8}, {10, 3}} {
				checkScreen(t, m, th, size[0], size[1])
			}
			// Nothing is on, so every bar is empty.
			for _, row := range screen(t, m, th, 100, 30) {
				if rest, ok := strings.CutPrefix(row, "100 columns"); ok && strings.TrimSpace(rest) != "" {
					t.Errorf("the bar of a config with nothing on is %q", rest)
				}
			}
			send(t, m, up(), down(), space(), left(), right(), shift('j'), shift('k'), char('d'), char('e'), esc())
			if m.Editing() {
				t.Error("still editing")
			}
			send(t, m, char('a'))
			send(t, m, typed("one")...)
			send(t, m, enter(), esc())
			if got := ids(m.Config()); !slices.Contains(got, "shell:one") {
				t.Errorf("segments are %v, want the one added among them", got)
			}
			for _, size := range [][2]int{{100, 30}, {60, 12}, {10, 3}} {
				checkScreen(t, m, th, size[0], size[1])
			}
			res := send(t, m, char('s'))
			if res.Save == nil {
				t.Fatalf("s gave %+v, want a config to save", res)
			}
			back, _, err := statusline.Parse(res.Save.Encode())
			if err != nil || !slices.Equal(back.Segments, res.Save.Segments) {
				t.Errorf("the saved config does not read back as itself: %v", err)
			}
		})
	}
}

// forty returns a config of forty segments: the ops preset, and shell
// segments on both sides up to the most a file holds.
func forty(t *testing.T) statusline.Config {
	t.Helper()
	c := preset(t, "ops")
	for i := 0; len(c.Segments) < 40; i++ {
		c.Segments = append(c.Segments, statusline.Segment{
			Name: "s" + strconv.Itoa(i), From: statusline.FromShell, On: i%2 == 0,
			Side: []statusline.Side{statusline.Left, statusline.Right}[i%2], Drop: i % 4,
			Command: "echo " + strconv.Itoa(i), Interval: statusline.DefaultInterval, Timeout: statusline.DefaultTimeout,
		})
	}
	return c
}

// Forty segments: the table scrolls to keep the selected one on the screen
// at every size, says where it is, and the config still saves.
func TestFortySegments(t *testing.T) {
	t.Parallel()
	for _, term := range terminals() {
		t.Run(term.name, func(t *testing.T) {
			t.Parallel()
			th := themeOf(term.caps)
			c := forty(t)
			if len(c.Segments) != 40 {
				t.Fatalf("%d segments, want 40", len(c.Segments))
			}
			m := sledit.New(c, path, live(th))
			walk := slices.Concat(slices.Repeat([]tea.Msg{down()}, 39), slices.Repeat([]tea.Msg{up()}, 39))
			for i, step := range walk {
				at := min(i, 78-i)
				name := c.Segments[at].Name
				for _, width := range []int{100, 80, 79, 60, 40, 30} {
					for _, height := range []int{30, 16, 8} {
						checkScreen(t, m, th, width, height)
						rows := screen(t, m, th, width, height)
						if !slices.ContainsFunc(rows, func(r string) bool { return strings.Contains(r, "] "+name+" ") }) {
							t.Fatalf("%dx%d: with %s selected its row is not on the screen\n%s", width, height, name, strings.Join(rows, "\n"))
						}
						if want := strconv.Itoa(at+1) + "/40"; !strings.Contains(rowWith(t, rows, "SEGMENT"), want) {
							t.Errorf("%dx%d: the heading row is %q, want it to say %s", width, height, rowWith(t, rows, "SEGMENT"), want)
						}
					}
				}
				send(t, m, step)
			}
			// The list is full: 29 mod and shell segments of 32, so three
			// more can be added and no fourth.
			for i := range 4 {
				send(t, m, char('a'))
				send(t, m, typed("more"+strconv.Itoa(i))...)
				send(t, m, enter(), esc())
			}
			if n := len(m.Config().Segments); n != 44 {
				t.Fatalf("%d segments after three more, want 44", n)
			}
			if res := send(t, m, char('a')); m.Editing() || !strings.Contains(res.Note, "at most 32") {
				t.Errorf("a 33rd was not refused: %+v", res)
			}
			res := send(t, m, char('s'))
			if res.Save == nil || res.Note != "saved" {
				t.Fatalf("s gave %+v, want a config to save", res)
			}
			back, warnings, err := statusline.Parse(res.Save.Encode())
			// The three added have no command yet, and Parse says so.
			if err != nil || len(warnings) != 4 {
				t.Fatalf("the saved file does not read back: %v, warnings %q", err, warnings)
			}
			if !slices.Equal(back.Segments, res.Save.Segments) {
				t.Error("read back as another config")
			}
		})
	}
}

// Mods can bring a config past the 32 mod and shell segments a file is read
// back with. It is still saved, and the screen says what will happen.
func TestSavePastTheLimit(t *testing.T) {
	t.Parallel()
	offered := make([]statusline.ModSegment, 0, statusline.MaxSegments+2)
	for i := range statusline.MaxSegments + 2 {
		offered = append(offered, statusline.ModSegment{Mod: "m" + strconv.Itoa(i), Name: "seg"})
	}
	c := preset(t, statusline.DefaultPreset).WithMods(offered) // load, and 34 of mods
	m := sledit.New(c, path, nil)
	res := send(t, m, char('s'))
	if res.Save == nil || len(res.Save.Segments) != len(c.Segments) {
		t.Fatalf("s gave %+v, want the whole config to save", res)
	}
	const want = "saved, but only the first 32 mod and shell segments are read back"
	if res.Note != want {
		t.Errorf("note is %q, want %q", res.Note, want)
	}
	th := themeOf(trueDark())
	rows := screen(t, m, th, 100, 30)
	if got := rows[len(rows)-1]; got != want {
		t.Errorf("the bottom row is %q, want the note", got)
	}
	back, warnings, err := statusline.Parse(res.Save.Encode())
	if err != nil || len(warnings) != 1 || len(back.Segments) != len(c.Segments)-3 {
		t.Errorf("read back %d segments with warnings %q and error %v, want three fewer and one warning", len(back.Segments), warnings, err)
	}
	// It counts as saved: esc leaves at once.
	if res := send(t, m, esc()); !res.Done {
		t.Errorf("esc gave %+v, want done", res)
	}
}

// Several characters in one key press are text, as an input method hands
// them over. They are never a key, whatever they spell.
func TestTextIsNotAKey(t *testing.T) {
	t.Parallel()
	word := func(text string) tea.KeyPressMsg {
		return tea.KeyPressMsg{Code: rune(text[0]), Text: text}
	}
	words := []tea.Msg{word("esc"), word("up"), word("down"), word("enter"), word("tab"), word("space"), word("left"), word("home")}

	m := sledit.New(drawing(t), path, nil)
	sel(t, m, "core:cache")
	send(t, m, space()) // a change, so that esc would ask
	before := m.Config()
	for _, msg := range words {
		if res := send(t, m, msg); res != (sledit.Result{}) {
			t.Errorf("%q gave %+v, want nothing", msg, res)
		}
	}
	if !slices.Equal(m.Config().Segments, before.Segments) || m.Selected() != "core:cache" {
		t.Error("a word changed the config or the selection")
	}

	send(t, m, char('e'))
	wipe(t, m)
	send(t, m, words...)
	if !m.Editing() {
		t.Fatal("a word closed the field")
	}
	if got := inputRow(t, m); got != "format ❯ escupdownentertabspacelefthome" {
		t.Errorf("the input row is %q, want every word typed into it", got)
	}
	send(t, m, enter())
	if got := segment(t, m, "core:cache").Format; got != "escupdownentertabspacelefthome" {
		t.Errorf("format is %q", got)
	}
}

// A field takes at most 2048 characters, and text the file cannot hold is
// refused with the field still open.
func TestEditLongText(t *testing.T) {
	t.Parallel()
	th := themeOf(trueDark())
	m := sledit.New(drawing(t), path, nil)
	sel(t, m, "shell:load")
	openField(t, m, "command")
	wipe(t, m)
	send(t, m, tea.PasteMsg{Content: strings.Repeat("x", 5000)})
	for _, size := range [][2]int{{100, 30}, {80, 16}, {30, 8}} {
		checkScreen(t, m, th, size[0], size[1])
	}
	if res := send(t, m, enter()); res.Note != "" {
		t.Fatalf("2048 characters were refused: %s", res.Note)
	}
	if got := segment(t, m, "shell:load").Command; got != strings.Repeat("x", 2048) {
		t.Errorf("the command is %d bytes long, want the first 2048", len(got))
	}
	for _, size := range [][2]int{{100, 30}, {80, 16}, {30, 8}} {
		checkScreen(t, m, th, size[0], size[1])
	}
	// 2048 characters of three bytes each are more than a command holds.
	openField(t, m, "command")
	wipe(t, m)
	send(t, m, tea.PasteMsg{Content: strings.Repeat("愛", 5000)})
	res := send(t, m, enter())
	if !strings.Contains(res.Note, "command is longer than 2048 bytes") || !m.Editing() {
		t.Errorf("enter gave %+v, want the command refused and the field open", res)
	}
	for _, size := range [][2]int{{100, 30}, {80, 16}, {30, 8}} {
		checkScreen(t, m, th, size[0], size[1])
	}
	if got := segment(t, m, "shell:load").Command; got != strings.Repeat("x", 2048) {
		t.Error("refused text changed the command")
	}
}

// Switching a shell segment on that has nothing to run says so.
func TestToggleShellWithoutCommand(t *testing.T) {
	t.Parallel()
	m := sledit.New(drawing(t), path, nil)
	send(t, m, char('a'))
	send(t, m, typed("disk")...)
	send(t, m, enter(), esc())
	if got := segment(t, m, "shell:disk"); !got.On || got.Command != "" {
		t.Fatalf("the new segment is %+v, want it on and with no command", got)
	}
	if res := send(t, m, space()); res.Note != "" {
		t.Errorf("switching it off gave the note %q", res.Note)
	}
	if res := send(t, m, space()); res.Note != "disk has no command yet: e gives it one" {
		t.Errorf("switching it on gave the note %q", res.Note)
	}
	// A segment with a command, and one that is not the shell's, say nothing.
	for _, id := range []string{"shell:load", "core:tokens"} {
		sel(t, m, id)
		if res := send(t, m, space()); res.Note != "" {
			t.Errorf("switching %s on gave the note %q", id, res.Note)
		}
	}
}

// The screen starts nothing of its own: no key gives back a command, so no
// timer blinks a cursor and ctrl+v does not go and read the clipboard.
func TestNoCommands(t *testing.T) {
	t.Parallel()
	m := sledit.New(drawing(t), path, nil)
	ctrlV := tea.KeyPressMsg{Code: 'v', Mod: tea.ModCtrl}
	msgs := slices.Concat(
		[]tea.Msg{down(), space(), right(), shift('j'), char('p'), char('s'), ctrlV, char('e')},
		typed("abc"),
		[]tea.Msg{ctrlV, left(), key(tea.KeyHome), key(tea.KeyBackspace), tea.PasteMsg{Content: "x"}, down(), up(), enter(), esc()},
		[]tea.Msg{char('a'), ctrlV, char('n'), enter(), esc(), char('d'), esc(), esc()},
	)
	for i, msg := range msgs {
		if _, cmd := m.Update(msg); cmd != nil {
			t.Errorf("message %d (%v) gave back a command", i, msg)
		}
	}
}
