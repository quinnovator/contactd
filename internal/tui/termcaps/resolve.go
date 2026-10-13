package termcaps

import (
	"strconv"
	"strings"
)

const (
	// A cell is taken to be 8 by 16 pixels until the terminal says.
	defaultCellW, defaultCellH = 8, 16
	// A reported cell is scaled down to fit 16 by 32, so that an image on a
	// dense screen does not cost four times the bytes, and to 8 by 16 over
	// SSH, where the bytes cross a network.
	maxCellW, maxCellH = 16, 32
)

// Resolve works out what the terminal can do from the environment, the
// settings and the terminal's answers so far. It is pure: the same three
// values always give the same Caps. For each field the first rule that
// matches wins:
//
//   - Color: the setting; none when NO_COLOR is not empty; what was detected.
//   - Glyphs: the setting; ASCII when TERM is empty, dumb, linux, ansi or a
//     DEC VT; ASCII when the first of LC_ALL, LC_CTYPE and LANG that is set
//     names a charset that is not UTF-8; ASCII when RUNEWIDTH_EASTASIAN is
//     "1"; Unicode.
//   - Background: the theme setting; the terminal's answer; unknown.
//   - Tmux: TMUX is set, TERM starts with "tmux", or a version reply did.
//   - Terminal: the version reply; outside tmux, "kitty" or "ghostty" when
//     TERM or TERM_PROGRAM contains that word.
//   - Images: text when set. Pixels when set, given Unicode glyphs and 256
//     colours; blocks when set, given Unicode glyphs and 16 colours; else
//     the next level down. Unset: pixels when the kitty query was answered
//     OK (through passthrough in tmux), Terminal starts with "kitty" or
//     "ghostty", and there are Unicode glyphs and 256 colours; blocks given
//     Unicode glyphs and 16 colours; text.
//   - Width: grapheme once mode 2027 was recognised; wcwidth.
//   - Cell: the reported size, scaled to fit 16x32, or 8x16 over SSH; 8x16.
//   - ShiftEnter: keyboard enhancements were reported.
//   - Settled: nothing is asked; the deadline passed; or, when device
//     attributes were asked for, one reply came (two in tmux).
func Resolve(env Env, o Overrides, r Replies) Caps {
	c, _ := resolve(env, o, r)
	return c
}

// reasons says why each field of a Caps has its value.
type reasons struct {
	color, background, glyphs, images, width, cell, tmux, terminal, shiftEnter, settled string
}

// resolve is Resolve with the reasons, so that Explain cannot disagree with
// it.
func resolve(env Env, o Overrides, r Replies) (Caps, reasons) {
	var c Caps
	var why reasons
	c.Color, why.color = resolveColor(env, o)
	c.Glyphs, why.glyphs = resolveGlyphs(env, o)
	c.Background, why.background = resolveBackground(o, r)
	c.Tmux, why.tmux = resolveTmux(env, r)
	c.Terminal, why.terminal = resolveTerminal(env, r, c.Tmux)
	c.Images, why.images = resolveImages(o, r, c)
	c.Width, why.width = WidthWc, "mode 2027 was not reported"
	if r.Graphemes {
		c.Width, why.width = WidthGrapheme, "the terminal recognised mode 2027"
	}
	c.CellW, c.CellH, why.cell = resolveCell(env, r)
	c.ShiftEnter, why.shiftEnter = false, "no key enhancements were reported"
	if r.KeyFlags > 0 {
		c.ShiftEnter, why.shiftEnter = true, "key enhancements were reported"
	}
	c.Settled, why.settled = resolveSettled(env, o, r, c.Tmux)
	return c, why
}

// configured is the reason for a value that a setting gave.
const configured = "configured"

func resolveColor(env Env, o Overrides) (Color, string) {
	if c, ok := o.color(); ok {
		return c, configured
	}
	if env.NoColor != "" {
		return ColorNone, "NO_COLOR is set"
	}
	if env.Detected < ColorNone || env.Detected > ColorTrue {
		return ColorNone, "nothing was detected"
	}
	return env.Detected, "detected from the environment"
}

