package termcaps

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Env is what the environment says. Detected is colorprofile.Detect's answer,
// mapped by the caller.
type Env struct {
	Term, ColorTerm, TermProgram, NoColor, LCAll, LCCtype, Lang string
	// EastAsian is RUNEWIDTH_EASTASIAN.
	Tmux, Sty, SSHTTY, SSHConnection, EastAsian string
	Detected                                    Color
}

// ReadEnv reads the variables Env holds through getenv: TERM, COLORTERM,
// TERM_PROGRAM, NO_COLOR, LC_ALL, LC_CTYPE, LANG, TMUX, STY, SSH_TTY,
// SSH_CONNECTION and RUNEWIDTH_EASTASIAN.
func ReadEnv(getenv func(string) string, detected Color) Env {
	return Env{
		Term:          getenv("TERM"),
		ColorTerm:     getenv("COLORTERM"),
		TermProgram:   getenv("TERM_PROGRAM"),
		NoColor:       getenv("NO_COLOR"),
		LCAll:         getenv("LC_ALL"),
		LCCtype:       getenv("LC_CTYPE"),
		Lang:          getenv("LANG"),
		Tmux:          getenv("TMUX"),
		Sty:           getenv("STY"),
		SSHTTY:        getenv("SSH_TTY"),
		SSHConnection: getenv("SSH_CONNECTION"),
		EastAsian:     getenv("RUNEWIDTH_EASTASIAN"),
		Detected:      detected,
	}
}

// Overrides are the settings: "" and "auto" mean detect.
type Overrides struct {
	// Color is truecolor, 256, 16 or none. Glyphs is unicode or ascii.
	// Theme is dark or light. Images is pixels, blocks or text.
	Color, Glyphs, Theme, Images string
	// NoProbe means the terminal is asked nothing.
	NoProbe bool
}

// The words each setting takes, besides "" and "auto".
const (
	wordTrueColor = "truecolor"
	word256       = "256"
	word16        = "16"
	wordNone      = "none"
	wordUnicode   = "unicode"
	wordASCII     = "ascii"
	wordDark      = "dark"
	wordLight     = "light"
	wordPixels    = "pixels"
	wordBlocks    = "blocks"
	wordText      = "text"
	wordAuto      = "auto"
)

// ParseOverrides checks the four settings and returns them in the form
// Resolve reads: lower case, with "auto" as "". A value that is not one of
// the setting's words is an error that names the setting and lists the
// words; every bad value is reported, and each is left as "" in the result.
func ParseOverrides(color, glyphs, theme, images string, noProbe bool) (Overrides, error) {
	o := Overrides{NoProbe: noProbe}
	var errs []error
	for _, s := range []struct {
		name, value string
		into        *string
		words       []string
	}{
		{"color", color, &o.Color, []string{wordTrueColor, word256, word16, wordNone}},
		{"glyphs", glyphs, &o.Glyphs, []string{wordUnicode, wordASCII}},
		{"theme", theme, &o.Theme, []string{wordDark, wordLight}},
		{"images", images, &o.Images, []string{wordPixels, wordBlocks, wordText}},
	} {
		word := strings.ToLower(strings.TrimSpace(s.value))
		switch {
		case word == "" || word == wordAuto:
		case slices.Contains(s.words, word):
			*s.into = word
		default:
			errs = append(errs, fmt.Errorf("%s: %q is not one of %s, %s",
				s.name, s.value, wordAuto, strings.Join(s.words, ", ")))
		}
	}
	return o, errors.Join(errs...)
}

// color is the Color override, if there is one.
func (o Overrides) color() (Color, bool) {
	switch o.Color {
	case wordTrueColor:
		return ColorTrue, true
	case word256:
		return Color256, true
	case word16:
		return Color16, true
	case wordNone:
		return ColorNone, true
	default:
		return 0, false
	}
}

// glyphs is the Glyphs override, if there is one.
func (o Overrides) glyphs() (Glyphs, bool) {
	switch o.Glyphs {
	case wordUnicode:
		return GlyphsUnicode, true
	case wordASCII:
		return GlyphsASCII, true
	default:
		return 0, false
	}
}

// background is the Theme override, if there is one.
func (o Overrides) background() (Background, bool) {
	switch o.Theme {
	case wordDark:
		return BgDark, true
	case wordLight:
		return BgLight, true
	default:
		return 0, false
	}
}

// plainTerm reports whether TERM names a terminal that gets ASCII glyphs and
// no questions: none at all, dumb, the Linux console, ansi, or a DEC VT.
func plainTerm(term string) bool {
	switch term = strings.ToLower(term); term {
	case "", "dumb", "linux", "ansi":
		return true
	default:
		// vt100, vt220, vt52. Not vte-256color, which is a modern terminal.
		return len(term) > 2 && term[:2] == "vt" && term[2] >= '0' && term[2] <= '9'
	}
}

// gnuScreen reports whether the client runs in GNU screen: TERM starts with
// "screen", or STY is set, and TMUX is not (tmux also calls itself screen).
func gnuScreen(env Env) bool {
	return env.Tmux == "" && (strings.HasPrefix(strings.ToLower(env.Term), "screen") || env.Sty != "")
}

// inTmux reports whether the environment says the client runs in tmux.
func inTmux(env Env) bool {
	return env.Tmux != "" || strings.HasPrefix(strings.ToLower(env.Term), "tmux")
}

// locale returns the first of LC_ALL, LC_CTYPE and LANG that is set, with
// its name, the way the C library chooses the character type.
func locale(env Env) (name, value string) {
	switch {
	case env.LCAll != "":
		return "LC_ALL", env.LCAll
	case env.LCCtype != "":
		return "LC_CTYPE", env.LCCtype
	case env.Lang != "":
		return "LANG", env.Lang
	default:
		return "", ""
	}
}

// charsetKind is what a locale value says about the character set.
type charsetKind uint8

const (
	// charsetUnnamed is no locale, or one that names no charset.
	charsetUnnamed charsetKind = iota + 1
	// charsetUTF8 is UTF-8, however it is spelled.
	charsetUTF8
	// charsetOther is C, POSIX, ISO-8859-1, eucJP and the rest.
	charsetOther
)

// charset says which kind of charset a locale value names. "C" and "POSIX"
// are ASCII. Otherwise the charset is what follows the dot, before any
// "@modifier": "en_US.UTF-8", "C.utf8", "de_DE.ISO-8859-15@euro". macOS also
// sets a bare "UTF-8". A value with no dot, such as "en_US", names none.
func charset(value string) charsetKind {
	if value == "C" || value == "POSIX" {
		return charsetOther
	}
	value, _, _ = strings.Cut(value, "@")
	lang, code, dotted := strings.Cut(value, ".")
	if !dotted {
		code = lang
	}
	switch code = strings.ToLower(strings.NewReplacer("-", "", "_", "").Replace(code)); {
	case code == "utf8":
		return charsetUTF8
	case dotted && code != "":
		return charsetOther
	default:
		return charsetUnnamed
	}
}
