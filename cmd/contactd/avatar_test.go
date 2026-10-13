package main

import (
	"strings"
	"testing"

	"github.com/quinnovator/contactd/internal/tui"
)

// Whether the avatar is shown is settled like every display setting: the
// flag, then the variable, then chat.toml. With nothing said it is shown
// where the terminal can draw it. The rest of it is chat.toml's.
func TestAvatarSettingsPrecedence(t *testing.T) {
	t.Parallel()
	file := "[avatar]\nshow = \"off\"\nlook = \"tide\"\nvoice = \"deep\"\ncells = \"octant\"\nrate = 60\nmute = true\n"
	for _, tc := range []struct {
		name string
		src  source
		want tui.Avatar
	}{
		{"nothing said", source{}, tui.Avatar{Show: "auto"}},
		{"chat.toml", source{file: file}, tui.Avatar{Show: "off", Look: "tide", Voice: "deep", Cells: "octant", Rate: 60, Mute: true}},
		{
			"the variables win over chat.toml",
			source{file: file, env: map[string]string{"CONTACTD_AVATAR": "on", "CONTACTD_MUTE": "false"}},
			tui.Avatar{Show: "on", Look: "tide", Voice: "deep", Cells: "octant", Rate: 60},
		},
		{
			"the flags win over the variables",
			source{args: []string{"-avatar=AUTO", "-mute"}, env: map[string]string{"CONTACTD_AVATAR": "off", "CONTACTD_MUTE": "0"}},
			tui.Avatar{Show: "auto", Mute: true},
		},
		{"a flag alone", source{args: []string{"-avatar", "off"}}, tui.Avatar{Show: "off"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d, err := settleFrom(t, tc.src)
			if err != nil {
				t.Fatal(err)
			}
			if d.avatar != tc.want {
				t.Errorf("the avatar is %+v, want %+v", d.avatar, tc.want)
			}
			if opt := d.options("/home/jack", "/home/jack/src"); opt.Avatar != tc.want {
				t.Errorf("the interface is started with %+v", opt.Avatar)
			}
		})
	}
}

func TestAvatarSettingsRefused(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		src  source
		msg  string
	}{
		{"a flag that says neither", source{args: []string{"-avatar=sometimes"}}, `avatar is "sometimes"; it can be auto, on or off`},
		{"a variable that says neither", source{env: map[string]string{"CONTACTD_AVATAR": "yes"}}, `avatar is "yes"; it can be auto, on or off`},
		{"a variable that is no switch", source{env: map[string]string{"CONTACTD_MUTE": "quiet"}}, `CONTACTD_MUTE is "quiet"; it can be true or false`},
		{"a look nobody made", source{file: "[avatar]\nlook = \"plaid\"\n"}, `the avatar's look is "plaid"; it can be ember, tide, orchid, moss`},
		{"a voice nobody has", source{file: "[avatar]\nvoice = \"loud\"\n"}, `the avatar's voice is "loud"; it can be chirp, pip, mellow, retro, deep`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := settleFrom(t, tc.src)
			if err == nil || !strings.Contains(err.Error(), tc.msg) {
				t.Errorf("the error is %v, want it to say %q", err, tc.msg)
			}
		})
	}
}

// The voice is not opened for a client that shows no avatar, nor over SSH,
// where it would sound at the far end.
func TestTheVoiceIsOpenedWhereItCanBeHeard(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		src  source
		want bool
	}{
		{"by default", source{}, true},
		{"muted: it can be unmuted", source{args: []string{"-mute"}}, true},
		{"switched off", source{args: []string{"-avatar=off"}}, false},
		{"over SSH", source{env: map[string]string{"SSH_CONNECTION": "10.0.0.1 22 10.0.0.2 22"}}, false},
		{"in an SSH terminal", source{env: map[string]string{"SSH_TTY": "/dev/pts/3"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d, err := settleFrom(t, tc.src)
			if err != nil {
				t.Fatal(err)
			}
			if got := d.sounds(); got != tc.want {
				t.Errorf("sounds() = %v, want %v", got, tc.want)
			}
		})
	}
}
