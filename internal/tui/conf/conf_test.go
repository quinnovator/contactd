package conf_test

import (
	"path/filepath"
	"testing"

	"github.com/quinnovator/contactd/internal/tui/conf"
)

// env is a fixed environment for Locate.
type env map[string]string

func (e env) get(key string) string { return e[key] }

func TestLocate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name            string
		env             env
		home, userCache string
		want            conf.Dirs
	}{
		{
			"nothing set: under the home directory, and the system's cache",
			env{},
			"/home/ada", "/home/ada/.cache",
			conf.Dirs{Config: "/home/ada/.config/contactd", Cache: "/home/ada/.cache/contactd"},
		},
		{
			"macOS: config is still under .config, never Application Support",
			env{},
			"/Users/ada", "/Users/ada/Library/Caches",
			conf.Dirs{Config: "/Users/ada/.config/contactd", Cache: "/Users/ada/Library/Caches/contactd"},
		},
		{
			"XDG_CONFIG_HOME moves the config directory only",
			env{"XDG_CONFIG_HOME": "/xdg/config"},
			"/home/ada", "/home/ada/.cache",
			conf.Dirs{Config: "/xdg/config/contactd", Cache: "/home/ada/.cache/contactd"},
		},
		{
			"XDG_CACHE_HOME moves the cache directory only",
			env{"XDG_CACHE_HOME": "/xdg/cache/"},
			"/home/ada", "/home/ada/.cache",
			conf.Dirs{Config: "/home/ada/.config/contactd", Cache: "/xdg/cache/contactd"},
		},
		{
			"CONTACTD_CONFIG_DIR is the directory itself, and wins over XDG",
			env{"CONTACTD_CONFIG_DIR": "/etc/chat/", "XDG_CONFIG_HOME": "/xdg/config", "XDG_CACHE_HOME": "/xdg/cache"},
			"/home/ada", "/home/ada/.cache",
			conf.Dirs{Config: "/etc/chat", Cache: "/xdg/cache/contactd"},
		},
		{
			"CONTACTD_CONFIG_DIR does not move the cache",
			env{"CONTACTD_CONFIG_DIR": "/etc/chat"},
			"/home/ada", "/home/ada/.cache",
			conf.Dirs{Config: "/etc/chat", Cache: "/home/ada/.cache/contactd"},
		},
		{
			"a relative CONTACTD_CONFIG_DIR is the user's own choice",
			env{"CONTACTD_CONFIG_DIR": "./cfg"},
			"/home/ada", "/home/ada/.cache",
			conf.Dirs{Config: "cfg", Cache: "/home/ada/.cache/contactd"},
		},
		{
			"relative XDG values are ignored",
			env{"XDG_CONFIG_HOME": "config", "XDG_CACHE_HOME": "./cache"},
			"/home/ada", "/home/ada/.cache",
			conf.Dirs{Config: "/home/ada/.config/contactd", Cache: "/home/ada/.cache/contactd"},
		},
		{
			"no home directory and no cache directory: nowhere",
			env{},
			"", "",
			conf.Dirs{},
		},
		{
			"no home directory, but the environment names both",
			env{"XDG_CONFIG_HOME": "/xdg/config", "XDG_CACHE_HOME": "/xdg/cache"},
			"", "",
			conf.Dirs{Config: "/xdg/config/contactd", Cache: "/xdg/cache/contactd"},
		},
		{
			"a relative XDG value and no home directory: nowhere, not the current directory",
			env{"XDG_CONFIG_HOME": "config", "XDG_CACHE_HOME": "cache"},
			"", "",
			conf.Dirs{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			want := conf.Dirs{Config: filepath.FromSlash(tc.want.Config), Cache: filepath.FromSlash(tc.want.Cache)}
			if got := conf.Locate(tc.env.get, tc.home, tc.userCache); got != want {
				t.Errorf("Locate = %+v, want %+v", got, want)
			}
		})
	}
}

func TestDirsFiles(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		dirs conf.Dirs
		want [5]string // ChatFile, StatusLineFile, ModsDir, ModsState, WasmCache
	}{
		{
			"both directories",
			conf.Dirs{Config: "/c/contactd", Cache: "/k/contactd"},
			[5]string{
				"/c/contactd/chat.toml", "/c/contactd/statusline.toml", "/c/contactd/mods",
				"/c/contactd/mods.toml", "/k/contactd/wazero",
			},
		},
		{
			"no cache directory",
			conf.Dirs{Config: "/c/contactd"},
			[5]string{"/c/contactd/chat.toml", "/c/contactd/statusline.toml", "/c/contactd/mods", "/c/contactd/mods.toml", ""},
		},
		{
			// A bare "chat.toml" would be read from wherever the client was
			// started, so no directory must mean no path.
			"no config directory",
			conf.Dirs{Cache: "/k/contactd"},
			[5]string{"", "", "", "", "/k/contactd/wazero"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := [5]string{
				tc.dirs.ChatFile(), tc.dirs.StatusLineFile(), tc.dirs.ModsDir(), tc.dirs.ModsState(), tc.dirs.WasmCache(),
			}
			want := tc.want
			for i := range want {
				want[i] = filepath.FromSlash(want[i])
			}
			if got != want {
				t.Errorf("got  %q\nwant %q", got, want)
			}
		})
	}
}

func TestShort(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, path, home, want string
	}{
		{"a file under home", "/Users/ada/.config/contactd/statusline.toml", "/Users/ada", "~/.config/contactd/statusline.toml"},
		{"home itself", "/Users/ada", "/Users/ada", "~"},
		{"home with a trailing slash", "/Users/ada/.config/contactd/mods", "/Users/ada/", "~/.config/contactd/mods"},
		{"a path that only starts like home", "/Users/adamant/x", "/Users/ada", "/Users/adamant/x"},
		{"a path outside home", "/etc/contactd/chat.toml", "/Users/ada", "/etc/contactd/chat.toml"},
		{"home deeper than the path", "/Users", "/Users/ada", "/Users"},
		{"home unknown", "/Users/ada/x", "", "/Users/ada/x"},
		{"home is the root", "/etc/x", "/", "/etc/x"},
		{"a relative path", "cfg/chat.toml", "/Users/ada", "cfg/chat.toml"},
		{"no path", "", "/Users/ada", ""},
		{"a path that is not clean", "/Users/ada//.config/./contactd", "/Users/ada", "~/.config/contactd"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := conf.Short(filepath.FromSlash(tc.path), filepath.FromSlash(tc.home))
			if want := filepath.FromSlash(tc.want); got != want {
				t.Errorf("Short(%q, %q) = %q, want %q", tc.path, tc.home, got, want)
			}
		})
	}
}
