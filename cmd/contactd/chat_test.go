package main

import (
	"context"
	"flag"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/colorprofile"

	"github.com/quinnovator/contactd/internal/tui/termcaps"
)

// source is where the display settings of one run come from: the command
// line, the environment and chat.toml. TERM is xterm-256color unless the
// environment says otherwise, and the terminal seems to show 256 colours.
type source struct {
	args []string
	env  map[string]string
	// file is chat.toml; "" means there is none.
	file string
}

// settleFrom parses the flags and settles the display settings the way
// chatMain does, with a config directory of the test's own.
func settleFrom(t *testing.T, src source) (display, error) {
	t.Helper()
	dir := t.TempDir()
	if src.file != "" {
		if err := os.WriteFile(filepath.Join(dir, "chat.toml"), []byte(src.file), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	env := map[string]string{"TERM": "xterm-256color", "CONTACTD_CONFIG_DIR": dir}
	maps.Copy(env, src.env)
	fs := flag.NewFlagSet("contactd chat", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	look := addDisplayFlags(fs)
	if err := fs.Parse(src.args); err != nil {
		t.Fatalf("the flags %q do not parse: %v", src.args, err)
	}
	return look.settle(func(name string) string { return env[name] }, "/home/jack", "/home/jack/.cache", termcaps.Color256)
}

// For each setting the flag wins, then the CONTACTD_ variable, then
// chat.toml; for the colours NO_COLOR comes next, and then what was detected.
func TestDisplaySettingsPrecedence(t *testing.T) {
	t.Parallel()
	all := "color = \"16\"\nglyphs = \"ascii\"\ntheme = \"light\"\nimages = \"text\"\nprobe = false\nmods = false\n"
	for _, tc := range []struct {
		name string
		src  source
		want termcaps.Overrides
		// caps is what the client then starts with.
		color  termcaps.Color
		glyphs termcaps.Glyphs
		mods   bool
	}{
		{
			name: "nothing said: everything is detected",
			want: termcaps.Overrides{}, color: termcaps.Color256, glyphs: termcaps.GlyphsUnicode, mods: true,
		},
		{
			name: "NO_COLOR is heard before the terminal",
			src:  source{env: map[string]string{"NO_COLOR": "1"}},
			want: termcaps.Overrides{}, color: termcaps.ColorNone, glyphs: termcaps.GlyphsUnicode, mods: true,
		},
		{
			name:  "chat.toml",
			src:   source{file: all},
			want:  termcaps.Overrides{Color: "16", Glyphs: "ascii", Theme: "light", Images: "text", NoProbe: true},
			color: termcaps.Color16, glyphs: termcaps.GlyphsASCII,
		},
		{
			name: "chat.toml wins over NO_COLOR",
			src:  source{file: "color = 256\n", env: map[string]string{"NO_COLOR": "1"}},
			want: termcaps.Overrides{Color: "256"}, color: termcaps.Color256, glyphs: termcaps.GlyphsUnicode, mods: true,
		},
		{
			name: "the variables win over chat.toml",
			src: source{file: all, env: map[string]string{
				"CONTACTD_COLOR": "truecolor", "CONTACTD_GLYPHS": "unicode", "CONTACTD_THEME": "dark",
				"CONTACTD_IMAGES": "blocks", "CONTACTD_PROBE": "true", "CONTACTD_MODS": "1",
			}},
			want:  termcaps.Overrides{Color: "truecolor", Glyphs: "unicode", Theme: "dark", Images: "blocks"},
			color: termcaps.ColorTrue, glyphs: termcaps.GlyphsUnicode, mods: true,
		},
		{
			name: "the flags win over the variables and chat.toml",
			src: source{
				args: []string{"-color=none", "-glyphs=ascii", "-theme=light", "-images=text", "-probe=false", "-mods=false"},
				env: map[string]string{
					"CONTACTD_COLOR": "truecolor", "CONTACTD_GLYPHS": "unicode", "CONTACTD_THEME": "dark",
					"CONTACTD_IMAGES": "blocks", "CONTACTD_PROBE": "true", "CONTACTD_MODS": "true",
				},
				file: "color = \"16\"\ntheme = \"dark\"\nimages = \"pixels\"\n",
			},
			want:  termcaps.Overrides{Color: "none", Glyphs: "ascii", Theme: "light", Images: "text", NoProbe: true},
			color: termcaps.ColorNone, glyphs: termcaps.GlyphsASCII,
		},
		{
			name: "a flag that says auto still wins, and means detect",
			src: source{
				args: []string{"-color=auto", "--glyphs", "auto", "-probe", "-mods"},
				env:  map[string]string{"CONTACTD_COLOR": "16", "CONTACTD_PROBE": "false", "CONTACTD_MODS": "false"},
				file: all,
			},
			want:  termcaps.Overrides{Theme: "light", Images: "text"},
			color: termcaps.Color256, glyphs: termcaps.GlyphsUnicode, mods: true,
		},
		{
			name:  "a variable that says auto wins over chat.toml",
			src:   source{env: map[string]string{"CONTACTD_COLOR": "auto", "CONTACTD_THEME": "AUTO"}, file: all},
			want:  termcaps.Overrides{Glyphs: "ascii", Images: "text", NoProbe: true},
			color: termcaps.Color256, glyphs: termcaps.GlyphsASCII,
		},
		{
			name: "-ascii is -glyphs=ascii",
			src:  source{args: []string{"--ascii"}, env: map[string]string{"CONTACTD_GLYPHS": "unicode"}, file: "glyphs = \"unicode\"\n"},
			want: termcaps.Overrides{Glyphs: "ascii"}, color: termcaps.Color256, glyphs: termcaps.GlyphsASCII, mods: true,
		},
		{
			name: "-ascii beside the same -glyphs",
			src:  source{args: []string{"-ascii", "-glyphs=ASCII"}},
			want: termcaps.Overrides{Glyphs: "ascii"}, color: termcaps.Color256, glyphs: termcaps.GlyphsASCII, mods: true,
		},
		{
			name: "NO_COLOR loses to the variable and to the flag",
			src:  source{args: []string{"--color=16"}, env: map[string]string{"NO_COLOR": "1", "CONTACTD_COLOR": "256"}},
			want: termcaps.Overrides{Color: "16"}, color: termcaps.Color16, glyphs: termcaps.GlyphsUnicode, mods: true,
		},
		{
			name: "a locale that is not UTF-8 is detected as ASCII, and a setting wins over it",
			src:  source{env: map[string]string{"LANG": "C", "CONTACTD_GLYPHS": "unicode"}},
			want: termcaps.Overrides{Glyphs: "unicode"}, color: termcaps.Color256, glyphs: termcaps.GlyphsUnicode, mods: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d, err := settleFrom(t, tc.src)
			if err != nil {
				t.Fatal(err)
			}
			if d.over != tc.want {
				t.Errorf("the settings are\n%+v, want\n%+v", d.over, tc.want)
			}
			caps := termcaps.Resolve(d.env, d.over, termcaps.Replies{})
			if caps.Color != tc.color || caps.Glyphs != tc.glyphs {
				t.Errorf("the client starts with %v colours and %v glyphs, want %v and %v", caps.Color, caps.Glyphs, tc.color, tc.glyphs)
			}
			if d.mods != tc.mods {
				t.Errorf("mods are loaded: %v, want %v", d.mods, tc.mods)
			}
			if d.env.Term != "xterm-256color" || d.env.Detected != termcaps.Color256 {
				t.Errorf("the environment was read as %+v", d.env)
			}
		})
	}
}

// A terminal that says it cannot place the cursor is not given a full
// screen, unless the person says with -color what it can do.
func TestADumbTerminalIsRefused(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		src     source
		refused string // what the refusal starts with; "" when the client starts
	}{
		{name: "dumb", src: source{env: map[string]string{"TERM": "dumb"}}, refused: `TERM is "dumb"`},
		{name: "DUMB", src: source{env: map[string]string{"TERM": "DUMB"}}, refused: `TERM is "DUMB"`},
		{name: "not set", src: source{env: map[string]string{"TERM": ""}}, refused: "TERM is not set"},
		{
			name:    "the variable and chat.toml are not the person saying so now",
			src:     source{env: map[string]string{"TERM": "dumb", "CONTACTD_COLOR": "256"}, file: "color = \"16\"\n"},
			refused: `TERM is "dumb"`,
		},
		{
			name:    "refused before chat.toml is read",
			src:     source{env: map[string]string{"TERM": "dumb"}, file: "color = 8\n"},
			refused: `TERM is "dumb"`,
		},
		{name: "dumb with -color", src: source{args: []string{"-color=none"}, env: map[string]string{"TERM": "dumb"}}},
		{name: "dumb with -color given and empty", src: source{args: []string{"-color="}, env: map[string]string{"TERM": "dumb"}}},
		{name: "not set with -color", src: source{args: []string{"-color", "auto"}, env: map[string]string{"TERM": ""}}},
		{name: "a terminal", src: source{env: map[string]string{"TERM": "vt100"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d, err := settleFrom(t, tc.src)
			if tc.refused == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				if caps := termcaps.Resolve(d.env, d.over, termcaps.Replies{}); caps.Glyphs != termcaps.GlyphsASCII {
					t.Errorf("a plain terminal is drawn with %v glyphs", caps.Glyphs)
				}
				return
			}
			if err == nil {
				t.Fatal("the full-screen client would start")
			}
			msg := err.Error()
			if !strings.HasPrefix(msg, tc.refused) {
				t.Errorf("the refusal is %q, want it to start with %q", msg, tc.refused)
			}
			// It says what to do instead: one-shot mode, or -color.
			for _, part := range []string{`contactd chat ada "question"`, "-color"} {
				if !strings.Contains(msg, part) {
					t.Errorf("the refusal does not mention %s: %q", part, msg)
				}
			}
		})
	}
}

func TestDisplaySettingsRefused(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		src  source
		want []string // parts of the error
	}{
		{name: "a flag's value", src: source{args: []string{"-color=24bit"}}, want: []string{"color", `"24bit"`, "truecolor"}},
		{
			name: "every bad value is named",
			src:  source{args: []string{"-theme=solarized"}, env: map[string]string{"CONTACTD_IMAGES": "sixel"}},
			want: []string{`"solarized"`, `"sixel"`},
		},
		{name: "a variable that is not yes or no", src: source{env: map[string]string{"CONTACTD_PROBE": "maybe"}}, want: []string{"CONTACTD_PROBE", `"maybe"`}},
		{name: "the other one", src: source{env: map[string]string{"CONTACTD_MODS": "2"}}, want: []string{"CONTACTD_MODS", `"2"`}},
		{name: "-ascii against -glyphs", src: source{args: []string{"-ascii", "-glyphs=unicode"}}, want: []string{"-ascii", "-glyphs=unicode"}},
		{name: "a mistake in chat.toml", src: source{file: "theme = \"dark\"\ncolor = 8\n"}, want: []string{"chat.toml", "line 2", "color is 8"}},
		{name: "chat.toml that does not parse", src: source{file: "color = \n"}, want: []string{"chat.toml", "line 1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := settleFrom(t, tc.src)
			if err == nil {
				t.Fatal("the settings were accepted")
			}
			for _, part := range tc.want {
				if !strings.Contains(err.Error(), part) {
					t.Errorf("the error does not mention %s: %q", part, err)
				}
			}
		})
	}
}

