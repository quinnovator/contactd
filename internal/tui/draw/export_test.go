package draw

import "time"

// MaxMarkdown is the longest reply that is read as Markdown, in bytes.
const MaxMarkdown = maxMarkdown

// Counts returns how many replies the Renderer cut into parts, how often it
// called the Markdown renderer, and how many source lines of the reply in
// flight it wrapped.
func (r *Renderer) Counts() (splits, markdowns, liveWraps int) {
	return r.n.splits, r.n.markdowns, r.n.liveWraps
}

// Folded is folded.
func (r *Renderer) Folded(text string, w, limit int) (rows []string, hidden int) {
	return r.folded(text, w, limit)
}

// TrimRight is trimRight.
func TrimRight(line string) string { return trimRight(line) }

// Links is links.
func Links(lines []string) []string { return links(lines) }

// Definitions is definitions.
func Definitions(src string) string { return definitions(src) }

// Seconds is seconds.
func Seconds(d time.Duration) string { return seconds(d) }

// Unsound returns how many renderings the Renderer threw away because they
// held something a terminal would act on.
func (r *Renderer) Unsound() int { return r.n.unsound }

// Shield is shield.
func (r *Renderer) Shield(src string) (out, mark string, ok bool) { return r.shield(src) }

// Sound is sound.
func (r *Renderer) Sound(out string) bool { return r.sound(out) }

// StandIn is standIn.
func StandIn(s string) string { return standIn(s) }

// MaxNest and MaxWork are maxNest and maxWork.
const (
	MaxNest = maxNest
	MaxWork = maxWork
)

// Weigh is weigh.
func Weigh(src string) (depth, work, plain int, fences []string) {
	l := weigh(src)
	return l.depth, l.work, l.plain, l.fences
}
