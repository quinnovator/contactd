package conf_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/quinnovator/contactd/internal/tui/conf"
	"github.com/quinnovator/contactd/internal/tui/toml"
)

func defaults() conf.Chat { return conf.Chat{Probe: true, Mods: true} }

// writeChat puts a chat.toml in a fresh directory and returns its path.
func writeChat(t *testing.T, src string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "chat.toml")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadChat(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		src      string
		want     conf.Chat
		warnings []string
	}{
		{"an empty file is the defaults", "", defaults(), nil},
		{"comments alone are the defaults", "# color = \"none\"\n\n# probe = false\n", defaults(), nil},
		{
			"the file as the design writes it",
			`color  = "auto"    # auto | truecolor | 256 | 16 | none
glyphs = "auto"    # auto | unicode | ascii
theme  = "auto"    # auto | dark | light
images = "auto"    # auto | pixels | blocks | text
probe  = true
mods   = true

[glyph]            # overrides for the Unicode set, by the names in section 3
tool  = "●"
elbow = "└"
`,
			conf.Chat{
				Color: "auto", Glyphs: "auto", Theme: "auto", Images: "auto", Probe: true, Mods: true,
				Glyph: map[string]string{"tool": "●", "elbow": "└"},
			},
			nil,
		},
		{
			"every setting given a value",
			"color = \"256\"\nglyphs = \"ascii\"\ntheme = \"light\"\nimages = \"blocks\"\nprobe = false\nmods = false\n",
			conf.Chat{Color: "256", Glyphs: "ascii", Theme: "light", Images: "blocks"},
			nil,
		},
		{
			"the other values",
			"color = \"truecolor\"\nglyphs = \"unicode\"\ntheme = \"dark\"\nimages = \"pixels\"\n",
			conf.Chat{Color: "truecolor", Glyphs: "unicode", Theme: "dark", Images: "pixels", Probe: true, Mods: true},
			nil,
		},
		{"colour 16", "color = \"16\"\n", conf.Chat{Color: "16", Probe: true, Mods: true}, nil},
		{"colour 256 as a number", "color = 256\n", conf.Chat{Color: "256", Probe: true, Mods: true}, nil},
		{"colour 16 as a number", "color = 16 # a plain console\n", conf.Chat{Color: "16", Probe: true, Mods: true}, nil},
		{
			"the design's own comment, taken at its word",
			"theme = \"dark\"\ncolor = 256\nprobe = false\n",
			conf.Chat{Color: "256", Theme: "dark", Mods: true},
			nil,
		},
		{"colour none", "color = \"none\"\n", conf.Chat{Color: "none", Probe: true, Mods: true}, nil},
		{"images text", "images = 'text'\n", conf.Chat{Images: "text", Probe: true, Mods: true}, nil},
		{"an empty value means detect, as auto does", "color = \"\"\ntheme = \"\"\n", defaults(), nil},
		{"one switch off leaves the other on", "mods = false\n", conf.Chat{Probe: true}, nil},
		{"an empty glyph table is no overrides", "[glyph]\n", defaults(), nil},
		{
			"glyph names and values are passed on unchecked",
			"[glyph]\nnot-a-glyph = \"\"\ntool = \"far too wide\"\n",
			conf.Chat{Probe: true, Mods: true, Glyph: map[string]string{"not-a-glyph": "", "tool": "far too wide"}},
			nil,
		},
		{
			"unknown keys are named, in file order, and the rest still counts",
			"colour = \"none\"\ncolor = \"16\"\nturn = 8\nprobe = false\n",
			conf.Chat{Color: "16", Mods: true},
			[]string{`line 1: unknown key "colour"`, `line 3: unknown key "turn"`},
		},
		{
			"unknown tables are named, an array of them once",
			"theme = \"dark\"\n[glyphs]\ntool = \"*\"\n[glyph]\ntool = \"+\"\n[glyph.box]\nh = \"-\"\n" +
				"[[segment]]\nname = \"cache\"\n[[segment]]\nname = \"clock\"\n[[command]]\n[[segment]]\n",
			conf.Chat{Theme: "dark", Probe: true, Mods: true, Glyph: map[string]string{"tool": "+"}},
			[]string{
				"line 2: unknown table [glyphs]", "line 6: unknown table [glyph.box]",
				"line 8: unknown table [[segment]]", "line 12: unknown table [[command]]",
			},
		},
		{
			"a long unknown name is cut short",
			strings.Repeat("k", 300) + " = 1\n[" + strings.Repeat("t", 300) + "]\n[[" + strings.Repeat("a", 300) + "]]\n",
			defaults(),
			[]string{
				`line 1: unknown key "` + strings.Repeat("k", 48) + `..."`,
				"line 2: unknown table [" + strings.Repeat("t", 48) + "...]",
				"line 3: unknown table [[" + strings.Repeat("a", 48) + "...]]",
			},
		},
		{
			"an unknown key may hold any kind of value",
			"a = 1\nb = [\"x\"]\nc = true\n",
			defaults(),
			[]string{`line 1: unknown key "a"`, `line 2: unknown key "b"`, `line 3: unknown key "c"`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, warnings, err := conf.LoadChat(writeChat(t, tc.src))
			if err != nil {
				t.Fatalf("LoadChat: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("chat\n got %+v\nwant %+v", got, tc.want)
			}
			if !reflect.DeepEqual(warnings, tc.warnings) {
				t.Errorf("warnings\n got %q\nwant %q", warnings, tc.warnings)
			}
		})
	}
}

