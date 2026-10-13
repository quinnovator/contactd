package termcaps_test

import (
	"strings"
	"testing"

	"github.com/quinnovator/contactd/internal/tui/termcaps"
)

func TestReadEnv(t *testing.T) {
	t.Parallel()
	vars := map[string]string{
		"TERM": "xterm-kitty", "COLORTERM": "truecolor", "TERM_PROGRAM": "ghostty", "NO_COLOR": "yes",
		"LC_ALL": "en_US.UTF-8", "LC_CTYPE": "UTF-8", "LANG": "C", "TMUX": "/tmp/tmux-501/default,1,0",
		"STY": "1234.pts-0.host", "SSH_TTY": "/dev/pts/3", "SSH_CONNECTION": "10.0.0.1 50000 10.0.0.2 22",
		"RUNEWIDTH_EASTASIAN": "1",
		// Not read: nothing here is evidence of anything.
		"KITTY_WINDOW_ID": "7", "CLICOLOR_FORCE": "1", "LC_TERMINAL": "iTerm2",
	}
	asked := map[string]int{}
	got := termcaps.ReadEnv(func(name string) string {
		asked[name]++
		return vars[name]
	}, termcaps.ColorTrue)
	want := termcaps.Env{
		Term: "xterm-kitty", ColorTerm: "truecolor", TermProgram: "ghostty", NoColor: "yes",
		LCAll: "en_US.UTF-8", LCCtype: "UTF-8", Lang: "C", Tmux: "/tmp/tmux-501/default,1,0",
		Sty: "1234.pts-0.host", SSHTTY: "/dev/pts/3", SSHConnection: "10.0.0.1 50000 10.0.0.2 22",
		EastAsian: "1", Detected: termcaps.ColorTrue,
	}
	if got != want {
		t.Errorf("ReadEnv:\n got %+v\nwant %+v", got, want)
	}
	for _, name := range []string{"KITTY_WINDOW_ID", "CLICOLOR_FORCE", "LC_TERMINAL"} {
		if asked[name] != 0 {
			t.Errorf("ReadEnv read %s", name)
		}
	}
	if len(asked) != 12 {
		t.Errorf("ReadEnv read %d variables, want 12: %v", len(asked), asked)
	}

	empty := termcaps.ReadEnv(func(string) string { return "" }, 0)
	if empty != (termcaps.Env{}) {
		t.Errorf("ReadEnv of an empty environment = %+v", empty)
	}
}

func TestParseOverrides(t *testing.T) {
	t.Parallel()
	type in struct {
		color, glyphs, theme, images string
		noProbe                      bool
	}
	cases := []struct {
		name string
		in   in
		want termcaps.Overrides
		errs []string // each must be in the error; none means no error
	}{
		{name: "nothing set", want: termcaps.Overrides{}},
		{name: "auto is detection", in: in{"auto", "auto", "auto", "auto", false}, want: termcaps.Overrides{}},
		{
			name: "one word each",
			in:   in{"truecolor", "unicode", "dark", "pixels", true},
			want: termcaps.Overrides{Color: "truecolor", Glyphs: "unicode", Theme: "dark", Images: "pixels", NoProbe: true},
		},
		{
			name: "the other words",
			in:   in{"256", "ascii", "light", "blocks", false},
			want: termcaps.Overrides{Color: "256", Glyphs: "ascii", Theme: "light", Images: "blocks"},
		},
		{name: "16 colours and text", in: in{color: "16", images: "text"}, want: termcaps.Overrides{Color: "16", Images: "text"}},
		{name: "no colour", in: in{color: "none"}, want: termcaps.Overrides{Color: "none"}},
		{
			name: "case and spaces do not matter",
			in:   in{" TrueColor ", "ASCII", "Dark\n", "\tBlocks", false},
			want: termcaps.Overrides{Color: "truecolor", Glyphs: "ascii", Theme: "dark", Images: "blocks"},
		},
		{name: "AUTO in capitals", in: in{color: "AUTO"}, want: termcaps.Overrides{}},
		{name: "only the probe switched off", in: in{noProbe: true}, want: termcaps.Overrides{NoProbe: true}},
		{
			name: "a colour that is not a word",
			in:   in{color: "24bit", glyphs: "ascii"},
			want: termcaps.Overrides{Glyphs: "ascii"},
			errs: []string{`color: "24bit" is not one of auto, truecolor, 256, 16, none`},
		},
		{
			name: "a glyph set that is not a word",
			in:   in{glyphs: "utf8"}, errs: []string{`glyphs: "utf8" is not one of auto, unicode, ascii`},
		},
		{
			name: "a theme that is not a word",
			in:   in{theme: "solarized"}, errs: []string{`theme: "solarized" is not one of auto, dark, light`},
		},
		{
			name: "an image level that is not a word",
			in:   in{images: "sixel"}, errs: []string{`images: "sixel" is not one of auto, pixels, blocks, text`},
		},
		{
			name: "a word of another setting",
			in:   in{color: "dark", theme: "256"},
			errs: []string{`color: "dark" is not one of`, `theme: "256" is not one of`},
		},
		{
			name: "every bad value is reported, and the good ones kept",
			in:   in{"x", "ascii", "y", "z", true},
			want: termcaps.Overrides{Glyphs: "ascii", NoProbe: true},
			errs: []string{`color: "x"`, `theme: "y"`, `images: "z"`},
		},
		{
			name: "a hostile value is quoted in the error",
			in:   in{color: "\x1b[2J"}, errs: []string{`color: "\x1b[2J" is not one of`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := termcaps.ParseOverrides(tc.in.color, tc.in.glyphs, tc.in.theme, tc.in.images, tc.in.noProbe)
			if got != tc.want {
				t.Errorf("ParseOverrides = %+v, want %+v", got, tc.want)
			}
			if (err != nil) != (len(tc.errs) > 0) {
				t.Fatalf("ParseOverrides error = %v, want %d problems", err, len(tc.errs))
			}
			for _, want := range tc.errs {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not say %q", err, want)
				}
			}
			if err != nil && strings.ContainsRune(err.Error(), 0x1B) {
				t.Errorf("error %q holds an escape character", err)
			}
		})
	}
}

