package frame_test

import (
	"fmt"
	"testing"

	"github.com/quinnovator/contactd/internal/tui/frame"
)

// counts lists the row counts of a plan, with the names to report them by.
func counts(r frame.Rows) (names []string, rows []int) {
	return []string{"Header", "HeaderRule", "Body", "Palette", "BarRule", "Bar", "Input", "Hint", "InputText"},
		[]int{r.Header, r.HeaderRule, r.Body, r.Palette, r.BarRule, r.Bar, r.Input, r.Hint, r.InputText}
}

// checkPlan returns what is wrong with the plan Layout made for a terminal of
// three rows or more, or "" when nothing is. It states the rules of the
// layout a second time, apart from the code that follows them.
func checkPlan(r frame.Rows, width, height, lines, want int, filled bool) string {
	names, rows := counts(r)
	for i, n := range rows {
		if n < 0 {
			return fmt.Sprintf("%s is %d", names[i], n)
		}
	}
	sum := r.Header + r.HeaderRule + r.Body + r.Palette + r.BarRule + r.Bar + r.Input + r.Hint
	one := func(shown bool) int {
		if shown {
			return 1
		}
		return 0
	}
	boxed := width >= 40 && height >= 16
	text := min(max(lines, 1), min(8, max(1, (height-6)/3)))
	switch {
	case sum != height:
		return fmt.Sprintf("the rows sum to %d", sum)
	case r.Height() != sum:
		return fmt.Sprintf("Height is %d and the rows sum to %d", r.Height(), sum)
	case r.Body < 1:
		return "no transcript row"
	case r.InputText < 1:
		return "no input text row"
	case r.InputText != text:
		return fmt.Sprintf("InputText is %d, want %d", r.InputText, text)
	case r.Boxed != boxed || r.Boxed != frame.BoxedAt(width, height):
		return fmt.Sprintf("Boxed is %t", r.Boxed)
	case r.Input != r.InputText+2*one(boxed):
		return fmt.Sprintf("Input is %d around %d rows of text", r.Input, r.InputText)
	case r.Header != one(height >= 6):
		return fmt.Sprintf("Header is %d", r.Header)
	case r.HeaderRule != one(height >= 8):
		return fmt.Sprintf("HeaderRule is %d", r.HeaderRule)
	case r.BarRule != one(!filled && height >= 8):
		return fmt.Sprintf("BarRule is %d", r.BarRule)
	case r.Bar != 1:
		return fmt.Sprintf("Bar is %d", r.Bar)
	case r.Hint != one(height >= 16):
		return fmt.Sprintf("Hint is %d", r.Hint)
	case r.Palette > max(want, 0):
		return fmt.Sprintf("Palette is %d, more than the %d it wants", r.Palette, want)
	case r.Palette > 0 && r.Body < 3:
		return fmt.Sprintf("the palette has %d rows and leaves the transcript %d", r.Palette, r.Body)
	case r.Palette < max(want, 0) && r.Body > 3:
		return fmt.Sprintf("the palette has %d of %d rows while the transcript has %d", r.Palette, want, r.Body)
	case r.InputTop() != sum-r.Input-r.Hint:
		return fmt.Sprintf("InputTop is %d", r.InputTop())
	}
	return ""
}

// Every size from 1 to 300 columns and 3 to 100 rows, with short and long
// drafts, the palette closed, open and greedy, and both kinds of bar.
func TestLayoutProperty(t *testing.T) {
	t.Parallel()
	drafts := []int{-2, 0, 1, 2, 3, 5, 8, 9, 50}
	wants := []int{-1, 0, 1, 5, 11, 200}
	for _, filled := range []bool{true, false} {
		t.Run(fmt.Sprintf("filled=%t", filled), func(t *testing.T) {
			t.Parallel()
			for width := 1; width <= 300; width++ {
				for height := 3; height <= 100; height++ {
					for _, lines := range drafts {
						for _, want := range wants {
							r := frame.Layout(width, height, lines, want, filled)
							if msg := checkPlan(r, width, height, lines, want, filled); msg != "" {
								t.Fatalf("Layout(%d, %d, %d, %d, %t) = %+v: %s", width, height, lines, want, filled, r, msg)
							}
						}
					}
				}
			}
		})
	}
}

// Every draft from 1 to 50 lines against every palette of 0 to 20 rows, at
// every height and on both sides of the width that gives the input its box.
func TestLayoutEveryDraftAndPalette(t *testing.T) {
	t.Parallel()
	for _, filled := range []bool{true, false} {
		t.Run(fmt.Sprintf("filled=%t", filled), func(t *testing.T) {
			t.Parallel()
			for _, width := range []int{1, 30, 39, 40, 41, 100, 300} {
				for height := 3; height <= 100; height++ {
					for lines := 1; lines <= 50; lines++ {
						for want := range 21 {
							r := frame.Layout(width, height, lines, want, filled)
							if msg := checkPlan(r, width, height, lines, want, filled); msg != "" {
								t.Fatalf("Layout(%d, %d, %d, %d, %t) = %+v: %s", width, height, lines, want, filled, r, msg)
							}
						}
					}
				}
			}
		})
	}
}

