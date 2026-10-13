package statusline

import (
	"context"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/quinnovator/contactd/internal/tui/safe"
)

const (
	// shellRunes is how much of a command's output one variable holds.
	shellRunes = 64
	// maxOutput is how much of a command's output is kept, in bytes.
	maxOutput = 4 << 10
)

// Runner runs the command of a shell segment and returns what it printed.
// Run blocks, so the client calls it away from its update loop. An error
// means the command could not be run, failed, or took longer than timeout.
type Runner interface {
	Run(ctx context.Context, command string, timeout time.Duration) (string, error)
}

// ShellValue is the value of a shell segment after one run of its command:
// out is what Run returned and err its error.
//
// $out is the first line of the output that is not blank. With a Match, $1
// to $9 are the groups it found anywhere in the output, which it is run
// over as the command printed it. Each variable is then made safe to draw
// and holds at most 64 runes; with ascii, every character outside ASCII
// becomes "?". The value's number is $1 when there is one, and else $out,
// when that reads as a number.
//
// A run that failed, a Match that is not an expression and a Match that
// found nothing all show the segment's name and "?" in the dim tone.
//
// Only the first 4 KiB of out are looked at, whatever the Runner returned.
func ShellValue(seg Segment, out string, err error, ascii bool) Value {
	failed := Value{Text: safe.Line(seg.Name, shellRunes, ascii) + " ?", Tone: toneDim}
	if err != nil {
		return failed
	}
	// The client's own Runner keeps to this; another may not.
	if len(out) > maxOutput {
		out = out[:maxOutput]
	}
	vars := map[string]string{"out": safe.Line(firstLine(out), shellRunes, ascii)}
	number := vars["out"]
	if seg.Match != "" {
		re, err := regexp.Compile(seg.Match)
		if err != nil {
			return failed
		}
		groups := re.FindStringSubmatch(out)
		if groups == nil {
			return failed
		}
		for i := 1; i < len(groups) && i <= 9; i++ {
			vars[strconv.Itoa(i)] = safe.Line(groups[i], shellRunes, ascii)
		}
		if len(groups) > 1 {
			number = vars["1"]
		}
	}
	v := Value{Vars: vars}
	if n, err := strconv.ParseFloat(strings.TrimSpace(number), 64); err == nil && !math.IsInf(n, 0) && !math.IsNaN(n) {
		v.Num, v.HasNum = n, true
	}
	return v
}

// firstLine returns the first line that has something on it.
func firstLine(s string) string {
	for line := range strings.SplitSeq(s, "\n") {
		if strings.TrimSpace(line) != "" {
			return line
		}
	}
	return ""
}

// Due returns the shell segments to run now: those that are on, have a
// command, and last finished a run at least their interval ago. last holds
// when each segment's latest run finished, keyed by Segment.ID; a segment
// not in it has never run and is due.
//
// The caller keeps a segment that is still running from starting again: Due
// knows only what finished.
func Due(c Config, last map[string]time.Time, now time.Time) []Segment {
	var due []Segment
	seen := make(map[string]bool)
	for _, s := range c.Segments {
		if k, _ := s.kind(); k != kindShell || !s.On || strings.TrimSpace(s.Command) == "" {
			continue
		}
		id := s.ID()
		if seen[id] {
			continue
		}
		seen[id] = true
		if at, ran := last[id]; ran && now.Sub(at) < max(s.Interval, MinInterval) {
			continue
		}
		due = append(due, s)
	}
	return due
}
