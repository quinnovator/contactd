package stage

import (
	"cmp"
	"fmt"
	"slices"
)

const (
	exhibitGapCols = 6
	exhibitGapRows = 2
	autoBlockCols  = maxTextCols + textMargin
	autoCardCols   = 160
	searchWidth    = 12
	searchCards    = 16
)

type placement struct {
	index int
	box   rect
	cut   bool
}

type composition struct {
	block  rect
	free   []rect
	placed []placement
	score  int
	rank   int
}

type measureKey struct{ index, cols, rows int }

type composer struct {
	stage    *Stage
	area     rect
	measured map[measureKey]Size
}

// compose searches bounded alternatives, using measurements rather than
// invoking renderers for speculative placements. All exhibits, including
// anchored ones, participate in the same collision-free packing problem.
func (s *Stage) compose(area rect) (rect, string) {
	if len(s.cards) == 0 {
		return area, ""
	}
	inner := area
	if inner.w >= 2*cardMargin+minStripCols {
		inner.x += cardMargin
		inner.w -= 2 * cardMargin
	}
	if inner.h >= minBlockRows+4 {
		inner.y++
		inner.h -= 2
	}
	// Reserve a footer even when the chosen layout fits everything. Its
	// appearance then cannot move the content and trigger another layout.
	if inner.h >= 2 {
		inner.h--
	}
	p := composer{stage: s, area: inner, measured: make(map[measureKey]Size)}
	states := p.speakers()
	speakers := slices.Clone(states)
	order := p.order()
	for at, index := range order {
		remaining := order[at+1:]
		var next []composition
		for _, state := range states {
			next = append(next, state) // explicitly account for hidden content
			for _, option := range p.options(index, state, remaining) {
				next = append(next, composition{
					block: state.block, free: subtract(state.free, padded(option.box)),
					placed: append(slices.Clone(state.placed), option),
					score:  state.score + p.value(option),
				})
			}
		}
		for i := range next {
			next[i].rank = next[i].score + p.potential(next[i].free, remaining)
		}
		slices.SortStableFunc(next, func(a, b composition) int { return cmp.Compare(b.rank, a.rank) })
		states = uniqueStates(next, searchWidth)
	}
	best := states[0]
	for _, grid := range p.grids(speakers, order) {
		if grid.score > best.score {
			best = grid
		}
	}
	p.center(&best)
	for _, c := range s.cards {
		c.on = false
	}
	shortened := 0
	for _, item := range best.placed {
		c := s.cards[item.index]
		s.draw(c, item.box.w, item.box.h)
		c.on = c.w > 0 && len(c.rows) > 0
		c.x, c.y, c.h = item.box.x, item.box.y, min(item.box.h, len(c.rows))
		if item.cut || c.clipped || len(c.rows) > item.box.h {
			shortened++
		}
		if len(c.rows) > item.box.h {
			c.clipped = true
			c.rows = slices.Clone(c.rows[:item.box.h])
			c.rows[len(c.rows)-1] = s.th.Truncate("… more (ctrl+o)", item.box.w)
			c.w = max(c.w, s.th.Width(c.rows[len(c.rows)-1]))
		}
	}
	hidden := len(s.cards) - len(best.placed)
	footer := ""
	if area.h >= 2 && (hidden > 0 || shortened > 0) {
		footer = fmt.Sprintf("%d hidden, %d shortened · ctrl+o to inspect", hidden, shortened)
	}
	return best.block, footer
}

func (p *composer) order() []int {
	order := make([]int, len(p.stage.cards))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int {
		ca, cb := p.stage.cards[a].ex, p.stage.cards[b].ex
		if n := cmp.Compare(cb.Priority, ca.Priority); n != 0 {
			return n
		}
		if ca.Where != Auto && cb.Where == Auto {
			return -1
		}
		if ca.Where == Auto && cb.Where != Auto {
			return 1
		}
		return cmp.Compare(b, a)
	})
	return order[:min(len(order), searchCards)]
}

