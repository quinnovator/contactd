package statusline_test

import (
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/quinnovator/contactd/internal/tui/statusline"
)

func TestSegmentID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		segment statusline.Segment
		want    string
	}{
		{statusline.Segment{Name: "cache", From: "core"}, "core:cache"},
		{statusline.Segment{Name: "cache"}, "core:cache"},
		{statusline.Segment{Name: "branch", From: "mod:git"}, "mod:git/branch"},
		{statusline.Segment{Name: "load", From: "shell"}, "shell:load"},
		// A mod's segment and a core one of the same name are two segments.
		{statusline.Segment{Name: "cache", From: "mod:cache"}, "mod:cache/cache"},
		{statusline.Segment{Name: "x", From: "panel"}, "panel:x"},
	}
	for _, tt := range tests {
		if got := tt.segment.ID(); got != tt.want {
			t.Errorf("the ID of %+v is %q, want %q", tt.segment, got, tt.want)
		}
	}
}

func TestSideString(t *testing.T) {
	t.Parallel()
	for side, want := range map[statusline.Side]string{statusline.Left: "left", statusline.Right: "right", 0: "unset", 9: "unset"} {
		if got := side.String(); got != want {
			t.Errorf("Side(%d) is %q, want %q", side, got, want)
		}
	}
}

// The table of section 4: which segments each preset switches on, and on
// which side.
func TestPresets(t *testing.T) {
	t.Parallel()
	if got, want := statusline.Presets(), []string{"minimal", "default", "ops"}; !slices.Equal(got, want) {
		t.Fatalf("the presets are %q, want %q", got, want)
	}
	tests := []struct {
		name        string
		left, right []string
	}{
		{"minimal", []string{"state", "cache"}, []string{"stop", "scroll", "clock"}},
		{
			"default",
			[]string{"state", "route", "cache", "tokens", "latency"},
			[]string{"activity", "stop", "scroll", "clock"},
		},
		{
			"ops",
			[]string{"state", "contact", "route", "cache", "tokens", "latency"},
			[]string{"activity", "stop", "scroll", "agents", "clock"},
		},
	}
	listed := []string{
		"core:state", "core:contact", "core:route", "core:cache", "core:tokens", "core:latency",
		"core:activity", "core:stop", "core:scroll", "core:agents", "shell:load", "core:clock",
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := preset(t, tt.name)
			var ids, left, right []string
			for _, s := range c.Segments {
				ids = append(ids, s.ID())
				switch {
				case !s.On:
				case s.Side == statusline.Left:
					left = append(left, s.Name)
				case s.Side == statusline.Right:
					right = append(right, s.Name)
				default:
					t.Errorf("%s has no side", s.ID())
				}
				if err := s.Validate(); err != nil {
					t.Errorf("%s: %v", s.ID(), err)
				}
				if s.From == statusline.FromShell && s.On {
					t.Errorf("the preset switches the shell segment %s on", s.Name)
				}
			}
			if !slices.Equal(ids, listed) {
				t.Errorf("it lists %q, want %q", ids, listed)
			}
			if !slices.Equal(left, tt.left) || !slices.Equal(right, tt.right) {
				t.Errorf("on are %q and %q, want %q and %q", left, right, tt.left, tt.right)
			}

			// The config is the caller's: changing it changes no later one.
			c.Segments[0].Name = "changed"
			if again := preset(t, tt.name); again.Segments[0].Name != "state" {
				t.Error("a change to one copy of the preset shows in the next")
			}
		})
	}
	if c, ok := statusline.Preset("fancy"); ok || c.Segments != nil {
		t.Errorf("Preset(fancy) is %+v, %v; want nothing", c, ok)
	}
	if statusline.DefaultPreset != "default" {
		t.Errorf("DefaultPreset is %q", statusline.DefaultPreset)
	}
}

