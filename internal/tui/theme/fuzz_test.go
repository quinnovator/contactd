package theme_test

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/quinnovator/contactd/internal/tui/safe"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
	"github.com/quinnovator/contactd/internal/tui/theme"
)

// fuzzTheme makes the theme a fuzz target draws with, or nil for a width
// that is not worth trying.
func fuzzTheme(w int, grapheme bool) *theme.Theme {
	if w < -2 || w > 200 {
		return nil
	}
	if grapheme {
		return withWidth(termcaps.WidthGrapheme)
	}
	return withWidth(termcaps.WidthWc)
}

// checkWidths checks what holds for any text at all: nothing the theme draws
// is wider than it was told, and a row is exactly as wide.
func checkWidths(t *testing.T, th *theme.Theme, s string, w int) {
	t.Helper()
	for _, line := range th.Wrap(s, w) {
		if th.Width(line) > max(w, 1) {
			t.Errorf("Wrap(%q, %d): line %q is %d cells wide", s, w, line, th.Width(line))
		}
	}
	for _, line := range th.Hardwrap(s, w) {
		if th.Width(line) > max(w, 1) {
			t.Errorf("Hardwrap(%q, %d): line %q is %d cells wide", s, w, line, th.Width(line))
		}
	}
	one := strings.ReplaceAll(s, "\n", " ")
	if got := th.Width(th.Truncate(one, w)); got > max(w, 0) {
		t.Errorf("Truncate(%q, %d) is %d cells wide", one, w, got)
	}
	rule, bar := th.Bar([]theme.Cell{{Text: "live", Tone: "ok"}, {Text: one}}, []theme.Cell{{Text: one, Tone: "dim"}}, w)
	if rule != "" {
		t.Errorf("a filled bar has a rule: %q", rule)
	}
	box := th.Box([]string{one}, w, one, one)
	if w >= 5 {
		// Both borders stand, whatever is between them.
		for _, row := range box[1 : len(box)-1] {
			if got := ansi.Strip(row); !strings.HasPrefix(got, th.G.BoxV+" ") || !strings.HasSuffix(got, " "+th.G.BoxV) {
				t.Errorf("%q in %d cells: box row %q lost a border", one, w, row)
			}
		}
	}
	rows := append(box,
		bar, th.Pad(one, w), th.Rule(w, one), th.Spread(one, one, w), th.Selected(one, w), th.CodeFill(one, w))
	for _, row := range rows {
		if got := th.Width(row); got != max(w, 0) {
			t.Errorf("%q in %d cells: row %q is %d cells wide", one, w, row, got)
		}
	}
	// A control character left in a row would move the terminal's cursor
	// where the measure counted nothing.
	rows = append(rows, th.Truncate(one, w))
	rows = append(rows, th.Wrap(s, w)...)
	rows = append(rows, th.Hardwrap(s, w)...)
	for _, row := range rows {
		if controls(row) {
			t.Errorf("%q in %d cells: row %q holds a control character", s, w, row)
		}
	}
}

// FuzzText is text as it reaches the theme: made safe, then styled by the
// client. Its right edge holds, and its styles are kept.
func FuzzText(f *testing.F) {
	for _, s := range samples() {
		f.Add(s, 12, false)
		f.Add(s, 3, true)
	}
	f.Add("a\x1b]8;;http://x\x07b\x1b[31mc\x9b0md\r\n\u202e", 5, true)
	f.Fuzz(func(t *testing.T, s string, w int, grapheme bool) {
		th := fuzzTheme(w, grapheme)
		if th == nil {
			return
		}
		s = safe.Clean(s, !grapheme)
		checkWidths(t, th, s, w)

		const on = "\x1b[1;38;2;94;196;200m"
		styled := th.Style("accent").Render("> ") + s
		checkWidths(t, th, styled, w)
		if w < 1 {
			return
		}
		one := strings.ReplaceAll(styled, "\n", " ")
		for _, row := range []string{
			th.Box([]string{one}, w, "", "")[1], th.Pad(one, w), th.Truncate(one, w), th.Selected(one, w),
			th.Wrap(styled, w)[0], th.Hardwrap(styled, w)[0],
		} {
			if !strings.Contains(row, on) {
				t.Errorf("%q in %d cells: %q lost its style", one, w, row)
			}
		}
	})
}

// FuzzRaw is any bytes at all: broken escape sequences, bytes that are not
// UTF-8, control characters. Nothing like it should reach the theme, and if
// it does the right edge still holds.
func FuzzRaw(f *testing.F) {
	for _, s := range samples() {
		f.Add(s, 12, false)
		f.Add(s, 3, true)
	}
	f.Add("a\x1b]8;;http://x\x07b\x1b[31mc\x9b0md\r\n\u202e", 5, true)
	f.Add("\x1b[31", 5, true)
	f.Add("\x1bP+q\x1b\\abc\x1b_Gi=1;AAAA\x1b\\def", 4, false)
	f.Add("00000\xe6\xe5", 9, false)
	f.Add("\x1b[00m0\xf0", -1, true)
	f.Fuzz(func(t *testing.T, s string, w int, grapheme bool) {
		if th := fuzzTheme(w, grapheme); th != nil {
			checkWidths(t, th, s, w)
		}
	})
}
