package frame_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/quinnovator/contactd/internal/tui/frame"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
	"github.com/quinnovator/contactd/internal/tui/tuitest"
)

// paletteSentence is what the hint line says while the palette is open.
const paletteSentence = "a line that starts with / is a command and is never sent to the contact"

// As the line gets narrower the hints go one at a time: contacts, details,
// the new-line key, send. Each row is the widest or the narrowest width that
// shows its text.
func TestHintsDropOrder(t *testing.T) {
	t.Parallel()
	const (
		all        = " enter send · ctrl+j new line · / commands · ctrl+o details · ctrl+p contacts"
		noContacts = " enter send · ctrl+j new line · / commands · ctrl+o details"
		noDetails  = " enter send · ctrl+j new line · / commands"
		noNewline  = " enter send · / commands"
		commands   = " / commands"
	)
	tests := []struct {
		width int
		shift bool
		want  string
	}{
		{300, false, all},
		{100, false, all},
		{77, false, all},
		{76, false, noContacts},
		{59, false, noContacts},
		{58, false, noDetails},
		{42, false, noDetails},
		{41, false, noNewline},
		{24, false, noNewline},
		{23, false, commands},
		{11, false, commands},
		{10, false, " / comman…"},
		{3, false, " /…"},
		{2, false, " …"},
		{1, false, ""},
		{100, true, " enter send · shift+enter new line · / commands · ctrl+o details · ctrl+p contacts"},
		{82, true, " enter send · shift+enter new line · / commands · ctrl+o details · ctrl+p contacts"},
		{81, true, " enter send · shift+enter new line · / commands · ctrl+o details"},
		{64, true, " enter send · shift+enter new line · / commands · ctrl+o details"},
		{63, true, " enter send · shift+enter new line · / commands"},
		{47, true, " enter send · shift+enter new line · / commands"},
		{46, true, noNewline},
		{23, true, commands},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%d columns, shift+enter %t", tt.width, tt.shift), func(t *testing.T) {
			t.Parallel()
			th := dark()
			got := frame.Hints(th, frame.HintState{ShiftEnter: tt.shift}, tt.width)
			wantRow(t, th, got, tt.width)
			if shown(got) != tt.want {
				t.Errorf("Hints at %d\n got %q\nwant %q", tt.width, shown(got), tt.want)
			}
		})
	}
}

// At every width the hints shown are the first few of one fixed order, and
// "/ commands" is among them from the width that holds it.
func TestHintsCommandsIsLast(t *testing.T) {
	t.Parallel()
	for _, l := range everyLook() {
		for _, shift := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s, shift+enter %t", l.name, shift), func(t *testing.T) {
				t.Parallel()
				th := themeOf(l)
				newline := "ctrl+j new line"
				if shift {
					newline = "shift+enter new line"
				}
				// From the hint that stays longest to the one that goes first.
				keep := []string{"/ commands", "enter send", newline, "ctrl+o details", "ctrl+p contacts"}
				room := 1 + th.Width("/ commands")
				last := 0
				for width := 1; width <= 300; width++ {
					got := frame.Hints(th, frame.HintState{ShiftEnter: shift}, width)
					wantRow(t, th, got, width)
					text := ansi.Strip(got)
					n := 0
					for n < len(keep) && strings.Contains(text, keep[n]) {
						n++
					}
					for _, h := range keep[n:] {
						if strings.Contains(text, h) {
							t.Fatalf("at %d columns %q is shown without %q: %q", width, h, keep[n], text)
						}
					}
					if width >= room && n == 0 {
						t.Fatalf("at %d columns \"/ commands\" is gone: %q", width, text)
					}
					if n < last {
						t.Fatalf("at %d columns there are fewer hints than at %d: %q", width, width-1, text)
					}
					last = n
					if width >= room && !strings.HasPrefix(text, " ") {
						t.Fatalf("at %d columns the line has no leading space: %q", width, text)
					}
				}
				if last != len(keep) {
					t.Errorf("at 300 columns only %d hints are shown", last)
				}
			})
		}
	}
}

// The hints are dim, and joined by the theme's separator.
func TestHintsStyle(t *testing.T) {
	t.Parallel()
	for _, l := range looks() {
		t.Run(l.name, func(t *testing.T) {
			t.Parallel()
			th := themeOf(l)
			mid := " " + th.G.Mid + " "
			text := strings.Join([]string{
				"enter send", "ctrl+j new line", "/ commands", "ctrl+o details", "ctrl+p contacts",
			}, mid)
			got := frame.Hints(th, frame.HintState{}, 100)
			if want := th.Pad(" "+th.Style("dim").Render(text), 100); got != want {
				t.Errorf("Hints\n got %q\nwant %q", got, want)
			}
		})
	}
}