// The core segments as section 4 lists them: side, turn to give way, and
// the cache's warning.
func TestCoreSegmentDefaults(t *testing.T) {
	t.Parallel()
	const l, r = statusline.Left, statusline.Right
	want := map[string]statusline.Segment{
		"state":    {Side: l, Drop: 0},
		"contact":  {Side: l, Drop: 5},
		"route":    {Side: l, Drop: 2},
		"cache":    {Side: l, Drop: 4, WarnBelow: 50, HasWarn: true},
		"tokens":   {Side: l, Drop: 1},
		"latency":  {Side: l, Drop: 3},
		"activity": {Side: r, Drop: 6},
		"stop":     {Side: r, Drop: 0},
		"scroll":   {Side: r, Drop: 0},
		"agents":   {Side: r, Drop: 1},
		"clock":    {Side: r, Drop: 7},
	}
	seen := 0
	for _, s := range preset(t, "ops").Segments {
		w, ok := want[s.Name]
		if !ok || s.From != statusline.FromCore {
			continue
		}
		seen++
		if s.Side != w.Side || s.Drop != w.Drop || s.WarnBelow != w.WarnBelow || s.HasWarn != w.HasWarn {
			t.Errorf("%s is side %s, drop %d, warn %d (%v); want %s, %d, %d (%v)",
				s.Name, s.Side, s.Drop, s.WarnBelow, s.HasWarn, w.Side, w.Drop, w.WarnBelow, w.HasWarn)
		}
		if s.Format == "" {
			t.Errorf("%s has no format for the editor to show", s.Name)
		}
	}
	if seen != len(want) {
		t.Errorf("%d core segments, want %d", seen, len(want))
	}
	load := preset(t, "ops").Segments[10]
	wantLoad := statusline.Segment{
		Name: "load", From: "shell", Side: r, Drop: 1, Format: "load $1",
		Command: "uptime", Match: `load averages?: ([0-9.]+)`,
		Interval: 10 * time.Second, Timeout: time.Second,
	}
	if load != wantLoad {
		t.Errorf("the load segment is %+v, want %+v", load, wantLoad)
	}
}

