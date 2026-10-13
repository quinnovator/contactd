package safe_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/quinnovator/contactd/internal/tui/safe"
)

// harmless reports what is wrong with text that claims to be safe to draw,
// or "" when nothing is.
func harmless(s string) string {
	if !utf8.ValidString(s) {
		return "not valid UTF-8"
	}
	if strings.IndexByte(s, 0x1B) >= 0 {
		return "contains ESC"
	}
	for _, r := range s {
		switch {
		case r == '\n':
		case r < 0x20:
			return "contains a C0 control"
		case r == 0x7F:
			return "contains DEL"
		case r >= 0x80 && r <= 0x9F:
			return "contains a C1 control"
		case r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069:
			return "contains a bidi override or isolate"
		case r == 0x10EEEE:
			return "contains the kitty placeholder"
		}
	}
	return ""
}

type cleanCase struct {
	name    string
	in      string
	joiners bool // dropJoiners
	want    string
}

func cleanCases() []cleanCase {
	return []cleanCase{
		// What stays.
		{name: "empty", in: "", want: ""},
		{name: "ascii", in: "p99 peaked on Thursday at 7.2 s.", want: "p99 peaked on Thursday at 7.2 s."},
		{name: "newline kept", in: "one\ntwo\n", want: "one\ntwo\n"},
		{name: "accents, CJK and emoji kept", in: "café 世界 😀 ⏺ ❯", want: "café 世界 😀 ⏺ ❯"},
		{name: "a written U+FFFD kept", in: "a\uFFFDb", want: "a\uFFFDb"},
		{name: "direction marks are not overrides", in: "a\u200E\u200Fb", want: "a\u200E\u200Fb"},
		{name: "U+2065 and U+206A sit outside the isolates", in: "\u2065\u206A", want: "\u2065\u206A"},
		{name: "U+00A0 sits outside C1", in: "a\u00A0b", want: "a\u00A0b"},
		{name: "U+2029 and U+202F sit outside the overrides", in: "\u2029\u202F", want: "\u2029\u202F"},
		{name: "U+007E and U+0020 sit outside the controls", in: " ~", want: " ~"},
		{name: "U+10EEED and U+10EEEF are not the placeholder", in: "\U0010EEED\U0010EEEF", want: "\U0010EEED\U0010EEEF"},

		// Line ends and tabs.
		{name: "CRLF becomes LF", in: "one\r\ntwo\r\n", want: "one\ntwo\n"},
		{name: "lone CR dropped", in: "10%\r20%", want: "10%20%"},
		{name: "tab is four spaces", in: "a\tb", want: "a    b"},
		{name: "tabs at the start", in: "\t\tx", want: "        x"},

		// Every escape class: the introducer and the terminator go, the
		// printable rest is shown.
		{name: "CSI colour", in: "\x1b[31mred\x1b[0m", want: "[31mred[0m"},
		{name: "CSI erase and home", in: "\x1b[2J\x1b[H", want: "[2J[H"},
		{name: "CSI private mode", in: "\x1b[?1049h\x1b[?25l", want: "[?1049h[?25l"},
		{name: "CSI cut short", in: "tail\x1b[38;5;", want: "tail[38;5;"},
		{name: "OSC title ended by BEL", in: "\x1b]0;owned\x07after", want: "]0;ownedafter"},
		{name: "OSC link ended by ST", in: "\x1b]8;;http://x\x1b\\link\x1b]8;;\x1b\\", want: "]8;;http://x\\link]8;;\\"},
		{name: "OSC unterminated", in: "ok\x1b]52;c;aGk=", want: "ok]52;c;aGk="},
		{name: "OSC 1337", in: "\x1b]1337;File=inline=1:AAAA\x07", want: "]1337;File=inline=1:AAAA"},
		{name: "DCS sixel", in: "\x1bPq#0;2;0;0;0~\x1b\\", want: "Pq#0;2;0;0;0~\\"},
		{name: "DCS unterminated", in: "\x1bP+q544e", want: "P+q544e"},
		{name: "DCS tmux passthrough", in: "\x1bPtmux;\x1b\x1b[2J\x1b\\", want: "Ptmux;[2J\\"},
		{name: "APC kitty graphics", in: "\x1b_Ga=T,f=24,s=1,v=1;AAAA\x1b\\", want: "_Ga=T,f=24,s=1,v=1;AAAA\\"},
		{name: "APC unterminated", in: "\x1b_Ga=d", want: "_Ga=d"},
		{name: "PM", in: "\x1b^private\x1b\\", want: "^private\\"},
		{name: "SOS", in: "\x1bXstring\x1b\\", want: "Xstring\\"},
		{name: "SS3", in: "\x1bOP", want: "OP"},
		{name: "reset", in: "\x1bc", want: "c"},
		{name: "save and restore cursor", in: "\x1b7x\x1b8", want: "7x8"},
		{name: "charset switch", in: "\x1b(0qqq\x1b(B", want: "(0qqq(B"},
		{name: "lone ESC at the end", in: "abc\x1b", want: "abc"},
		{name: "lone ESC alone", in: "\x1b", want: ""},
		{name: "ESC ESC", in: "\x1b\x1b[A", want: "[A"},

		// C0 and DEL.
		{name: "NUL", in: "a\x00b", want: "ab"},
		{name: "BEL", in: "a\x07b", want: "ab"},
		{name: "backspace", in: "secret\x08\x08\x08\x08\x08\x08public", want: "secretpublic"},
		{name: "vertical tab and form feed", in: "a\x0b\x0cb", want: "ab"},
		{name: "shift out and in", in: "a\x0eb\x0fc", want: "abc"},
		{name: "CAN and SUB", in: "a\x18\x1ab", want: "ab"},
		{name: "DEL", in: "a\x7fb", want: "ab"},
		{name: "every C0: the tab and the LF are what is left", in: c0(), want: "    \n"},

		// C1, written as UTF-8 and as bare bytes.
		{name: "C1 CSI", in: "\u009b31mred", want: "31mred"},
		{name: "C1 OSC and ST", in: "\u009d0;title\u009c", want: "0;title"},
		{name: "C1 DCS", in: "\u0090q\u009c", want: "q"},
		{name: "C1 APC", in: "\u009fGa=d\u009c", want: "Ga=d"},
		{name: "C1 NEL", in: "a\u0085b", want: "ab"},
		{name: "every C1", in: c1(), want: ""},
		{name: "a bare 0x9B byte", in: "\x9b31m", want: "\uFFFD31m"},
		{name: "a bare 0x9D byte and 0x9C", in: "\x9d0;t\x9c", want: "\uFFFD0;t\uFFFD"},
		{name: "a bare 0xFF byte", in: "a\xffb", want: "a\uFFFDb"},
		{name: "a rune cut short", in: "ok\xe2\x82", want: "ok\uFFFD\uFFFD"},

		// Bidi.
		{name: "right-to-left override", in: "red\u202E alert", want: "red alert"},
		{name: "every override", in: "\u202A\u202B\u202C\u202D\u202E", want: ""},
		{name: "every isolate", in: "a\u2066\u2067\u2068\u2069b", want: "ab"},

		// The kitty placeholder.
		{name: "U+10EEEE", in: "a\U0010EEEEb", want: "ab"},
		{name: "U+10EEEE keeps no cell: its diacritics fall on what is before", in: "x\U0010EEEE\u0305\u030D", want: "x\u0305\u030D"},
		{name: "U+10EEEE with joiners dropped", in: "\U0010EEEE", joiners: true, want: ""},

		// Joiners.
		{name: "ZWJ kept by default", in: "👨\u200D👩\u200D👧", want: "👨\u200D👩\u200D👧"},
		{name: "VS16 kept by default", in: "❤\uFE0F", want: "❤\uFE0F"},
		{name: "ZWJ dropped on request", in: "👨\u200D👩\u200D👧", joiners: true, want: "👨👩👧"},
		{name: "VS16 dropped on request", in: "❤\uFE0F ⏺\uFE0F", joiners: true, want: "❤ ⏺"},
		{name: "VS15 is not VS16", in: "❤\uFE0E", joiners: true, want: "❤\uFE0E"},
		{name: "the rest still applies with joiners dropped", in: "\x1b[1m\u200Dx\r\n\ty", joiners: true, want: "[1mx\n    y"},

		// A change late in the text keeps what came before it.
		{name: "clean prefix kept", in: "café 世界 then\x1b[0m end", want: "café 世界 then[0m end"},
	}
}