// A note takes the place of the hints, and of the palette's sentence.
func TestHintsNote(t *testing.T) {
	t.Parallel()
	const note = "not sent: not connected"
	tests := []struct {
		name  string
		state frame.HintState
		width int
		tone  string
		want  string
	}{
		{"in the warn tone", frame.HintState{Note: note}, 100, "warn", note},
		{"in a tone of its own", frame.HintState{Note: note, NoteTone: "bad"}, 100, "bad", note},
		{"in the ok tone", frame.HintState{Note: "saved", NoteTone: "ok"}, 100, "ok", "saved"},
		{"dim", frame.HintState{Note: "copied", NoteTone: "dim"}, 100, "dim", "copied"},
		{"with the palette open", frame.HintState{Note: note, PaletteOpen: true, ShiftEnter: true}, 100, "warn", note},
		{"exactly as wide as the line", frame.HintState{Note: note}, 24, "warn", note},
		{"truncated", frame.HintState{Note: note}, 23, "warn", "not sent: not connect…"},
		{"truncated to very little", frame.HintState{Note: note}, 5, "warn", "not…"},
		{"on one line", frame.HintState{Note: "not sent:\nnot connected"}, 100, "warn", note},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			th := dark()
			got := frame.Hints(th, tt.state, tt.width)
			wantRow(t, th, got, tt.width)
			if shown(got) != " "+tt.want {
				t.Errorf("the line reads %q, want %q", shown(got), " "+tt.want)
			}
			if styled := th.Style(tt.tone).Render(tt.want); !strings.Contains(got, styled) {
				t.Errorf("the note is not in the %s tone: %q", tt.tone, got)
			}
			if strings.Contains(got, "commands") || strings.Contains(got, "starts with") {
				t.Errorf("the hints are still there: %q", got)
			}
		})
	}
}

// While the palette is open the line is one sentence about commands.
func TestHintsPalette(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		state frame.HintState
		width int
		want  string
	}{
		{"at 100 columns", frame.HintState{PaletteOpen: true}, 100, " " + paletteSentence},
		{"whatever the new-line key", frame.HintState{PaletteOpen: true, ShiftEnter: true}, 100, " " + paletteSentence},
		{"exactly as wide as the line", frame.HintState{PaletteOpen: true}, 72, " " + paletteSentence},
		{
			"truncated one column under",
			frame.HintState{PaletteOpen: true},
			71,
			" a line that starts with / is a command and is never sent to the conta…",
		},
		{
			"truncated at 46 columns",
			frame.HintState{PaletteOpen: true},
			46,
			" a line that starts with / is a command and i…",
		},
		{"the keys come back when it closes", frame.HintState{}, 30, " enter send · / commands"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			th := dark()
			got := frame.Hints(th, tt.state, tt.width)
			wantRow(t, th, got, tt.width)
			if shown(got) != tt.want {
				t.Errorf("the line reads %q, want %q", shown(got), tt.want)
			}
			if styled := th.Style("dim").Render(strings.TrimPrefix(tt.want, " ")); !strings.Contains(got, styled) {
				t.Errorf("the line is not dim: %q", got)
			}
		})
	}
}

// A note is drawn as text whatever it holds: no escape sequence in it reaches
// the row, in a tone that adds a colour of its own and in one that adds
// nothing, cut or whole.
func TestHintsNoteIsInert(t *testing.T) {
	t.Parallel()
	tones := []string{"", "warn", "bad", "ok", "tool", "mod", "dim", "no such tone"}
	for _, l := range everyLook() {
		t.Run(l.name, func(t *testing.T) {
			t.Parallel()
			th := themeOf(l)
			for _, h := range hostile() {
				for _, tone := range tones {
					for _, width := range []int{1, 2, 5, 12, 13, 30, 100} {
						got := frame.Hints(th, frame.HintState{Note: "not sent: " + h + "not connected", NoteTone: tone}, width)
						wantRow(t, th, got, width)
						wantInert(t, got)
					}
				}
			}
			for _, state := range []frame.HintState{{}, {ShiftEnter: true}, {PaletteOpen: true}} {
				for width := 1; width <= 100; width++ {
					wantInert(t, frame.Hints(th, state, width))
				}
			}
		})
	}
}

// What is left of an escape sequence is drawn as the characters it was made
// of.
func TestHintsNoteAsText(t *testing.T) {
	t.Parallel()
	tests := []struct{ note, want string }{
		{"not sent: \x1b[7mnot connected", " not sent: [7mnot connected"},
		{"saved\x1b[2J", " saved[2J"},
		{"\x1b]0;title\asaved", " ]0;titlesaved"},
		{"\x1b_Gf=24;AAAA\x1b\\", " _Gf=24;AAAA\\"},
		{"saved\x1b", " saved"},
		{"\x1b\x1b\x1b", ""},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%q", tt.note), func(t *testing.T) {
			t.Parallel()
			th := dark()
			got := frame.Hints(th, frame.HintState{Note: tt.note, NoteTone: "no such tone"}, 60)
			wantRow(t, th, got, 60)
			if strings.TrimRight(got, " ") != tt.want {
				t.Errorf("the line is %q, want %q", strings.TrimRight(got, " "), tt.want)
			}
		})
	}
}