func (p *composer) speakers() []composition {
	a := p.area
	var boxes []rect
	for _, w := range []int{min(autoBlockCols, a.w*45/100), minBlockCols, 32, 36, 48, autoBlockCols} {
		if w < minBlockCols || w+exhibitGapCols+minStripCols > a.w {
			continue
		}
		boxes = append(boxes, rect{a.x, a.y, w, a.h}, rect{a.x + a.w - w, a.y, w, a.h})
	}
	for _, h := range []int{min(28, a.h*3/5), a.h / 2, minBlockRows} {
		if h < minBlockRows || h+exhibitGapRows+2 > a.h {
			continue
		}
		w := min(a.w, autoBlockCols)
		x := a.x + (a.w-w)/2
		boxes = append(boxes, rect{x, a.y, w, h}, rect{x, a.y + a.h - h, w, h})
	}
	// The words alone are the final fallback on a very small terminal.
	boxes = append(boxes, a)
	out := make([]composition, 0, len(boxes))
	for _, b := range boxes {
		height := b.h
		if len(p.stage.peers) > 0 && height >= 3 {
			height--
		}
		v := share(b.w, height, min(len(p.stage.below), max(height-1, 0)))
		lines := max(len(p.stage.th.Wrap(p.stage.turn.Text, max(v.textW, 1))), 1)
		readable := min(v.textH*2400/lines, 2400)
		score := min(v.rows, 22)*60 + min(v.textW, 60)*10 + readable
		if len(p.stage.peers) > 0 && v.cols >= 54 && v.rows >= 11 {
			score += 1000
		}
		out = append(out, composition{block: b, free: subtract([]rect{a}, padded(b)), score: score})
	}
	return out
}

func (p *composer) measure(index, cols, rows int) Size {
	cols, rows = max(cols, 1), max(rows, 1)
	key := measureKey{index, cols, rows}
	if size, ok := p.measured[key]; ok {
		return size
	}
	c := p.stage.cards[index]
	var size Size
	if c.ex.Measure != nil {
		size = c.ex.Measure(cols, rows)
	} else {
		if c.sample.Cols == 0 {
			p.stage.draw(c, min(max(p.area.w, 1), autoCardCols), 256)
			c.sample = Size{c.w, len(c.rows)}
		}
		size = Size{min(cols, c.sample.Cols), c.sample.Rows}
	}
	size.Cols, size.Rows = min(max(size.Cols, 0), cols), min(max(size.Rows, 0), 1<<20)
	p.measured[key] = size
	return size
}

func (p *composer) options(index int, state composition, remaining []int) []placement {
	c := p.stage.cards[index]
	preferred := c.ex.PreferredCols
	if preferred <= 0 {
		preferred = min(p.measure(index, autoCardCols, 256).Cols, 80)
	}
	minimum := c.ex.MinCols
	if minimum <= 0 {
		minimum = min(preferred, 24)
	}
	var out []placement
	for _, free := range state.free {
		if free.w < min(minimum, p.area.w) || free.h < 2 {
			continue
		}
		for _, cols := range []int{min(preferred, free.w), free.w, free.w / 2, minimum, c.w} {
			if cols < min(minimum, p.area.w) || cols > free.w || cols <= 0 {
				continue
			}
			for _, limit := range []int{free.h, max((free.h-exhibitGapRows)/2, 2), min(free.h, 4)} {
				size := p.measure(index, cols, limit)
				h := min(size.Rows, limit)
				if size.Cols == 0 || h == 0 {
					continue
				}
				for _, box := range locations(free, size.Cols, h, c.ex.Where) {
					if c.ex.Where != Auto {
						want := locations(p.area, size.Cols, h, c.ex.Where)[0]
						box.x = min(max(want.x, free.x), free.x+free.w-box.w)
						box.y = min(max(want.y, free.y), free.y+free.h-box.h)
					}
					option := placement{index: index, box: box, cut: size.Rows > h}
					if !slices.Contains(out, option) {
						out = append(out, option)
					}
				}
			}
		}
	}
	type ranked struct {
		item placement
		rank int
	}
	scored := make([]ranked, len(out))
	for i, item := range out {
		scored[i] = ranked{item, p.value(item) + p.potential(subtract(state.free, padded(item.box)), remaining)}
	}
	slices.SortStableFunc(scored, func(a, b ranked) int { return cmp.Compare(b.rank, a.rank) })
	out = out[:min(len(scored), 24)]
	for i := range out {
		out[i] = scored[i].item
	}
	return out
}

