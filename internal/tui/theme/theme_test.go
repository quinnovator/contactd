package theme_test

import (
	"slices"
	"strings"
	"sync"
	"testing"

	"charm.land/bubbles/v2/textarea"
	"github.com/charmbracelet/x/ansi"

	"github.com/quinnovator/contactd/internal/tui/termcaps"
	"github.com/quinnovator/contactd/internal/tui/theme"
)

func TestNewIsPure(t *testing.T) {
	t.Parallel()
	o := theme.Options{Glyph: map[string]string{"tool": "●", "bogus": "x"}}
	for _, c := range colors() {
		for _, bg := range backgrounds() {
			caps := termcaps.Caps{
				Color: c, Background: bg, Glyphs: termcaps.GlyphsUnicode, Width: termcaps.WidthGrapheme,
				Images: termcaps.ImagesBlocks, CellW: 9, CellH: 18, Terminal: "ghostty", Settled: true,
			}
			a, b := theme.New(caps, o), theme.New(caps, o)
			if a == b {
				t.Fatal("New returned the same Theme twice")
			}
			if a.Caps != caps || a.G != b.G || a.BarFilled != b.BarFilled {
				t.Errorf("%s: two themes from one Caps differ", capsName(c, bg))
			}
			if !slices.Equal(everything(a), everything(b)) {
				t.Errorf("%s: two themes from one Caps draw differently", capsName(c, bg))
			}
		}
	}
}

func TestUnsetCapsAreDrawnTheSafeWay(t *testing.T) {
	t.Parallel()
	th := theme.New(termcaps.Caps{}, theme.Options{Glyph: map[string]string{"tool": "●"}})
	if th.G != theme.GlyphSet(termcaps.GlyphsASCII) {
		t.Errorf("glyphs = %+v, want the ASCII set", th.G)
	}
	if th.BarFilled {
		t.Error("the bar is filled")
	}
	for _, s := range everything(th) {
		for i := range len(s) {
			if s[i] >= 0x80 {
				t.Fatalf("%q holds byte %#x", s, s[i])
			}
		}
		for _, c := range paint(t, s) {
			if c.fg != "" || c.bg != "" {
				t.Fatalf("%q is coloured", s)
			}
		}
	}
}

func TestTextarea(t *testing.T) {
	t.Parallel()
	for _, c := range colors() {
		for _, bg := range backgrounds() {
			th := newTheme(c, bg)
			st := th.Textarea()
			for name, state := range map[string]textarea.StyleState{"focused": st.Focused, "blurred": st.Blurred} {
				// The box is drawn by Theme.Box. A border or padding here
				// would make the textarea narrower than it was told to be.
				if state.Base.GetHorizontalFrameSize() != 0 || state.Base.GetVerticalFrameSize() != 0 {
					t.Errorf("%s %s: Base has a frame", capsName(c, bg), name)
				}
				for what, got := range map[string]string{
					"Base": state.Base.Render("typed"), "Text": state.Text.Render("typed"), "CursorLine": state.CursorLine.Render("typed"),
				} {
					if got != "typed" {
						t.Errorf("%s %s: %s styles the draft: %q", capsName(c, bg), name, what, got)
					}
				}
				if got, want := state.Placeholder.Render("x"), th.Style("dim").Render("x"); got != want {
					t.Errorf("%s %s: placeholder %q, want dim %q", capsName(c, bg), name, got, want)
				}
			}
			if got, want := st.Focused.Prompt.Render("x"), th.Style("accent").Render("x"); got != want {
				t.Errorf("%s: prompt %q, want the accent %q", capsName(c, bg), got, want)
			}
			if got, want := st.Blurred.Prompt.Render("x"), th.Style("dim").Render("x"); got != want {
				t.Errorf("%s: blurred prompt %q, want dim %q", capsName(c, bg), got, want)
			}
			if st.Cursor.Blink {
				t.Errorf("%s: the cursor blinks", capsName(c, bg))
			}
			if st.Cursor.Color != nil {
				t.Errorf("%s: the cursor is given a colour: %v", capsName(c, bg), st.Cursor.Color)
			}
			selection := paint(t, st.Focused.Selection.Render("x"))[0]
			if (selection.bg != "") != th.BarFilled || selection.reverse == th.BarFilled {
				t.Errorf("%s: selection drawn %+v", capsName(c, bg), selection)
			}
		}
	}
}

func TestTextareaStylesWorkInATextarea(t *testing.T) {
	t.Parallel()
	for _, c := range colors() {
		th := newTheme(c, termcaps.BgDark)
		in := textarea.New()
		in.ShowLineNumbers = false
		in.Placeholder = "Message ada"
		in.SetPromptFunc(2, func(textarea.PromptInfo) string { return th.G.Prompt + " " })
		in.SetVirtualCursor(false)
		in.SetStyles(th.Textarea())
		in.SetWidth(40)
		in.SetHeight(1)
		in.Focus()

		empty := in.View()
		if got := ansi.Strip(empty); !strings.HasPrefix(got, "❯ Message ada") {
			t.Errorf("%v: empty input = %q", c, got)
		}
		in.SetValue("and what would the share have been?")
		typed := in.View()
		if got := strings.TrimRight(ansi.Strip(typed), " "); got != "❯ and what would the share have been?" {
			t.Errorf("%v: input = %q", c, got)
		}
		for _, view := range []string{empty, typed} {
			if w := th.Width(view); w > 40 {
				t.Errorf("%v: the input is %d cells wide in a width of 40: %q", c, w, view)
			}
		}
		// The prompt is the accent; what was typed has no colour.
		cells := paint(t, typed)
		if accent := drawn(t, th, "accent"); cells[0].fg != accent.fg || cells[0].bold != accent.bold {
			t.Errorf("%v: prompt drawn %+v, want %+v", c, cells[0], accent)
		}
		for _, got := range cells[2:] {
			if got.fg != "" || got.bg != "" || got.bold || got.faint {
				t.Fatalf("%v: the draft is styled: %+v", c, got)
			}
		}
	}
}

func TestOneThemeFromManyGoroutines(t *testing.T) {
	t.Parallel()
	th := theme.New(termcaps.Caps{
		Color: termcaps.ColorTrue, Background: termcaps.BgDark, Glyphs: termcaps.GlyphsUnicode, Width: termcaps.WidthWc,
	}, theme.Options{Glyph: map[string]string{"tool": "●"}})
	want := everything(th)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if !slices.Equal(everything(th), want) {
				t.Error("a theme drew differently from another goroutine")
			}
			_ = th.Markdown()
			_ = th.Textarea()
			_ = th.Wrap(zwjFamily+" "+cjkText, 9)
		})
	}
	wg.Wait()
}
