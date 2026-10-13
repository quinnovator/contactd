package termcaps_test

import (
	"strings"
	"testing"
	"unicode"

	"github.com/quinnovator/contactd/internal/tui/termcaps"
	"github.com/quinnovator/contactd/internal/tui/tuitest"
)

type resolveCase struct {
	name string
	env  termcaps.Env
	o    termcaps.Overrides
	r    termcaps.Replies
	want termcaps.Caps
}

// base is what the plain xterm of these tests resolves to before the
// terminal has answered anything. Rows state how they differ from it.
func base(change ...func(*termcaps.Caps)) termcaps.Caps {
	c := termcaps.Caps{
		Color: termcaps.Color256, Background: termcaps.BgUnknown, Glyphs: termcaps.GlyphsUnicode,
		Images: termcaps.ImagesBlocks, Width: termcaps.WidthWc, CellW: 8, CellH: 16,
	}
	for _, f := range change {
		f(&c)
	}
	return c
}

func color(v termcaps.Color) func(*termcaps.Caps) { return func(c *termcaps.Caps) { c.Color = v } }

func images(v termcaps.Images) func(*termcaps.Caps) {
	return func(c *termcaps.Caps) { c.Images = v }
}

func bg(v termcaps.Background) func(*termcaps.Caps) {
	return func(c *termcaps.Caps) { c.Background = v }
}

func cellSize(w, h int) func(*termcaps.Caps) {
	return func(c *termcaps.Caps) { c.CellW, c.CellH = w, h }
}

func terminal(v string) func(*termcaps.Caps) { return func(c *termcaps.Caps) { c.Terminal = v } }

func settled(c *termcaps.Caps) { c.Settled = true }

func inTmux(c *termcaps.Caps) { c.Tmux = true }

// ascii is what follows from ASCII glyphs: images are text.
func ascii(c *termcaps.Caps) {
	c.Glyphs, c.Images = termcaps.GlyphsASCII, termcaps.ImagesText
}

// noColour is what follows from no colour: images are text.
func noColour(c *termcaps.Caps) {
	c.Color, c.Images = termcaps.ColorNone, termcaps.ImagesText
}

func colorCases() []resolveCase {
	return []resolveCase{
		{name: "a plain xterm before any answer", env: xterm(), want: base()},
		{
			name: "truecolor as detected",
			env:  xterm(detected(termcaps.ColorTrue)), want: base(color(termcaps.ColorTrue)),
		},
		{name: "16 colours as detected", env: xterm(detected(termcaps.Color16)), want: base(color(termcaps.Color16))},
		{name: "nothing detected is no colour", env: xterm(detected(0)), want: base(noColour)},
		{name: "a detected value out of range is no colour", env: xterm(detected(9)), want: base(noColour)},
		{name: "NO_COLOR=1", env: xterm(noColor("1")), want: base(noColour)},
		{name: "NO_COLOR=yes", env: xterm(detected(termcaps.ColorTrue), noColor("yes")), want: base(noColour)},
		{name: "NO_COLOR=0 is still set", env: xterm(noColor("0")), want: base(noColour)},
		{name: "NO_COLOR empty is not set", env: xterm(noColor("")), want: base()},
		{
			name: "the colour setting beats NO_COLOR",
			env:  xterm(noColor("1")), o: termcaps.Overrides{Color: "256"}, want: base(),
		},
		{
			name: "colour set to none on a truecolor terminal",
			env:  xterm(detected(termcaps.ColorTrue)), o: termcaps.Overrides{Color: "none"}, want: base(noColour),
		},
		{
			name: "colour set to truecolor where 16 were detected",
			env:  xterm(detected(termcaps.Color16)), o: termcaps.Overrides{Color: "truecolor"},
			want: base(color(termcaps.ColorTrue)),
		},
		{
			name: "colour set to 16",
			env:  xterm(detected(termcaps.ColorTrue)), o: termcaps.Overrides{Color: "16"},
			want: base(color(termcaps.Color16)),
		},
		{name: "colour set to auto is detection", env: xterm(), o: termcaps.Overrides{Color: "auto"}, want: base()},
		{
			name: "a colour setting that is not a word is detection",
			env:  xterm(), o: termcaps.Overrides{Color: "mauve"}, want: base(),
		},
		{
			name: "COLORTERM is never evidence of colour or of pixels",
			env:  xterm(func(e *termcaps.Env) { e.ColorTerm = "truecolor" }), want: base(),
		},
	}
}

