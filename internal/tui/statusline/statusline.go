// Package statusline decides what the status bar says.
//
// The bar is a row of segments: a few words each, some at the left edge and
// some at the right. A segment comes from one of three places. A core
// segment shows something the client knows, such as the connection or the
// last model call. A mod segment shows what a mod returned. A shell segment
// shows the output of a command, run on an interval.
//
// A Config lists the segments, in screen order. It is read from
// statusline.toml, or is one of three presets. A Value is what one segment
// has to show right now. Layout puts the two together for a width: it picks
// the segments that fit, shortens or drops the rest in a set order, and
// returns the cells of each side. It draws nothing; the theme does.
//
// Everything here except the shell runner and Load is pure: no clock, no
// files, no terminal.
package statusline

import (
	"cmp"
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/quinnovator/contactd/internal/tui/safe"
)

const (
	// FromCore is the From of a core segment.
	FromCore = "core"
	// FromShell is the From of a shell segment.
	FromShell = "shell"
	// fromMod starts the From of a mod segment; the mod's name follows.
	fromMod = "mod:"

	// MinInterval is the shortest interval a shell segment may have.
	MinInterval = time.Second
	// DefaultInterval is the interval of a shell segment whose entry in the
	// file names none.
	DefaultInterval = 10 * time.Second
	// DefaultTimeout is the timeout of a shell segment whose entry in the
	// file names none.
	DefaultTimeout = time.Second
	// MaxSegments is how many mod and shell segments a file may list. Core
	// segments do not count: there are only so many.
	MaxSegments = 32

	// maxName is the longest name of a segment or a mod, in bytes.
	maxName = 64
	// maxFormat, maxMatch and maxCommand are the longest values the keys of
	// those names may have, in bytes. With MaxSegments they keep a file
	// Encode writes under the size Parse reads, whatever the values hold.
	maxFormat  = 256
	maxMatch   = 1024
	maxCommand = 2048
	// maxNumber is the highest drop and warn_below a file may hold, and its
	// negative less one the lowest warn_below: what 32 bits hold, so that an
	// int holds them everywhere.
	maxNumber = 1<<31 - 1
	// maxCell is the longest text one segment shows, in runes.
	maxCell = 120
)

// Side is the edge of the bar a segment sits at.
type Side uint8

const (
	// Left is the left edge.
	Left Side = iota + 1
	// Right is the right edge.
	Right
)

// String returns the word statusline.toml uses: "left" or "right".
func (s Side) String() string {
	switch s {
	case Left:
		return "left"
	case Right:
		return "right"
	}
	return "unset"
}

// Segment is one entry of the status line: what to show and how it behaves
// when the bar is too narrow.
type Segment struct {
	// Name says which segment this is among those From offers.
	Name string
	// From is "core", "mod:<mod>" or "shell". "" means core.
	From string
	// On says the segment is drawn. A segment that is off stays listed.
	On bool
	// Side is the edge it sits at. Zero means the segment's usual side: a
	// core segment's own, and the right for a mod or shell segment.
	Side Side
	// Format is what the segment shows, and Short what it shows when the
	// bar is narrow. $name and ${name} are replaced by the value's
	// variables, and $$ is a dollar sign. "" takes the segment's default; a
	// segment whose default Short is empty has no short form.
	Format, Short string
	// WarnBelow puts the segment in the warn tone when its value's number
	// is under it. It counts only with HasWarn.
	WarnBelow int
	HasWarn   bool
	// Drop is the segment's turn to give way when the bar is too narrow: 1
	// goes first, and 0 never goes.
	Drop int
	// Command is what a shell segment runs, with /bin/sh -c. Match is an
	// optional RE2 expression run over its output; the groups are $1 to $9.
	Command, Match string
	// Interval is how long a shell segment waits after one run before the
	// next. Timeout is how long a run may take.
	Interval, Timeout time.Duration
}

// kind is where a segment comes from.
type kind uint8

const (
	kindCore kind = iota + 1
	kindMod
	kindShell
)

// kind reads From. For a mod segment it also returns the mod's name. A From
// that is none of the three gives zero.
func (s Segment) kind() (kind, string) {
	switch s.From {
	case "", FromCore:
		return kindCore, ""
	case FromShell:
		return kindShell, ""
	}
	if mod, ok := strings.CutPrefix(s.From, fromMod); ok && checkModName(mod) == "" {
		return kindMod, mod
	}
	return 0, ""
}

// ID names the segment among all segments: "core:cache", "mod:git/branch",
// "shell:load". Values are keyed by it.
func (s Segment) ID() string {
	k, mod := s.kind()
	switch k {
	case kindCore:
		return FromCore + ":" + s.Name
	case kindShell:
		return FromShell + ":" + s.Name
	case kindMod:
		return fromMod + mod + "/" + s.Name
	}
	return s.From + ":" + s.Name
}