// The line is fitted by measuring it, so a separator and an ellipsis of three
// cells move the widths at which hints go and never make the row wider.
func TestHintsWideGlyphs(t *testing.T) {
	t.Parallel()
	for _, l := range everyLook() {
		if l.caps.Glyphs == termcaps.GlyphsASCII {
			continue // overrides apply to the Unicode set only
		}
		t.Run(l.name, func(t *testing.T) {
			t.Parallel()
			th := wideGlyphs(t, l)
			tests := []struct {
				width int
				want  string
			}{
				{85, " enter send <=> ctrl+j new line <=> / commands <=> ctrl+o details <=> ctrl+p contacts"},
				{84, " enter send <=> ctrl+j new line <=> / commands <=> ctrl+o details"},
				{65, " enter send <=> ctrl+j new line <=> / commands <=> ctrl+o details"},
				{64, " enter send <=> ctrl+j new line <=> / commands"},
				{46, " enter send <=> ctrl+j new line <=> / commands"},
				{45, " enter send <=> / commands"},
				{26, " enter send <=> / commands"},
				{25, " / commands"},
				{11, " / commands"},
				{10, " / comm~~~"},
				{4, " ~~~"},
				{3, " /"},
			}
			for _, tt := range tests {
				if got := shown(frame.Hints(th, frame.HintState{}, tt.width)); got != tt.want {
					t.Errorf("Hints at %d\n got %q\nwant %q", tt.width, got, tt.want)
				}
			}
			states := []frame.HintState{{}, {ShiftEnter: true}, {PaletteOpen: true}, {Note: "not sent: not connected"}}
			for _, state := range states {
				for width := 1; width <= 120; width++ {
					wantRow(t, th, frame.Hints(th, state, width), width)
				}
			}
		})
	}
}

// A terminal with no width has no hint line.
func TestHintsNoWidth(t *testing.T) {
	t.Parallel()
	th := dark()
	states := []frame.HintState{{}, {PaletteOpen: true}, {Note: "not sent: not connected"}}
	for _, state := range states {
		for _, width := range []int{0, -1, -80} {
			if got := frame.Hints(th, state, width); got != "" {
				t.Errorf("Hints(%+v, %d) = %q, want nothing", state, width, got)
			}
		}
	}
}

// In every state and every terminal the line is one row of exactly the
// width, and with ASCII glyphs it holds no byte above 0x7F.
func TestHintsWidth(t *testing.T) {
	t.Parallel()
	states := []frame.HintState{
		{},
		{ShiftEnter: true},
		{PaletteOpen: true},
		{Note: "not sent: not connected"},
		{Note: "no command /foo; /help lists them; start with // to send it", NoteTone: "bad"},
		{Note: "这是一个很长的中文句子用来测试 👨‍👩‍👧 👍🏽", NoteTone: "mod"},
		{Note: "a\tb\r\nc\x07d", NoteTone: "no such tone"},
	}
	for _, l := range everyLook() {
		t.Run(l.name, func(t *testing.T) {
			t.Parallel()
			th := themeOf(l)
			for i, state := range states {
				for width := 1; width <= 200; width++ {
					got := frame.Hints(th, state, width)
					if strings.ContainsAny(got, "\r\n\t\a") || th.Width(got) != width {
						t.Fatalf("Hints(%+v, %d) is %d cells wide: %q", state, width, th.Width(got), got)
					}
					if l.caps.Glyphs == termcaps.GlyphsASCII && i < 5 {
						wantASCII(t, got)
					}
				}
			}
		})
	}
}

func TestHintsGolden(t *testing.T) {
	t.Parallel()
	states := []struct {
		name  string
		state frame.HintState
	}{
		{"keys", frame.HintState{}},
		{"keys with shift+enter", frame.HintState{ShiftEnter: true}},
		{"palette", frame.HintState{PaletteOpen: true}},
		{"note", frame.HintState{Note: "not sent: not connected"}},
	}
	var b strings.Builder
	for _, l := range looks() {
		th := themeOf(l)
		for _, s := range states {
			for _, width := range []int{100, 60, 46, 30} {
				fmt.Fprintf(&b, "%s, %s at %d\n%s", l.name, s.name, width, quoted(frame.Hints(th, s.state, width)))
			}
		}
	}
	tuitest.Golden(t, "hints.golden", []byte(b.String()))
}