func glyphCases() []resolveCase {
	off := func(c *termcaps.Caps) { ascii(c); settled(c) }
	return []resolveCase{
		{name: "LANG=C", env: xterm(lang("C")), want: base(ascii)},
		{name: "LANG=POSIX", env: xterm(lang("POSIX")), want: base(ascii)},
		{name: "LANG=C.UTF-8 is UTF-8", env: xterm(lang("C.UTF-8")), want: base()},
		{name: "LANG=en_US.utf8", env: xterm(lang("en_US.utf8")), want: base()},
		{name: "LANG=en_US.ISO-8859-1", env: xterm(lang("en_US.ISO-8859-1")), want: base(ascii)},
		{name: "LANG=de_DE.ISO-8859-15@euro", env: xterm(lang("de_DE.ISO-8859-15@euro")), want: base(ascii)},
		{name: "LANG=ja_JP.eucJP", env: xterm(lang("ja_JP.eucJP")), want: base(ascii)},
		{name: "LANG=sr_RS.UTF-8@latin", env: xterm(lang("sr_RS.UTF-8@latin")), want: base()},
		{name: "LANG=en_US names no charset", env: xterm(lang("en_US")), want: base()},
		{name: "no locale at all", env: xterm(lang("")), want: base()},
		{
			name: "LC_CTYPE=UTF-8, as macOS sets it",
			env:  xterm(lang(""), func(e *termcaps.Env) { e.LCCtype = "UTF-8" }), want: base(),
		},
		{
			name: "LC_ALL=C beats a UTF-8 LANG",
			env:  xterm(func(e *termcaps.Env) { e.LCAll = "C" }), want: base(ascii),
		},
		{
			name: "LC_CTYPE beats LANG=C",
			env:  xterm(lang("C"), func(e *termcaps.Env) { e.LCCtype = "en_US.UTF-8" }), want: base(),
		},
		{
			name: "LC_ALL beats LC_CTYPE=C",
			env:  xterm(func(e *termcaps.Env) { e.LCAll, e.LCCtype = "en_US.UTF-8", "C" }), want: base(),
		},
		{
			name: "LC_CTYPE=C beats a UTF-8 LANG",
			env:  xterm(func(e *termcaps.Env) { e.LCCtype = "C" }), want: base(ascii),
		},
		{name: "TERM=linux", env: xterm(term("linux"), detected(termcaps.Color16)), want: base(color(termcaps.Color16), off)},
		{name: "TERM=dumb", env: xterm(term("dumb"), detected(termcaps.ColorNone)), want: base(noColour, off)},
		{name: "TERM empty", env: xterm(term(""), detected(termcaps.ColorNone)), want: base(noColour, off)},
		{name: "TERM=ansi", env: xterm(term("ansi"), detected(termcaps.Color16)), want: base(color(termcaps.Color16), off)},
		{name: "TERM=vt100", env: xterm(term("vt100"), detected(termcaps.ColorNone)), want: base(noColour, off)},
		{name: "TERM=vt220", env: xterm(term("vt220"), detected(termcaps.ColorNone)), want: base(noColour, off)},
		{name: "TERM=vte-256color is not a DEC VT", env: xterm(term("vte-256color")), want: base()},
		{name: "TERM=DUMB in capitals", env: xterm(term("DUMB"), detected(termcaps.ColorNone)), want: base(noColour, off)},
		{
			name: "RUNEWIDTH_EASTASIAN=1",
			env:  xterm(func(e *termcaps.Env) { e.EastAsian = "1" }), want: base(ascii),
		},
		{
			name: "RUNEWIDTH_EASTASIAN=0",
			env:  xterm(func(e *termcaps.Env) { e.EastAsian = "0" }), want: base(),
		},
		{
			name: "glyphs set to unicode beats TERM=linux",
			env:  xterm(term("linux"), detected(termcaps.Color16)), o: termcaps.Overrides{Glyphs: "unicode"},
			want: base(color(termcaps.Color16), settled),
		},
		{
			name: "glyphs set to unicode beats LANG=C and RUNEWIDTH_EASTASIAN",
			env:  xterm(lang("C"), func(e *termcaps.Env) { e.EastAsian = "1" }), o: termcaps.Overrides{Glyphs: "unicode"},
			want: base(),
		},
		{name: "glyphs set to ascii beats a UTF-8 locale", env: xterm(), o: termcaps.Overrides{Glyphs: "ascii"}, want: base(ascii)},
	}
}

