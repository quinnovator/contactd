package theme_test

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"charm.land/glamour/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/quinnovator/contactd/internal/tui/termcaps"
	"github.com/quinnovator/contactd/internal/tui/theme"
	"github.com/quinnovator/contactd/internal/tui/tuitest"
)

// render draws Markdown the way the client does: with the theme's style and,
// where it names one, its formatter.
func render(t *testing.T, th *theme.Theme, src string) string {
	t.Helper()
	opts := []glamour.TermRendererOption{glamour.WithStyles(th.Markdown()), glamour.WithWordWrap(60)}
	if f := th.ChromaFormatter(); f != "" {
		opts = append(opts, glamour.WithChromaFormatter(f))
	}
	r, err := glamour.NewTermRenderer(opts...)
	if err != nil {
		t.Fatalf("renderer: %v", err)
	}
	out, err := r.Render(src)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return out
}

// painted renders Markdown and returns its characters, without the newlines
// and without the hyperlink sequences glamour wraps a link in.
func painted(t *testing.T, th *theme.Theme, src string) []cell {
	t.Helper()
	out := regexp.MustCompile("\x1b\\]8;[^\a]*\a").ReplaceAllString(render(t, th, src), "")
	return paint(t, strings.ReplaceAll(out, "\n", ""))
}

// find returns the cells that draw the first occurrence of want.
func find(t *testing.T, cells []cell, want string) []cell {
	t.Helper()
	all := text(cells)
	i := strings.Index(all, want)
	if i < 0 {
		t.Fatalf("%q is not in %q", want, all)
	}
	start := len([]rune(all[:i]))
	return cells[start : start+len([]rune(want))]
}

func TestChromaFormatter(t *testing.T) {
	t.Parallel()
	want := map[string][2]string{ // formatter, chroma theme
		"truecolor/dark":    {"terminal16m", "github-dark"},
		"truecolor/light":   {"terminal16m", "github"},
		"truecolor/unknown": {"", ""},
		"256/dark":          {"terminal256", "github-dark"},
		"256/light":         {"terminal256", "github"},
		"256/unknown":       {"", ""},
		"16/dark":           {"", ""},
		"16/light":          {"", ""},
		"16/unknown":        {"", ""},
		"none/dark":         {"", ""},
		"none/light":        {"", ""},
		"none/unknown":      {"", ""},
	}
	for _, c := range colors() {
		for _, bg := range backgrounds() {
			for _, th := range []*theme.Theme{newTheme(c, bg), asciiTheme(c, bg)} {
				name := capsName(c, bg)
				w, ok := want[name]
				if !ok {
					t.Fatalf("no row for %s", name)
				}
				if got := th.ChromaFormatter(); got != w[0] {
					t.Errorf("%s: ChromaFormatter = %q, want %q", name, got, w[0])
				}
				md := th.Markdown()
				if md.CodeBlock.Theme != w[1] {
					t.Errorf("%s: code block theme = %q, want %q", name, md.CodeBlock.Theme, w[1])
				}
				if md.CodeBlock.Chroma != nil {
					t.Errorf("%s: the code block sets chroma colours of its own", name)
				}
				if md.Document.Color != nil || md.Document.BackgroundColor != nil || md.Text.Color != nil || md.Paragraph.Color != nil {
					t.Errorf("%s: body text is given a colour", name)
				}
				if md.Document.Margin != nil || md.CodeBlock.Margin != nil {
					t.Errorf("%s: the style has a margin; whoever draws a reply indents it", name)
				}
			}
		}
	}
	if got := theme.New(termcaps.Caps{}, theme.Options{}).ChromaFormatter(); got != "" {
		t.Errorf("ChromaFormatter of an unset Caps = %q", got)
	}
}

func TestMarkdownTakesItsAccentsFromTheRoles(t *testing.T) {
	t.Parallel()
	const src = "# Title here\n\nplain words with `inline` code and a [label](https://example.com/x).\n\n> quoted words\n"
	for _, c := range colors() {
		for _, bg := range backgrounds() {
			t.Run(capsName(c, bg), func(t *testing.T) {
				t.Parallel()
				th := newTheme(c, bg)
				cells := painted(t, th, src)
				accent, tool, dim := drawn(t, th, "accent"), drawn(t, th, "tool"), drawn(t, th, "dim")

				for _, got := range find(t, cells, "plain words with") {
					if got.fg != "" || got.bg != "" || got.bold || got.faint {
						t.Fatalf("body text is styled: %+v", got)
					}
				}
				for _, got := range find(t, cells, "# Title here") {
					if got.fg != accent.fg || !got.bold {
						t.Fatalf("heading drawn %+v, want the accent %+v", got, accent)
					}
				}
				for _, got := range find(t, cells, "label") {
					if got.fg != tool.fg {
						t.Fatalf("link text drawn %+v, want the tool tone %+v", got, tool)
					}
				}
				// glamour draws nothing faint, so where dim is faint and has
				// no colour these two are told by their shape alone.
				for _, got := range find(t, cells, "https://example.com/x") {
					if got.fg != dim.fg || !got.underline {
						t.Fatalf("address drawn %+v, want the dim colour %q and underlined", got, dim.fg)
					}
				}
				for _, got := range find(t, cells, "quoted words") {
					if got.fg != dim.fg {
						t.Fatalf("quote drawn %+v, want the dim colour %q", got, dim.fg)
					}
				}
				if !strings.Contains(text(cells), th.G.Sep+" quoted words") {
					t.Errorf("the quote has no bar: %q", text(cells))
				}
				if accent.fg == "" {
					// No colour to tell inline code by: the backquotes stay.
					if !strings.Contains(text(cells), "`inline`") {
						t.Errorf("inline code lost its backquotes: %q", text(cells))
					}
					return
				}
				fill := paint(t, th.CodeFill("x", 1))[0].bg
				for _, got := range find(t, cells, "inline") {
					if got.fg != accent.fg || got.bg != fill || got.bold {
						t.Fatalf("inline code drawn %+v, want the accent colour %q on %q", got, accent.fg, fill)
					}
				}
				if strings.Contains(text(cells), "`") {
					t.Errorf("inline code kept its backquotes: %q", text(cells))
				}
			})
		}
	}
}

