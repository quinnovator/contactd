package termcaps_test

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/quinnovator/contactd/internal/tui/termcaps"
	"github.com/quinnovator/contactd/internal/tui/tuitest"
)

func TestPlanFor(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		env  termcaps.Env
		o    termcaps.Overrides
		want termcaps.Plan
	}{
		{name: "an ordinary terminal", env: xterm(), want: termcaps.ProbeFull},
		{name: "kitty", env: kitty(), want: termcaps.ProbeFull},
		{name: "iTerm2", env: xterm(program("iTerm.app")), want: termcaps.ProbeFull},
		{name: "over SSH", env: xterm(func(e *termcaps.Env) { e.SSHTTY = "/dev/pts/0" }), want: termcaps.ProbeFull},
		{name: "vte-256color is a modern terminal", env: xterm(term("vte-256color")), want: termcaps.ProbeFull},
		{name: "switched off", env: kitty(), o: termcaps.Overrides{NoProbe: true}, want: termcaps.ProbeOff},
		{name: "switched off in tmux", env: tmux(), o: termcaps.Overrides{NoProbe: true}, want: termcaps.ProbeOff},
		{name: "TERM empty", env: xterm(term("")), want: termcaps.ProbeOff},
		{name: "no environment at all", env: termcaps.Env{}, want: termcaps.ProbeOff},
		{name: "TERM=dumb", env: xterm(term("dumb")), want: termcaps.ProbeOff},
		{name: "TERM=linux", env: xterm(term("linux")), want: termcaps.ProbeOff},
		{name: "TERM=ansi", env: xterm(term("ansi")), want: termcaps.ProbeOff},
		{name: "TERM=vt100", env: xterm(term("vt100")), want: termcaps.ProbeOff},
		{name: "TERM=vt220", env: xterm(term("vt220")), want: termcaps.ProbeOff},
		{name: "TERM=vt52", env: xterm(term("vt52")), want: termcaps.ProbeOff},
		{name: "TERM=dumb in tmux", env: tmux(term("dumb")), want: termcaps.ProbeOff},
		{name: "the glyph setting does not bring probing back", env: xterm(term("linux")), o: termcaps.Overrides{Glyphs: "unicode"}, want: termcaps.ProbeOff},
		{name: "TERM=screen without TMUX is GNU screen", env: xterm(term("screen")), want: termcaps.ProbeOff},
		{name: "TERM=screen-256color without TMUX", env: xterm(term("screen-256color")), want: termcaps.ProbeOff},
		{name: "TERM=screen.xterm-256color without TMUX", env: xterm(term("screen.xterm-256color")), want: termcaps.ProbeOff},
		{name: "STY set, whatever TERM says", env: xterm(func(e *termcaps.Env) { e.Sty = "1234.pts-0.host" }), want: termcaps.ProbeOff},
		{name: "TERM=screen-256color with TMUX is tmux", env: tmux(term("screen-256color")), want: termcaps.ProbeTmux},
		{name: "STY left over inside tmux", env: tmux(func(e *termcaps.Env) { e.Sty = "1234.pts-0.host" }), want: termcaps.ProbeTmux},
		{name: "tmux", env: tmux(), want: termcaps.ProbeTmux},
		{name: "TMUX set with an xterm TERM", env: tmux(term("xterm-256color")), want: termcaps.ProbeTmux},
		{name: "TERM=tmux-256color without TMUX", env: xterm(term("tmux-256color")), want: termcaps.ProbeTmux},
		{name: "TERM=TMUX-256COLOR in capitals", env: xterm(term("TMUX-256COLOR")), want: termcaps.ProbeTmux},
		{name: "a TERM that mentions tmux later is not tmux", env: xterm(term("xterm-tmux")), want: termcaps.ProbeFull},
		{name: "TERM=SCREEN in capitals", env: xterm(term("SCREEN")), want: termcaps.ProbeOff},
		{name: "a TERM that mentions screen later is not GNU screen", env: xterm(term("xterm-screen")), want: termcaps.ProbeFull},
		{
			name: "STY set with a tmux TERM and no TMUX is GNU screen",
			env:  xterm(term("tmux-256color"), func(e *termcaps.Env) { e.Sty = "1234.pts-0.host" }), want: termcaps.ProbeOff,
		},
		{name: "Apple Terminal", env: xterm(program("Apple_Terminal")), want: termcaps.ProbeBasic},
		{
			name: "Apple Terminal with tmux in between is still only asked for its background",
			env:  tmux(program("Apple_Terminal")), want: termcaps.ProbeBasic,
		},
		{name: "Apple Terminal with TERM=dumb", env: xterm(program("Apple_Terminal"), term("dumb")), want: termcaps.ProbeOff},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := termcaps.PlanFor(tc.env, tc.o); got != tc.want {
				t.Errorf("PlanFor = %v, want %v", got, tc.want)
			}
		})
	}
}