// What ParseOverrides accepts is what Resolve acts on: each word changes the
// field it names.
func TestParsedOverridesReachResolve(t *testing.T) {
	t.Parallel()
	cases := []struct {
		color, glyphs, theme, images string
		check                        func(termcaps.Caps) bool
	}{
		{color: "truecolor", check: func(c termcaps.Caps) bool { return c.Color == termcaps.ColorTrue }},
		{color: "256", check: func(c termcaps.Caps) bool { return c.Color == termcaps.Color256 }},
		{color: "16", check: func(c termcaps.Caps) bool { return c.Color == termcaps.Color16 }},
		{color: "none", check: func(c termcaps.Caps) bool { return c.Color == termcaps.ColorNone }},
		{glyphs: "unicode", check: func(c termcaps.Caps) bool { return c.Glyphs == termcaps.GlyphsUnicode }},
		{glyphs: "ascii", check: func(c termcaps.Caps) bool { return c.Glyphs == termcaps.GlyphsASCII }},
		{theme: "dark", check: func(c termcaps.Caps) bool { return c.Background == termcaps.BgDark }},
		{theme: "light", check: func(c termcaps.Caps) bool { return c.Background == termcaps.BgLight }},
		{images: "pixels", glyphs: "unicode", color: "256", check: func(c termcaps.Caps) bool { return c.Images == termcaps.ImagesPixels }},
		{images: "blocks", glyphs: "unicode", color: "16", check: func(c termcaps.Caps) bool { return c.Images == termcaps.ImagesBlocks }},
		{images: "text", glyphs: "unicode", color: "truecolor", check: func(c termcaps.Caps) bool { return c.Images == termcaps.ImagesText }},
	}
	// An environment in which every one of those is a change: a kitty that
	// answered, or the Linux console.
	for _, env := range []termcaps.Env{kitty(), xterm(term("linux"), detected(termcaps.Color16))} {
		for _, tc := range cases {
			o, err := termcaps.ParseOverrides(strings.ToUpper(tc.color), tc.glyphs, tc.theme, tc.images, false)
			if err != nil {
				t.Fatal(err)
			}
			if c := termcaps.Resolve(env, o, fold(kittyReply(31, true), background(env.Term == "linux"), da1())); !tc.check(c) {
				t.Errorf("with %+v on TERM=%s: Caps = %+v", o, env.Term, c)
			}
		}
	}
}

func TestNames(t *testing.T) {
	t.Parallel()
	cases := []struct {
		got, want string
	}{
		{termcaps.ColorNone.String(), "none"},
		{termcaps.Color16.String(), "16"},
		{termcaps.Color256.String(), "256"},
		{termcaps.ColorTrue.String(), "truecolor"},
		{termcaps.Color(0).String(), "unset"},
		{termcaps.BgUnknown.String(), "unknown"},
		{termcaps.BgDark.String(), "dark"},
		{termcaps.BgLight.String(), "light"},
		{termcaps.Background(0).String(), "unset"},
		{termcaps.GlyphsUnicode.String(), "unicode"},
		{termcaps.GlyphsASCII.String(), "ascii"},
		{termcaps.Glyphs(0).String(), "unset"},
		{termcaps.ImagesText.String(), "text"},
		{termcaps.ImagesBlocks.String(), "blocks"},
		{termcaps.ImagesPixels.String(), "pixels"},
		{termcaps.Images(9).String(), "unset"},
		{termcaps.WidthWc.String(), "wcwidth"},
		{termcaps.WidthGrapheme.String(), "grapheme"},
		{termcaps.Width(0).String(), "unset"},
		{termcaps.ProbeOff.String(), "off"},
		{termcaps.ProbeBasic.String(), "basic"},
		{termcaps.ProbeFull.String(), "full"},
		{termcaps.ProbeTmux.String(), "tmux"},
		{termcaps.Plan(0).String(), "unset"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("String() = %q, want %q", tc.got, tc.want)
		}
	}
}

// The orderings other packages rely on: "Color >= Color256" and the image
// levels from least to most. A zero value is below every real one.
func TestOrder(t *testing.T) {
	t.Parallel()
	if 0 >= termcaps.ColorNone || termcaps.ColorNone >= termcaps.Color16 ||
		termcaps.Color16 >= termcaps.Color256 || termcaps.Color256 >= termcaps.ColorTrue {
		t.Error("colours are not in order")
	}
	if 0 >= termcaps.ImagesText || termcaps.ImagesText >= termcaps.ImagesBlocks ||
		termcaps.ImagesBlocks >= termcaps.ImagesPixels {
		t.Error("image levels are not in order")
	}
	if termcaps.BgUnknown == 0 || termcaps.GlyphsUnicode == 0 || termcaps.WidthWc == 0 ||
		termcaps.ProbeOff == 0 || termcaps.ReplyBackground == 0 {
		t.Error("an enum starts at zero")
	}
}
