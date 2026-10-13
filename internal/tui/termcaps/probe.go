package termcaps

import (
	"strconv"
	"strings"
)

// The questions. Each is a whole sequence.
const (
	// askVersion is XTVERSION: the terminal's name and version.
	askVersion = "\x1b[>q"
	// askCell asks for the size of a cell in pixels.
	askCell = "\x1b[16t"
	// askDA1 asks for the primary device attributes. Every terminal answers
	// it, so it goes last: its answer says the others have had their turn.
	askDA1 = "\x1b[c"
)

// PlanFor chooses what to ask the terminal. The first rule that matches
// wins:
//
//   - ProbeOff when probing is switched off; when TERM is empty, dumb, linux,
//     ansi or a DEC VT; and in GNU screen (TERM starts with "screen", or STY
//     is set, and TMUX is not).
//   - ProbeBasic when TERM_PROGRAM contains "Apple": Apple Terminal.
//   - ProbeTmux when TMUX is set or TERM starts with "tmux".
//   - ProbeFull otherwise.
func PlanFor(env Env, o Overrides) Plan {
	plan, _ := planFor(env, o)
	return plan
}

// planFor is PlanFor with the reason.
func planFor(env Env, o Overrides) (Plan, string) {
	switch {
	case o.NoProbe:
		return ProbeOff, "probing is switched off"
	case plainTerm(env.Term):
		return ProbeOff, termIs(env)
	case gnuScreen(env):
		return ProbeOff, "GNU screen"
	case appleTerminal(env):
		return ProbeBasic, "Apple Terminal is asked for its background only"
	case inTmux(env):
		return ProbeTmux, "tmux"
	default:
		return ProbeFull, ""
	}
}

// appleTerminal reports whether TERM_PROGRAM names Apple's Terminal.
func appleTerminal(env Env) bool {
	return strings.Contains(env.TermProgram, "Apple")
}

// termIs says what TERM is, for a reason.
func termIs(env Env) string {
	if env.Term == "" {
		return "TERM is not set"
	}
	return "TERM is " + quote(env.Term)
}

// ProbeBytes returns the bytes of the one tea.Raw sent from Init; "" for Off
// and Basic. The background colour is asked through Bubble Tea, not here.
//
// The full plan asks for kitty graphics (query id 31), the terminal's name,
// the cell size, then the device attributes. The tmux plan asks tmux for its
// name, the cell size and the device attributes, then sends the passthrough
// round to the terminal behind it.
func ProbeBytes(p Plan) string {
	switch p {
	case ProbeOff, ProbeBasic:
		return ""
	case ProbeFull:
		return kittyQuery(kittyDirect) + askVersion + askCell + askDA1
	case ProbeTmux:
		return askVersion + askCell + askDA1 + TmuxRoundBytes()
	default:
		return ""
	}
}

// TmuxRoundBytes returns the passthrough round, for a tmux only discovered
// from XTVERSION: one tmux passthrough wrapper around a kitty graphics query
// (id 32), the name question and the device attributes. With passthrough
// switched off tmux drops it whole, and no second device-attributes reply
// comes.
func TmuxRoundBytes() string {
	return passthrough(kittyQuery(kittyOuter) + askVersion + askDA1)
}

// kittyQuery asks whether the terminal speaks the kitty graphics protocol:
// a one-pixel image that is checked and not stored.
func kittyQuery(id int) string {
	return "\x1b_Gi=" + strconv.Itoa(id) + ",s=1,v=1,a=q,t=d,f=24;AAAA\x1b\\"
}

// passthrough wraps sequences for the terminal behind tmux: a DCS that
// starts with "tmux;", with every ESC inside doubled.
func passthrough(seq string) string {
	return "\x1bPtmux;" + strings.ReplaceAll(seq, "\x1b", "\x1b\x1b") + "\x1b\\"
}