func resolveGlyphs(env Env, o Overrides) (Glyphs, string) {
	if g, ok := o.glyphs(); ok {
		return g, configured
	}
	if plainTerm(env.Term) {
		return GlyphsASCII, termIs(env)
	}
	name, value := locale(env)
	kind := charset(value)
	if kind == charsetOther {
		return GlyphsASCII, name + " is " + quote(value) + ", not UTF-8"
	}
	if env.EastAsian == "1" {
		return GlyphsASCII, "RUNEWIDTH_EASTASIAN is 1"
	}
	switch {
	case name == "":
		return GlyphsUnicode, "no locale is set"
	case kind == charsetUTF8:
		return GlyphsUnicode, name + " is " + quote(value)
	default:
		return GlyphsUnicode, name + " is " + quote(value) + ", which names no charset"
	}
}

func resolveBackground(o Overrides, r Replies) (Background, string) {
	if b, ok := o.background(); ok {
		return b, configured
	}
	switch r.Background {
	case BgDark, BgLight:
		return r.Background, "the terminal reported it"
	case BgUnknown:
	}
	return BgUnknown, "the terminal has not reported one"
}

func resolveTmux(env Env, r Replies) (bool, string) {
	switch {
	case env.Tmux != "":
		return true, "TMUX is set"
	case inTmux(env):
		return true, termIs(env)
	case r.TmuxSeen:
		return true, "the version reply names tmux"
	default:
		return false, "no sign of it"
	}
}

func resolveTerminal(env Env, r Replies, tmux bool) (string, string) {
	if name := terminalName(r.Version); name != "" {
		if tmux {
			return name, "its version reply, through tmux"
		}
		return name, "its version reply"
	}
	if tmux {
		// What the environment says inside tmux was set by whichever
		// terminal started the server, not the one attached now.
		return "", "no version reply through tmux"
	}
	for _, word := range []string{"kitty", "ghostty"} {
		if strings.Contains(strings.ToLower(env.Term), word) {
			return word, termIs(env)
		}
		if strings.Contains(strings.ToLower(env.TermProgram), word) {
			return word, "TERM_PROGRAM is " + quote(env.TermProgram)
		}
	}
	return "", "no version reply"
}

// resolveImages reads the fields of c that are already set: Color, Glyphs,
// Tmux and Terminal.
func resolveImages(o Overrides, r Replies, c Caps) (Images, string) {
	unicode := c.Glyphs == GlyphsUnicode
	blocks := unicode && c.Color >= Color16
	pixels := unicode && c.Color >= Color256
	switch o.Images {
	case wordText:
		return ImagesText, configured
	case wordPixels:
		switch {
		case pixels:
			return ImagesPixels, configured
		case blocks:
			return ImagesBlocks, "configured as pixels, which need 256 colours"
		default:
			return ImagesText, "configured as pixels, which need Unicode glyphs and 256 colours"
		}
	case wordBlocks:
		if blocks {
			return ImagesBlocks, configured
		}
		return ImagesText, "configured as blocks, which need Unicode glyphs and 16 colours"
	}

	answered := r.Kitty
	if c.Tmux {
		answered = r.KittyOuter
	}
	listed := strings.HasPrefix(c.Terminal, "kitty") || strings.HasPrefix(c.Terminal, "ghostty")
	switch {
	case !unicode:
		return ImagesText, "half blocks need Unicode glyphs"
	case !blocks:
		return ImagesText, "half blocks need 16 colours"
	case !pixels:
		return ImagesBlocks, "pixels need 256 colours"
	case answered && listed:
		return ImagesPixels, "kitty graphics answered"
	case answered && c.Terminal == "":
		return ImagesBlocks, "kitty graphics answered, but the terminal did not give its name"
	case answered:
		return ImagesBlocks, "kitty graphics answered, but pixels are for kitty and Ghostty only"
	case c.Tmux:
		return ImagesBlocks, "no kitty graphics reply through tmux; is allow-passthrough on?"
	default:
		return ImagesBlocks, "no kitty graphics reply"
	}
}

