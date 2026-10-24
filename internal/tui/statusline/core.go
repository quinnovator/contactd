package statusline

import (
	"slices"
	"strconv"
)

const (
	// DefaultPreset is the preset a client with no file uses.
	DefaultPreset = "default"

	// ConnConnecting is the Snapshot.Conn of a connection being opened, for
	// the first time or again.
	ConnConnecting = "connecting"
	// ConnLive is the Snapshot.Conn of a connection that is open.
	ConnLive = "live"
	// ConnDown is the Snapshot.Conn of a connection that failed and will be
	// tried again.
	ConnDown = "down"
	// ConnGone is the Snapshot.Conn of a contact that no longer exists.
	ConnGone = "gone"

	// The tone names this package gives out. They are the ones mods use and
	// the theme draws.
	toneDim  = "dim"
	toneTool = "tool"
	toneOK   = "ok"
	toneWarn = "warn"
	toneBad  = "bad"
)

// coreNames lists the core segments in the order a preset shows them.
func coreNames() []string {
	return []string{
		"state", "contact", "route", "cache", "tokens", "latency",
		"activity", "stop", "scroll", "agents", "clock",
	}
}

// coreSegment returns a core segment as it is by default: its side, its
// formats, its turn to give way. It is switched off.
func coreSegment(name string) (Segment, bool) {
	s := Segment{Name: name, From: FromCore}
	switch name {
	case "state":
		s.Side, s.Format, s.Short = Left, "$dot $state", "$dot $state"
	case "contact":
		s.Side, s.Format, s.Drop = Left, "$name", 5
	case "route":
		s.Side, s.Format, s.Drop = Left, "$model", 2
	case "cache":
		s.Side, s.Format, s.Short, s.Drop = Left, "cache $pct%", "$pct%", 4
		s.WarnBelow, s.HasWarn = 50, true
	case "tokens":
		s.Side, s.Format, s.Drop = Left, "$input tok", 1
	case "latency":
		s.Side, s.Format, s.Drop = Left, "${secs}s", 3
	case "activity":
		s.Side, s.Format, s.Short, s.Drop = Right, "$what", "$tool", 6
	case "stop":
		s.Side, s.Format, s.Short = Right, "esc stop", "esc"
	case "scroll":
		s.Side, s.Format, s.Short = Right, "$newer newer", "$newer"
	case "agents":
		s.Side, s.Format, s.Short, s.Drop = Right, "$n sub-agent$s", "$n", 1
	case "clock":
		s.Side, s.Format, s.Short, s.Drop = Right, "$time", "$time", 7
	default:
		return Segment{}, false
	}
	return s, true
}

// loadSegment is the shell segment every preset lists, switched off, so
// that the editor has one to show.
func loadSegment() Segment {
	return Segment{
		Name: "load", From: FromShell, Side: Right, Drop: 1,
		Command: "uptime", Match: `load averages?: ([0-9.]+)`, Format: "load $1",
		Interval: DefaultInterval, Timeout: DefaultTimeout,
	}
}

// Presets lists the presets by name: minimal, default, ops.
func Presets() []string { return []string{"minimal", DefaultPreset, "ops"} }

// Preset returns a preset. Each lists every core segment and the load shell
// segment, in the same order; they differ only in which are on. No preset
// switches a shell segment on. The Config is the caller's to change.
func Preset(name string) (Config, bool) {
	var on []string
	switch name {
	case "minimal":
		on = []string{"state", "cache", "stop", "scroll", "clock"}
	case DefaultPreset:
		on = []string{"state", "route", "cache", "tokens", "latency", "activity", "stop", "scroll", "clock"}
	case "ops":
		on = []string{
			"state", "contact", "route", "cache", "tokens", "latency",
			"activity", "stop", "scroll", "agents", "clock",
		}
	default:
		return Config{}, false
	}
	names := coreNames()
	segments := make([]Segment, 0, len(names)+1)
	for _, n := range names {
		if n == "clock" {
			segments = append(segments, loadSegment())
		}
		s, _ := coreSegment(n)
		s.On = slices.Contains(on, n)
		segments = append(segments, s)
	}
	return Config{Segments: segments}, true
}

func defaultPreset() Config {
	c, _ := Preset(DefaultPreset)
	return c
}

// Value is what one segment has to show right now.
type Value struct {
	// Vars are the variables the segment's formats may name.
	Vars map[string]string
	// Tone is "", or one of dim, accent, tool, ok, warn, bad, mod.
	Tone string
	// Num is compared with the segment's WarnBelow. It counts only with
	// HasNum.
	Num    float64
	HasNum bool
	// Hide leaves the segment out.
	Hide bool
	// Text, when not empty, is shown in place of the format, and in place
	// of the short format when the segment has one. It is for a value no
	// format fits, such as "call failed".
	Text string
}

// ModValue is the value of a mod segment, from the mod's reply: its text,
// its short form and its tone. The default formats of a mod segment are
// "$text" and "$short". An empty text hides the segment.
func ModValue(text, short, tone string) Value {
	return Value{
		Vars: map[string]string{"text": text, "short": short},
		Tone: tone,
		Hide: text == "",
	}
}