// What chat.toml holds besides the settings reaches the client, and what
// it holds that the client does not know is said, with the file and the line.
func TestChatFileIsLoaded(t *testing.T) {
	t.Parallel()
	d, err := settleFrom(t, source{file: "turn = 8\ntheme = \"dark\"\n\n[glyph]\ntool = \"●\"\nelbow = \"└\"\n"})
	if err != nil {
		t.Fatal(err)
	}
	if d.chat.Glyph["tool"] != "●" || d.chat.Glyph["elbow"] != "└" || len(d.chat.Glyph) != 2 {
		t.Errorf("the glyph table is %q", d.chat.Glyph)
	}
	if d.over.Theme != "dark" {
		t.Errorf("the theme is %q, want dark: an unknown key does not spoil the rest", d.over.Theme)
	}
	want := []string{d.dirs.ChatFile() + `: line 1: unknown key "turn"`}
	if !slices.Equal(d.warnings, want) {
		t.Errorf("the warnings are %q, want %q", d.warnings, want)
	}
	if filepath.Base(d.dirs.ChatFile()) != "chat.toml" || !filepath.IsAbs(d.dirs.ChatFile()) {
		t.Errorf("chat.toml is at %q", d.dirs.ChatFile())
	}

	// No file at all is the defaults, with nothing to say.
	d, err = settleFrom(t, source{})
	if err != nil || len(d.warnings) != 0 || d.chat.Glyph != nil || !d.chat.Probe || !d.chat.Mods {
		t.Errorf("with no chat.toml: %+v, warnings %q, error %v", d.chat, d.warnings, err)
	}
}

