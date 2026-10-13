package conf

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/quinnovator/contactd/internal/tui/toml"
)

// Chat is chat.toml: the lasting form of the client's display settings.
type Chat struct {
	// Color, Glyphs, Theme and Images are each "" or "auto" to let the
	// client detect, or one of the values chat.toml allows:
	// truecolor, 256, 16, none; unicode, ascii; dark, light; pixels, blocks,
	// text. A colour the file gives as the number 256 or 16 is "256" or "16"
	// here.
	Color, Glyphs, Theme, Images string
	// Probe lets the client ask the terminal what it can do. Mods lets it
	// load mods. Both are true unless the file says otherwise.
	Probe, Mods bool
	// Glyph is the [glyph] table: replacements for single glyphs, by glyph
	// name. It is nil when there are none. The names and the values are not
	// checked here; the theme knows the glyphs.
	Glyph map[string]string
}

// LoadChat reads chat.toml. A missing file is the defaults, and so is no
// path at all.
//
// The warnings name what the file has that the client does not know, each
// with its line: an unknown key or table is skipped, and the rest of the
// file still counts. A file that cannot be read or parsed, or a known key
// with a value it cannot take, is an error that names the file and the line;
// the Chat returned with an error is the defaults.
func LoadChat(path string) (Chat, []string, error) {
	defaults := Chat{Probe: true, Mods: true}
	src, err := readFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return defaults, nil, nil
	}
	if err != nil {
		return defaults, nil, fmt.Errorf("chat settings: %w", err)
	}
	chat, warnings, err := parseChat(src)
	if err != nil {
		return defaults, nil, fmt.Errorf("%s: %w", path, err)
	}
	return chat, warnings, nil
}

// readFile reads a settings file, stopping just past the size the parser
// accepts, so that a huge file is refused there and not held in memory.
func readFile(path string) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // the path is the user's own config file
	if err != nil {
		return nil, err //nolint:wrapcheck // the caller wraps it, and tests it for a missing file
	}
	defer f.Close() //nolint:errcheck // read only; nothing is lost
	src, err := io.ReadAll(io.LimitReader(f, toml.MaxSize+1))
	if err != nil {
		return nil, err //nolint:wrapcheck // as above
	}
	return src, nil
}

func parseChat(src []byte) (Chat, []string, error) {
	chat := Chat{Probe: true, Mods: true}
	doc, err := toml.Parse(src)
	if err != nil {
		return chat, nil, err //nolint:wrapcheck // the caller adds the file's path
	}
	var warnings []string
	root := doc.Root()
	for _, key := range root.Keys() {
		switch key {
		case "color":
			chat.Color, err = colour(root, key)
		case "glyphs":
			chat.Glyphs, err = choice(root, key, "unicode", "ascii")
		case "theme":
			chat.Theme, err = choice(root, key, "dark", "light")
		case "images":
			chat.Images, err = choice(root, key, "pixels", "blocks", "text")
		case "probe":
			chat.Probe, _, err = root.Bool(key)
		case "mods":
			chat.Mods, _, err = root.Bool(key)
		default:
			warnings = append(warnings, warning(root.KeyLine(key), "unknown key "+strconv.Quote(clip(key))))
		}
		if err != nil {
			return chat, nil, err //nolint:wrapcheck // as above
		}
	}
	arrays := make(map[string]bool)
	for i := range doc.Tables[1:] {
		t := &doc.Tables[i+1]
		switch {
		case t.Name != "glyph" && t.Array:
			// One note for the whole array, at its first element.
			if !arrays[t.Name] {
				arrays[t.Name] = true
				warnings = append(warnings, warning(t.Line, "unknown table [["+clip(t.Name)+"]]"))
			}
		case t.Name != "glyph":
			warnings = append(warnings, warning(t.Line, "unknown table ["+clip(t.Name)+"]"))
		case t.Array:
			return chat, nil, &toml.Error{Line: t.Line, Msg: "glyph is one table, written [glyph]"}
		default:
			if chat.Glyph, err = glyphs(t); err != nil {
				return chat, nil, err
			}
		}
	}
	return chat, warnings, nil
}

// warning is a note about one line of the file, written as an error from
// the parser is.
func warning(line int, msg string) string {
	return (&toml.Error{Line: line, Msg: msg}).Error()
}

// clip shortens a name from the file for a note, so that the note stays one
// short line. Names are bare keys, so any byte is a place to cut.
func clip(name string) string {
	const most = 48
	if len(name) <= most {
		return name
	}
	return name[:most] + "..."
}

// choice reads a setting that takes one of a few words. "" and "auto" are
// always among them. A word that is not is refused with the key's line.
func choice(t *toml.Table, key string, values ...string) (string, error) {
	v, _, err := t.String(key)
	if err != nil {
		return "", err //nolint:wrapcheck // the caller adds the file's path
	}
	if v == "" || v == "auto" || slices.Contains(values, v) {
		return v, nil
	}
	last := len(values) - 1
	return "", &toml.Error{
		Line: t.KeyLine(key),
		Msg:  fmt.Sprintf("%s is %.48q; it can be auto, %s or %s", key, v, strings.Join(values[:last], ", "), values[last]),
	}
}

// colour reads the colour setting. It takes the words choice allows, and
// also 256 and 16 written as numbers, which is how the file's own comment
// lists them. Any other number is refused with the key's line.
func colour(t *toml.Table, key string) (string, error) {
	values := []string{"truecolor", "256", "16", "none"}
	n, _, err := t.Int(key)
	if err != nil {
		// Not a number: a word, or a value of a type no setting takes.
		return choice(t, key, values...)
	}
	if v := strconv.FormatInt(n, 10); slices.Contains(values, v) {
		return v, nil
	}
	return "", &toml.Error{
		Line: t.KeyLine(key),
		Msg:  fmt.Sprintf("%s is %d; it can be auto, truecolor, 256, 16 or none", key, n),
	}
}

// glyphs reads the [glyph] table. Every value must be a string. An empty
// table is no overrides, which is a nil map.
func glyphs(t *toml.Table) (map[string]string, error) {
	var out map[string]string
	for _, key := range t.Keys() {
		v, _, err := t.String(key)
		if err != nil {
			return nil, err //nolint:wrapcheck // the caller adds the file's path
		}
		if out == nil {
			out = make(map[string]string)
		}
		out[key] = v
	}
	return out, nil
}