func TestLoadChatMissing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, path := range []string{
		filepath.Join(dir, "chat.toml"),
		filepath.Join(dir, "no", "such", "directory", "chat.toml"),
		"", // no config directory at all
	} {
		got, warnings, err := conf.LoadChat(path)
		if err != nil || warnings != nil || !reflect.DeepEqual(got, defaults()) {
			t.Errorf("LoadChat(%q) = %+v, %q, %v; want the defaults", path, got, warnings, err)
		}
	}
}

func TestLoadChatRefuses(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		src  string
		line int    // the line a *toml.Error names; 0 when the error is not one
		msg  string // part of the message
	}{
		{"a colour that does not exist", "color = \"purple\"\n", 1, `color is "purple"; it can be auto, truecolor, 256, 16 or none`},
		{"a colour count that does not exist", "theme = \"dark\"\ncolor = 8\n", 2, "color is 8; it can be auto, truecolor, 256, 16 or none"},
		{"a colour count of nothing", "color = 0\n", 1, "color is 0; it can be auto"},
		{"a colour count below nothing", "\n\ncolor = -16\n", 3, "color is -16; it can be auto"},
		{"a colour count one off", "color = 257\n", 1, "color is 257; it can be auto"},
		{"a colour count past 64 bits of sense", "glyphs = \"ascii\"\ncolor = 9223372036854775807\n", 2, "color is 9223372036854775807; it can be auto"},
		{"a colour that is a switch", "color = true\n", 1, "color must be a string, not true or false"},
		{"a number where only a word goes", "theme = 16\n", 1, "theme must be a string, not an integer"},
		{"a number for the image level", "images = 256\n", 1, "images must be a string, not an integer"},
		{"a glyph set that does not exist", "# mine\n\nglyphs = \"emoji\"\n", 3, `glyphs is "emoji"; it can be auto, unicode or ascii`},
		{"a theme that does not exist", "theme = \"solarized\"\n", 1, `theme is "solarized"; it can be auto, dark or light`},
		{"an image level that does not exist", "probe = false\nimages = \"sixel\"\n", 2, `images is "sixel"; it can be auto, pixels, blocks or text`},
		{"a value in the wrong case", "theme = \"Dark\"\n", 1, `theme is "Dark"`},
		{"a value with a space", "color = \"16 \"\n", 1, `color is "16 "`},
		{"a list where one value goes", "images = [\"pixels\", \"blocks\"]\n", 1, "images must be a string, not an array of strings"},
		{"a switch written as a string", "\nprobe = \"no\"\n", 2, "probe must be true or false, not a string"},
		{"a switch written as a number", "probe = true\n\n\nmods = 0\n", 4, "mods must be true or false, not an integer"},
		{"a glyph that is not a string", "[glyph]\ntool = \"*\"\nelbow = 1\n", 3, "elbow must be a string, not an integer"},
		{"a glyph that is a switch", "[glyph]\ntool = true\n", 2, "tool must be a string, not true or false"},
		{"glyph as an array of tables", "color = \"16\"\n[[glyph]]\ntool = \"*\"\n", 2, "glyph is one table, written [glyph]"},
		{"a bad value after good ones", "color = \"16\"\nglyphs = \"ascii\"\nmods = false\ntheme = \"blue\"\n", 4, `theme is "blue"`},
		{"a file that does not parse", "color = \"16\"\ntheme = dark\n", 2, `"dark" is not a value; a string needs quotes`},
		{"a key set twice", "probe = true\nprobe = false\n", 2, "key probe is set twice"},
		{"a construct outside the subset", "probe = true\nglyph = { tool = \"*\" }\n", 2, "inline tables are not supported"},
		{"a long value is cut short", "theme = \"" + strings.Repeat("x", 5000) + "\"\n", 1, `theme is "` + strings.Repeat("x", 48) + `"; it can be`},
		{"a value that would move the cursor", "theme = \"\\u001B[2J\"\n", 1, `theme is "\x1b[2J"; it can be`},
		{"a file that is not text", "\x00\x01\x02\xff", 1, "the file is not UTF-8"},
		{"a file over the size limit", "# " + strings.Repeat("x", toml.MaxSize) + "\n", 0, "larger than 1 MiB"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := writeChat(t, tc.src)
			got, warnings, err := conf.LoadChat(path)
			if err == nil {
				t.Fatalf("LoadChat accepted it: %+v", got)
			}
			if !strings.Contains(err.Error(), tc.msg) {
				t.Errorf("error %q does not say %q", err, tc.msg)
			}
			if !strings.HasPrefix(err.Error(), path+": ") {
				t.Errorf("error %q does not start with the file's path", err)
			}
			te, isTOML := errors.AsType[*toml.Error](err)
			switch {
			case tc.line == 0 && isTOML && te.Line != 0:
				t.Errorf("error %q names line %d, want none", err, te.Line)
			case tc.line != 0 && (!isTOML || te.Line != tc.line):
				t.Errorf("error %q does not name line %d", err, tc.line)
			}
			// A file with a mistake changes nothing: not even the settings
			// before the mistake.
			if !reflect.DeepEqual(got, defaults()) || warnings != nil {
				t.Errorf("with an error, got %+v and warnings %q; want the defaults and none", got, warnings)
			}
		})
	}
}

func TestLoadChatUnreadable(t *testing.T) {
	t.Parallel()
	// A directory where the file should be: it exists, and cannot be read.
	path := filepath.Join(t.TempDir(), "chat.toml")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	got, warnings, err := conf.LoadChat(path)
	if err == nil {
		t.Fatalf("LoadChat read a directory: %+v", got)
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q does not name the path", err)
	}
	if !reflect.DeepEqual(got, defaults()) || warnings != nil {
		t.Errorf("with an error, got %+v and warnings %q; want the defaults and none", got, warnings)
	}
}