// The config directory is the flag's, then the variable's, then the one the
// system's conventions name.
func TestConfigDirPrecedence(t *testing.T) {
	t.Parallel()
	flagged, named := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(flagged, "chat.toml"), []byte("theme = \"light\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(named, "chat.toml"), []byte("theme = \"dark\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		args  []string
		env   map[string]string
		dir   string
		theme string
	}{
		{name: "the flag", args: []string{"-config-dir", flagged}, env: map[string]string{"CONTACTD_CONFIG_DIR": named}, dir: flagged, theme: "light"},
		{name: "the variable", env: map[string]string{"CONTACTD_CONFIG_DIR": named, "XDG_CONFIG_HOME": "/xdg"}, dir: named, theme: "dark"},
		{name: "XDG", env: map[string]string{"XDG_CONFIG_HOME": "/xdg"}, dir: "/xdg/contactd"},
		{name: "the home directory", dir: "/home/jack/.config/contactd"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := map[string]string{"TERM": "xterm-256color"}
			maps.Copy(env, tc.env)
			fs := flag.NewFlagSet("contactd chat", flag.ContinueOnError)
			look := addDisplayFlags(fs)
			if err := fs.Parse(tc.args); err != nil {
				t.Fatal(err)
			}
			d, err := look.settle(func(name string) string { return env[name] }, "/home/jack", "/home/jack/.cache", termcaps.ColorTrue)
			if err != nil {
				t.Fatal(err)
			}
			if d.dirs.Config != tc.dir || d.over.Theme != tc.theme {
				t.Errorf("the config directory is %q with the theme %q, want %q and %q", d.dirs.Config, d.over.Theme, tc.dir, tc.theme)
			}
			if d.dirs.Cache != "/home/jack/.cache/contactd" {
				t.Errorf("the cache directory is %q", d.dirs.Cache)
			}
		})
	}
}

