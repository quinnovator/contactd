package statusline_test

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/quinnovator/contactd/internal/tui/statusline"
	"github.com/quinnovator/contactd/internal/tui/tuitest"
)

// The rows section 4 of the spec works out, and the Frame mock's bar.
func TestLayoutWorkedRows(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		snap        statusline.Snapshot
		width       int
		left, right string
	}{
		{
			"100 columns", idle(), 100,
			" ● live │ deepseek-flash │ cache 82% │ 3.7k tok │ 7.2s", "12:04 ",
		},
		// Tokens go, then the route: 60 > 46, 49 > 46, 32 fits.
		{"46 columns", idle(), 46, "● live │ cache 82% │ 7.2s", "12:04"},
		{"30 columns is compact", idle(), 30, "● live │ 82%", "12:04"},
		{
			"100 columns while a tool runs", toolRunning(), 100,
			" ● live │ deepseek-flash │ cache 48% │ 3.2k tok │ 5.4s", "running python │ esc stop │ 12:06 ",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			vals := statusline.CoreValues(tt.snap, unicodeMarks())
			left, right := statusline.Layout(preset(t, "default"), vals, tt.width, client())
			l, r := sides(left, right, tt.width, " │ ")
			if l != tt.left || r != tt.right {
				t.Errorf("row is %q ... %q, want %q ... %q", l, r, tt.left, tt.right)
			}
			row := bar(left, right, tt.width, " │ ")
			if got := utf8.RuneCountInString(row); got != tt.width {
				t.Errorf("row is %d cells wide, want %d: %q", got, tt.width, row)
			}
		})
	}
}

// The Frame mock colours its bar: the state green, a low cache share in the
// warn tone, the running tool in the tool tone, the stop hint dim.
func TestLayoutTones(t *testing.T) {
	t.Parallel()
	vals := statusline.CoreValues(toolRunning(), unicodeMarks())
	left, right := statusline.Layout(preset(t, "default"), vals, 100, client())
	want := []statusline.Cell{
		{ID: "core:state", Text: "● live", Tone: "ok"},
		{ID: "core:route", Text: "deepseek-flash"},
		{ID: "core:cache", Text: "cache 48%", Tone: "warn"},
		{ID: "core:tokens", Text: "3.2k tok"},
		{ID: "core:latency", Text: "5.4s"},
	}
	if !slices.Equal(left, want) {
		t.Errorf("left is %+v, want %+v", left, want)
	}
	want = []statusline.Cell{
		{ID: "core:activity", Text: "running python", Tone: "tool"},
		{ID: "core:stop", Text: "esc stop", Tone: "dim"},
		{ID: "core:clock", Text: "12:06"},
	}
	if !slices.Equal(right, want) {
		t.Errorf("right is %+v, want %+v", right, want)
	}

	// At 82% the cache is above the segment's warn_below, so it has no tone.
	left, _ = statusline.Layout(preset(t, "default"), statusline.CoreValues(idle(), unicodeMarks()), 100, client())
	if got := left[2]; got != (statusline.Cell{ID: "core:cache", Text: "cache 82%"}) {
		t.Errorf("the cache cell is %+v, want no tone", got)
	}
}

// The status-line editor mock. Its 60-column preview drops the route and
// the latency; the build keeps them, because they fit (section 4).
func TestLayoutDrawing(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		marks       statusline.Marks
		branch, sep string
		width       int
		left, right string
	}{
		{
			"100 columns", unicodeMarks(), "⎇ main", " │ ", 100,
			" ● live │ ada │ deepseek-flash │ cache 82% │ 7.2s", "⎇ main │ 12:04 ",
		},
		// 66 cells are needed; without the branch 57, and 57 fits 60.
		{
			"60 columns", unicodeMarks(), "⎇ main", " │ ", 60,
			" ● live │ ada │ deepseek-flash │ cache 82% │ 7.2s", "12:04 ",
		},
		{
			"no colour", asciiMarks(), "main", " | ", 100,
			" * live | ada | deepseek-flash | cache 82% | 7.2s", "main | 12:04 ",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			vals := statusline.CoreValues(idle(), tt.marks)
			vals["mod:git/branch"] = statusline.ModValue(tt.branch, "", "")
			left, right := statusline.Layout(drawing(t), vals, tt.width, client())
			if l, r := sides(left, right, tt.width, tt.sep); l != tt.left || r != tt.right {
				t.Errorf("row is %q ... %q, want %q ... %q", l, r, tt.left, tt.right)
			}
		})
	}
}