func TestMarkdownUsesTheGlyphSet(t *testing.T) {
	t.Parallel()
	const src = "- item\n\n- [x] done\n- [ ] open\n\n> quote\n\n---\n\n| a | b |\n| - | - |\n| 1 | 2 |\n\n![alt](x.png)\n\nTerm\n: meaning\n"
	uni := text(painted(t, newTheme(termcaps.ColorNone, termcaps.BgUnknown), src))
	for _, want := range []string{"• item", "[x] done", "[ ] open", "│ quote", "────────", "Image: alt →"} {
		if !strings.Contains(uni, want) {
			t.Errorf("Unicode rendering has no %q:\n%s", want, uni)
		}
	}

	for _, c := range colors() {
		for _, bg := range backgrounds() {
			th := asciiTheme(c, bg)
			cfg, err := json.Marshal(th.Markdown())
			if err != nil {
				t.Fatal(err)
			}
			out := render(t, th, src+"\n# Heading\n\n`code` and *emphasis*\n\n```go\nx := 1\n```\n")
			for name, s := range map[string]string{"style": string(cfg), "rendering": out} {
				for i := range len(s) {
					if s[i] >= 0x80 {
						t.Fatalf("%s: the ASCII %s holds byte %#x: %q", capsName(c, bg), name, s[i], s)
					}
				}
			}
			for _, want := range []string{"- item", "| quote", "--------", "Image: alt ->", "-> meaning", "+---"} {
				if !strings.Contains(ansi.Strip(out), want) {
					t.Errorf("%s: ASCII rendering has no %q:\n%s", capsName(c, bg), want, ansi.Strip(out))
				}
			}
		}
	}
}

func TestMarkdownFollowsAChangeOfTheme(t *testing.T) {
	t.Parallel()
	// glamour keeps a code block's own colours for the life of the process,
	// which is why the style never sets any. Rendering dark, light and dark
	// again must give the first result a third time, and what is in the
	// golden file whichever test ran first.
	const src = "## Fix\n\nPut the time note after the tool steps:\n\n```go\nliveMsgs = append(liveMsgs, toolSteps...) // then the note\n```\n"
	dark, light := newTheme(termcaps.ColorTrue, termcaps.BgDark), newTheme(termcaps.ColorTrue, termcaps.BgLight)
	first := render(t, dark, src)
	second := render(t, light, src)
	third := render(t, dark, src)
	if first != third {
		t.Errorf("the dark rendering changed after a light one:\n%q\n%q", first, third)
	}
	if first == second {
		t.Error("dark and light render the same")
	}
	indexed := render(t, newTheme(termcaps.Color256, termcaps.BgDark), src)
	plain := render(t, newTheme(termcaps.Color16, termcaps.BgDark), src)
	if !strings.Contains(first, "38;2;") || strings.Contains(indexed, "38;2;") || !strings.Contains(indexed, "38;5;") {
		t.Errorf("highlighting does not follow the number of colours:\n%q\n%q", first, indexed)
	}
	for _, c := range find(t, paint(t, strings.ReplaceAll(plain, "\n", "")), "liveMsgs = append(liveMsgs, toolSteps...) // then the note") {
		if c.fg != "" || c.bg != "" || c.bold || c.italic {
			t.Fatalf("code is highlighted with 16 colours: %+v", c)
		}
	}
	tuitest.Golden(t, "markdown.golden", []byte(
		"truecolor/dark\n"+quoted(strings.Split(first, "\n")...)+
			"\ntruecolor/light\n"+quoted(strings.Split(second, "\n")...)+
			"\n256/dark\n"+quoted(strings.Split(indexed, "\n")...)+
			"\n16/dark\n"+quoted(strings.Split(plain, "\n")...)))
}