func TestColours(t *testing.T) {
	t.Parallel()
	for profile, want := range map[colorprofile.Profile]termcaps.Color{
		colorprofile.TrueColor:   termcaps.ColorTrue,
		colorprofile.ANSI256:     termcaps.Color256,
		colorprofile.ANSI:        termcaps.Color16,
		colorprofile.ASCII:       termcaps.ColorNone,
		colorprofile.NoTTY:       termcaps.ColorNone,
		colorprofile.Unknown:     termcaps.ColorNone,
		colorprofile.Profile(99): termcaps.ColorNone,
	} {
		if got := colours(profile); got != want {
			t.Errorf("a %v terminal is read as %v colours, want %v", profile, got, want)
		}
	}
}

// The mod host is opened unless the settings say no mods, over the config
// directory's mods and the cache directory's compiled modules. It loads
// nothing by being opened. With no host, the reason is there to be shown.
func TestOpenMods(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		src  source
		// state is mods.toml in the config directory; "" means there is none.
		state string
		// installed is the name of a mod in the config directory, or "".
		installed string
		// why is the reason there is no host; "" means there is one.
		why string
	}{
		{name: "nothing says otherwise"},
		{name: "a mod is installed and on", installed: "git", state: "[mod.git]\non = true\n"},
		{name: "the flag", src: source{args: []string{"-mods=false"}}, why: "the client was started with -mods=false"},
		{
			name: "the flag wins over the variable",
			src:  source{args: []string{"-mods=false"}, env: map[string]string{"CONTACTD_MODS": "true"}},
			why:  "the client was started with -mods=false",
		},
		{name: "the variable", src: source{env: map[string]string{"CONTACTD_MODS": "0"}}, why: `CONTACTD_MODS is "0"`},
		{name: "chat.toml", src: source{file: "mods = false\n"}, why: "chat.toml has mods = false"},
		{name: "the variable wins over chat.toml", src: source{file: "mods = false\n", env: map[string]string{"CONTACTD_MODS": "true"}}},
		{name: "a record of the mods that cannot be read", state: "[mod.git\n", why: "mods.toml: mods state: line 1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			if tc.src.env == nil {
				tc.src.env = map[string]string{}
			}
			// Both directories are the test's own, and chat.toml is in the one
			// the host is opened over.
			tc.src.env["CONTACTD_CONFIG_DIR"], tc.src.env["XDG_CACHE_HOME"] = dir, t.TempDir()
			if tc.src.file != "" {
				if err := os.WriteFile(filepath.Join(dir, "chat.toml"), []byte(tc.src.file), 0o600); err != nil {
					t.Fatal(err)
				}
				tc.src.file = ""
			}
			if tc.state != "" {
				if err := os.WriteFile(filepath.Join(dir, "mods.toml"), []byte(tc.state), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.installed != "" {
				manifest, err := os.ReadFile(filepath.Join("..", "..", "examples", "mods", tc.installed, "mod.toml"))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Join(dir, "mods", tc.installed), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "mods", tc.installed, "mod.toml"), manifest, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			d, err := settleFrom(t, tc.src)
			if err != nil {
				t.Fatal(err)
			}
			if d.dirs.Config != dir {
				t.Fatalf("the config directory is %q, want %q", d.dirs.Config, dir)
			}
			// What the interface is started with carries the host, or the reason
			// there is none, beside everything else that was settled.
			opt := d.options("/home/jack", "/home/jack/src")
			host, why := opt.Mods, opt.ModsOff
			if opt.Dirs != d.dirs || opt.Home != "/home/jack" || opt.WorkDir != "/home/jack/src" ||
				opt.Env != d.env || opt.Overrides != d.over {
				t.Errorf("the interface is started with %+v", opt)
			}
			if host != nil {
				t.Cleanup(func() {
					if err := host.Close(context.WithoutCancel(t.Context())); err != nil {
						t.Errorf("Close: %v", err)
					}
				})
			}
			switch {
			case tc.why == "" && (host == nil || why != ""):
				t.Fatalf("there is no host: %q", why)
			case tc.why != "" && (host != nil || !strings.Contains(why, tc.why)):
				t.Fatalf("the host is %v and the reason %q, want no host and %q", host, why, tc.why)
			case host == nil:
				return
			}
			// The mods the directory holds are listed, and none is loaded yet.
			var listed []string
			for _, info := range host.Mods() {
				listed = append(listed, info.Manifest.Name+" "+info.Status.String())
			}
			want := []string(nil)
			if tc.installed != "" {
				want = []string{tc.installed + " loading"}
			}
			if !slices.Equal(listed, want) {
				t.Errorf("the host lists %q, want %q", listed, want)
			}
		})
	}
}