// side is the edge the segment sits at, with zero resolved.
func (s Segment) side() Side {
	if s.Side == Left || s.Side == Right {
		return s.Side
	}
	if k, _ := s.kind(); k == kindCore {
		if def, ok := coreSegment(s.Name); ok {
			return def.Side
		}
	}
	return Right
}

// formats returns the format and the short format in force: the segment's
// own, or its default.
func (s Segment) formats() (format, short string) {
	format, short = s.Format, s.Short
	if format != "" && short != "" {
		return format, short
	}
	var defFormat, defShort string
	switch k, _ := s.kind(); k {
	case kindCore:
		def, _ := coreSegment(s.Name)
		defFormat, defShort = def.Format, def.Short
	case kindMod:
		defFormat, defShort = "$text", "$short"
	case kindShell:
		defFormat = "$out"
	}
	if format == "" {
		format = defFormat
	}
	if short == "" {
		short = defShort
	}
	return format, short
}

// Text returns what the segment shows for a value: in full, and in its short
// form. short is "" when the segment has no short form. Both are "" when
// there is nothing to show.
//
// The text is safe to draw whatever the value and the formats hold: it is
// one line with no control character, no space at either end, and at most
// 120 runes.
func (s Segment) Text(v Value) (full, short string) {
	if v.Hide {
		return "", ""
	}
	format, shortFormat := s.formats()
	if v.Text != "" {
		full = tidy(v.Text)
		if full != "" && shortFormat != "" {
			short = full
		}
		return full, short
	}
	full = tidy(expand(format, v.Vars))
	if full == "" || shortFormat == "" {
		return full, ""
	}
	return full, tidy(expand(shortFormat, v.Vars))
}

// Validate says why Parse would refuse the segment in a file, or returns
// nil. An editor checks a change with it before saving.
func (s Segment) Validate() error {
	k, _ := s.kind()
	msg := cmp.Or(
		checkName(s.Name),
		checkFrom(k, s.From),
		checkDrop(int64(s.Drop)),
		s.checkWarn(),
		checkLong("format", s.Format, maxFormat),
		checkLong("short", s.Short, maxFormat),
	)
	if msg != "" {
		return errors.New(msg)
	}
	switch k {
	case kindCore:
		if _, ok := coreSegment(s.Name); !ok {
			msg = coreMessage(s.Name)
		}
	case kindMod:
	case kindShell:
		msg = s.checkShell()
	}
	if msg != "" {
		return errors.New(msg)
	}
	return nil
}

// checkWarn says what is wrong with the segment's warn_below, which counts
// only with HasWarn.
func (s Segment) checkWarn() string {
	if !s.HasWarn {
		return ""
	}
	return checkWarn(int64(s.WarnBelow))
}

// checkShell says what is wrong with a shell segment's own fields.
func (s Segment) checkShell() string {
	return cmp.Or(
		checkLong("command", s.Command, maxCommand),
		checkMatch(s.Match),
		checkInterval(s.Interval),
		checkTimeout(s.Timeout, s.Interval),
	)
}

// Config is a status line: its segments, where file order is screen order on
// each side.
type Config struct{ Segments []Segment }

// ModSegment names a segment a mod offers.
type ModSegment struct{ Mod, Name string }

// WithMods returns the config with a segment for each of these that it does
// not list yet. They go on the right, before the clock, switched on, and
// first to give way. A segment the config already lists is left as it is,
// on or off. The receiver is not changed.
//
// A mod segment whose mod is not loaded needs no removing: it has no value,
// so Layout leaves it out.
func (c Config) WithMods(enabled []ModSegment) Config {
	listed := make(map[string]bool, len(c.Segments))
	for _, s := range c.Segments {
		listed[s.ID()] = true
	}
	var add []Segment
	for _, m := range enabled {
		s := Segment{Name: m.Name, From: fromMod + m.Mod, On: true, Side: Right, Drop: 1}
		// A name the file could not hold is left out, so that what the
		// editor saves can always be read back.
		if checkModName(m.Mod) != "" || checkName(m.Name) != "" || listed[s.ID()] {
			continue
		}
		listed[s.ID()] = true
		add = append(add, s)
	}
	at := slices.IndexFunc(c.Segments, func(s Segment) bool {
		return s.ID() == "core:clock" && s.side() == Right
	})
	if at < 0 {
		at = len(c.Segments)
	}
	return Config{Segments: slices.Concat(c.Segments[:at], add, c.Segments[at:])}
}

// checkName says what is wrong with a segment's name, or returns "".
func checkName(name string) string {
	switch {
	case name == "":
		return "a segment needs a name"
	case len(name) > maxName:
		return "the name " + quote(name) + " is longer than " + strconv.Itoa(maxName) + " bytes"
	case !utf8.ValidString(name) || strings.ContainsFunc(name, unsafeInName):
		return "the name " + quote(name) + " has a space or a control character"
	}
	return ""
}

// checkModName says what is wrong with the mod of a "mod:<mod>" From. A
// slash is refused so that an ID splits one way only.
func checkModName(mod string) string {
	if msg := checkName(mod); msg != "" || strings.Contains(mod, "/") {
		return "not a mod's name"
	}
	return ""
}