// sequences splits probe bytes into the escape sequences they are made of,
// and fails on anything that is not a whole CSI, APC or DCS sequence. A DCS
// is read the way tmux reads one: ESC ESC is an ESC that belongs to the
// payload, and ESC \ ends it.
func sequences(s string) ([]string, error) {
	var out []string
	for len(s) > 0 {
		n, err := sequence(s)
		if err != nil {
			return out, err
		}
		out = append(out, s[:n])
		s = s[n:]
	}
	return out, nil
}

// sequence returns the length of the escape sequence s starts with.
func sequence(s string) (int, error) {
	if len(s) < 2 || s[0] != 0x1B {
		return 0, errors.New("bytes outside an escape sequence: " + strconv.Quote(s))
	}
	switch s[1] {
	case '[':
		i := 2
		for i < len(s) && s[i] >= 0x30 && s[i] <= 0x3F {
			i++
		}
		for i < len(s) && s[i] >= 0x20 && s[i] <= 0x2F {
			i++
		}
		if i == len(s) || s[i] < 0x40 || s[i] > 0x7E {
			return 0, errors.New("a CSI sequence with no final byte: " + strconv.Quote(s))
		}
		return i + 1, nil
	case '_':
		for i := 2; i < len(s); i++ {
			switch {
			case s[i] == 0x1B && i+1 < len(s) && s[i+1] == '\\':
				return i + 2, nil
			case s[i] < 0x20 || s[i] > 0x7E:
				return 0, errors.New("an APC sequence broken by a control byte: " + strconv.Quote(s))
			}
		}
		return 0, errors.New("an APC sequence with no terminator: " + strconv.Quote(s))
	case 'P':
		for i := 2; i < len(s); i++ {
			if s[i] != 0x1B {
				continue
			}
			switch {
			case i+1 < len(s) && s[i+1] == '\\':
				return i + 2, nil
			case i+1 < len(s) && s[i+1] == 0x1B:
				i++
			default:
				return 0, errors.New("a DCS sequence with a lone ESC inside: " + strconv.Quote(s))
			}
		}
		return 0, errors.New("a DCS sequence with no terminator: " + strconv.Quote(s))
	default:
		return 0, errors.New("an escape sequence of a kind the probe does not send: " + strconv.Quote(s))
	}
}

// unwrap returns what a tmux passthrough sequence carries to the terminal
// behind tmux, and whether seq is one.
func unwrap(seq string) (string, bool) {
	body, ok := strings.CutPrefix(seq, "\x1bPtmux;")
	if !ok {
		return "", false
	}
	body, ok = strings.CutSuffix(body, "\x1b\\")
	if !ok {
		return "", false
	}
	return strings.ReplaceAll(body, "\x1b\x1b", "\x1b"), true
}

// listing writes probe bytes one sequence to a line, quoted, with what a
// passthrough carries indented under it. It fails the test when any
// sequence, at either level, is not terminated.
func listing(t *testing.T, s string) (text string, outer, inner []string) {
	t.Helper()
	var b strings.Builder
	outer, err := sequences(s)
	if err != nil {
		t.Fatalf("probe bytes %q: %v", s, err)
	}
	for _, seq := range outer {
		b.WriteString(strconv.Quote(seq) + "\n")
		carried, ok := unwrap(seq)
		if !ok {
			continue
		}
		if strings.Count(seq, "\x1b") != 2*strings.Count(carried, "\x1b")+2 {
			t.Errorf("passthrough %q: not every ESC inside is doubled", seq)
		}
		inner, err = sequences(carried)
		if err != nil {
			t.Fatalf("passthrough %q carries %q: %v", seq, carried, err)
		}
		for _, in := range inner {
			b.WriteString("    carries " + strconv.Quote(in) + "\n")
		}
	}
	return b.String(), outer, inner
}

const (
	wantVersion = "\x1b[>q"
	wantCell    = "\x1b[16t"
	wantDA1     = "\x1b[c"
)

func wantKitty(id int) string {
	return "\x1b_Gi=" + strconv.Itoa(id) + ",s=1,v=1,a=q,t=d,f=24;AAAA\x1b\\"
}

