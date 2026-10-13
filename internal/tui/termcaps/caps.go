// Package termcaps works out what the terminal can do.
//
// There are three sources: the environment, the settings, and what the
// terminal answers when asked. Resolve turns all three into one Caps value,
// and everything the client draws derives from that value. Resolve is pure,
// so each rule is a row in a table test, and a late answer from the terminal
// is handled by resolving again and comparing.
//
// Nothing here does I/O. The caller reads the environment, sends the probe
// bytes and turns the terminal's answers into Reply values.
package termcaps

// Color is how many colours the terminal shows. The values are ordered: a
// larger one shows more.
type Color uint8

const (
	// ColorNone is no colour: bold, faint and reverse only.
	ColorNone Color = iota + 1
	// Color16 is the 16 ANSI colours.
	Color16
	// Color256 is the 256-colour palette.
	Color256
	// ColorTrue is 24-bit colour.
	ColorTrue
)

// String is the name the settings use: "none", "16", "256" or "truecolor".
func (c Color) String() string {
	switch c {
	case ColorNone:
		return "none"
	case Color16:
		return "16"
	case Color256:
		return "256"
	case ColorTrue:
		return "truecolor"
	default:
		return unset
	}
}

// Background is whether the terminal's background is dark or light.
type Background uint8

const (
	// BgUnknown means the terminal has not said, so colours must read on
	// both.
	BgUnknown Background = iota + 1
	// BgDark is a dark background.
	BgDark
	// BgLight is a light background.
	BgLight
)

// String is "unknown", "dark" or "light".
func (b Background) String() string {
	switch b {
	case BgUnknown:
		return "unknown"
	case BgDark:
		return "dark"
	case BgLight:
		return "light"
	default:
		return unset
	}
}

// Glyphs is the set of symbols the client draws with.
type Glyphs uint8

const (
	// GlyphsUnicode is the set as designed.
	GlyphsUnicode Glyphs = iota + 1
	// GlyphsASCII is a stand-in for each symbol from bytes under 0x80.
	GlyphsASCII
)

// String is "unicode" or "ascii".
func (g Glyphs) String() string {
	switch g {
	case GlyphsUnicode:
		return "unicode"
	case GlyphsASCII:
		return "ascii"
	default:
		return unset
	}
}

// Images is how an image in a reply is shown. The values are ordered: a
// larger one shows more of the picture.
type Images uint8

const (
	// ImagesText is one line that names the image.
	ImagesText Images = iota + 1
	// ImagesBlocks is the picture drawn with half-block characters.
	ImagesBlocks
	// ImagesPixels is the picture itself, through the kitty graphics
	// protocol.
	ImagesPixels
)

// String is the name the settings use: "text", "blocks" or "pixels".
func (i Images) String() string {
	switch i {
	case ImagesText:
		return "text"
	case ImagesBlocks:
		return "blocks"
	case ImagesPixels:
		return "pixels"
	default:
		return unset
	}
}

// Width is how the terminal counts the cells a piece of text takes.
type Width uint8

const (
	// WidthWc is one character at a time, as wcwidth does.
	WidthWc Width = iota + 1
	// WidthGrapheme is one grapheme cluster at a time (mode 2027).
	WidthGrapheme
)

// String is "wcwidth" or "grapheme".
func (w Width) String() string {
	switch w {
	case WidthWc:
		return "wcwidth"
	case WidthGrapheme:
		return "grapheme"
	default:
		return unset
	}
}

// Plan is what the client asks the terminal when it starts.
type Plan uint8

const (
	// ProbeOff asks nothing.
	ProbeOff Plan = iota + 1
	// ProbeBasic asks for the background colour only.
	ProbeBasic
	// ProbeFull asks for the background, kitty graphics, the terminal's
	// name, the cell size and the device attributes.
	ProbeFull
	// ProbeTmux asks tmux, then the terminal behind it through passthrough.
	ProbeTmux
)

// String is "off", "basic", "full" or "tmux".
func (p Plan) String() string {
	switch p {
	case ProbeOff:
		return "off"
	case ProbeBasic:
		return "basic"
	case ProbeFull:
		return "full"
	case ProbeTmux:
		return "tmux"
	default:
		return unset
	}
}

// unset is how a value that was never set prints. Resolve never returns one.
const unset = "unset"

// Caps is what the terminal can do. It is comparable: a change is c != old.
type Caps struct {
	Color      Color
	Background Background
	Glyphs     Glyphs
	Images     Images
	Width      Width
	// CellW and CellH are the pixels per cell; 8x16 until the terminal says.
	CellW, CellH int
	// Tmux means graphics commands go through tmux passthrough.
	Tmux bool
	// ShiftEnter means key enhancements were reported, so shift+enter can
	// be told from enter.
	ShiftEnter bool
	// Terminal is the terminal's name in lower case: its XTVERSION answer,
	// or "kitty" or "ghostty" when only the environment says so, or "".
	Terminal string
	// Settled means probing is over: nothing that was asked is still awaited.
	Settled bool
}
