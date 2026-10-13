// Package conf finds the terminal client's files and reads its settings.
//
// The client keeps what a person wrote in one config directory (chat.toml,
// statusline.toml, mods and mods.toml) and what it can rebuild in one cache
// directory. This package says where those are, reads chat.toml, writes a
// file so that a reader never sees half of it, and decides whether a file
// may be trusted with commands to run.
package conf

import (
	"path/filepath"
	"strings"
)

// Dirs are the client's two directories. Either is "" when there is nowhere
// to put it: no home directory is known and the environment names none. The
// path of a file in a directory that is "" is "" as well.
type Dirs struct {
	// Config holds what the user wrote or chose.
	Config string
	// Cache holds what can be thrown away.
	Cache string
}

// Locate works out the directories from the environment.
//
// Config is $CONTACTD_CONFIG_DIR, else $XDG_CONFIG_HOME/contactd, else
// <home>/.config/contactd. It is never the operating system's own choice
// (os.UserConfigDir), so the files are in the same place on every system.
// Cache is $XDG_CACHE_HOME/contactd, else <userCache>/contactd, where
// userCache is what os.UserCacheDir returned.
//
// An XDG variable that is not an absolute path is ignored, as its
// specification says: a relative one would make the current directory's
// files the client's settings.
func Locate(getenv func(string) string, home, userCache string) Dirs {
	var d Dirs
	switch dir, xdg := getenv("CONTACTD_CONFIG_DIR"), getenv("XDG_CONFIG_HOME"); {
	case dir != "":
		d.Config = filepath.Clean(dir)
	case filepath.IsAbs(xdg):
		d.Config = filepath.Join(xdg, "contactd")
	case home != "":
		d.Config = filepath.Join(home, ".config", "contactd")
	}
	switch xdg := getenv("XDG_CACHE_HOME"); {
	case filepath.IsAbs(xdg):
		d.Cache = filepath.Join(xdg, "contactd")
	case userCache != "":
		d.Cache = filepath.Join(userCache, "contactd")
	}
	return d
}

// ChatFile is the settings file, <Config>/chat.toml.
func (d Dirs) ChatFile() string { return under(d.Config, "chat.toml") }

// StatusLineFile is the status line's layout, <Config>/statusline.toml.
func (d Dirs) StatusLineFile() string { return under(d.Config, "statusline.toml") }

// ModsDir is where installed mods live, <Config>/mods.
func (d Dirs) ModsDir() string { return under(d.Config, "mods") }

// ModsState records which mods are on, <Config>/mods.toml.
func (d Dirs) ModsState() string { return under(d.Config, "mods.toml") }

// WasmCache is where compiled mods are kept, <Cache>/wazero.
func (d Dirs) WasmCache() string { return under(d.Cache, "wazero") }

// under joins a name to a directory. With no directory there is no path: a
// bare name would be a file in whatever directory the client was started in.
func under(dir, name string) string {
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, name)
}

// Short replaces the home directory at the start of a path with ~, for the
// screens' headers. A path outside home, or any path when home is unknown,
// comes back as it was.
func Short(path, home string) string {
	if path == "" || home == "" {
		return path
	}
	sep := string(filepath.Separator)
	home = filepath.Clean(home)
	if home == sep {
		return path
	}
	clean := filepath.Clean(path)
	if clean == home {
		return "~"
	}
	if rest, ok := strings.CutPrefix(clean, home+sep); ok {
		return "~" + sep + rest
	}
	return path
}
