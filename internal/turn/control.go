package turn

import "sync/atomic"

// Phase is what an in-flight turn is doing, which decides how it may be
// interrupted (Piece 7).
type Phase int32

const (
	// PhasePlan covers recall, planning and waiting for the first token.
	PhasePlan Phase = iota
	// PhaseStream means reply text is streaming.
	PhaseStream
	// PhaseTool means a tool is executing. It is never killed, since it may
	// have side effects.
	PhaseTool
	// phaseToolReplan is PhaseTool with a new message waiting: finish the
	// tool, append its result, then re-plan instead of continuing.
	phaseToolReplan
	// phaseCancelled means the actor has cancelled the turn. No tool may
	// start afterwards.
	phaseCancelled
)

// Control is the one piece of state an actor and its in-flight turn share.
// Every transition is a compare-and-swap on a single word, so exactly one of
// "the turn was cancelled" and "the turn will re-plan after its tool" holds,
// with no window in between.
type Control struct {
	state atomic.Int32
}

// Phase returns the current phase. The replan and cancelled sub-states report
// as PhaseTool and PhaseStream.
func (c *Control) Phase() Phase {
	switch p := Phase(c.state.Load()); p {
	case phaseToolReplan:
		return PhaseTool
	case phaseCancelled:
		return PhaseStream
	case PhasePlan, PhaseStream, PhaseTool:
		return p
	default:
		return p
	}
}

// Interrupt is called by the actor when a new message arrives. It reports
// whether the actor should cancel the turn. During a tool run it returns
// false and flags the turn to re-plan once the tool has finished.
func (c *Control) Interrupt() (cancel bool) {
	for {
		old := c.state.Load()
		switch Phase(old) {
		case PhaseTool:
			if c.state.CompareAndSwap(old, int32(phaseToolReplan)) {
				return false
			}
		case phaseToolReplan:
			return false
		case phaseCancelled:
			return true
		case PhasePlan, PhaseStream:
			if c.state.CompareAndSwap(old, int32(phaseCancelled)) {
				return true
			}
		}
	}
}

// Cancel marks the turn cancelled regardless of phase. The actor uses it at
// shutdown, when even a running tool has to stop.
func (c *Control) Cancel() { c.state.Store(int32(phaseCancelled)) }

// streaming marks the first token. It is a no-op once cancelled.
func (c *Control) streaming() {
	c.state.CompareAndSwap(int32(PhasePlan), int32(PhaseStream))
}

// beginTool enters the tool phase. It fails if the turn was cancelled first.
func (c *Control) beginTool() bool {
	for {
		old := c.state.Load()
		if Phase(old) == phaseCancelled {
			return false
		}
		if c.state.CompareAndSwap(old, int32(PhaseTool)) {
			return true
		}
	}
}

// endTool leaves the tool phase and reports whether a re-plan was requested
// while the tool ran.
func (c *Control) endTool() (replan bool) {
	for {
		old := c.state.Load()
		if Phase(old) == phaseCancelled {
			return false
		}
		if c.state.CompareAndSwap(old, int32(PhasePlan)) {
			return Phase(old) == phaseToolReplan
		}
	}
}