func backgroundCases() []resolveCase {
	return []resolveCase{
		{name: "a dark background reported", env: xterm(), r: fold(background(true)), want: base(bg(termcaps.BgDark))},
		{name: "a light background reported", env: xterm(), r: fold(background(false)), want: base(bg(termcaps.BgLight))},
		{
			name: "the theme setting beats the report",
			env:  xterm(), o: termcaps.Overrides{Theme: "light"}, r: fold(background(true)),
			want: base(bg(termcaps.BgLight)),
		},
		{
			name: "the theme set with nothing reported",
			env:  xterm(), o: termcaps.Overrides{Theme: "dark"}, want: base(bg(termcaps.BgDark)),
		},
		{
			name: "a background reported after the deadline still counts",
			env:  xterm(), r: fold(deadline(), background(true)), want: base(bg(termcaps.BgDark), settled),
		},
	}
}

func tmuxCases() []resolveCase {
	return []resolveCase{
		{name: "tmux before any answer", env: tmux(), want: base(inTmux)},
		{name: "tmux has answered, the terminal behind it has not", env: tmux(), r: fold(version("tmux 3.7c"), da1()), want: base(inTmux)},
		{
			name: "tmux with passthrough, no id-32 reply: the outer terminal is not kitty",
			env:  tmux(), r: fold(version("tmux 3.7c"), da1(), version("XTerm(390)"), da1()),
			want: base(inTmux, settled, terminal("xterm(390)")),
		},
		{
			name: "tmux with the id-32 reply from kitty",
			env:  tmux(), r: fold(version("tmux 3.7c"), da1(), kittyReply(32, true), version("kitty(0.42.1)"), da1()),
			want: base(inTmux, settled, terminal("kitty(0.42.1)"), images(termcaps.ImagesPixels)),
		},
		{
			name: "tmux with the id-32 reply from Ghostty",
			env:  tmux(), r: fold(version("tmux 3.7c"), da1(), kittyReply(32, true), version("ghostty 1.2.0"), da1()),
			want: base(inTmux, settled, terminal("ghostty 1.2.0"), images(termcaps.ImagesPixels)),
		},
		{
			name: "in tmux the id-31 reply is not evidence",
			env:  tmux(), r: fold(kittyReply(31, true), version("kitty(0.42.1)"), da1(), da1()),
			want: base(inTmux, settled, terminal("kitty(0.42.1)")),
		},
		{
			name: "in tmux the environment does not name the terminal",
			env:  tmux(program("ghostty")), r: fold(version("tmux 3.7c"), da1(), kittyReply(32, true), da1()),
			want: base(inTmux, settled),
		},
		{
			name: "tmux with passthrough off: the deadline settles on half blocks",
			env:  tmux(), r: fold(version("tmux 3.7c"), cell(16, 32), da1(), deadline()),
			want: base(inTmux, settled, cellSize(16, 32)),
		},
		{
			name: "TERM=tmux-256color without TMUX, as over SSH from a pane",
			env:  xterm(term("tmux-256color")), r: fold(da1()), want: base(inTmux),
		},
		{
			name: "TERM=screen-256color with TMUX is tmux",
			env:  tmux(term("screen-256color")), r: fold(da1()), want: base(inTmux),
		},
		{
			name: "tmux known only from its version reply waits for the second round",
			env:  xterm(), r: fold(version("tmux 3.4"), da1()), want: base(inTmux),
		},
		{
			name: "tmux known only from its version reply, second round answered",
			env:  xterm(), r: fold(version("tmux 3.4"), da1(), kittyReply(32, true), version("kitty(0.42.1)"), da1()),
			want: base(inTmux, settled, terminal("kitty(0.42.1)"), images(termcaps.ImagesPixels)),
		},
		{
			name: "pixels in tmux need only 256 colours, which is what tmux is detected as",
			env:  tmux(), o: termcaps.Overrides{Color: "256"},
			r:    fold(kittyReply(32, true), version("kitty(0.42.1)")),
			want: base(inTmux, terminal("kitty(0.42.1)"), images(termcaps.ImagesPixels)),
		},
	}
}