// Every preset at the four widths the mock-ups use, idle and while a tool
// runs, and the drawing's segments at its two preview widths.
func TestLayoutGolden(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	write := func(c statusline.Config, vals map[string]statusline.Value, width int) {
		left, right := statusline.Layout(c, vals, width, client())
		fmt.Fprintf(&b, "%4d |%s|\n", width, bar(left, right, width, " │ "))
	}
	states := []struct {
		name string
		snap statusline.Snapshot
	}{
		{"idle", idle()},
		{"a tool running", toolRunning()},
	}
	for _, name := range statusline.Presets() {
		for _, state := range states {
			fmt.Fprintf(&b, "%s, %s\n", name, state.name)
			vals := statusline.CoreValues(state.snap, unicodeMarks())
			for _, width := range []int{100, 60, 46, 30} {
				write(preset(t, name), vals, width)
			}
			b.WriteByte('\n')
		}
	}
	b.WriteString("the drawing's segments, idle\n")
	vals := statusline.CoreValues(idle(), unicodeMarks())
	vals["mod:git/branch"] = statusline.ModValue("⎇ main", "", "")
	for _, width := range []int{100, 60} {
		write(drawing(t), vals, width)
	}
	tuitest.Golden(t, "layout.golden", []byte(b.String()))
}

// fixed returns a segment that shows its format as written, and a short
// form when one is given. It needs a value to be drawn; values does that.
func fixed(name string, side statusline.Side, drop int, format, short string) statusline.Segment {
	return statusline.Segment{
		Name: name, From: statusline.FromShell, On: true, Side: side, Drop: drop,
		Format: format, Short: short,
	}
}

// values gives every segment of a config an empty value.
func values(c statusline.Config) map[string]statusline.Value {
	vals := make(map[string]statusline.Value, len(c.Segments))
	for _, s := range c.Segments {
		vals[s.ID()] = statusline.Value{}
	}
	return vals
}

