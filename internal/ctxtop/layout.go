package ctxtop

import (
	"math"

	"github.com/quinnovator/contactd/internal/diagnostic"
)

type cell struct {
	tier       int // -1 is unoccupied context capacity
	start, end float64
	first      bool
}

// layout reserves a cell for each nonempty tier, then apportions the rest.
// It never clips the live tier off the end to make room for small tiers.
func layout(s diagnostic.Snapshot, span, width int) []cell {
	width = max(0, min(width, 1000))
	if width == 0 {
		return nil
	}
	total := max(0, s.Total())
	span = max(1, max(span, total))
	tiers := make([]diagnostic.Tier, 0, len(s.Tiers))
	for _, t := range s.Tiers {
		if t.Tokens > 0 {
			tiers = append(tiers, t)
		}
	}
	// Below one cell per tier, show the tiers that fit. The normal minimum
	// viewer width has ample room for all five; tiny windows must still fit.
	if len(tiers) > width {
		tiers = tiers[:width]
	}
	used := min(width, max(len(tiers), int(math.Round(float64(total)*float64(width)/float64(span)))))
	counts := make([]int, len(tiers))
	weights := make([]float64, len(tiers))
	for i := range tiers {
		counts[i] = 1
		weights[i] = float64(tiers[i].Tokens) * float64(used) / float64(max(1, total))
	}
	for left := used - len(tiers); left > 0; left-- {
		best := 0
		for i := range tiers {
			if weights[i]-float64(counts[i]) > weights[best]-float64(counts[best]) {
				best = i
			}
		}
		counts[best]++
	}
	cells := make([]cell, 0, width)
	off := 0
	for i, t := range tiers {
		for j := range counts[i] {
			cells = append(cells, cell{
				tier: t.Index, first: j == 0,
				start: float64(off) + float64(j)*float64(t.Tokens)/float64(counts[i]),
				end:   float64(off) + float64(j+1)*float64(t.Tokens)/float64(counts[i]),
			})
		}
		off += t.Tokens
	}
	remaining := width - len(cells)
	for j := range remaining {
		cells = append(cells, cell{
			tier:  -1,
			start: float64(total) + float64(j)*float64(span-total)/float64(remaining),
			end:   float64(total) + float64(j+1)*float64(span-total)/float64(remaining),
		})
	}
	return cells
}

func column(cells []cell, tokens float64) int {
	for i, c := range cells {
		if tokens < c.end-1e-9 {
			return i
		}
	}
	return max(0, len(cells)-1)
}

// reportedPosition maps the provider's cache ratio into estimated-token space.
func reportedPosition(s diagnostic.Snapshot) float64 {
	if !s.Reported || s.ReportedInput <= 0 {
		return 0
	}
	return float64(s.Total()) * min(1, max(0, float64(s.ReportedCached)/float64(s.ReportedInput)))
}