func kittyCases() []resolveCase {
	true24 := color(termcaps.ColorTrue)
	return []resolveCase{
		{
			name: "kitty by TERM with the reply",
			env:  kitty(), r: fold(kittyReply(31, true), da1()),
			want: base(true24, settled, terminal("kitty"), images(termcaps.ImagesPixels)),
		},
		{
			name: "kitty by its version reply",
			env:  xterm(), r: fold(kittyReply(31, true), version("kitty(0.42.1)"), da1()),
			want: base(settled, terminal("kitty(0.42.1)"), images(termcaps.ImagesPixels)),
		},
		{
			name: "Ghostty by TERM",
			env:  kitty(term("xterm-ghostty")), r: fold(kittyReply(31, true), da1()),
			want: base(true24, settled, terminal("ghostty"), images(termcaps.ImagesPixels)),
		},
		{
			name: "Ghostty by TERM_PROGRAM",
			env:  xterm(program("ghostty")), r: fold(kittyReply(31, true), da1()),
			want: base(settled, terminal("ghostty"), images(termcaps.ImagesPixels)),
		},
		{
			name: "the version reply beats what TERM says",
			env:  kitty(), r: fold(kittyReply(31, true), version("WezTerm 20240203-110809-5046fc22"), da1()),
			want: base(true24, settled, terminal("wezterm 20240203-110809-5046fc22")),
		},
		{
			name: "a kitty reply from a terminal not on the allowlist",
			env:  xterm(detected(termcaps.ColorTrue)), r: fold(kittyReply(31, true), version("Konsole 24.02.2"), da1()),
			want: base(true24, settled, terminal("konsole 24.02.2")),
		},
		{
			name: "a kitty reply from a terminal whose name only contains kitty",
			env:  xterm(), r: fold(kittyReply(31, true), version("not-kitty 1.0"), da1()),
			want: base(settled, terminal("not-kitty 1.0")),
		},
		{
			name: "a kitty reply from a terminal whose name only contains ghostty",
			env:  xterm(), r: fold(kittyReply(31, true), version("xghostty 1.0"), da1()),
			want: base(settled, terminal("xghostty 1.0")),
		},
		{
			name: "TERM in capitals still names kitty",
			env:  kitty(term("XTERM-KITTY")), r: fold(kittyReply(31, true), da1()),
			want: base(true24, settled, terminal("kitty"), images(termcaps.ImagesPixels)),
		},
		{
			name: "TERM_PROGRAM in capitals still names Ghostty",
			env:  xterm(program("Ghostty")), r: fold(kittyReply(31, true), da1()),
			want: base(settled, terminal("ghostty"), images(termcaps.ImagesPixels)),
		},
		{
			name: "a kitty reply from a terminal that gives no name",
			env:  xterm(), r: fold(kittyReply(31, true), da1()), want: base(settled),
		},
		{
			name: "TERM=xterm-kitty alone is not evidence",
			env:  kitty(), r: fold(da1()), want: base(true24, settled, terminal("kitty")),
		},
		{
			name: "kitty that answered with an error",
			env:  kitty(), r: fold(kittyReply(31, false), da1()), want: base(true24, settled, terminal("kitty")),
		},
		{
			name: "outside tmux the id-32 reply is not evidence",
			env:  kitty(), r: fold(kittyReply(32, true), da1()), want: base(true24, settled, terminal("kitty")),
		},
		{
			name: "kitty held to 16 colours gets half blocks",
			env:  kitty(), o: termcaps.Overrides{Color: "16"}, r: fold(kittyReply(31, true), da1()),
			want: base(color(termcaps.Color16), settled, terminal("kitty")),
		},
		{
			name: "kitty with ASCII glyphs gets text",
			env:  kitty(), o: termcaps.Overrides{Glyphs: "ascii"}, r: fold(kittyReply(31, true), da1()),
			want: base(true24, ascii, settled, terminal("kitty")),
		},
		{
			name: "kitty with NO_COLOR gets text",
			env:  kitty(noColor("1")), r: fold(kittyReply(31, true), da1()),
			want: base(noColour, settled, terminal("kitty")),
		},
		{
			name: "a kitty reply after the deadline changes nothing",
			env:  kitty(), r: fold(deadline(), kittyReply(31, true)), want: base(true24, settled, terminal("kitty")),
		},
	}
}