func TestLayoutRules(t *testing.T) {
	t.Parallel()
	const l, r = statusline.Left, statusline.Right
	wide58, wide59 := strings.Repeat("x", 58), strings.Repeat("x", 59)
	// Under 40 columns Layout shows short forms only, which would hide what
	// most of these rows are about. So a row narrower than that is drawn
	// with characters several cells wide: the width, the separator and the
	// gap grow by the same factor and the arithmetic stays as written. The
	// rows about the compact form itself are marked, and are not scaled.
	tests := []struct {
		name        string
		segments    []statusline.Segment
		width       int
		compact     bool
		left, right []string
	}{
		{
			"file order is screen order on each side",
			[]statusline.Segment{
				fixed("a", l, 1, "aa", ""), fixed("b", r, 1, "bb", ""),
				fixed("c", l, 1, "cc", ""), fixed("d", r, 1, "dd", ""),
			},
			50, false,
			[]string{"aa", "cc"},
			[]string{"bb", "dd"},
		},
		{
			"what fits exactly stays: 4 + 3 + 4, a gap of 2, 4",
			[]statusline.Segment{fixed("a", l, 1, "aaaa", ""), fixed("c", l, 1, "cccc", ""), fixed("b", r, 1, "bbbb", "")},
			17, false,
			[]string{"aaaa", "cccc"},
			[]string{"bbbb"},
		},
		{
			"of two with the same drop the right side goes first",
			[]statusline.Segment{fixed("a", l, 1, "aaaa", ""), fixed("b", r, 1, "bbbb", "")},
			9, false,
			[]string{"aaaa"},
			nil,
		},
		{
			"of two with the same drop on one side the later goes first",
			[]statusline.Segment{fixed("a", l, 1, "aaaa", ""), fixed("c", l, 1, "cccc", "")},
			10, false,
			[]string{"aaaa"},
			nil,
		},
		{
			"the lowest drop goes first, wherever it is",
			[]statusline.Segment{fixed("a", l, 1, "aaaa", ""), fixed("b", r, 2, "bbbb", "")},
			9, false,
			nil,
			[]string{"bbbb"},
		},
		{
			"a segment switches to its short form before it goes",
			[]statusline.Segment{fixed("a", l, 1, "aaaaaa", "a"), fixed("b", r, 2, "bbbb", "")},
			8, false,
			[]string{"a"},
			[]string{"bbbb"},
		},
		{
			// a short leaves 7, which is over 6. The walk goes on to b, which
			// has no short form.
			"when that is not enough the next one gives way",
			[]statusline.Segment{fixed("a", l, 1, "aaaaaa", "a"), fixed("b", r, 2, "bbbb", "")},
			6, false,
			[]string{"a"},
			nil,
		},
		{
			// 15 cells; a short leaves 10, which is over 9; b short leaves 5.
			"every segment is shortened before one with a short form is removed",
			[]statusline.Segment{fixed("a", l, 1, "aaaaaa", "a"), fixed("b", l, 2, "bbbbbb", "b")},
			9, false,
			[]string{"a", "b"},
			nil,
		},
		{
			// Both short need 5. The walk starts again, and a goes first.
			"with all of them short, the lowest drop is removed first",
			[]statusline.Segment{fixed("a", l, 1, "aaaaaa", "a"), fixed("b", l, 2, "bbbbbb", "b")},
			4, false,
			[]string{"b"},
			nil,
		},
		{
			// a is shortened, c has no short form and goes, b is shortened:
			// 2 + 3 + 2. Then a goes.
			"a walk shortens some and removes others, in drop order",
			[]statusline.Segment{
				fixed("a", l, 1, "aaaaaa", "aa"), fixed("c", l, 2, "cccc", ""), fixed("b", l, 3, "bbbbbb", "bb"),
			},
			7, false,
			[]string{"aa", "bb"},
			nil,
		},
		{
			"and the second walk removes them in the same order",
			[]statusline.Segment{
				fixed("a", l, 1, "aaaaaa", "aa"), fixed("c", l, 2, "cccc", ""), fixed("b", l, 3, "bbbbbb", "bb"),
			},
			6, false,
			[]string{"bb"},
			nil,
		},
		{
			// The clock's short form is its full one. It is not removed in the
			// walk that shortens the others, although it shows no change.
			"a short form the same as the full one counts as a short form",
			[]statusline.Segment{fixed("a", l, 2, "aaaaaa", "a"), fixed("k", r, 1, "kkkk", "kkkk")},
			8, false,
			[]string{"a"},
			[]string{"kkkk"},
		},
		{
			"it stops as soon as the row fits",
			[]statusline.Segment{
				fixed("a", l, 3, "aaaa", ""), fixed("b", l, 1, "bbbb", ""),
				fixed("c", l, 2, "cccc", ""), fixed("d", l, 4, "dddd", ""),
			},
			20, false,
			[]string{"aaaa", "cccc", "dddd"},
			nil,
		},
		{
			"a drop of 0 never gives way: the right side is removed whole",
			[]statusline.Segment{fixed("s", l, 0, "ssssssss", ""), fixed("r", r, 0, "rrrr", "")},
			10, false,
			[]string{"ssssssss"},
			nil,
		},
		{
			"then the left is cut",
			[]statusline.Segment{fixed("s", l, 0, "ssssssss", ""), fixed("r", r, 0, "rrrr", "")},
			5, false,
			[]string{"sssss"},
			nil,
		},
		{
			"a cut cell has no space at its end",
			[]statusline.Segment{fixed("s", l, 0, "ab cd ef", "")},
			3, false,
			[]string{"ab"},
			nil,
		},
		{
			"cells that fit whole are kept and the next is cut",
			[]statusline.Segment{fixed("x", l, 0, "xxxx", ""), fixed("y", l, 0, "yyyyyy", "")},
			10, false,
			[]string{"xxxx", "yyy"},
			nil,
		},
		{
			"a cell with room for one character keeps it",
			[]statusline.Segment{fixed("x", l, 0, "xxxx", ""), fixed("y", l, 0, "yyyyyy", "")},
			8, false,
			[]string{"xxxx", "y"},
			nil,
		},
		{
			"a cell with no room after the separator goes",
			[]statusline.Segment{fixed("x", l, 0, "xxxx", ""), fixed("y", l, 0, "yyyyyy", "")},
			7, false,
			[]string{"xxxx"},
			nil,
		},
		{
			"a right side alone that does not fit is removed",
			[]statusline.Segment{fixed("r", r, 0, "rrrrrrrr", "")},
			5, false,
			nil,
			nil,
		},
		{
			"under 40 columns only short forms are shown",
			[]statusline.Segment{fixed("a", l, 0, "aaaa", "a"), fixed("b", l, 0, "bbbb", ""), fixed("c", r, 0, "cccc", "c")},
			39, true,
			[]string{"a"},
			[]string{"c"},
		},
		{
			"at 40 columns the full forms are back",
			[]statusline.Segment{fixed("a", l, 0, "aaaa", "a"), fixed("b", l, 0, "bbbb", ""), fixed("c", r, 0, "cccc", "c")},
			40, false,
			[]string{"aaaa", "bbbb"},
			[]string{"cccc"},
		},
		{
			"compact segments still give way in drop order",
			[]statusline.Segment{fixed("a", l, 2, "aaaa", "aaa"), fixed("b", l, 1, "bbbb", "bbb"), fixed("c", l, 3, "cccc", "ccc")},
			10, true,
			[]string{"aaa", "ccc"},
			nil,
		},
		{
			"from 60 columns there is a cell of padding at each end: 58 fit",
			[]statusline.Segment{fixed("a", l, 1, wide58, "")},
			60, false,
			[]string{wide58},
			nil,
		},
		{
			"and 59 do not",
			[]statusline.Segment{fixed("a", l, 1, wide59, "")},
			60, false,
			nil,
			nil,
		},
		{
			"under 60 columns there is no padding: 59 fit",
			[]statusline.Segment{fixed("a", l, 1, wide59, "")},
			59, false,
			[]string{wide59},
			nil,
		},
		{
			"a segment that is off is not drawn",
			[]statusline.Segment{
				fixed("a", l, 1, "aaaa", ""),
				{Name: "b", From: statusline.FromShell, Side: l, Format: "bbbb"},
			},
			50, false,
			[]string{"aaaa"},
			nil,
		},
		{
			"a side of zero is the right for a shell segment and the segment's own for a core one",
			[]statusline.Segment{
				{Name: "x", From: statusline.FromShell, On: true, Format: "xx"},
				{Name: "stop", On: true},
				{Name: "cache", On: true, Format: "cc"},
			},
			50, false,
			[]string{"cc"},
			[]string{"xx", "esc stop"},
		},
		{"no width, no cells", []statusline.Segment{fixed("a", l, 0, "aaaa", "")}, 0, true, nil, nil},
		{"a negative width is no width", []statusline.Segment{fixed("a", l, 0, "aaaa", "")}, -3, true, nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			unit := 1
			if !tt.compact && tt.width < 40 {
				unit = (39 + tt.width) / tt.width
			}
			width := unit * tt.width
			if !tt.compact && (width < 40 || (unit > 1 && width >= 60)) {
				t.Fatalf("%d columns scale to %d, which is not from 40 to 59", tt.width, width)
			}
			o := statusline.LayoutOptions{
				SepWidth: 3 * unit, MinGap: 2 * unit,
				Measure: func(s string) int { return unit * utf8.RuneCountInString(s) },
			}
			c := statusline.Config{Segments: tt.segments}
			left, right := statusline.Layout(c, values(c), width, o)
			if !slices.Equal(texts(left), tt.left) || !slices.Equal(texts(right), tt.right) {
				t.Errorf("got %q and %q, want %q and %q", texts(left), texts(right), tt.left, tt.right)
			}
			if (left == nil) != (len(left) == 0) || (right == nil) != (len(right) == 0) {
				t.Errorf("an empty side is not nil: %#v, %#v", left, right)
			}
		})
	}
}

