package theme

import (
	"charm.land/lipgloss/v2"

	"github.com/quinnovator/contactd/internal/tui/termcaps"
)

// look is how one role is drawn: a colour, or the attribute that stands in
// for it where the terminal has no colour that reads.
type look struct {
	// fg is "#rrggbb", or a palette index ("0" to "15" are the ANSI
	// colours). "" is the terminal's own foreground.
	fg          string
	bold, faint bool
}

// hue is a role drawn in a colour, or plain when c is "".
func hue(c string) look { return look{fg: c} }

// bold is a role drawn bold, in a colour or in none.
func bold(c string) look { return look{fg: c, bold: true} }

// faint is a role drawn faint: what dim text and rules are with 16 colours,
// where the only grey is one many themes make invisible.
func faint() look { return look{faint: true} }

// style is the look as a lipgloss style.
func (l look) style() lipgloss.Style {
	s := lipgloss.NewStyle()
	if l.fg != "" {
		s = s.Foreground(lipgloss.Color(l.fg))
	}
	if l.bold {
		s = s.Bold(true)
	}
	if l.faint {
		s = s.Faint(true)
	}
	return s
}

// palette is every role for one number of colours and one background.
type palette struct {
	accent, tool, dim, ok, warn, bad, mod look
	// rule and border are the lines: the rules across the screen, and the
	// box around the input and the command list.
	rule, border look
	// barFill and codeFill are backgrounds: of the status bar, and of a code
	// block. "" is no fill.
	barFill, codeFill string
}

// paletteFor picks the roles. Each value is chosen by hand for its column: a
// colour that is right in 24 bits is not sent to a 16-colour terminal to be
// approximated, because the nearest ANSI colour to the warning yellow is
// bright red.
//
// A terminal that has not said whether it is dark or light gets colours that
// read on both, and no fills. With 24-bit colour and an unknown background
// the 256-colour set is used: it is the one checked against both.
func paletteFor(c termcaps.Color, bg termcaps.Background) palette {
	switch c {
	case termcaps.ColorTrue:
		return paletteTrue(bg)
	case termcaps.Color256:
		return palette256(bg)
	case termcaps.Color16:
		return palette16(bg)
	case termcaps.ColorNone:
		return paletteNone()
	}
	return paletteNone()
}

// paletteTrue is the 24-bit set: the colours of the mock-ups.
func paletteTrue(bg termcaps.Background) palette {
	switch bg {
	case termcaps.BgDark:
		return palette{
			accent: bold("#5ec4c8"), tool: hue("#6ea8e6"), dim: hue("#7f8a91"),
			ok: hue("#7dc38f"), warn: hue("#d9b25c"), bad: hue("#e06c75"), mod: hue("#c49ae0"),
			rule: hue("#2f373e"), border: hue("#3a444c"),
			barFill: "#1b2126", codeFill: "#181d22",
		}
	case termcaps.BgLight:
		return palette{
			accent: bold("#0f6f77"), tool: hue("#2a5cab"), dim: hue("#636c74"),
			ok: hue("#2c7341"), warn: hue("#8a6100"), bad: hue("#b3261e"), mod: hue("#7b3fa6"),
			rule: hue("#cfcabb"), border: hue("#b9b4a4"),
			barFill: "#ece9df", codeFill: "#eeebe2",
		}
	case termcaps.BgUnknown:
		return palette256(bg)
	}
	return palette256(termcaps.BgUnknown)
}

// palette256 is the set for the 256-colour palette. The unknown column holds
// only colours of middling brightness, which is all that reads on both black
// and white.
func palette256(bg termcaps.Background) palette {
	unknown := palette{
		accent: bold("30"), tool: hue("67"), dim: hue("244"),
		ok: hue("65"), warn: hue("100"), bad: hue("196"), mod: hue("134"),
		rule: hue("244"), border: hue("244"),
	}
	switch bg {
	case termcaps.BgDark:
		return palette{
			accent: bold("80"), tool: hue("75"), dim: hue("244"),
			ok: hue("114"), warn: hue("179"), bad: hue("203"), mod: hue("176"),
			rule: hue("238"), border: hue("240"),
			barFill: "235", codeFill: "235",
		}
	case termcaps.BgLight:
		return palette{
			accent: bold("30"), tool: hue("25"), dim: hue("242"),
			ok: hue("28"), warn: hue("94"), bad: hue("124"), mod: hue("91"),
			rule: hue("250"), border: hue("248"),
			barFill: "254", codeFill: "254",
		}
	case termcaps.BgUnknown:
		return unknown
	}
	return unknown
}

// palette16 is the set for the 16 ANSI colours, which each terminal theme
// draws its own way. It never uses black, white or their bright forms, and on
// a light background never yellow, cyan or green: those are the ones themes
// most often make unreadable. Where no colour is safe the role is plain, bold
// or faint.
func palette16(bg termcaps.Background) palette {
	unknown := palette{
		accent: bold(""), tool: hue(""), dim: faint(),
		ok: hue(""), warn: bold(""), bad: hue("9"), mod: hue(""),
		rule: faint(), border: faint(),
	}
	switch bg {
	case termcaps.BgDark:
		return palette{
			accent: bold("6"), tool: hue("4"), dim: faint(),
			ok: hue("2"), warn: hue("3"), bad: hue("9"), mod: hue("5"),
			rule: faint(), border: faint(),
		}
	case termcaps.BgLight:
		return palette{
			accent: bold("4"), tool: hue("5"), dim: faint(),
			ok: hue(""), warn: bold("5"), bad: hue("1"), mod: hue("5"),
			rule: faint(), border: faint(),
		}
	case termcaps.BgUnknown:
		return unknown
	}
	return unknown
}

// paletteNone is the set without colour: bold and faint only.
func paletteNone() palette {
	return palette{
		accent: bold(""), tool: hue(""), dim: faint(),
		ok: hue(""), warn: bold(""), bad: bold(""), mod: hue(""),
		rule: hue(""), border: hue(""),
	}
}