func imageSettingCases() []resolveCase {
	answered := fold(kittyReply(31, true), da1())
	return []resolveCase{
		{
			name: "images set to text on kitty",
			env:  kitty(), o: termcaps.Overrides{Images: "text"}, r: answered,
			want: base(color(termcaps.ColorTrue), settled, terminal("kitty"), images(termcaps.ImagesText)),
		},
		{
			name: "images set to blocks on kitty",
			env:  kitty(), o: termcaps.Overrides{Images: "blocks"}, r: answered,
			want: base(color(termcaps.ColorTrue), settled, terminal("kitty")),
		},
		{
			name: "images set to pixels needs no evidence",
			env:  xterm(), o: termcaps.Overrides{Images: "pixels"}, want: base(images(termcaps.ImagesPixels)),
		},
		{
			name: "images set to pixels at 16 colours is blocks",
			env:  xterm(detected(termcaps.Color16)), o: termcaps.Overrides{Images: "pixels"},
			want: base(color(termcaps.Color16)),
		},
		{
			name: "images set to pixels with no colour is text",
			env:  xterm(noColor("1")), o: termcaps.Overrides{Images: "pixels"}, want: base(noColour),
		},
		{
			name: "images set to pixels with ASCII glyphs is text",
			env:  xterm(lang("C")), o: termcaps.Overrides{Images: "pixels"}, want: base(ascii),
		},
		{
			name: "images set to blocks at 16 colours",
			env:  xterm(detected(termcaps.Color16)), o: termcaps.Overrides{Images: "blocks"},
			want: base(color(termcaps.Color16)),
		},
		{
			name: "images set to blocks with no colour is text",
			env:  xterm(detected(termcaps.ColorNone)), o: termcaps.Overrides{Images: "blocks"}, want: base(noColour),
		},
		{
			name: "images set to blocks with ASCII glyphs is text",
			env:  xterm(), o: termcaps.Overrides{Images: "blocks", Glyphs: "ascii"}, want: base(ascii),
		},
		{
			name: "half blocks need 16 colours",
			env:  xterm(detected(termcaps.ColorNone)), want: base(noColour),
		},
	}
}

func cellCases() []resolveCase {
	ssh := func(e *termcaps.Env) { e.SSHTTY = "/dev/ttys003" }
	sshConn := func(e *termcaps.Env) { e.SSHConnection = "10.0.0.1 50000 10.0.0.2 22" }
	return []resolveCase{
		{name: "a 10x20 cell", env: xterm(), r: fold(cell(10, 20)), want: base(cellSize(10, 20))},
		{name: "a 16x32 cell is the limit", env: xterm(), r: fold(cell(16, 32)), want: base(cellSize(16, 32))},
		{name: "a 20x40 cell is scaled to 16x32", env: xterm(), r: fold(cell(20, 40)), want: base(cellSize(16, 32))},
		{name: "a tall cell keeps its shape", env: xterm(), r: fold(cell(18, 45)), want: base(cellSize(13, 32))},
		{name: "a wide cell keeps its shape", env: xterm(), r: fold(cell(30, 40)), want: base(cellSize(16, 21))},
		{name: "a sliver of a cell is at least one pixel wide", env: xterm(), r: fold(cell(1, 4000)), want: base(cellSize(1, 32))},
		{name: "a small cell is kept", env: xterm(), r: fold(cell(6, 12)), want: base(cellSize(6, 12))},
		{name: "over SSH a 10x20 cell is scaled to 8x16", env: xterm(ssh), r: fold(cell(10, 20)), want: base(cellSize(8, 16))},
		{name: "over SSH, by SSH_CONNECTION alone", env: xterm(sshConn), r: fold(cell(16, 32)), want: base(cellSize(8, 16))},
		{name: "over SSH a smaller cell is kept", env: xterm(ssh), r: fold(cell(7, 14)), want: base(cellSize(7, 14))},
		{name: "over SSH with no reply", env: xterm(ssh, sshConn), want: base()},
		{
			name: "a cell size built by hand that makes no sense is the default",
			env:  xterm(), r: termcaps.Replies{CellW: -4, CellH: 20}, want: base(),
		},
	}
}