func TestLayoutValues(t *testing.T) {
	t.Parallel()
	const l = statusline.Left
	warn := func(s statusline.Segment, below int) statusline.Segment {
		s.WarnBelow, s.HasWarn = below, true
		return s
	}
	number := func(n float64) statusline.Value { return statusline.Value{Num: n, HasNum: true} }
	tests := []struct {
		name    string
		segment statusline.Segment
		value   *statusline.Value
		width   int
		want    []statusline.Cell
	}{
		{"a segment with no value is not drawn", fixed("a", l, 0, "aaaa", ""), nil, 50, nil},
		{"a hidden value is not drawn", fixed("a", l, 0, "aaaa", ""), &statusline.Value{Hide: true}, 50, nil},
		{
			"a format that comes out empty is not drawn",
			fixed("a", l, 0, "$out", ""),
			&statusline.Value{Vars: map[string]string{"out": "  "}},
			50, nil,
		},
		{
			"variables go into the format",
			fixed("a", l, 0, "up $1 of $2", ""),
			&statusline.Value{Vars: map[string]string{"1": "3", "2": "4"}, Tone: "mod"},
			50,
			[]statusline.Cell{{ID: "shell:a", Text: "up 3 of 4", Tone: "mod"}},
		},
		{
			"the short form has its own format",
			fixed("a", l, 0, "load $1", "$1"),
			&statusline.Value{Vars: map[string]string{"1": "0.42"}},
			39,
			[]statusline.Cell{{ID: "shell:a", Text: "0.42"}},
		},
		{
			"a short form that comes out empty is no short form",
			fixed("a", l, 0, "load $1", "$2"),
			&statusline.Value{Vars: map[string]string{"1": "0.42", "2": ""}},
			39, nil,
		},
		{
			"a number under warn_below is drawn in the warn tone",
			warn(fixed("a", l, 0, "aaaa", ""), 50), new(number(49.9)), 50,
			[]statusline.Cell{{ID: "shell:a", Text: "aaaa", Tone: "warn"}},
		},
		{
			"a number at warn_below is not",
			warn(fixed("a", l, 0, "aaaa", ""), 50), new(number(50)), 50,
			[]statusline.Cell{{ID: "shell:a", Text: "aaaa"}},
		},
		{
			"a value with no number never warns",
			warn(fixed("a", l, 0, "aaaa", ""), 50), &statusline.Value{Num: 1}, 50,
			[]statusline.Cell{{ID: "shell:a", Text: "aaaa"}},
		},
		{
			"a segment with no warn_below never warns",
			fixed("a", l, 0, "aaaa", ""), new(number(-5)), 50,
			[]statusline.Cell{{ID: "shell:a", Text: "aaaa"}},
		},
		{
			"a value's own tone wins over the warning",
			warn(fixed("a", l, 0, "aaaa", ""), 50),
			&statusline.Value{Num: 1, HasNum: true, Tone: "bad"},
			50,
			[]statusline.Cell{{ID: "shell:a", Text: "aaaa", Tone: "bad"}},
		},
		{
			"a value's text replaces the format",
			fixed("a", l, 0, "load $1", ""),
			&statusline.Value{Text: "call failed", Tone: "bad", Vars: map[string]string{"1": "9"}},
			50,
			[]statusline.Cell{{ID: "shell:a", Text: "call failed", Tone: "bad"}},
		},
		{
			"and the short format, when the segment has one",
			fixed("a", l, 0, "load $1", "$1"), &statusline.Value{Text: "call failed"}, 39,
			[]statusline.Cell{{ID: "shell:a", Text: "call failed"}},
		},
		{
			"but gives a segment without a short form none",
			fixed("a", l, 0, "load $1", ""), &statusline.Value{Text: "call failed"}, 39, nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			vals := map[string]statusline.Value{}
			if tt.value != nil {
				vals[tt.segment.ID()] = *tt.value
			}
			c := statusline.Config{Segments: []statusline.Segment{tt.segment}}
			left, right := statusline.Layout(c, vals, tt.width, client())
			if !slices.Equal(left, tt.want) || right != nil {
				t.Errorf("got %+v and %+v, want %+v and nothing", left, right, tt.want)
			}
		})
	}
}