// Under three rows the plan promises less, but it never has a negative count
// and never more rows than the terminal.
func TestLayoutTooSmall(t *testing.T) {
	t.Parallel()
	tests := []struct {
		height int
		want   frame.Rows
	}{
		{2, frame.Rows{Body: 1, Input: 1, InputText: 1}},
		{1, frame.Rows{Input: 1, InputText: 1}},
		{0, frame.Rows{}},
		{-1, frame.Rows{}},
		{-100, frame.Rows{}},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("height %d", tt.height), func(t *testing.T) {
			t.Parallel()
			for _, width := range []int{-5, 0, 1, 39, 40, 100, 300} {
				for _, lines := range []int{-1, 0, 1, 9} {
					for _, want := range []int{-1, 0, 11} {
						for _, filled := range []bool{true, false} {
							got := frame.Layout(width, tt.height, lines, want, filled)
							if got != tt.want {
								t.Errorf("Layout(%d, %d, %d, %d, %t) = %+v, want %+v",
									width, tt.height, lines, want, filled, got, tt.want)
							}
							if got.Height() != max(tt.height, 0) {
								t.Errorf("Layout(%d, %d, ...) has %d rows", width, tt.height, got.Height())
							}
						}
					}
				}
			}
		})
	}
}

// A width under one is a terminal too narrow for a box, and nothing else.
func TestLayoutNoWidth(t *testing.T) {
	t.Parallel()
	for _, width := range []int{0, -1, -400} {
		for height := 3; height <= 100; height++ {
			r := frame.Layout(width, height, 4, 11, false)
			if msg := checkPlan(r, width, height, 4, 11, false); msg != "" {
				t.Fatalf("Layout(%d, %d, 4, 11, false) = %+v: %s", width, height, r, msg)
			}
		}
	}
}

// The screens the mock-ups draw.
func TestLayoutAsDrawn(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		width, height int
		filled        bool
		want          frame.Rows
	}{
		{
			"Frame at 100x30: 1 + 1 + 23 + 1 + 3 + 1", 100, 30, true,
			frame.Rows{Header: 1, HeaderRule: 1, Body: 23, Bar: 1, Input: 3, Hint: 1, Boxed: true, InputText: 1},
		},
		{
			"Compatibility at 46x12 with a filled bar: no box, no hint", 46, 12, true,
			frame.Rows{Header: 1, HeaderRule: 1, Body: 8, Bar: 1, Input: 1, InputText: 1},
		},
		{
			"Compatibility at 46x12 with a rule above the bar", 46, 12, false,
			frame.Rows{Header: 1, HeaderRule: 1, Body: 7, BarRule: 1, Bar: 1, Input: 1, InputText: 1},
		},
		{
			"Compatibility at 30x12", 30, 12, true,
			frame.Rows{Header: 1, HeaderRule: 1, Body: 8, Bar: 1, Input: 1, InputText: 1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := frame.Layout(tt.width, tt.height, 1, 0, tt.filled)
			if got != tt.want {
				t.Errorf("Layout(%d, %d, 1, 0, %t) = %+v, want %+v", tt.width, tt.height, tt.filled, got, tt.want)
			}
		})
	}
}