func settleCases() []resolveCase {
	apple := program("Apple_Terminal")
	return []resolveCase{
		{name: "one device-attributes reply settles", env: xterm(), r: fold(da1()), want: base(settled)},
		{name: "the deadline settles", env: xterm(), r: fold(deadline()), want: base(settled)},
		{name: "probing switched off is settled at once", env: xterm(), o: termcaps.Overrides{NoProbe: true}, want: base(settled)},
		{name: "Apple Terminal waits for the deadline", env: xterm(apple), r: fold(background(false)), want: base(bg(termcaps.BgLight))},
		{name: "Apple Terminal is not settled by a stray reply", env: xterm(apple), r: fold(da1(), da1()), want: base()},
		{name: "Apple Terminal settles at the deadline", env: xterm(apple), r: fold(deadline()), want: base(settled)},
		{name: "GNU screen is asked nothing", env: xterm(term("screen-256color")), want: base(settled)},
		{
			name: "GNU screen by STY, whatever TERM says",
			env:  xterm(func(e *termcaps.Env) { e.Sty = "1234.pts-0.host" }), want: base(settled),
		},
		{
			name: "mode 2027 recognised",
			env:  xterm(), r: fold(mode2027(true)),
			want: base(func(c *termcaps.Caps) { c.Width = termcaps.WidthGrapheme }),
		},
		{name: "mode 2027 not recognised", env: xterm(), r: fold(mode2027(false)), want: base()},
		{
			name: "key enhancements reported",
			env:  xterm(), r: fold(keyboard(1)), want: base(func(c *termcaps.Caps) { c.ShiftEnter = true }),
		},
		{name: "key enhancements reported as none", env: xterm(), r: fold(keyboard(0)), want: base()},
		{
			name: "everything at once: Ghostty on a light theme over SSH",
			env:  kitty(term("xterm-ghostty"), func(e *termcaps.Env) { e.SSHConnection = "a b c d" }),
			r: fold(background(false), mode2027(true), keyboard(7), kittyReply(31, true),
				version("ghostty 1.2.0"), cell(18, 36), da1()),
			want: termcaps.Caps{
				Color: termcaps.ColorTrue, Background: termcaps.BgLight, Glyphs: termcaps.GlyphsUnicode,
				Images: termcaps.ImagesPixels, Width: termcaps.WidthGrapheme, CellW: 8, CellH: 16,
				ShiftEnter: true, Terminal: "ghostty 1.2.0", Settled: true,
			},
		},
	}
}

func resolveCases() []resolveCase {
	var all []resolveCase
	for _, group := range [][]resolveCase{
		colorCases(), glyphCases(), backgroundCases(), tmuxCases(), kittyCases(),
		imageSettingCases(), cellCases(), settleCases(),
	} {
		all = append(all, group...)
	}
	return all
}

