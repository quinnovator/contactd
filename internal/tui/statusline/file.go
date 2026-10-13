package statusline

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"time"

	"github.com/quinnovator/contactd/internal/tui/toml"
)

// header is the first line of a file Encode writes.
const header = "# contactd status line. Written by /statusline edit; comments are not kept.\n\n"

// Parse reads the text of statusline.toml: a [[segment]] table for each
// segment, in screen order.
//
// The warnings name what was skipped, each with its line: a key or table the
// client does not know, a core segment it does not have, a segment listed
// twice, mod and shell segments beyond MaxSegments. The rest of the file
// still counts. A file that is not TOML, or a known key with a value it
// cannot take, is an error that names the line; the Config returned with an
// error is the default preset.
//
// A key that is absent takes the segment's default: on, the segment's usual
// side and turn to give way, its default formats, no warning. A shell
// segment runs every 10 s with 1 s to finish. Core segments the file does not
// list are added at the end, switched off, so that an editor can offer them.
func Parse(data []byte) (Config, []string, error) {
	c, warnings, err := parse(data)
	if err != nil {
		return defaultPreset(), nil, fmt.Errorf("status line: %w", err)
	}
	return c, warnings, nil
}

// Load reads statusline.toml as Parse does. A missing file is the default
// preset, and so is no path at all. An error names the file, and comes with
// the default preset too.
func Load(path string) (Config, []string, error) {
	if path == "" {
		return defaultPreset(), nil, nil
	}
	data, err := readFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return defaultPreset(), nil, nil
	}
	if err != nil {
		return defaultPreset(), nil, fmt.Errorf("status line: %w", err)
	}
	c, warnings, err := parse(data)
	if err != nil {
		return defaultPreset(), nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, warnings, nil
}

// readFile reads the file, stopping just past the size the parser accepts,
// so that a huge file is refused there and not held in memory.
func readFile(path string) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // the path is the user's own config file
	if err != nil {
		return nil, err //nolint:wrapcheck // the caller wraps it, and tests it for a missing file
	}
	defer f.Close() //nolint:errcheck // read only; nothing is lost
	data, err := io.ReadAll(io.LimitReader(f, toml.MaxSize+1))
	if err != nil {
		return nil, err //nolint:wrapcheck // as above
	}
	return data, nil
}

// Encode writes the config as statusline.toml: a one-line comment, then
// every segment in order with its settings. The same config always gives the
// same bytes.
//
// A config from Parse, a preset or WithMods is read back by Parse as an
// equal config. So is one changed since, as long as every segment that was
// changed or added passes Validate and names its Side and its From, and none
// is listed twice.
func (c Config) Encode() []byte {
	doc := toml.NewDoc()
	for _, s := range c.Segments {
		k, _ := s.kind()
		from := s.From
		if k == kindCore {
			from = FromCore
		}
		t := doc.Add("segment", true)
		t.Set("name", s.Name)
		t.Set("from", from)
		t.Set("on", s.On)
		t.Set("side", s.side().String())
		if s.Format != "" {
			t.Set("format", s.Format)
		}
		if s.Short != "" {
			t.Set("short", s.Short)
		}
		if s.HasWarn {
			t.Set("warn_below", s.WarnBelow)
		}
		t.Set("drop", max(s.Drop, 0))
		if k != kindShell {
			continue
		}
		t.Set("command", s.Command)
		if s.Match != "" {
			t.Set("match", s.Match)
		}
		t.Set("interval", s.Interval.String())
		t.Set("timeout", s.Timeout.String())
	}
	return append([]byte(header), doc.Encode()...)
}

func parse(data []byte) (Config, []string, error) {
	doc, err := toml.Parse(data)
	if err != nil {
		return Config{}, nil, err //nolint:wrapcheck // the caller says which file
	}
	var warn warnings
	root := doc.Root()
	for _, key := range root.Keys() {
		warn.add(root.KeyLine(key), "unknown key "+quote(key))
	}
	var c Config
	listed := make(map[string]bool)
	arrays := make(map[string]bool)
	others, full := 0, false
	for i := range doc.Tables[1:] {
		t := &doc.Tables[i+1]
		switch {
		case t.Name != "segment" && t.Array:
			// One note for the whole array, at its first element.
			if !arrays[t.Name] {
				arrays[t.Name] = true
				warn.add(t.Line, "unknown table [["+clip(t.Name)+"]]")
			}
			continue
		case t.Name != "segment":
			warn.add(t.Line, "unknown table ["+clip(t.Name)+"]")
			continue
		case !t.Array:
			return Config{}, nil, &toml.Error{Line: t.Line, Msg: "segment is a list, written [[segment]]"}
		}
		s, ok, err := readSegment(t, &warn)
		if err != nil {
			return Config{}, nil, err
		}
		if !ok {
			continue
		}
		k, _ := s.kind()
		switch id := s.ID(); {
		case listed[id]:
			warn.add(t.Line, "the segment "+quote(id)+" is listed twice; this one is left out")
		case k != kindCore && others == MaxSegments:
			// One note for all that are left out.
			if !full {
				full = true
				warn.add(t.Line, fmt.Sprintf("more than %d mod and shell segments; the rest are left out", MaxSegments))
			}
		default:
			if k != kindCore {
				others++
			}
			listed[id] = true
			c.Segments = append(c.Segments, s)
		}
	}
	for _, name := range coreNames() {
		if s, _ := coreSegment(name); !listed[s.ID()] {
			c.Segments = append(c.Segments, s)
		}
	}
	return c, warn, nil
}

// warnings are the notes about a file, in the order they were found.
type warnings []string