// Snapshot is what the client knows that a core segment can show.
type Snapshot struct {
	// Conn is the connection: ConnConnecting, ConnLive, ConnDown or
	// ConnGone. ConnErr says why it is down.
	Conn, ConnErr string
	// Contact is the contact's label. Provider and Model are its route.
	Contact, Provider, Model string
	// HasCall says there is a last model call; CallFailed that it failed,
	// and CallCanceled that the caller stopped it.
	// Input, Cached and Output are its token counts and MS how long it
	// took.
	HasCall, CallFailed, CallCanceled bool
	CacheReported                     bool // the cache count was present, including zero
	Input, Cached, Output             int
	MS                                int64
	// Replying says a reply is in flight. Running names the tool that is
	// executing.
	Replying bool
	Running  string
	// LoadingOlder says an earlier page is being read. Scrolled says the
	// view is not following the newest line.
	LoadingOlder, Scrolled bool
	// Agents is how many sub-agents are at work.
	Agents int
	// Clock is the time, already formatted, "15:04".
	Clock string
}

// Marks are the glyphs core segments draw, in the glyph set in use.
type Marks struct {
	// Dot stands before the connection state. Newer points at newer lines
	// below.
	Dot, Newer string
}

// CoreValues returns the value of every core segment, keyed by Segment.ID.
// A segment with nothing to show has Hide set.
//
// The variables, by segment:
//
//	state     $dot $state $err
//	contact   $name
//	route     $model $provider
//	cache     $pct $cached $input
//	tokens    $input $output
//	latency   $secs $ms
//	activity  $what $tool
//	scroll    $newer
//	agents    $n $s
//	clock     $time
//
// Token counts are written short: 950, 3.2k, 1.5M. $s is "s" unless $n is 1.
// $tool is the running tool alone, and is empty when none runs.
func CoreValues(s Snapshot, m Marks) map[string]Value {
	hidden := Value{Hide: true}
	vals := map[string]Value{
		"core:state":    stateValue(s, m),
		"core:contact":  {Vars: map[string]string{"name": s.Contact}, Hide: s.Contact == ""},
		"core:route":    {Vars: map[string]string{"model": s.Model, "provider": s.Provider}, Hide: s.Model == ""},
		"core:cache":    cacheValue(s),
		"core:tokens":   hidden,
		"core:latency":  hidden,
		"core:activity": activityValue(s),
		"core:stop":     {Tone: toneDim, Hide: !s.Replying && s.Running == ""},
		"core:scroll":   {Vars: map[string]string{"newer": m.Newer}, Tone: toneDim, Hide: !s.Scrolled},
		"core:agents":   hidden,
		"core:clock":    {Vars: map[string]string{"time": s.Clock}, Hide: s.Clock == ""},
	}
	if s.HasCall && (s.Input > 0 || s.Output > 0) {
		vals["core:tokens"] = Value{Vars: map[string]string{"input": count(s.Input), "output": count(s.Output)}}
	}
	if s.HasCall && s.MS > 0 {
		vals["core:latency"] = Value{Vars: map[string]string{
			"secs": strconv.FormatFloat(float64(s.MS)/1000, 'f', 1, 64),
			"ms":   strconv.FormatInt(s.MS, 10),
		}}
	}
	if s.Agents > 0 {
		plural := "s"
		if s.Agents == 1 {
			plural = ""
		}
		vals["core:agents"] = Value{Vars: map[string]string{"n": strconv.Itoa(s.Agents), "s": plural}}
	}
	return vals
}

// stateValue is the connection: a word, and a tone that says the same.
func stateValue(s Snapshot, m Marks) Value {
	vars := map[string]string{"dot": m.Dot, "err": s.ConnErr}
	v := Value{Vars: vars}
	switch s.Conn {
	case ConnLive:
		vars["state"], v.Tone = "live", toneOK
	case ConnConnecting:
		vars["state"], v.Tone = "connecting", toneDim
	case ConnDown:
		vars["state"], v.Tone = "offline, retrying", toneWarn
	case ConnGone:
		// Nothing is connected or will be, so there is no dot.
		vars["dot"], vars["state"], v.Tone = "", "gone", toneBad
	default:
		return Value{Hide: true}
	}
	return v
}

// cacheValue is how much of the last prompt came from the provider's cache.
func cacheValue(s Snapshot) Value {
	switch {
	case !s.HasCall:
		return Value{Hide: true}
	case s.CallCanceled:
		return Value{Text: "stopped", Tone: toneDim}
	case s.CallFailed:
		return Value{Text: "call failed", Tone: toneBad}
	case s.Input <= 0 || !s.CacheReported:
		return Value{Hide: true}
	}
	pct := min(max(100*int64(s.Cached)/int64(s.Input), 0), 100)
	return Value{
		Vars: map[string]string{
			"pct":    strconv.FormatInt(pct, 10),
			"cached": count(s.Cached),
			"input":  count(s.Input),
		},
		Num:    float64(pct),
		HasNum: true,
	}
}

// activityValue is what is happening now: the first of a running tool, a
// reply being written, and an earlier page being read.
func activityValue(s Snapshot) Value {
	var what, tool, tone string
	switch {
	case s.Running != "":
		what, tool, tone = "running "+s.Running, s.Running, toneTool
	case s.Replying:
		what, tone = "replying", toneDim
	case s.LoadingOlder:
		what, tone = "loading earlier", toneDim
	default:
		return Value{Hide: true}
	}
	return Value{Vars: map[string]string{"what": what, "tool": tool}, Tone: tone}
}

// count writes a token count short: 950, 3.2k, 1.5M.
func count(n int) string {
	switch {
	case n < 1000:
		return strconv.Itoa(n)
	case n < 999_950:
		return strconv.FormatFloat(float64(n)/1e3, 'f', 1, 64) + "k"
	}
	return strconv.FormatFloat(float64(n)/1e6, 'f', 1, 64) + "M"
}