func TestResolve(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for _, tc := range resolveCases() {
		if seen[tc.name] {
			t.Errorf("two rows are named %q", tc.name)
		}
		seen[tc.name] = true
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := termcaps.Resolve(tc.env, tc.o, tc.r)
			if got != tc.want {
				t.Errorf("Resolve:\n got %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

// Every combination of a spread of environments, settings and answers: the
// rules that hold whatever the inputs are.
func TestResolveAlwaysHolds(t *testing.T) {
	t.Parallel()
	envs := []termcaps.Env{
		{},
		xterm(), tmux(), kitty(), xterm(term("linux")), xterm(term("dumb")), xterm(lang("C")),
		xterm(noColor("1")), xterm(detected(termcaps.Color16)), xterm(program("Apple_Terminal")),
		xterm(term("screen")), kitty(func(e *termcaps.Env) { e.SSHTTY = "/dev/pts/1" }),
		tmux(func(e *termcaps.Env) { e.SSHConnection = "a b c d" }),
		xterm(term("xterm\x1b[2J"), lang("en_US.UTF-8\x1b]0;t\x07"), program("gho\u202Estty")),
		xterm(term("x\u0301term-世界\U0010EEEE"), lang("fr_FR.ISO-8859-1\u009b\u0301é"), program("ghostty ⏺\uFE0F")),
		xterm(term("tmux-256color"), func(e *termcaps.Env) { e.Sty = "1234.pts-0.host" }),
	}
	settings := []termcaps.Overrides{
		{},
		{NoProbe: true},
		{Color: "none"},
		{Color: "16"},
		{Color: "truecolor"},
		{Glyphs: "ascii"},
		{Glyphs: "unicode"},
		{Theme: "light"},
		{Images: "pixels"},
		{Images: "blocks"},
		{Images: "text"},
		{Color: "bogus", Glyphs: "bogus", Theme: "bogus", Images: "bogus"},
	}
	answers := []termcaps.Replies{
		{},
		fold(da1()), fold(deadline()), fold(kittyReply(31, true), version("kitty(0.42.1)"), cell(10, 20), da1()),
		fold(version("tmux 3.7c"), da1(), kittyReply(32, true), version("ghostty 1.2.0"), da1()),
		fold(background(true), mode2027(true), keyboard(3)), fold(cell(4096, 4096)), fold(cell(1, 4096)),
		fold(cell(4096, 1)), fold(version("evil\x1b[2J\u009b\U0010EEEE name"), kittyReply(31, true), da1()),
		fold(version("\x1f\x7fkitty\x7f \x1f(1)"), kittyReply(31, true), da1()),
		{CellW: 1 << 30, CellH: 1 << 30, DA1: -1, Background: 9, Version: "MiXed\x00Case"},
	}
	for _, env := range envs {
		for _, o := range settings {
			for _, r := range answers {
				c := termcaps.Resolve(env, o, r)
				if again := termcaps.Resolve(env, o, r); again != c {
					t.Fatalf("Resolve is not pure: %+v then %+v", c, again)
				}
				if problem := wellFormed(env, o, c); problem != "" {
					t.Errorf("Resolve(%+v, %+v, %+v) = %+v: %s", env, o, r, c, problem)
				}
				lines := termcaps.Explain(env, o, r)
				if problem := explains(lines, c); problem != "" {
					t.Errorf("Explain(%+v, %+v, %+v) = %q: %s", env, o, r, lines, problem)
				}
				// After the deadline, the answers that are ignored change
				// nothing, in what is kept or in what is resolved.
				if r.Deadline {
					for _, late := range []termcaps.Reply{version("kitty(1)"), version("tmux 3.4"), kittyReply(31, true), kittyReply(32, true), da1(), cell(9, 18)} {
						if after := termcaps.Fold(r, late); after != r {
							t.Errorf("Fold(%+v, %+v) = %+v after the deadline", r, late, after)
						}
					}
				}
			}
		}
	}
}

// wellFormed reports a Caps that breaks a rule every Caps obeys, or "".
func wellFormed(env termcaps.Env, o termcaps.Overrides, c termcaps.Caps) string {
	switch {
	case c.Color < termcaps.ColorNone || c.Color > termcaps.ColorTrue:
		return "Color is not a colour"
	case c.Background < termcaps.BgUnknown || c.Background > termcaps.BgLight:
		return "Background is not a background"
	case c.Glyphs != termcaps.GlyphsUnicode && c.Glyphs != termcaps.GlyphsASCII:
		return "Glyphs is not a glyph set"
	case c.Images < termcaps.ImagesText || c.Images > termcaps.ImagesPixels:
		return "Images is not a level"
	case c.Width != termcaps.WidthWc && c.Width != termcaps.WidthGrapheme:
		return "Width is not a width"
	case c.Images == termcaps.ImagesPixels && (c.Glyphs != termcaps.GlyphsUnicode || c.Color < termcaps.Color256):
		return "pixels without Unicode glyphs and 256 colours"
	case c.Images == termcaps.ImagesBlocks && (c.Glyphs != termcaps.GlyphsUnicode || c.Color < termcaps.Color16):
		return "blocks without Unicode glyphs and 16 colours"
	case c.Images != termcaps.ImagesText && o.Images == "text":
		return "images shown although set to text"
	case c.CellW < 1 || c.CellW > 16 || c.CellH < 1 || c.CellH > 32:
		return "the cell is outside 1x1 to 16x32"
	case (env.SSHTTY != "" || env.SSHConnection != "") && (c.CellW > 8 || c.CellH > 16):
		return "the cell is larger than 8x16 over SSH"
	case termcaps.PlanFor(env, o) == termcaps.ProbeOff && !c.Settled:
		return "nothing is asked, yet it is not settled"
	case env.Tmux != "" && !c.Tmux:
		return "TMUX is set, yet Tmux is false"
	case c.Terminal != strings.ToLower(c.Terminal) || len(c.Terminal) > 64 || !printable(c.Terminal):
		return "Terminal is not a short lower-case printable name"
	}
	return ""
}

func printable(s string) bool {
	for _, r := range s {
		if r > unicode.MaxASCII || !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

// explains reports a set of Explain lines that does not describe c, or "".
func explains(lines []string, c termcaps.Caps) string {
	terminalName := c.Terminal
	if terminalName == "" {
		terminalName = "unknown"
	}
	yes := map[bool]string{true: "yes", false: "no"}
	starts := []string{
		"color: " + c.Color.String(),
		"background: " + c.Background.String(),
		"glyphs: " + c.Glyphs.String(),
		"images: " + c.Images.String(),
		"width: " + c.Width.String(),
		"cell: ",
		"tmux: " + yes[c.Tmux],
		"terminal: " + terminalName,
		"shift+enter: " + yes[c.ShiftEnter],
		"probe: ",
	}
	if len(lines) != len(starts) {
		return "not one line per field"
	}
	for i, line := range lines {
		if !strings.HasPrefix(line, starts[i]) {
			return "line " + line + " does not start with " + starts[i]
		}
		if !strings.HasSuffix(line, ")") || !strings.Contains(line, " (") {
			return "line " + line + " gives no reason"
		}
		for _, r := range line {
			if r < 0x20 || r > 0x7E {
				return "a line holds a character that is not printable ASCII"
			}
		}
	}
	if settledWord := strings.Contains(lines[9], "settled"); settledWord != c.Settled && !strings.HasPrefix(lines[9], "probe: off") {
		return "the probe line disagrees with Settled"
	}
	return ""
}

func TestExplainGolden(t *testing.T) {
	t.Parallel()
	scenes := []struct {
		name string
		env  termcaps.Env
		o    termcaps.Overrides
		r    termcaps.Replies
	}{
		{name: "a plain xterm before any answer", env: xterm()},
		{
			name: "kitty, settled",
			env:  kitty(),
			r: fold(background(true), mode2027(true), keyboard(1), kittyReply(31, true),
				version("kitty(0.42.1)"), cell(20, 40), da1()),
		},
		{
			name: "tmux with passthrough off, after the deadline",
			env:  tmux(), r: fold(version("tmux 3.7c"), cell(16, 32), da1(), deadline()),
		},
		{
			name: "tmux in front of Ghostty over SSH",
			env:  tmux(func(e *termcaps.Env) { e.SSHTTY = "/dev/pts/3" }),
			r: fold(background(false), version("tmux 3.7c"), cell(10, 22), da1(),
				kittyReply(32, true), version("ghostty 1.2.0"), da1()),
		},
		{name: "tmux waiting for the terminal behind it", env: tmux(), r: fold(version("tmux 3.7c"), da1())},
		{
			name: "a kitty reply from a terminal not on the allowlist",
			env:  xterm(detected(termcaps.ColorTrue)), r: fold(kittyReply(31, true), version("WezTerm 20240203"), da1()),
		},
		{name: "a serial console", env: termcaps.Env{Term: "vt100", Lang: "C", Detected: termcaps.ColorNone}},
		{name: "the Linux console", env: xterm(term("linux"), detected(termcaps.Color16))},
		{name: "TERM not set", env: termcaps.Env{}},
		{name: "LANG=C with NO_COLOR", env: xterm(lang("C"), noColor("yes")), r: fold(da1())},
		{name: "a locale that names no charset", env: xterm(lang("en_US")), r: fold(deadline())},
		{name: "RUNEWIDTH_EASTASIAN=1", env: xterm(func(e *termcaps.Env) { e.EastAsian = "1" })},
		{name: "Apple Terminal", env: xterm(program("Apple_Terminal")), r: fold(background(false))},
		{name: "GNU screen", env: xterm(term("screen-256color"))},
		{
			name: "everything configured",
			env:  xterm(noColor("1")),
			o:    termcaps.Overrides{Color: "truecolor", Glyphs: "unicode", Theme: "light", Images: "pixels", NoProbe: true},
		},
		{
			name: "pixels configured where they cannot be shown",
			env:  xterm(detected(termcaps.Color16)), o: termcaps.Overrides{Images: "pixels"}, r: fold(da1()),
		},
		{
			name: "a hostile environment is quoted",
			env:  xterm(term("vt100\x1b[2J"), lang("C\x1b]0;owned\x07"), program("kitty\x1b[0m")),
		},
	}
	var b strings.Builder
	for i, s := range scenes {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("# " + s.name + "\n")
		for _, line := range termcaps.Explain(s.env, s.o, s.r) {
			b.WriteString(line + "\n")
		}
	}
	tuitest.Golden(t, "explain.golden", []byte(b.String()))
}