func unsafeInName(r rune) bool {
	return unicode.IsSpace(r) || unicode.IsControl(r) || r == utf8.RuneError
}

// checkMatch says why an expression cannot be used, or returns "".
func checkMatch(expr string) string {
	if msg := checkLong("match", expr, maxMatch); msg != "" {
		return msg
	}
	if _, err := regexp.Compile(expr); err != nil {
		return "match is not an expression: " + safe.Line(err.Error(), 120, false)
	}
	return ""
}

func checkInterval(d time.Duration) string {
	if d < MinInterval {
		return "interval is " + d.String() + "; the least is " + MinInterval.String()
	}
	return ""
}

func checkTimeout(d, interval time.Duration) string {
	switch {
	case d <= 0:
		return "timeout is " + d.String() + "; it must be more than 0"
	case d > interval:
		return "timeout is " + d.String() + "; the most is the interval, " + interval.String()
	}
	return ""
}

func fromMessage(from string) string {
	return "from is " + quote(from) + "; it can be core, mod:<name> or shell"
}

// checkFrom says what is wrong with a From that kind could not read.
func checkFrom(k kind, from string) string {
	if k == 0 {
		return fromMessage(from)
	}
	return ""
}

func coreMessage(name string) string {
	return "no core segment is named " + quote(name)
}

// checkDrop says what is wrong with a drop, or returns "".
func checkDrop(n int64) string {
	switch {
	case n < 0:
		return "drop is " + strconv.FormatInt(n, 10) + "; it can be 0 (never) or more"
	case n > maxNumber:
		return "drop is " + strconv.FormatInt(n, 10) + "; the most is " + strconv.Itoa(maxNumber)
	}
	return ""
}

// checkWarn says what is wrong with a warn_below, or returns "".
func checkWarn(n int64) string {
	if n < -maxNumber-1 || n > maxNumber {
		return "warn_below is " + strconv.FormatInt(n, 10) + "; it can be " +
			strconv.Itoa(-maxNumber-1) + " to " + strconv.Itoa(maxNumber)
	}
	return ""
}

// checkLong says a key's value is over its limit, or returns "".
func checkLong(key, value string, most int) string {
	if len(value) > most {
		return key + " is longer than " + strconv.Itoa(most) + " bytes"
	}
	return ""
}

// quote writes a word from the file for a note: quoted, and cut short so
// the note stays one line.
func quote(s string) string {
	const most = 48
	if len(s) > most {
		s = strings.ToValidUTF8(s[:most], "") + "..."
	}
	return strconv.Quote(s)
}

// tidy makes one segment's text fit to draw: one line, nothing that could
// start an escape sequence, no space at either end, at most maxCell runes.
// Clean text comes back as it is.
func tidy(s string) string {
	s = safe.Clean(strings.TrimSpace(s), true)
	// A byte count within the limit means the rune count is too.
	if len(s) > maxCell || strings.ContainsAny(s, "\n\u2028\u2029") {
		s = safe.Line(s, maxCell, false)
	}
	return strings.TrimSpace(s)
}

// expand replaces $name and ${name} in a format with the variable of that
// name. A name is one digit, or letters, digits and underscores that do not
// start with a digit. $$ is a dollar sign. A name with no variable is left
// as written, so that a mistake shows on the bar.
func expand(format string, vars map[string]string) string {
	if !strings.Contains(format, "$") {
		return format
	}
	var b strings.Builder
	for {
		i := strings.IndexByte(format, '$')
		if i < 0 || i == len(format)-1 {
			b.WriteString(format)
			return b.String()
		}
		b.WriteString(format[:i])
		rest := format[i+1:]
		name, n := varName(rest)
		switch {
		case rest[0] == '$':
			b.WriteByte('$')
			format = rest[1:]
			continue
		case n == 0:
			b.WriteByte('$')
			format = rest
			continue
		}
		if v, ok := vars[name]; ok {
			b.WriteString(v)
		} else {
			b.WriteString(format[i : i+1+n])
		}
		format = rest[n:]
	}
}

// varName reads a variable's name from the text after a dollar sign. It
// returns the name and how many bytes it took, braces included; zero when no
// name starts there.
func varName(s string) (name string, n int) {
	if s[0] == '{' {
		end := strings.IndexByte(s, '}')
		if end < 0 {
			return "", 0
		}
		name, used := bareName(s[1:end])
		if used == 0 || used != end-1 {
			return "", 0
		}
		return name, end + 1
	}
	return bareName(s)
}

func bareName(s string) (name string, n int) {
	if s == "" {
		return "", 0
	}
	if s[0] >= '0' && s[0] <= '9' {
		return s[:1], 1
	}
	for n < len(s) && nameByte(s[n]) {
		n++
	}
	return s[:n], n
}

func nameByte(c byte) bool {
	return c == '_' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}