// Each rule of the layout, on both sides of the size it turns on.
func TestLayoutLadder(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		width, height int
		lines, want   int
		filled        bool
		rows          frame.Rows
	}{
		{
			"three rows: transcript, bar and input", 80, 3, 1, 0, true,
			frame.Rows{Body: 1, Bar: 1, Input: 1, InputText: 1},
		},
		{
			"no header at 5 rows", 80, 5, 1, 0, true,
			frame.Rows{Body: 3, Bar: 1, Input: 1, InputText: 1},
		},
		{
			"the header from 6 rows", 80, 6, 1, 0, true,
			frame.Rows{Header: 1, Body: 3, Bar: 1, Input: 1, InputText: 1},
		},
		{
			"no header rule at 7 rows", 80, 7, 1, 0, true,
			frame.Rows{Header: 1, Body: 4, Bar: 1, Input: 1, InputText: 1},
		},
		{
			"the header rule from 8 rows", 80, 8, 1, 0, true,
			frame.Rows{Header: 1, HeaderRule: 1, Body: 4, Bar: 1, Input: 1, InputText: 1},
		},
		{
			"no bar rule at 7 rows, fill or none", 80, 7, 1, 0, false,
			frame.Rows{Header: 1, Body: 4, Bar: 1, Input: 1, InputText: 1},
		},
		{
			"the bar rule from 8 rows where the bar has no fill", 80, 8, 1, 0, false,
			frame.Rows{Header: 1, HeaderRule: 1, Body: 3, BarRule: 1, Bar: 1, Input: 1, InputText: 1},
		},
		{
			"no box and no hint at 15 rows", 40, 15, 1, 0, true,
			frame.Rows{Header: 1, HeaderRule: 1, Body: 11, Bar: 1, Input: 1, InputText: 1},
		},
		{
			"the box and the hint from 16 rows", 40, 16, 1, 0, true,
			frame.Rows{Header: 1, HeaderRule: 1, Body: 9, Bar: 1, Input: 3, Hint: 1, Boxed: true, InputText: 1},
		},
		{
			"the hint and no box at 39 columns", 39, 16, 1, 0, true,
			frame.Rows{Header: 1, HeaderRule: 1, Body: 11, Bar: 1, Input: 1, Hint: 1, InputText: 1},
		},
		{
			"no box at 39 columns, however tall", 39, 100, 1, 0, true,
			frame.Rows{Header: 1, HeaderRule: 1, Body: 95, Bar: 1, Input: 1, Hint: 1, InputText: 1},
		},
		{
			"a wide terminal of 15 rows has no box", 300, 15, 1, 0, false,
			frame.Rows{Header: 1, HeaderRule: 1, Body: 10, BarRule: 1, Bar: 1, Input: 1, InputText: 1},
		},
		{
			"a draft of 5 lines is shown whole at 30 rows", 100, 30, 5, 0, true,
			frame.Rows{Header: 1, HeaderRule: 1, Body: 19, Bar: 1, Input: 7, Hint: 1, Boxed: true, InputText: 5},
		},
		{
			"a draft of 20 lines gets 8 rows at 30", 100, 30, 20, 0, true,
			frame.Rows{Header: 1, HeaderRule: 1, Body: 16, Bar: 1, Input: 10, Hint: 1, Boxed: true, InputText: 8},
		},
		{
			"and never more than 8", 100, 100, 20, 0, true,
			frame.Rows{Header: 1, HeaderRule: 1, Body: 86, Bar: 1, Input: 10, Hint: 1, Boxed: true, InputText: 8},
		},
		{
			"7 rows of text at 29: (29-6)/3", 100, 29, 20, 0, true,
			frame.Rows{Header: 1, HeaderRule: 1, Body: 16, Bar: 1, Input: 9, Hint: 1, Boxed: true, InputText: 7},
		},
		{
			"3 rows of text at 16", 100, 16, 20, 0, true,
			frame.Rows{Header: 1, HeaderRule: 1, Body: 7, Bar: 1, Input: 5, Hint: 1, Boxed: true, InputText: 3},
		},
		{
			"2 rows of text at 12", 46, 12, 20, 0, true,
			frame.Rows{Header: 1, HeaderRule: 1, Body: 7, Bar: 1, Input: 2, InputText: 2},
		},
		{
			"1 row of text at 11", 46, 11, 20, 0, true,
			frame.Rows{Header: 1, HeaderRule: 1, Body: 7, Bar: 1, Input: 1, InputText: 1},
		},
		{
			"1 row of text under 6 rows", 46, 4, 20, 0, true,
			frame.Rows{Body: 2, Bar: 1, Input: 1, InputText: 1},
		},
		{
			"an empty draft is one row", 100, 30, 0, 0, true,
			frame.Rows{Header: 1, HeaderRule: 1, Body: 23, Bar: 1, Input: 3, Hint: 1, Boxed: true, InputText: 1},
		},
		{
			"the palette gets what it wants", 100, 30, 1, 11, true,
			frame.Rows{Header: 1, HeaderRule: 1, Body: 12, Palette: 11, Bar: 1, Input: 3, Hint: 1, Boxed: true, InputText: 1},
		},
		{
			"less whatever keeps 3 rows of transcript", 100, 30, 1, 40, true,
			frame.Rows{Header: 1, HeaderRule: 1, Body: 3, Palette: 20, Bar: 1, Input: 3, Hint: 1, Boxed: true, InputText: 1},
		},
		{
			"the palette at 46x12", 46, 12, 1, 11, true,
			frame.Rows{Header: 1, HeaderRule: 1, Body: 3, Palette: 5, Bar: 1, Input: 1, InputText: 1},
		},
		{
			"the palette at 46x12 with a bar rule", 46, 12, 1, 11, false,
			frame.Rows{Header: 1, HeaderRule: 1, Body: 3, Palette: 4, BarRule: 1, Bar: 1, Input: 1, InputText: 1},
		},
		{
			"the palette and a long draft share a short screen", 100, 16, 3, 11, false,
			frame.Rows{
				Header: 1, HeaderRule: 1, Body: 3, Palette: 3, BarRule: 1, Bar: 1, Input: 5, Hint: 1,
				Boxed: true, InputText: 3,
			},
		},
		{
			"no palette where the transcript has 3 rows", 80, 5, 1, 11, true,
			frame.Rows{Body: 3, Bar: 1, Input: 1, InputText: 1},
		},
		{
			"no palette where the transcript has fewer", 80, 3, 1, 11, true,
			frame.Rows{Body: 1, Bar: 1, Input: 1, InputText: 1},
		},
		{
			"a palette that wants less than nothing", 100, 30, 1, -4, true,
			frame.Rows{Header: 1, HeaderRule: 1, Body: 23, Bar: 1, Input: 3, Hint: 1, Boxed: true, InputText: 1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := frame.Layout(tt.width, tt.height, tt.lines, tt.want, tt.filled)
			if got != tt.rows {
				t.Errorf("Layout(%d, %d, %d, %d, %t)\n got %+v\nwant %+v",
					tt.width, tt.height, tt.lines, tt.want, tt.filled, got, tt.rows)
			}
			if got.Height() != tt.height {
				t.Errorf("the rows sum to %d, want %d", got.Height(), tt.height)
			}
		})
	}
}