// c0 is every C0 control in order.
func c0() string {
	var b strings.Builder
	for r := range rune(0x20) {
		b.WriteRune(r)
	}
	return b.String()
}

// c1 is every C1 control in order, as UTF-8.
func c1() string {
	var b strings.Builder
	for r := rune(0x80); r <= 0x9F; r++ {
		b.WriteRune(r)
	}
	return b.String()
}

// Every string of two bytes, which covers every C0 and C1 control, alone,
// after any byte and before any byte, as UTF-8 and as bare bytes. The second
// half checks that cleaning the two bytes apart and joining the results is
// as safe as cleaning them together: a caller may cut text anywhere.
func TestEveryTwoBytes(t *testing.T) {
	t.Parallel()
	for a := range 256 {
		for b := range 256 {
			s := string([]byte{byte(a), byte(b)})
			for _, flag := range []bool{false, true} {
				whole := safe.Clean(s, flag)
				if why := harmless(whole); why != "" {
					t.Fatalf("Clean(%q, %v) = %q: %s", s, flag, whole, why)
				}
				apart := safe.Clean(s[:1], flag) + safe.Clean(s[1:], flag)
				if why := harmless(apart); why != "" {
					t.Fatalf("Clean(%q, %v) + Clean(%q, %v) = %q: %s", s[:1], flag, s[1:], flag, apart, why)
				}
				line := safe.Line(s, 8, flag)
				if why := oneLine(line, 8, flag); why != "" {
					t.Fatalf("Line(%q, 8, %v) = %q: %s", s, flag, line, why)
				}
				apart = safe.Line(s[:1], 8, flag) + safe.Line(s[1:], 8, flag)
				if why := oneLine(apart, 16, flag); why != "" {
					t.Fatalf("Line(%q, 8, %v) + Line(%q, 8, %v) = %q: %s", s[:1], flag, s[1:], flag, apart, why)
				}
			}
		}
	}
}