// add writes a note about one line as an error from the parser is written.
func (w *warnings) add(line int, msg string) {
	*w = append(*w, (&toml.Error{Line: line, Msg: msg}).Error())
}

// clip shortens a table's name for a note. Names are bare keys and dots, so
// any byte is a place to cut.
func clip(name string) string {
	const most = 48
	if len(name) <= most {
		return name
	}
	return name[:most] + "..."
}

// readSegment reads one [[segment]] table. ok is false, with a note, for a
// segment that is skipped; an error is a value that cannot be taken.
func readSegment(t *toml.Table, warn *warnings) (s Segment, ok bool, err error) {
	r := reader{t: t}
	s.Name, _ = r.str("name")
	s.From = FromCore
	if from, has := r.str("from"); has {
		s.From = from
	}
	if r.err != nil {
		return Segment{}, false, r.err
	}
	if msg := checkName(s.Name); msg != "" {
		// A segment with no name key is blamed on its header.
		return Segment{}, false, &toml.Error{Line: max(t.KeyLine("name"), t.Line), Msg: msg}
	}
	k, _ := s.kind()
	switch k {
	case kindCore:
		// from = "" is core too; it is kept as the word.
		s.From = FromCore
	case kindMod, kindShell:
	default:
		return Segment{}, false, &toml.Error{Line: t.KeyLine("from"), Msg: fromMessage(s.From)}
	}

	// What an absent key means depends on the segment.
	def := Segment{Side: Right, Drop: 1}
	if k == kindCore {
		var known bool
		if def, known = coreSegment(s.Name); !known {
			warn.add(t.Line, coreMessage(s.Name)+"; left out")
			return Segment{}, false, nil
		}
	}
	s.On, s.Side, s.Drop = true, def.Side, def.Drop
	if k == kindShell {
		s.Interval, s.Timeout = DefaultInterval, DefaultTimeout
	}

	for _, key := range t.Keys() {
		switch key {
		case "name", "from":
		case "on":
			s.On = r.boolean(key, s.On)
		case "side":
			s.Side = r.side(key)
		case "format":
			s.Format = r.short(key, maxFormat)
		case "short":
			s.Short = r.short(key, maxFormat)
		case "warn_below":
			s.WarnBelow, s.HasWarn = r.integer(key, checkWarn), true
		case "drop":
			s.Drop = r.integer(key, checkDrop)
		case "command", "match", "interval", "timeout":
			if k != kindShell {
				warn.add(t.KeyLine(key), key+" is for shell segments; ignored")
				continue
			}
			r.shell(key, &s)
		default:
			warn.add(t.KeyLine(key), "unknown key "+quote(key))
		}
	}
	if r.err == nil && k == kindShell {
		// Checked last, since the two keys may come in either order.
		if msg := checkTimeout(s.Timeout, s.Interval); msg != "" {
			r.refuse(t.KeyLine("timeout"), msg)
		}
		if s.Command == "" {
			warn.add(t.Line, "the shell segment "+quote(s.Name)+" has no command; it shows nothing")
		}
	}
	if r.err != nil {
		return Segment{}, false, r.err
	}
	return s, true, nil
}

// reader reads the keys of one table and keeps the first error, so that
// each key is one line to read.
type reader struct {
	t   *toml.Table
	err error
}

// refuse records a value that cannot be taken, with its line.
func (r *reader) refuse(line int, msg string) {
	if r.err == nil {
		r.err = &toml.Error{Line: line, Msg: msg}
	}
}

func (r *reader) str(key string) (string, bool) {
	if r.err != nil {
		return "", false
	}
	v, ok, err := r.t.String(key)
	if err != nil {
		r.err = err
		return "", false
	}
	return v, ok
}

// short reads a string of at most this many bytes.
func (r *reader) short(key string, most int) string {
	v, _ := r.str(key)
	if msg := checkLong(key, v, most); msg != "" {
		r.refuse(r.t.KeyLine(key), msg)
		return ""
	}
	return v
}

func (r *reader) boolean(key string, def bool) bool {
	if r.err != nil {
		return def
	}
	v, _, err := r.t.Bool(key)
	if err != nil {
		r.err = err
		return def
	}
	return v
}

// integer reads a whole number that check has nothing to say against.
func (r *reader) integer(key string, check func(int64) string) int {
	if r.err != nil {
		return 0
	}
	v, _, err := r.t.Int(key)
	if err != nil {
		r.err = err
		return 0
	}
	if msg := check(v); msg != "" {
		r.refuse(r.t.KeyLine(key), msg)
		return 0
	}
	return int(v)
}

func (r *reader) side(key string) Side {
	switch v, _ := r.str(key); v {
	case Left.String():
		return Left
	case Right.String():
		return Right
	default:
		r.refuse(r.t.KeyLine(key), "side is "+quote(v)+"; it can be left or right")
		return 0
	}
}

// duration reads a length of time written as Go writes one: "10s", "1m30s".
func (r *reader) duration(key string) time.Duration {
	v, _ := r.str(key)
	if r.err != nil {
		return 0
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		r.refuse(r.t.KeyLine(key), key+" is "+quote(v)+`; write a time such as "10s"`)
		return 0
	}
	return d
}

// shell reads one of the keys only a shell segment has.
func (r *reader) shell(key string, s *Segment) {
	var msg string
	switch key {
	case "command":
		s.Command = r.short(key, maxCommand)
	case "match":
		s.Match, _ = r.str(key)
		msg = checkMatch(s.Match)
	case "interval":
		s.Interval = r.duration(key)
		msg = checkInterval(s.Interval)
	case "timeout":
		// Its upper limit is the interval, which may not be read yet.
		s.Timeout = r.duration(key)
	}
	if msg != "" {
		r.refuse(r.t.KeyLine(key), msg)
	}
}