func TestBoxedAt(t *testing.T) {
	t.Parallel()
	tests := []struct {
		width, height int
		want          bool
	}{
		{100, 30, true},
		{40, 16, true},
		{39, 16, false},
		{40, 15, false},
		{46, 12, false},
		{30, 12, false},
		{300, 15, false},
		{39, 100, false},
		{0, 0, false},
		{-40, -16, false},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%dx%d", tt.width, tt.height), func(t *testing.T) {
			t.Parallel()
			if got := frame.BoxedAt(tt.width, tt.height); got != tt.want {
				t.Errorf("BoxedAt(%d, %d) = %t, want %t", tt.width, tt.height, got, tt.want)
			}
		})
	}
}

// The input's first row is what the cursor's row is counted from.
func TestRowsInputTop(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		rows frame.Rows
		top  int
	}{
		{"Frame at 100x30", frame.Layout(100, 30, 1, 0, true), 26},
		{"with the palette open", frame.Layout(100, 30, 1, 11, true), 26},
		{"with a draft of 3 lines", frame.Layout(100, 30, 3, 0, true), 24},
		{"46x12 with a bar rule", frame.Layout(46, 12, 1, 0, false), 11},
		{"one row", frame.Layout(46, 1, 1, 0, false), 0},
		{"no rows", frame.Layout(46, 0, 1, 0, false), 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.rows.InputTop(); got != tt.top {
				t.Errorf("InputTop of %+v = %d, want %d", tt.rows, got, tt.top)
			}
		})
	}
}

func TestImageBox(t *testing.T) {
	t.Parallel()
	tests := []struct {
		width, height int
		cols, rows    int
	}{
		{100, 30, 44, 10},
		{100, 36, 44, 12},
		{100, 35, 44, 11},
		{100, 100, 44, 12},
		{300, 300, 44, 12},
		{48, 30, 44, 10},
		{47, 30, 43, 10},
		{46, 12, 42, 4},
		{30, 12, 26, 4},
		{100, 9, 44, 3},
		{100, 8, 44, 2},
		{100, 6, 44, 2},
		{100, 3, 44, 2},
		{6, 30, 2, 10},
		{5, 30, 1, 10},
		{4, 30, 1, 10},
		{1, 1, 1, 2},
		{0, 0, 1, 2},
		{-10, -10, 1, 2},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%dx%d", tt.width, tt.height), func(t *testing.T) {
			t.Parallel()
			cols, rows := frame.ImageBox(tt.width, tt.height)
			if cols != tt.cols || rows != tt.rows {
				t.Errorf("ImageBox(%d, %d) = %d by %d, want %d by %d", tt.width, tt.height, cols, rows, tt.cols, tt.rows)
			}
		})
	}
}

func TestHelperStripKeepsBody(t *testing.T) {
	t.Parallel()
	for height := 1; height < 80; height++ {
		for want := range 12 {
			r := frame.Layout(100, height, 1, 0, false, want)
			if r.Height() != height || r.Helpers > 4 || (height < 16 && r.Helpers != 0) || (r.Helpers > 0 && r.Body < 5) {
				t.Fatalf("height=%d want=%d rows=%+v", height, want, r)
			}
		}
	}
}