func TestCoreValues(t *testing.T) {
	t.Parallel()
	with := func(change func(*statusline.Snapshot)) statusline.Snapshot {
		s := idle()
		change(&s)
		return s
	}
	conn := func(state string) statusline.Snapshot {
		return with(func(s *statusline.Snapshot) { s.Conn = state })
	}
	call := func(input, cached, output int, ms int64) statusline.Snapshot {
		return with(func(s *statusline.Snapshot) { s.Input, s.Cached, s.Output, s.MS = input, cached, output, ms })
	}
	noCall := with(func(s *statusline.Snapshot) { s.HasCall = false })
	failed := with(func(s *statusline.Snapshot) {
		s.CallFailed, s.Input, s.Cached, s.Output, s.MS = true, 0, 0, 0, 30000
	})
	replying := with(func(s *statusline.Snapshot) { s.Replying = true })
	loading := with(func(s *statusline.Snapshot) { s.LoadingOlder = true })
	agents := func(n int) statusline.Snapshot { return with(func(s *statusline.Snapshot) { s.Agents = n }) }
	uni, ascii := unicodeMarks(), asciiMarks()

	tests := []struct {
		name        string
		snap        statusline.Snapshot
		marks       statusline.Marks
		id          string
		full, short string // both empty: nothing is shown
		tone        string
	}{
		{"live", idle(), uni, "core:state", "● live", "● live", "ok"},
		{"live in ASCII", idle(), ascii, "core:state", "* live", "* live", "ok"},
		{"connecting", conn(statusline.ConnConnecting), uni, "core:state", "● connecting", "● connecting", "dim"},
		{"down", conn(statusline.ConnDown), uni, "core:state", "● offline, retrying", "● offline, retrying", "warn"},
		{"gone has no dot", conn(statusline.ConnGone), uni, "core:state", "gone", "gone", "bad"},
		{"no connection yet", conn(""), uni, "core:state", "", "", ""},
		{"a state this package does not know", conn("asleep"), uni, "core:state", "", "", ""},

		{"the contact", idle(), uni, "core:contact", "ada", "", ""},
		{"no contact", with(func(s *statusline.Snapshot) { s.Contact = "" }), uni, "core:contact", "", "", ""},
		{"the route is the model", idle(), uni, "core:route", "deepseek-flash", "", ""},
		{"no model", with(func(s *statusline.Snapshot) { s.Model = "" }), uni, "core:route", "", "", ""},

		{"the cache share", idle(), uni, "core:cache", "cache 82%", "82%", ""},
		{"a share is rounded down", call(3, 2, 0, 1), uni, "core:cache", "cache 66%", "66%", ""},
		{"nothing cached", call(3200, 0, 0, 1), uni, "core:cache", "cache 0%", "0%", ""},
		{"everything cached", call(3200, 3200, 0, 1), uni, "core:cache", "cache 100%", "100%", ""},
		{"more cached than sent is still 100", call(10, 50, 0, 1), uni, "core:cache", "cache 100%", "100%", ""},
		{"a negative count is 0", call(10, -5, 0, 1), uni, "core:cache", "cache 0%", "0%", ""},
		{"a prompt of no tokens has no share", call(0, 0, 40, 1), uni, "core:cache", "", "", ""},
		{"no call yet", noCall, uni, "core:cache", "", "", ""},
		{"a failed call", failed, uni, "core:cache", "call failed", "call failed", "bad"},

		{"tokens", idle(), uni, "core:tokens", "3.7k tok", "", ""},
		{"under a thousand tokens", call(950, 0, 0, 1), uni, "core:tokens", "950 tok", "", ""},
		{"a thousand", call(1000, 0, 0, 1), uni, "core:tokens", "1.0k tok", "", ""},
		{"just under a million", call(999_949, 0, 0, 1), uni, "core:tokens", "999.9k tok", "", ""},
		{"a million, not 1000.0k", call(999_950, 0, 0, 1), uni, "core:tokens", "1.0M tok", "", ""},
		{"millions", call(1_540_000, 0, 0, 1), uni, "core:tokens", "1.5M tok", "", ""},
		{"output alone", call(0, 0, 40, 1), uni, "core:tokens", "0 tok", "", ""},
		{"no tokens before a call", noCall, uni, "core:tokens", "", "", ""},
		{"no tokens from a call that counted none", failed, uni, "core:tokens", "", "", ""},

		{"latency", idle(), uni, "core:latency", "7.2s", "", ""},
		{"latency over ten seconds", call(1, 0, 0, 12345), uni, "core:latency", "12.3s", "", ""},
		{"a fast call", call(1, 0, 0, 340), uni, "core:latency", "0.3s", "", ""},
		{"no latency before a call", noCall, uni, "core:latency", "", "", ""},
		{"a failed call still took its time", failed, uni, "core:latency", "30.0s", "", ""},
		{"a call that reported no time", call(1, 0, 0, 0), uni, "core:latency", "", "", ""},

		{"a running tool", toolRunning(), uni, "core:activity", "running python", "python", "tool"},
		{"replying has no short form", replying, uni, "core:activity", "replying", "", "dim"},
		{"loading earlier has no short form", loading, uni, "core:activity", "loading earlier", "", "dim"},
		{
			"a reply comes before the page",
			with(func(s *statusline.Snapshot) { s.Replying, s.LoadingOlder = true, true }),
			uni, "core:activity", "replying", "", "dim",
		},
		{"idle", idle(), uni, "core:activity", "", "", ""},

		{"stop while replying", replying, uni, "core:stop", "esc stop", "esc", "dim"},
		{"stop while a tool runs", toolRunning(), uni, "core:stop", "esc stop", "esc", "dim"},
		{
			"stop for a tool that runs with no reply typing",
			with(func(s *statusline.Snapshot) { s.Running = "python" }),
			uni, "core:stop", "esc stop", "esc", "dim",
		},
		{"no stop when idle", idle(), uni, "core:stop", "", "", ""},
		{"no stop for a page being read", loading, uni, "core:stop", "", "", ""},

		{"scrolled up", with(func(s *statusline.Snapshot) { s.Scrolled = true }), uni, "core:scroll", "↓ newer", "↓", "dim"},
		{"scrolled up in ASCII", with(func(s *statusline.Snapshot) { s.Scrolled = true }), ascii, "core:scroll", "v newer", "v", "dim"},
		{"following", idle(), uni, "core:scroll", "", "", ""},

		{"one sub-agent", agents(1), uni, "core:agents", "1 sub-agent", "1", ""},
		{"two sub-agents", agents(2), uni, "core:agents", "2 sub-agents", "2", ""},
		{"no sub-agents", agents(0), uni, "core:agents", "", "", ""},

		{"the clock", idle(), uni, "core:clock", "12:04", "12:04", ""},
		{"no clock", with(func(s *statusline.Snapshot) { s.Clock = "" }), uni, "core:clock", "", "", ""},
	}
	ops := preset(t, "ops")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			vals := statusline.CoreValues(tt.snap, tt.marks)
			v, ok := vals[tt.id]
			if !ok {
				t.Fatalf("no value for %s", tt.id)
			}
			full, short := ops.Segments[index(t, ops, tt.id)].Text(v)
			if full != tt.full || short != tt.short {
				t.Errorf("shows %q, short %q; want %q, short %q", full, short, tt.full, tt.short)
			}
			if tt.full == "" {
				if !v.Hide {
					t.Errorf("the value is not hidden: %+v", v)
				}
				return
			}
			if v.Tone != tt.tone {
				t.Errorf("the tone is %q, want %q", v.Tone, tt.tone)
			}
		})
	}
}