// Every character that must never be drawn, each given a neighbour on both
// sides, in the encodings a hostile sender can choose from.
func TestNothingForbiddenGetsThrough(t *testing.T) {
	t.Parallel()
	var forbidden []rune
	for r := range rune(0x20) {
		if r != '\n' && r != '\t' {
			forbidden = append(forbidden, r)
		}
	}
	for r := rune(0x7F); r <= 0x9F; r++ {
		forbidden = append(forbidden, r)
	}
	forbidden = append(forbidden, 0x202A, 0x202B, 0x202C, 0x202D, 0x202E, 0x2066, 0x2067, 0x2068, 0x2069, 0x10EEEE)
	for _, r := range forbidden {
		in := "a" + string(r) + "b"
		for _, flag := range []bool{false, true} {
			if got := safe.Clean(in, flag); got != "ab" {
				t.Errorf("Clean(%q, %v) = %q, want ab", in, flag, got)
			}
			if got := safe.Line(in, 8, flag); got != "ab" {
				t.Errorf("Line(%q, 8, %v) = %q, want ab", in, flag, got)
			}
		}
		// The same character with its last byte cut off, with a byte too
		// many, and in the overlong form a lenient decoder would accept.
		enc := string(r)
		for _, in := range []string{enc[:len(enc)-1] + "b", enc + "\x80", "\xC2" + enc, overlong(r)} {
			for _, flag := range []bool{false, true} {
				if why := harmless(safe.Clean(in, flag)); why != "" {
					t.Errorf("Clean(%q, %v) = %q: %s", in, flag, safe.Clean(in, flag), why)
				}
				if why := oneLine(safe.Line(in, 8, flag), 8, flag); why != "" {
					t.Errorf("Line(%q, 8, %v) = %q: %s", in, flag, safe.Line(in, 8, flag), why)
				}
			}
		}
	}
}

// overlong encodes a character in one byte more than UTF-8 allows. A decoder
// that accepted it would see the character; Go's does not.
func overlong(r rune) string {
	tail := func(shift int) byte { return 0x80 | byte(r>>shift)&0x3F }
	switch {
	case r < 0x80:
		return string([]byte{0xC0 | byte(r>>6), tail(0)})
	case r < 0x800:
		return string([]byte{0xE0, tail(6), tail(0)})
	case r < 0x10000:
		return string([]byte{0xF0, tail(12), tail(6), tail(0)})
	default:
		return string([]byte{0xF8, tail(18), tail(12), tail(6), tail(0)})
	}
}

func TestClean(t *testing.T) {
	t.Parallel()
	for _, tc := range cleanCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := safe.Clean(tc.in, tc.joiners)
			if got != tc.want {
				t.Errorf("Clean(%q, %v) = %q, want %q", tc.in, tc.joiners, got, tc.want)
			}
			if why := harmless(got); why != "" {
				t.Errorf("Clean(%q, %v) = %q: %s", tc.in, tc.joiners, got, why)
			}
			if again := safe.Clean(got, tc.joiners); again != got {
				t.Errorf("Clean is not stable: %q became %q", got, again)
			}
		})
	}
}