func TestProbeBytes(t *testing.T) {
	t.Parallel()

	t.Run("off and basic send nothing", func(t *testing.T) {
		t.Parallel()
		for _, p := range []termcaps.Plan{termcaps.ProbeOff, termcaps.ProbeBasic, 0, 99} {
			if got := termcaps.ProbeBytes(p); got != "" {
				t.Errorf("ProbeBytes(%v) = %q, want nothing", p, got)
			}
		}
	})

	t.Run("full", func(t *testing.T) {
		t.Parallel()
		text, outer, inner := listing(t, termcaps.ProbeBytes(termcaps.ProbeFull))
		tuitest.Golden(t, "probe_full.golden", []byte(text))
		want := []string{wantKitty(31), wantVersion, wantCell, wantDA1}
		if strings.Join(outer, "|") != strings.Join(want, "|") {
			t.Errorf("sequences %q, want %q", outer, want)
		}
		if len(inner) != 0 {
			t.Errorf("the full plan sends a passthrough: %q", inner)
		}
	})

	t.Run("tmux", func(t *testing.T) {
		t.Parallel()
		sent := termcaps.ProbeBytes(termcaps.ProbeTmux)
		text, outer, inner := listing(t, sent)
		tuitest.Golden(t, "probe_tmux.golden", []byte(text))
		want := []string{wantVersion, wantCell, wantDA1, termcaps.TmuxRoundBytes()}
		if strings.Join(outer, "|") != strings.Join(want, "|") {
			t.Errorf("sequences %q, want %q", outer, want)
		}
		wantInner := []string{wantKitty(32), wantVersion, wantDA1}
		if strings.Join(inner, "|") != strings.Join(wantInner, "|") {
			t.Errorf("the passthrough carries %q, want %q", inner, wantInner)
		}
		// tmux swallows a kitty query sent to it directly, so none is.
		if strings.HasPrefix(sent, "\x1b_G") || strings.Contains(sent, "i=31") {
			t.Errorf("the tmux plan asks tmux itself about kitty graphics: %q", sent)
		}
	})

	t.Run("the passthrough round alone", func(t *testing.T) {
		t.Parallel()
		round := termcaps.TmuxRoundBytes()
		text, outer, inner := listing(t, round)
		tuitest.Golden(t, "tmux_round.golden", []byte(text))
		if len(outer) != 1 {
			t.Fatalf("the round is %d sequences, want one wrapper: %q", len(outer), outer)
		}
		want := []string{wantKitty(32), wantVersion, wantDA1}
		if strings.Join(inner, "|") != strings.Join(want, "|") {
			t.Errorf("the passthrough carries %q, want %q", inner, want)
		}
		// Written out by hand: one wrapper, every ESC inside it doubled.
		if byHand := "\x1bPtmux;\x1b\x1b_Gi=32,s=1,v=1,a=q,t=d,f=24;AAAA\x1b\x1b\\\x1b\x1b[>q\x1b\x1b[c\x1b\\"; round != byHand {
			t.Errorf("TmuxRoundBytes = %q, want %q", round, byHand)
		}
	})

	// The device-attributes question is the one every terminal answers, so
	// it is the last thing each terminal is asked.
	t.Run("device attributes are asked last", func(t *testing.T) {
		t.Parallel()
		for _, p := range []termcaps.Plan{termcaps.ProbeFull, termcaps.ProbeTmux} {
			_, outer, inner := listing(t, termcaps.ProbeBytes(p))
			direct := outer
			if len(inner) > 0 {
				direct = outer[:len(outer)-1]
				if inner[len(inner)-1] != wantDA1 {
					t.Errorf("%v: the passthrough ends with %q", p, inner[len(inner)-1])
				}
			}
			if direct[len(direct)-1] != wantDA1 {
				t.Errorf("%v: the direct questions end with %q", p, direct[len(direct)-1])
			}
		}
	})

	// Modes 2026 and 2027 are asked by Bubble Tea, and the background
	// colour through it.
	t.Run("nothing Bubble Tea asks is asked twice", func(t *testing.T) {
		t.Parallel()
		for _, p := range []termcaps.Plan{termcaps.ProbeFull, termcaps.ProbeTmux} {
			got := termcaps.ProbeBytes(p)
			for _, not := range []string{"\x1b]", "$p", "?2026", "?2027", "?u"} {
				if strings.Contains(got, not) {
					t.Errorf("ProbeBytes(%v) = %q contains %q", p, got, not)
				}
			}
		}
	})
}

// The check that every sequence is terminated can fail.
func TestSequencesRefusesWhatIsCutShort(t *testing.T) {
	t.Parallel()
	bad := []string{
		"\x1b", "\x1b[", "\x1b[16", "\x1b[>", "plain", "\x1b[c tail",
		"\x1b_Gi=31;AAAA", "\x1b_Gi=31;AAAA\x1b", "\x1b_Gi=31;\x07", "\x1b_Gi=31;AA\x1b[c\x1b\\",
		"\x1bPtmux;\x1b\x1b[c", "\x1bPtmux;\x1b[c\x1b\\", "\x1bPtmux;\x1b\x1b[c\x1b",
		"\x1b]11;?\x07", "\x1bc",
	}
	for _, s := range bad {
		if got, err := sequences(s); err == nil {
			t.Errorf("sequences(%q) = %q, want an error", s, got)
		}
	}
	good := map[string]int{
		"": 0, "\x1b[c": 1, "\x1b[>q\x1b[16t": 2, "\x1b_Ga=q;AAAA\x1b\\\x1b[c": 2,
		"\x1bPtmux;\x1b\x1b[c\x1b\\": 1, "\x1bPtmux;\x1b\x1b_G;A\x1b\x1b\\\x1b\\\x1b[c": 2,
	}
	for s, n := range good {
		if got, err := sequences(s); err != nil || len(got) != n {
			t.Errorf("sequences(%q) = %q, %v, want %d sequences", s, got, err, n)
		}
	}
	if _, ok := unwrap("\x1b[c"); ok {
		t.Error("unwrap took a CSI sequence for a passthrough")
	}
}