// A mod that is off or missing has no value, so its segment stays in the
// config and is not drawn.
func TestLayoutModWithoutValue(t *testing.T) {
	t.Parallel()
	c := drawing(t)
	vals := statusline.CoreValues(idle(), unicodeMarks())
	_, right := statusline.Layout(c, vals, 100, client())
	if got := texts(right); !slices.Equal(got, []string{"12:04"}) {
		t.Errorf("right is %q, want the clock alone", got)
	}
	vals["mod:git/branch"] = statusline.ModValue("", "", "")
	_, right = statusline.Layout(c, vals, 100, client())
	if got := texts(right); !slices.Equal(got, []string{"12:04"}) {
		t.Errorf("with an empty reply right is %q, want the clock alone", got)
	}
}

// A mod's short form is used when the bar is narrow.
func TestLayoutModShort(t *testing.T) {
	t.Parallel()
	c := statusline.Config{}.WithMods([]statusline.ModSegment{{Mod: "cache", Name: "cache"}})
	vals := map[string]statusline.Value{"mod:cache/cache": statusline.ModValue("cache avg 61%", "61%", "warn")}
	tests := []struct {
		width int
		want  statusline.Cell
	}{
		{50, statusline.Cell{ID: "mod:cache/cache", Text: "cache avg 61%", Tone: "warn"}},
		{39, statusline.Cell{ID: "mod:cache/cache", Text: "61%", Tone: "warn"}},
		{10, statusline.Cell{ID: "mod:cache/cache", Text: "61%", Tone: "warn"}},
	}
	for _, tt := range tests {
		t.Run(strconv.Itoa(tt.width), func(t *testing.T) {
			t.Parallel()
			left, right := statusline.Layout(c, vals, tt.width, client())
			if left != nil || !slices.Equal(right, []statusline.Cell{tt.want}) {
				t.Errorf("got %+v and %+v, want nothing and %+v", left, right, tt.want)
			}
		})
	}
}

// Layout asks Measure how wide a text is, and counts the separator and the
// gap it is told about.
func TestLayoutMeasure(t *testing.T) {
	t.Parallel()
	// Each has a short form, the same as its full one, so that the rows
	// under 40 columns are not emptied by the compact rule.
	c := statusline.Config{Segments: []statusline.Segment{
		fixed("a", statusline.Left, 2, "aaaa", "aaaa"),
		fixed("b", statusline.Left, 1, "bbbb", "bbbb"),
		fixed("c", statusline.Right, 3, "cccc", "cccc"),
	}}
	double := func(s string) int { return 2 * utf8.RuneCountInString(s) }
	tests := []struct {
		name        string
		o           statusline.LayoutOptions
		width       int
		left, right []string
	}{
		{"runes when there is no Measure", statusline.LayoutOptions{SepWidth: 3, MinGap: 2}, 17, []string{"aaaa", "bbbb"}, []string{"cccc"}},
		{"one too few", statusline.LayoutOptions{SepWidth: 3, MinGap: 2}, 16, []string{"aaaa"}, []string{"cccc"}},
		{"a wider separator", statusline.LayoutOptions{SepWidth: 4, MinGap: 2}, 17, []string{"aaaa"}, []string{"cccc"}},
		{"a wider gap", statusline.LayoutOptions{SepWidth: 3, MinGap: 3}, 17, []string{"aaaa"}, []string{"cccc"}},
		{"no separator and no gap", statusline.LayoutOptions{}, 12, []string{"aaaa", "bbbb"}, []string{"cccc"}},
		{"negative ones count as none", statusline.LayoutOptions{SepWidth: -4, MinGap: -4}, 12, []string{"aaaa", "bbbb"}, []string{"cccc"}},
		{
			"every cell twice as wide",
			statusline.LayoutOptions{SepWidth: 3, MinGap: 2, Measure: double},
			29,
			[]string{"aaaa", "bbbb"},
			[]string{"cccc"},
		},
		{
			"and one too few",
			statusline.LayoutOptions{SepWidth: 3, MinGap: 2, Measure: double},
			28,
			[]string{"aaaa"},
			[]string{"cccc"},
		},
		{
			"and none when every one is too wide",
			statusline.LayoutOptions{SepWidth: 3, MinGap: 2, Measure: double},
			7,
			nil,
			nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			left, right := statusline.Layout(c, values(c), tt.width, tt.o)
			if !slices.Equal(texts(left), tt.left) || !slices.Equal(texts(right), tt.right) {
				t.Errorf("got %q and %q, want %q and %q", texts(left), texts(right), tt.left, tt.right)
			}
		})
	}

	// A lone cell that never gives way is cut by cells: five cells hold two
	// characters of two cells each.
	lone := statusline.Config{Segments: []statusline.Segment{fixed("s", statusline.Left, 0, "ssssssss", "ssssssss")}}
	left, _ := statusline.Layout(lone, values(lone), 5, statusline.LayoutOptions{Measure: double})
	if got := texts(left); !slices.Equal(got, []string{"ss"}) {
		t.Errorf("cut to %q, want [ss]", got)
	}
}