// Every core segment has a value, and nothing else does.
func TestCoreValuesKeys(t *testing.T) {
	t.Parallel()
	var want []string
	for _, s := range preset(t, "ops").Segments {
		if s.From == statusline.FromCore {
			want = append(want, s.ID())
		}
	}
	slices.Sort(want)
	for _, snap := range []statusline.Snapshot{{}, idle(), toolRunning()} {
		got := slices.Sorted(maps.Keys(statusline.CoreValues(snap, unicodeMarks())))
		if !slices.Equal(got, want) {
			t.Errorf("the values are for %q, want %q", got, want)
		}
	}
}

// The variables section 4 lists, and the few it does not, through formats
// of the user's own.
func TestCoreVariables(t *testing.T) {
	t.Parallel()
	snap := toolRunning()
	snap.Conn, snap.ConnErr = statusline.ConnDown, "connection refused"
	snap.Agents, snap.Scrolled = 3, true
	vals := statusline.CoreValues(snap, unicodeMarks())
	tests := []struct {
		name, format, want string
	}{
		{"state", "[$dot] $state: $err", "[●] offline, retrying: connection refused"},
		{"contact", "@$name", "@ada"},
		{"route", "$provider/$model", "deepseek/deepseek-flash"},
		{"cache", "$cached of $input ($pct%)", "1.5k of 3.2k (48%)"},
		{"tokens", "$input in, $output out", "3.2k in, 210 out"},
		{"latency", "$ms ms, or ${secs}s", "5400 ms, or 5.4s"},
		{"activity", "$what ($tool)", "running python (python)"},
		{"scroll", "more $newer", "more ↓"},
		{"agents", "$n agent$s", "3 agents"},
		{"clock", "at $time", "at 12:06"},
	}
	for _, tt := range tests {
		s := statusline.Segment{Name: tt.name, From: statusline.FromCore, On: true, Format: tt.format}
		if got, _ := s.Text(vals[s.ID()]); got != tt.want {
			t.Errorf("%s with %q shows %q, want %q", tt.name, tt.format, got, tt.want)
		}
	}
}

func TestSegmentText(t *testing.T) {
	t.Parallel()
	vars := map[string]string{"pct": "82", "secs": "7.2", "1": "one", "2": "", "a_b": "under", "A": "upper"}
	tests := []struct {
		name, format, want string
	}{
		{"no variable", "esc stop", "esc stop"},
		{"a variable, then text", "cache $pct%", "cache 82%"},
		{"braces end a name", "${secs}s", "7.2s"},
		{"without them the name runs on", "$secss", "$secss"},
		{"a digit is a whole name", "$1$10", "oneone0"},
		{"an empty variable is empty", "[$2]", "[]"},
		{"an underscore is part of a name", "$a_b", "under"},
		{"names keep their case", "$A $a", "upper $a"},
		{"a name with no variable is left as written", "$nope and ${nope}", "$nope and ${nope}"},
		{"two dollars are one", "$$5 $$pct", "$5 $pct"},
		{"a dollar at the end", "cost $", "cost $"},
		{"a dollar before a space", "$ 5", "$ 5"},
		{"a brace never closed", "${pct", "${pct"},
		{"an empty brace", "${}", "${}"},
		{"a brace around more than a name", "${pct }", "${pct }"},
		{"a braced digit", "${1}1", "one1"},
		{"variables side by side", "$pct$pct", "8282"},
		{"spaces at the ends go", "  $pct  ", "82"},
		{"a variable's own dollar is not read again", "$1", "one"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := statusline.Segment{Name: "x", From: statusline.FromShell, Format: tt.format, Short: tt.format}
			full, short := s.Text(statusline.Value{Vars: vars})
			if full != tt.want || short != tt.want {
				t.Errorf("%q shows %q, short %q; want %q", tt.format, full, short, tt.want)
			}
		})
	}

	// What is left when a control character goes has no space at its ends.
	out := statusline.Segment{Name: "x", From: statusline.FromShell, Format: "$out", Short: "$out"}
	hidden := statusline.Value{Vars: map[string]string{"out": "\x1b main\t\x07"}}
	if full, short := out.Text(hidden); full != "main" || short != "main" {
		t.Errorf("a value with spaces inside control characters shows %q, short %q", full, short)
	}
	// A value that holds a dollar sign is text, not a format.
	s := statusline.Segment{Name: "x", From: statusline.FromShell, Format: "$out"}
	if got, _ := s.Text(statusline.Value{Vars: map[string]string{"out": "$out $1 $$"}}); got != "$out $1 $$" {
		t.Errorf("a value with dollar signs shows %q", got)
	}
	// A segment from nowhere this package knows has no default format.
	nowhere := statusline.Segment{Name: "x", From: "panel"}
	if full, short := nowhere.Text(statusline.Value{Vars: vars}); full != "" || short != "" {
		t.Errorf("a segment from nowhere shows %q, %q", full, short)
	}
}