func resolveCell(env Env, r Replies) (w, h int, why string) {
	if r.CellW <= 0 || r.CellH <= 0 {
		return defaultCellW, defaultCellH, "the terminal has not said"
	}
	limitW, limitH := maxCellW, maxCellH
	ssh := env.SSHTTY != "" || env.SSHConnection != ""
	if ssh {
		limitW, limitH = defaultCellW, defaultCellH
	}
	w, h = fitCell(min(r.CellW, maxCell), min(r.CellH, maxCell), limitW, limitH)
	reported := "the terminal reported " + strconv.Itoa(r.CellW) + "x" + strconv.Itoa(r.CellH)
	switch {
	case w == r.CellW && h == r.CellH:
		return w, h, "the terminal reported it"
	case ssh:
		return w, h, reported + ", scaled down for SSH"
	default:
		return w, h, reported + ", scaled down"
	}
}

// fitCell scales a cell down, keeping its shape, until it is no larger than
// the limit either way. It never scales up, and never goes below 1.
func fitCell(w, h, limitW, limitH int) (int, int) {
	if w <= limitW && h <= limitH {
		return w, h
	}
	if w*limitH >= h*limitW {
		// The width is what does not fit.
		return limitW, max(1, (h*limitW+w/2)/w)
	}
	return max(1, (w*limitH+h/2)/h), limitH
}

func resolveSettled(env Env, o Overrides, r Replies, tmux bool) (bool, string) {
	plan, why := planFor(env, o)
	switch plan {
	case ProbeOff:
		return true, why
	case ProbeBasic:
		if r.Deadline {
			return true, "the deadline passed"
		}
		return false, why
	case ProbeFull, ProbeTmux:
	}
	switch {
	case tmux && r.DA1 >= 2:
		return true, "tmux and the terminal behind it answered"
	case !tmux && r.DA1 >= 1:
		return true, "the terminal answered"
	case r.Deadline && tmux && r.DA1 == 1:
		return true, "the deadline passed with no reply through tmux"
	case r.Deadline:
		return true, "the deadline passed with no reply"
	case tmux && r.DA1 == 1:
		return false, "tmux answered; waiting for the terminal behind it"
	default:
		return false, "waiting for the terminal"
	}
}

// Explain says what Resolve decided and why, one line per field: the
// field, its value, and the reason in brackets. It is what /status prints.
// The lines hold nothing but printable ASCII: values taken from the
// environment are quoted and escaped, and the terminal's name is already
// plain.
func Explain(env Env, o Overrides, r Replies) []string {
	c, why := resolve(env, o, r)
	terminal := c.Terminal
	if terminal == "" {
		terminal = "unknown"
	}
	plan, _ := planFor(env, o)
	probe := plan.String()
	switch {
	case plan == ProbeOff:
	case c.Settled:
		probe += ", settled"
	default:
		probe += ", waiting"
	}
	return []string{
		line("color", c.Color.String(), why.color),
		line("background", c.Background.String(), why.background),
		line("glyphs", c.Glyphs.String(), why.glyphs),
		line("images", c.Images.String(), why.images),
		line("width", c.Width.String(), why.width),
		line("cell", strconv.Itoa(c.CellW)+"x"+strconv.Itoa(c.CellH), why.cell),
		line("tmux", yesNo(c.Tmux), why.tmux),
		line("terminal", terminal, why.terminal),
		line("shift+enter", yesNo(c.ShiftEnter), why.shiftEnter),
		line("probe", probe, why.settled),
	}
}

// quote writes a value from the environment so that it is safe and plain to
// show: in double quotes, with everything outside printable ASCII escaped.
func quote(value string) string {
	return strconv.QuoteToASCII(value)
}

// line is one line of Explain.
func line(field, value, why string) string {
	if why == "" {
		return field + ": " + value
	}
	return field + ": " + value + " (" + why + ")"
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