type lineCase struct {
	name  string
	in    string
	max   int
	ascii bool
	want  string
}

func lineCases() []lineCase {
	return []lineCase{
		{name: "plain", in: "cache 82%", max: 64, want: "cache 82%"},
		{name: "newline becomes a space", in: "one\ntwo", max: 64, want: "one two"},
		{name: "CRLF becomes one space", in: "one\r\ntwo", max: 64, want: "one two"},
		{name: "a trailing newline is dropped", in: "main\n", max: 64, want: "main"},
		{name: "a trailing CRLF is dropped", in: "main\r\n", max: 64, want: "main"},
		{name: "every trailing newline is dropped", in: "one\ntwo\n\n\n", max: 64, want: "one two"},
		{name: "a leading newline is a space", in: "\nmain", max: 64, want: " main"},
		{name: "newlines alone are nothing", in: "\n\r\n\n", max: 64, want: ""},
		{name: "a trailing space is kept", in: "main \n", max: 64, want: "main "},
		{name: "spaces around a span are kept", in: "  14 calls  ", max: 64, want: "  14 calls  "},
		{name: "a newline before the cut is a space", in: "ab\ncd", max: 3, want: "ab "},
		{name: "U+2028 and U+2029 are newlines", in: "one\u2028two\u2029three", max: 64, want: "one two three"},
		{name: "U+2028 and U+2029 are newlines with ascii", in: "one\u2028two\u2029", max: 64, ascii: true, want: "one two"},
		{name: "a trailing U+2028 is dropped", in: "main\u2028\n\u2029", max: 64, want: "main"},
		{name: "tab is four spaces", in: "a\tb", max: 64, want: "a    b"},
		{name: "cut to the limit", in: "deepseek-flash", max: 8, want: "deepseek"},
		{name: "the limit counts runes, not bytes", in: "日本語テキスト", max: 2, want: "日本"},
		{name: "the limit counts a tab as four", in: "a\tb", max: 3, want: "a  "},
		{name: "the limit counts what is left, not what was dropped", in: "\x1b\x1b\x1b\x1babc", max: 2, want: "ab"},
		{name: "exactly the limit", in: "abc", max: 3, want: "abc"},
		{name: "limit of zero", in: "abc", max: 0, want: ""},
		{name: "negative limit", in: "abc", max: -1, want: ""},
		{name: "unicode kept", in: "⎇ main · café", max: 64, want: "⎇ main · café"},
		{name: "ascii folds each rune above 0x7F", in: "⎇ main · café", max: 64, ascii: true, want: "? main ? caf?"},
		{name: "ascii folds a wide rune to one mark", in: "世界", max: 64, ascii: true, want: "??"},
		{name: "ascii folds a bad byte", in: "a\xffb", max: 64, ascii: true, want: "a?b"},
		{name: "a bad byte without ascii", in: "a\xffb", max: 64, want: "a\uFFFDb"},
		{name: "ascii leaves ASCII alone", in: "* live | 7.2s", max: 64, ascii: true, want: "* live | 7.2s"},
		{name: "joiners always dropped", in: "❤\uFE0F 👨\u200D👩", max: 64, want: "❤ 👨👩"},
		{name: "CSI", in: "\x1b[31mred\x1b[0m", max: 64, want: "[31mred[0m"},
		{name: "unterminated OSC from a mod", in: "ok\x1b]52;c;aGk=", max: 64, want: "ok]52;c;aGk="},
		{name: "unterminated APC", in: "\x1b_Ga=d", max: 64, want: "_Ga=d"},
		{name: "unterminated DCS", in: "\x1bPtmux;", max: 64, want: "Ptmux;"},
		{name: "lone ESC", in: "\x1b", max: 64, want: ""},
		{name: "C1 CSI", in: "\u009b2J", max: 64, want: "2J"},
		{name: "C1 with ascii", in: "\u009b2J\u009c", max: 64, ascii: true, want: "2J"},
		{name: "bidi", in: "a\u202Eb\u2066c", max: 64, want: "abc"},
		{name: "placeholder", in: "\U0010EEEE\u0305x", max: 64, ascii: true, want: "?x"},
		{name: "BEL and NUL", in: "a\x07\x00b", max: 64, want: "ab"},
	}
}