// A text that takes no cells, such as a zero-width space, is nothing to
// show: it is not a cell, and as a short form it is no short form.
func TestLayoutNothingToSee(t *testing.T) {
	t.Parallel()
	const l, r = statusline.Left, statusline.Right
	const zw = "\u200b"
	// Ten cells a character, so that a row from 40 to 59 columns can be too
	// narrow for two segments of four.
	visible := func(s string) int { return 10 * utf8.RuneCountInString(strings.ReplaceAll(s, zw, "")) }
	o := statusline.LayoutOptions{SepWidth: 30, MinGap: 10, Measure: visible}
	c := statusline.Config{Segments: []statusline.Segment{
		fixed("a", l, 0, "aaaa", zw), fixed("z", l, 0, zw, "z"), fixed("b", r, 1, "bbbb", zw),
	}}
	tests := []struct {
		name        string
		width       int
		left, right []string
	}{
		{"with room, the text of no width is left out", 200, []string{"aaaa"}, []string{"bbbb"}},
		// Shortened to nothing, b would fit: 40, a gap of 10, and 0.
		{"too narrow, a short form of no width is not switched to", 59, []string{"aaaa"}, nil},
		{"compact, it does not keep its segment", 39, []string{"z"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			left, right := statusline.Layout(c, values(c), tt.width, o)
			if !slices.Equal(texts(left), tt.left) || !slices.Equal(texts(right), tt.right) {
				t.Errorf("got %q and %q, want %q and %q", texts(left), texts(right), tt.left, tt.right)
			}
		})
	}
}

// crowded is a config with every segment on, some with long formats, a mod
// and a shell segment: more than any width holds.
func crowded(t *testing.T) (statusline.Config, map[string]statusline.Value) {
	t.Helper()
	c := preset(t, "ops").WithMods([]statusline.ModSegment{{Mod: "git", Name: "branch"}, {Mod: "cache", Name: "cache"}})
	for i := range c.Segments {
		c.Segments[i].On = true
	}
	c.Segments[index(t, c, "core:contact")].Format = "talking to $name, who is served by $provider"
	c.Segments[index(t, c, "core:clock")].Drop = 0
	snap := toolRunning()
	snap.Scrolled, snap.Agents, snap.LoadingOlder = true, 3, true
	snap.Conn, snap.ConnErr = statusline.ConnDown, "dial tcp 127.0.0.1:7777: connection refused"
	vals := statusline.CoreValues(snap, unicodeMarks())
	vals["mod:git/branch"] = statusline.ModValue("⎇ a-branch-with-a-long-name", "⎇", "mod")
	vals["mod:cache/cache"] = statusline.ModValue("cache avg 61%", "61%", "warn")
	vals["shell:load"] = statusline.ShellValue(c.Segments[index(t, c, "shell:load")], "load averages: 1.20 1.50", nil, false)
	return c, vals
}