// potential keeps room for the unplaced content while pruning the search.
// Without this look-ahead, a tall first artifact can crowd out a small second
// one before the planner has considered it.
func (p *composer) potential(free []rect, remaining []int) int {
	score := 0
	for _, index := range remaining {
		c := p.stage.cards[index]
		w := c.ex.MinCols
		if w <= 0 {
			w = 24
		}
		if slices.ContainsFunc(free, func(r rect) bool { return r.w >= min(w, p.area.w) && r.h >= 2 }) {
			score += 10000 * (1 + min(max(c.ex.Priority, 0), 3))
		}
	}
	return score
}

func (p *composer) value(item placement) int {
	c := p.stage.cards[item.index]
	b := item.box
	idealW := c.ex.PreferredCols
	if idealW <= 0 {
		idealW = min(p.measure(item.index, autoCardCols, 256).Cols, 80)
	}
	needed := p.measure(item.index, b.w, b.h)
	quality := 1800 * b.h / max(needed.Rows, 1)
	quality += 1000 * min(b.w, idealW) / max(idealW, 1)
	if c.ex.Scalable {
		ideal := p.measure(item.index, idealW, 256)
		quality = 2800 * min(b.w*b.h, ideal.Cols*ideal.Rows) / max(ideal.Cols*ideal.Rows, 1)
	}
	// Show as many useful artifacts as possible, then favor complete,
	// readable content. Explicit priority is bounded to keep scores safe.
	score := (10000 + quality) * (1 + min(max(c.ex.Priority, 0), 3))
	if c.ex.Where != Auto {
		want := locations(p.area, b.w, b.h, c.ex.Where)[0]
		score -= 12000*abs(b.x-want.x)/max(p.area.w, 1) + 12000*abs(b.y-want.y)/max(p.area.h, 1)
	} else {
		// Align the content with the center of the stage rather than
		// pinning every small card to an outside corner.
		score -= 100 * abs(2*b.y+b.h-2*p.area.y-p.area.h) / max(p.area.h, 1)
	}
	if c.on {
		// Small quality differences do not justify moving familiar content.
		score -= min(abs(c.x-b.x)+2*abs(c.y-b.y), 80)
	}
	return score
}

func (p *composer) center(state *composition) {
	for _, item := range state.placed {
		if p.stage.cards[item.index].ex.Where != Auto {
			return
		}
	}
	bound := state.block
	for _, item := range state.placed {
		b := item.box
		x, y := min(bound.x, b.x), min(bound.y, b.y)
		bound = rect{x, y, max(bound.x+bound.w, b.x+b.w) - x, max(bound.y+bound.h, b.y+b.h) - y}
	}
	dx := p.area.x + (p.area.w-bound.w)/2 - bound.x
	dy := p.area.y + (p.area.h-bound.h)/2 - bound.y
	state.block.x, state.block.y = state.block.x+dx, state.block.y+dy
	for i := range state.placed {
		state.placed[i].box.x += dx
		state.placed[i].box.y += dy
	}
}

func abs(n int) int { return max(n, -n) }

func uniqueStates(states []composition, limit int) []composition {
	var out []composition
	for _, state := range states {
		duplicate := slices.ContainsFunc(out, func(other composition) bool {
			return state.block == other.block && slices.Equal(state.placed, other.placed)
		})
		if !duplicate {
			out = append(out, state)
			if len(out) == limit {
				break
			}
		}
	}
	return out
}
