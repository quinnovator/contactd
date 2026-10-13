package conf_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/quinnovator/contactd/internal/tui/conf"
	"github.com/quinnovator/contactd/internal/tui/toml"
)

func TestLoadChatAvatar(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, src string
		want      conf.Avatar
		warnings  []string
	}{
		{"none", "theme = \"dark\"\n", conf.Avatar{}, nil},
		{"an empty table", "[avatar]\n", conf.Avatar{}, nil},
		{
			"every key",
			"[avatar]\nshow = \"on\"\nlook = \"tide\"\nvoice = \"deep\"\ncells = \"octant\"\nrate = 60\nmute = true\n",
			conf.Avatar{Show: "on", Look: "tide", Voice: "deep", Cells: "octant", Rate: 60, Mute: true},
			nil,
		},
		{"auto", "[avatar]\nshow = \"auto\"\ncells = \"auto\"\n", conf.Avatar{Show: "auto", Cells: "auto"}, nil},
		{"the bounds of the rate", "[avatar]\nrate = 8\n", conf.Avatar{Rate: 8}, nil},
		{
			// The client knows the looks and the voices; the file's reader
			// does not.
			"names are not checked here", "[avatar]\nlook = \"plaid\"\nvoice = \"loud\"\n",
			conf.Avatar{Look: "plaid", Voice: "loud"},
			nil,
		},
		{
			"an unknown key is a warning", "[glyph]\ntool = \"*\"\n\n[avatar]\nshow = \"off\"\nsize = 3\n",
			conf.Avatar{Show: "off"},
			[]string{`line 6: unknown key "size" in [avatar]`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, warnings, err := conf.LoadChat(writeChat(t, tc.src))
			if err != nil {
				t.Fatalf("LoadChat: %v", err)
			}
			if got.Avatar != tc.want {
				t.Errorf("avatar\n got %+v\nwant %+v", got.Avatar, tc.want)
			}
			if !reflect.DeepEqual(warnings, tc.warnings) {
				t.Errorf("warnings\n got %q\nwant %q", warnings, tc.warnings)
			}
			if !got.Probe || !got.Mods {
				t.Errorf("the rest of the file is not the defaults: %+v", got)
			}
		})
	}
}

func TestLoadChatRefusesAnAvatar(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, src string
		line      int
		msg       string
	}{
		{"a way to show it that does not exist", "[avatar]\nshow = \"always\"\n", 2, `show is "always"; it can be auto, on or off`},
		{"a switch for showing it", "[avatar]\nshow = true\n", 2, "show must be a string, not true or false"},
		{"cells that do not exist", "[avatar]\n\ncells = \"sextant\"\n", 3, `cells is "sextant"; it can be auto, quadrant or octant`},
		{"a rate too slow", "[avatar]\nrate = 7\n", 2, "rate is 7; it can be from 8 to 240 characters a second"},
		{"a rate too fast", "[avatar]\nrate = 241\n", 2, "rate is 241; it can be from 8 to 240"},
		{"a rate in words", "[avatar]\nrate = \"fast\"\n", 2, "rate must be an integer, not a string"},
		{"mute as a word", "[avatar]\nmute = \"yes\"\n", 2, "mute must be true or false, not a string"},
		{"a look that is a number", "[avatar]\nlook = 3\n", 2, "look must be a string, not an integer"},
		{"an array of avatars", "theme = \"dark\"\n[[avatar]]\nshow = \"on\"\n", 2, "avatar is one table, written [avatar]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, _, err := conf.LoadChat(writeChat(t, tc.src))
			if err == nil {
				t.Fatalf("LoadChat accepted it: %+v", got)
			}
			if !strings.Contains(err.Error(), tc.msg) {
				t.Errorf("error %q does not say %q", err, tc.msg)
			}
			var te *toml.Error
			if !errors.As(err, &te) || te.Line != tc.line {
				t.Errorf("error %q names line %v, want %d", err, te, tc.line)
			}
			if !reflect.DeepEqual(got, conf.Chat{Probe: true, Mods: true}) {
				t.Errorf("a refused file gave %+v, want the defaults", got)
			}
		})
	}
}