func TestModValue(t *testing.T) {
	t.Parallel()
	s := statusline.Segment{Name: "branch", From: "mod:git", On: true}
	tests := []struct {
		name              string
		text, short, tone string
		format            string
		full, wantShort   string
	}{
		{"text alone", "⎇ main", "", "", "", "⎇ main", ""},
		{"text and short", "cache avg 61%", "61%", "warn", "", "cache avg 61%", "61%"},
		{"an empty reply hides the segment", "", "61%", "", "", "", ""},
		{"a format of the user's own", "main", "", "mod", "on $text", "on main", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			seg := s
			seg.Format = tt.format
			v := statusline.ModValue(tt.text, tt.short, tt.tone)
			full, short := seg.Text(v)
			if full != tt.full || short != tt.wantShort {
				t.Errorf("shows %q, short %q; want %q, short %q", full, short, tt.full, tt.wantShort)
			}
			if v.Tone != tt.tone || v.Hide != (tt.text == "") {
				t.Errorf("tone %q, hide %v", v.Tone, v.Hide)
			}
		})
	}
}

func TestWithMods(t *testing.T) {
	t.Parallel()
	ids := func(c statusline.Config) []string {
		out := make([]string, 0, len(c.Segments))
		for _, s := range c.Segments {
			out = append(out, s.ID())
		}
		return out
	}
	git := statusline.ModSegment{Mod: "git", Name: "branch"}
	cache := statusline.ModSegment{Mod: "cache", Name: "cache"}
	added := func(m statusline.ModSegment) statusline.Segment {
		return statusline.Segment{Name: m.Name, From: "mod:" + m.Mod, On: true, Side: statusline.Right, Drop: 1}
	}

	t.Run("new ones go before the clock, on, first to give way", func(t *testing.T) {
		t.Parallel()
		base := preset(t, "default")
		got := base.WithMods([]statusline.ModSegment{git, cache})
		want := []string{
			"core:state", "core:contact", "core:route", "core:cache", "core:tokens", "core:latency",
			"core:activity", "core:stop", "core:scroll", "core:agents", "shell:load",
			"mod:git/branch", "mod:cache/cache", "core:clock",
		}
		if !slices.Equal(ids(got), want) {
			t.Errorf("the segments are %q, want %q", ids(got), want)
		}
		if s := got.Segments[11]; s != added(git) {
			t.Errorf("the new segment is %+v, want %+v", s, added(git))
		}
		for _, s := range got.Segments {
			if err := s.Validate(); err != nil {
				t.Errorf("%s: %v", s.ID(), err)
			}
		}
		if !reflect.DeepEqual(base, preset(t, "default")) {
			t.Error("WithMods changed its receiver")
		}
	})

	t.Run("one already listed is left as it is", func(t *testing.T) {
		t.Parallel()
		mine := added(git)
		mine.On, mine.Side, mine.Format = false, statusline.Left, "on $text"
		base := statusline.Config{Segments: []statusline.Segment{mine, {Name: "clock", On: true, Side: statusline.Right}}}
		got := base.WithMods([]statusline.ModSegment{git, cache, git})
		if want := []string{"mod:git/branch", "mod:cache/cache", "core:clock"}; !slices.Equal(ids(got), want) {
			t.Errorf("the segments are %q, want %q", ids(got), want)
		}
		if got.Segments[0] != mine {
			t.Errorf("the listed segment became %+v", got.Segments[0])
		}
	})

	t.Run("with no clock on the right they go last", func(t *testing.T) {
		t.Parallel()
		base := statusline.Config{Segments: []statusline.Segment{
			{Name: "clock", On: true, Side: statusline.Left},
			{Name: "state", On: true},
		}}
		got := base.WithMods([]statusline.ModSegment{git})
		if want := []string{"core:clock", "core:state", "mod:git/branch"}; !slices.Equal(ids(got), want) {
			t.Errorf("the segments are %q, want %q", ids(got), want)
		}
	})

	t.Run("a clock that is off still marks the place", func(t *testing.T) {
		t.Parallel()
		got := preset(t, "minimal")
		got.Segments[index(t, got, "core:clock")].On = false
		got = got.WithMods([]statusline.ModSegment{git})
		if at := index(t, got, "mod:git/branch"); ids(got)[at+1] != "core:clock" {
			t.Errorf("the segments are %q", ids(got))
		}
	})

	t.Run("an empty config", func(t *testing.T) {
		t.Parallel()
		got := statusline.Config{}.WithMods([]statusline.ModSegment{git})
		if len(got.Segments) != 1 || got.Segments[0] != added(git) {
			t.Errorf("got %+v", got.Segments)
		}
		if got := (statusline.Config{}).WithMods(nil); len(got.Segments) != 0 {
			t.Errorf("no mods gave %+v", got.Segments)
		}
	})

	t.Run("a name a file could not hold is left out", func(t *testing.T) {
		t.Parallel()
		got := statusline.Config{}.WithMods([]statusline.ModSegment{
			{Mod: "", Name: "x"},
			{Mod: "git", Name: ""},
			{Mod: "a/b", Name: "x"},
			{Mod: "git", Name: "two words"},
			{Mod: "git", Name: "esc\x1b"},
			{Mod: "git", Name: strings.Repeat("n", 65)},
			git,
		})
		if want := []string{"mod:git/branch"}; !slices.Equal(ids(got), want) {
			t.Errorf("the segments are %q, want %q", ids(got), want)
		}
	})
}

