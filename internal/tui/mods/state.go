package mods

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/quinnovator/contactd/internal/tui/conf"
	"github.com/quinnovator/contactd/internal/tui/safe"
	"github.com/quinnovator/contactd/internal/tui/toml"
)

// entry is what mods.toml remembers about one mod.
type entry struct {
	// on says the user switched the mod on.
	on bool
	// source is the directory the mod was installed from, or empty.
	source string
	// sha256 is the hash of the module when the mod was installed or last
	// switched on. A module that no longer matches is off.
	sha256 string
	// disabled says why the host switched the mod off, or is empty.
	disabled string
}

// state is mods.toml: one entry per mod, by name.
type state map[string]entry

// statePrefix starts the name of every table in mods.toml.
const statePrefix = "mod."

// parseState reads mods.toml. A table that is not a mod's, a mod name that
// could not be a directory, and a key the client does not know are skipped.
func parseState(src []byte) (state, error) {
	doc, err := toml.Parse(src)
	if err != nil {
		return nil, fmt.Errorf("mods state: %w", err)
	}
	st := state{}
	for i := range doc.Tables {
		t := &doc.Tables[i]
		name, ok := strings.CutPrefix(t.Name, statePrefix)
		if !ok || t.Array || !validName(name, false) {
			continue
		}
		var e entry
		if e.on, _, err = t.Bool("on"); err != nil {
			return nil, fmt.Errorf("mods state: %w", err)
		}
		for key, dst := range map[string]*string{"source": &e.source, "sha256": &e.sha256, "disabled": &e.disabled} {
			if *dst, _, err = t.String(key); err != nil {
				return nil, fmt.Errorf("mods state: %w", err)
			}
		}
		e.disabled = safe.Line(e.disabled, 200, false)
		st[name] = e
	}
	return st, nil
}

// encode writes the state as mods.toml, mods in name order.
func (s state) encode() []byte {
	doc := toml.NewDoc()
	for _, name := range slices.Sorted(maps.Keys(s)) {
		e := s[name]
		t := doc.Add(statePrefix+name, false)
		t.Set("on", e.on)
		t.Set("source", e.source)
		t.Set("sha256", e.sha256)
		t.Set("disabled", e.disabled)
	}
	return doc.Encode()
}

// loadState reads the state file. No path and no file are both an empty
// state.
func loadState(path string) (state, error) {
	if path == "" {
		return state{}, nil
	}
	src, err := readRegular(path, toml.MaxSize)
	if errors.Is(err, fs.ErrNotExist) {
		return state{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("mods state: %w", err)
	}
	st, err := parseState(src)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return st, nil
}

// saveState replaces the state file. With no path nothing is remembered.
func saveState(path string, s state) error {
	if path == "" {
		return nil
	}
	if err := conf.WriteFile(path, s.encode()); err != nil {
		return fmt.Errorf("mods state: %w", err)
	}
	return nil
}

// readRegular reads a whole file of at most limit bytes. It refuses anything
// that is not a regular file before opening it, so a pipe put where a file
// is expected cannot hold the client up.
func readRegular(path string, limit int64) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err //nolint:wrapcheck // the callers say which file this is
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("%s is %d bytes: the limit is %d", path, info.Size(), limit)
	}
	f, err := os.Open(path) //nolint:gosec // a mod's file, in the user's config directory or a path they typed
	if err != nil {
		return nil, err //nolint:wrapcheck // the callers say which file this is
	}
	defer f.Close() //nolint:errcheck // a read-only file has nothing to lose on close
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err //nolint:wrapcheck // the callers say which file this is
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s is over the limit of %d bytes", path, limit)
	}
	return data, nil
}