func TestLine(t *testing.T) {
	t.Parallel()
	for _, tc := range lineCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := safe.Line(tc.in, tc.max, tc.ascii)
			if got != tc.want {
				t.Errorf("Line(%q, %d, %v) = %q, want %q", tc.in, tc.max, tc.ascii, got, tc.want)
			}
			if why := oneLine(got, tc.max, tc.ascii); why != "" {
				t.Errorf("Line(%q, %d, %v) = %q: %s", tc.in, tc.max, tc.ascii, got, why)
			}
		})
	}
}

// oneLine reports what is wrong with a result of Line, or "".
func oneLine(s string, maxRunes int, ascii bool) string {
	if why := harmless(s); why != "" {
		return why
	}
	if strings.ContainsAny(s, "\n\u2028\u2029") {
		return "contains a newline"
	}
	if strings.ContainsAny(s, "\u200D\uFE0F") {
		return "contains a joiner"
	}
	if n := utf8.RuneCountInString(s); n > max(maxRunes, 0) {
		return "longer than the limit"
	}
	if ascii {
		for i := range len(s) {
			if s[i] >= 0x80 {
				return "contains a byte above 0x7F"
			}
		}
	}
	// C1 as a byte sequence, looked for in the bytes themselves.
	for i := 0; i+1 < len(s); i++ {
		if s[i] == 0xC2 && s[i+1] >= 0x80 && s[i+1] <= 0x9F {
			return "contains a C1 byte sequence"
		}
	}
	return ""
}

// seeds are inputs worth starting a fuzz run from.
func seeds() []string {
	first := []string{
		"\x1b]0;owned\x07\x1b[2J\x1b[31mred\u202E alert\r\x00",
		"\xc2", "\xc2\x9b", "\x9b", "\xe2\x80", "\xf4\x8e\xbb", "\xf4\x8e\xbb\xae",
		"\xc2\xc2\x9b\x9b", "\xed\xa0\x80", "\xc0\x9b", "\xe0\x80\x9b",
	}
	clean, line := cleanCases(), lineCases()
	out := make([]string, 0, len(first)+len(clean)+len(line))
	out = append(out, first...)
	for _, tc := range clean {
		out = append(out, tc.in)
	}
	for _, tc := range line {
		out = append(out, tc.in)
	}
	return out
}

func FuzzLine(f *testing.F) {
	for _, s := range seeds() {
		f.Add(s, 64, false)
		f.Add(s, 5, true)
	}
	f.Fuzz(func(t *testing.T, s string, maxRunes int, ascii bool) {
		got := safe.Line(s, maxRunes, ascii)
		if why := oneLine(got, maxRunes, ascii); why != "" {
			t.Errorf("Line(%q, %d, %v) = %q: %s", s, maxRunes, ascii, got, why)
		}
		// A line that is already safe and short enough is left alone.
		if again := safe.Line(got, maxRunes, ascii); again != got {
			t.Errorf("Line is not stable: %q became %q", got, again)
		}
	})
}

func FuzzClean(f *testing.F) {
	for _, s := range seeds() {
		f.Add(s, false)
		f.Add(s, true)
	}
	f.Fuzz(func(t *testing.T, s string, dropJoiners bool) {
		got := safe.Clean(s, dropJoiners)
		if why := harmless(got); why != "" {
			t.Errorf("Clean(%q, %v) = %q: %s", s, dropJoiners, got, why)
		}
		if strings.ContainsRune(got, '\t') {
			t.Errorf("Clean(%q, %v) = %q: contains a tab", s, dropJoiners, got)
		}
		if dropJoiners && strings.ContainsAny(got, "\u200D\uFE0F") {
			t.Errorf("Clean(%q, true) = %q: contains a joiner", s, got)
		}
		if again := safe.Clean(got, dropJoiners); again != got {
			t.Errorf("Clean is not stable: %q became %q", got, again)
		}
		// Nothing printable is lost: every rune that is kept as it is
		// comes out in order.
		if want := kept(s, dropJoiners); strings.ReplaceAll(got, " ", "") != want {
			t.Errorf("Clean(%q, %v) = %q: the printable text should be %q", s, dropJoiners, got, want)
		}
	})
}

// kept is a second, slower statement of what Clean keeps, with spaces left
// out so that a tab's four cannot be told from text.
func kept(s string, dropJoiners bool) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == ' ', r == '\t':
		case r == '\n':
			b.WriteRune(r)
		case r < 0x20, r >= 0x7F && r <= 0x9F:
		case r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069, r == 0x10EEEE:
		case dropJoiners && (r == 0x200D || r == 0xFE0F):
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