// The row is never wider than the bar, at any width, whatever is on it.
func TestLayoutNeverWider(t *testing.T) {
	t.Parallel()
	// wide counts every character outside ASCII as two cells, as a terminal
	// set to draw ambiguous characters wide does.
	wide := func(s string) int {
		n := 0
		for _, r := range s {
			n++
			if r >= utf8.RuneSelf {
				n++
			}
		}
		return n
	}
	type fixture struct {
		name string
		c    statusline.Config
		vals map[string]statusline.Value
	}
	snaps := []struct {
		name string
		snap statusline.Snapshot
	}{
		{"idle", idle()},
		{"a tool running", toolRunning()},
		{"gone", statusline.Snapshot{Conn: statusline.ConnGone, Contact: "ada", Clock: "12:04"}},
		{"a failed call", statusline.Snapshot{Conn: statusline.ConnLive, Model: "m", HasCall: true, CallFailed: true, Scrolled: true}},
		{"nothing known", statusline.Snapshot{}},
	}
	fixtures := make([]fixture, 0, len(snaps)*len(statusline.Presets())+2)
	for _, name := range statusline.Presets() {
		for _, s := range snaps {
			fixtures = append(fixtures, fixture{name + ", " + s.name, preset(t, name), statusline.CoreValues(s.snap, unicodeMarks())})
		}
	}
	drawn := statusline.CoreValues(idle(), unicodeMarks())
	drawn["mod:git/branch"] = statusline.ModValue("⎇ main", "", "")
	fixtures = append(fixtures, fixture{"the drawing", drawing(t), drawn})
	c, vals := crowded(t)
	fixtures = append(fixtures, fixture{"crowded", c, vals})

	options := []struct {
		name string
		o    statusline.LayoutOptions
	}{
		{"the client's", client()},
		{"wide characters", statusline.LayoutOptions{SepWidth: 3, MinGap: 2, Measure: wide}},
		{"a wide separator", statusline.LayoutOptions{SepWidth: 7, MinGap: 5}},
		{"nothing between cells", statusline.LayoutOptions{}},
	}
	for _, f := range fixtures {
		for _, o := range options {
			t.Run(f.name+", "+o.name, func(t *testing.T) {
				t.Parallel()
				for width := 1; width <= 200; width++ {
					left, right := statusline.Layout(f.c, f.vals, width, o.o)
					if got := need(left, right, width, o.o); got > width {
						t.Errorf("at %d columns the row needs %d: %q and %q", width, got, texts(left), texts(right))
					}
					for _, cell := range slices.Concat(left, right) {
						if cell.Text == "" || cell.ID == "" {
							t.Errorf("at %d columns there is an empty cell: %+v", width, cell)
						}
					}
				}
			})
		}
	}
}

// With room for everything, everything that has a value is drawn; a wider
// bar never shows less.
func TestLayoutShowsMoreWhenWider(t *testing.T) {
	t.Parallel()
	c, vals := crowded(t)
	shown := func(width int) int {
		left, right := statusline.Layout(c, vals, width, client())
		return len(left) + len(right)
	}
	// 14 segments are on and have a value: the eleven core ones, two mods
	// and the load.
	if got := shown(1000); got != 14 {
		t.Errorf("at 1000 columns %d cells are drawn, want 14", got)
	}
	// Under 40 columns only short forms are shown, and from 60 two cells go
	// to padding, so compare within each range.
	for _, r := range [][2]int{{1, 39}, {40, 59}, {60, 300}} {
		for width := r[0]; width < r[1]; width++ {
			if narrow, wider := shown(width), shown(width+1); wider < narrow {
				t.Errorf("%d columns show %d cells and %d columns show %d", width, narrow, width+1, wider)
			}
		}
	}
}

// Whatever a value or a format holds, a cell is one line of plain text.
func TestLayoutCellsAreSafe(t *testing.T) {
	t.Parallel()
	hostile := "\x1b]0;title\x07\x1b[31mred\x1b[0m\r\nnext\tline\u202e\u2066\x9b2J\U0010EEEE\u200d\ufe0f\x00\x7f"
	c := statusline.Config{Segments: []statusline.Segment{
		fixed("format", statusline.Left, 0, "a\x1b[2Jb\nc", "a\x1b[2Jb\nc"),
		fixed("vars", statusline.Left, 0, "$out", "$out"),
		fixed("text", statusline.Right, 0, "", "x"),
		fixed("long", statusline.Right, 0, "$out", "$out"),
		{Name: "contact", On: true, Short: "$name"},
		{Name: "activity", On: true},
	}}
	vals := statusline.CoreValues(statusline.Snapshot{Contact: hostile, Running: hostile}, unicodeMarks())
	vals["shell:format"] = statusline.Value{}
	vals["shell:vars"] = statusline.Value{Vars: map[string]string{"out": hostile}}
	vals["shell:text"] = statusline.Value{Text: hostile}
	vals["shell:long"] = statusline.Value{Vars: map[string]string{"out": strings.Repeat("é", 500)}}
	check := func(id, form, text string) {
		t.Helper()
		if text == "" {
			t.Errorf("%s has no %s form", id, form)
		}
		for _, r := range text {
			if r < 0x20 || (r >= 0x7f && r <= 0x9f) || r == 0x202e || r == 0x2066 || r == 0x10EEEE {
				t.Errorf("the %s form of %s holds %U: %q", form, id, r, text)
			}
		}
		if n := utf8.RuneCountInString(text); n > 120 {
			t.Errorf("the %s form of %s is %d runes long, want at most 120", form, id, n)
		}
		if text != strings.TrimSpace(text) {
			t.Errorf("the %s form of %s has a space at an end: %q", form, id, text)
		}
	}
	for _, s := range c.Segments {
		full, short := s.Text(vals[s.ID()])
		check(s.ID(), "full", full)
		check(s.ID(), "short", short)
	}
	left, right := statusline.Layout(c, vals, 1000, client())
	if len(left)+len(right) != len(c.Segments) {
		t.Fatalf("%d cells are drawn, want %d", len(left)+len(right), len(c.Segments))
	}
	for _, cell := range slices.Concat(left, right) {
		check(cell.ID, "drawn", cell.Text)
	}
	if got := left[0].Text; got != "a[2Jb c" {
		t.Errorf("the format is drawn as %q, want %q", got, "a[2Jb c")
	}
	if got, want := right[1].Text, strings.Repeat("é", 120); got != want {
		t.Errorf("the long value is drawn as %q, want 120 runes of it", got)
	}
}