func TestValidate(t *testing.T) {
	t.Parallel()
	shell := func(change func(*statusline.Segment)) statusline.Segment {
		s := statusline.Segment{
			Name: "load", From: statusline.FromShell, Command: "uptime",
			Interval: 10 * time.Second, Timeout: time.Second,
		}
		change(&s)
		return s
	}
	tests := []struct {
		name    string
		segment statusline.Segment
		want    string // a part of the error; "" for none
	}{
		{"a core segment", statusline.Segment{Name: "cache", From: "core"}, ""},
		{"no From is core", statusline.Segment{Name: "cache"}, ""},
		{"a mod segment", statusline.Segment{Name: "branch", From: "mod:git"}, ""},
		{"a shell segment", shell(func(*statusline.Segment) {}), ""},
		{"a shell segment with no command yet", shell(func(s *statusline.Segment) { s.Command = "" }), ""},
		{"the timeout may equal the interval", shell(func(s *statusline.Segment) { s.Timeout = s.Interval }), ""},
		{"the least interval", shell(func(s *statusline.Segment) { s.Interval = time.Second }), ""},

		{"no name", statusline.Segment{From: "core"}, "needs a name"},
		{"a name with a space", shell(func(s *statusline.Segment) { s.Name = "my load" }), "space"},
		{"a name with an escape", shell(func(s *statusline.Segment) { s.Name = "lo\x1bad" }), "control character"},
		{"a long name", shell(func(s *statusline.Segment) { s.Name = strings.Repeat("n", 65) }), "longer than 64"},
		{"an unknown core segment", statusline.Segment{Name: "turn", From: "core"}, `no core segment is named "turn"`},
		{"an unknown From", statusline.Segment{Name: "x", From: "panel"}, "it can be core, mod:<name> or shell"},
		{"a mod with no name", statusline.Segment{Name: "x", From: "mod:"}, "it can be core"},
		{"a mod with a slash", statusline.Segment{Name: "x", From: "mod:a/b"}, "it can be core"},
		{"a negative drop", statusline.Segment{Name: "cache", Drop: -1}, "drop is -1"},
		{"a long format", statusline.Segment{Name: "cache", Format: strings.Repeat("x", 257)}, "format is longer than 256"},
		{"a long short", statusline.Segment{Name: "cache", Short: strings.Repeat("x", 257)}, "short is longer than 256"},
		{"an interval under a second", shell(func(s *statusline.Segment) { s.Interval = 999 * time.Millisecond }), "the least is 1s"},
		{"no interval", shell(func(s *statusline.Segment) { s.Interval = 0 }), "interval is 0s"},
		{"no timeout", shell(func(s *statusline.Segment) { s.Timeout = 0 }), "timeout is 0s; it must be more than 0"},
		{"a timeout over the interval", shell(func(s *statusline.Segment) { s.Timeout = 11 * time.Second }), "the most is the interval, 10s"},
		{"a match that is not an expression", shell(func(s *statusline.Segment) { s.Match = "(" }), "match is not an expression"},
		{"a long match", shell(func(s *statusline.Segment) { s.Match = strings.Repeat("x", 1025) }), "match is longer than 1024"},
		{"a long command", shell(func(s *statusline.Segment) { s.Command = strings.Repeat("x", 2049) }), "command is longer than 2048"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.segment.Validate()
			switch {
			case tt.want == "" && err != nil:
				t.Errorf("refused: %v", err)
			case tt.want != "" && err == nil:
				t.Errorf("accepted, want an error with %q", tt.want)
			case tt.want != "" && !strings.Contains(err.Error(), tt.want):
				t.Errorf("the error is %q, want it to hold %q", err, tt.want)
			}
			if err != nil && strings.ContainsAny(err.Error(), "\x1b\n") {
				t.Errorf("the error is not one plain line: %q", err)
			}
		})
	}
}

// Validate holds a number to the range a file may hold, so that what an
// editor saved after asking it is never a file Parse refuses.
func TestValidateNumbers(t *testing.T) {
	t.Parallel()
	if strconv.IntSize < 64 {
		t.Skip("an int holds nothing a file cannot")
	}
	// Shifted at run time: as constants these would not compile where an int
	// is 32 bits.
	most := 1
	most = most<<31 - 1
	tests := []struct {
		name    string
		segment statusline.Segment
		want    string // a part of the error; "" for none
	}{
		{"the highest drop", statusline.Segment{Name: "cache", Drop: most}, ""},
		{"a drop over it", statusline.Segment{Name: "cache", Drop: most + 1}, "drop is 2147483648; the most is 2147483647"},
		{"the highest warn_below", statusline.Segment{Name: "cache", WarnBelow: most, HasWarn: true}, ""},
		{"the lowest warn_below", statusline.Segment{Name: "cache", WarnBelow: -most - 1, HasWarn: true}, ""},
		{
			"a warn_below over the highest",
			statusline.Segment{Name: "cache", WarnBelow: most + 1, HasWarn: true},
			"warn_below is 2147483648; it can be -2147483648 to 2147483647",
		},
		{
			"a warn_below under the lowest",
			statusline.Segment{Name: "cache", WarnBelow: -most - 2, HasWarn: true},
			"warn_below is -2147483649",
		},
		{"a warn_below that does not count is not looked at", statusline.Segment{Name: "cache", WarnBelow: most + 1}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.segment.Validate()
			switch {
			case tt.want == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case tt.want != "" && err == nil:
				t.Fatalf("accepted, want an error with %q", tt.want)
			case tt.want != "" && !strings.Contains(err.Error(), tt.want):
				t.Errorf("the error is %q, want it to hold %q", err, tt.want)
			}
			// Validate and Parse agree about the file Encode writes.
			c := statusline.Config{Segments: []statusline.Segment{tt.segment}}
			if _, _, parseErr := statusline.Parse(c.Encode()); (parseErr != nil) != (err != nil) {
				t.Errorf("Validate says %v and Parse says %v", err, parseErr)
			}
		})
	}
}