// Layout changes nothing it is given, and gives the same answer twice.
func TestLayoutIsPure(t *testing.T) {
	t.Parallel()
	c, vals := crowded(t)
	wantC, wantVals := crowded(t)
	for _, width := range []int{200, 60, 46, 30, 8} {
		left, right := statusline.Layout(c, vals, width, client())
		again, againRight := statusline.Layout(c, vals, width, client())
		if !slices.Equal(left, again) || !slices.Equal(right, againRight) {
			t.Errorf("at %d columns two calls differ", width)
		}
	}
	if !reflect.DeepEqual(c, wantC) {
		t.Error("Layout changed the config")
	}
	if !reflect.DeepEqual(vals, wantVals) {
		t.Error("Layout changed the values")
	}
}

func TestDropOrdinal(t *testing.T) {
	t.Parallel()
	draw := drawing(t)
	def := preset(t, "default")
	tied := statusline.Config{Segments: []statusline.Segment{
		fixed("a", statusline.Left, 1, "a", ""),
		fixed("b", statusline.Left, 1, "b", ""),
		fixed("c", statusline.Right, 1, "c", ""),
		{Name: "d", From: statusline.FromShell, Side: statusline.Left, Drop: 1},
	}}
	tests := []struct {
		name string
		c    statusline.Config
		id   string
		want int
	}{
		// The mock's detail pane: "drop order  3rd when narrow". On the left
		// the route (2) and the latency (3) go before the cache (4); tokens
		// are off.
		{"the drawing's cache", draw, "core:cache", 3},
		{"the drawing's route", draw, "core:route", 1},
		{"the drawing's latency", draw, "core:latency", 2},
		{"the drawing's contact", draw, "core:contact", 4},
		{"the state never goes", draw, "core:state", 0},
		{"the stop hint never goes", draw, "core:stop", 0},
		{"the right side is counted by itself", draw, "mod:git/branch", 2},
		{"the activity goes after the branch", draw, "core:activity", 3},
		{"the clock goes last on its side", draw, "core:clock", 4},
		{"a segment that is off still has a place", draw, "core:tokens", 1},
		{"with tokens on the cache is the 4th", def, "core:cache", 4},
		{"the default's tokens", def, "core:tokens", 1},
		{"of two with the same drop the later goes first", tied, "shell:a", 2},
		{"and so is the 1st", tied, "shell:b", 1},
		{"the other side does not count", tied, "shell:c", 1},
		{"a segment that is off counts the ones that are on", tied, "shell:d", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := statusline.DropOrdinal(tt.c, index(t, tt.c, tt.id)); got != tt.want {
				t.Errorf("DropOrdinal of %s is %d, want %d", tt.id, got, tt.want)
			}
		})
	}
	for _, i := range []int{-1, len(draw.Segments), 1 << 20} {
		if got := statusline.DropOrdinal(draw, i); got != 0 {
			t.Errorf("DropOrdinal(%d) is %d, want 0", i, got)
		}
	}
}

// The ordinal the editor shows is the order Layout really uses: narrowing
// the bar a cell at a time, one side's segments give way in that order. To
// give way is to be shortened or removed, whichever comes first.
func TestDropOrdinalMatchesLayout(t *testing.T) {
	t.Parallel()
	c := preset(t, "ops")
	// A long label, so that every segment that can give way has done so by
	// 40 columns, where the rule changes.
	snap := idle()
	snap.Contact = strings.Repeat("a", 30)
	vals := statusline.CoreValues(snap, unicodeMarks())
	shown := func(width int) map[string]string {
		left, _ := statusline.Layout(c, vals, width, client())
		cells := make(map[string]string, len(left))
		for _, cell := range left {
			cells[cell.ID] = cell.Text
		}
		return cells
	}
	full := shown(100)
	var gave []string
	for width := 99; width >= 40; width-- {
		now := shown(width)
		for _, s := range c.Segments {
			id := s.ID()
			if text, was := full[id]; was && now[id] != text && !slices.Contains(gave, id) {
				gave = append(gave, id)
			}
		}
	}
	want := []string{"core:tokens", "core:route", "core:latency", "core:cache", "core:contact"}
	if !slices.Equal(gave, want) {
		t.Fatalf("between 100 and 40 columns %q gave way, want %q", gave, want)
	}
	for n, id := range gave {
		if got := statusline.DropOrdinal(c, index(t, c, id)); got != n+1 {
			t.Errorf("%s gave way %d, and DropOrdinal says %d", id, n+1, got)
		}
	}
	// The cache was shortened, not removed: it outlasts the contact.
	if got, want := shown(51), map[string]string{"core:state": "● live", "core:cache": "82%"}; !maps.Equal(got, want) {
		t.Errorf("at 51 columns the left side is %v, want %v", got, want)
	}
}
