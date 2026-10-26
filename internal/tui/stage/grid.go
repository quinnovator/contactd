package stage

import "slices"

// grids adds ordered row/column layouts to the free-placement search. These
// preserve reading order and shared edges when several automatic artifacts
// can fit together, instead of scattering little cards into unrelated gaps.
func (p *composer) grids(speakers []composition, order []int) []composition {
	if len(order) < 2 {
		return nil
	}
	for _, index := range order {
		if p.stage.cards[index].ex.Where != Auto {
			return nil
		}
	}
	order = slices.Clone(order)
	slices.Sort(order)
	var out []composition
	for _, speaker := range speakers {
		for _, free := range speaker.free {
			for columns := 1; columns <= min(len(order), 4); columns++ {
				if grid, ok := p.grid(speaker, free, order, columns); ok {
					out = append(out, grid)
				}
			}
		}
	}
	return out
}

func (p *composer) grid(speaker composition, area rect, order []int, columns int) (composition, bool) {
	rows := (len(order) + columns - 1) / columns
	maxW := (area.w - (columns-1)*exhibitGapCols) / columns
	budget := area.h - (rows-1)*exhibitGapRows
	maxH := budget
	if maxW < minStripCols || budget < 2*rows {
		return composition{}, false
	}
	widths, heights := make([]int, columns), make([]int, rows)
	items := make([]placement, 0, len(order))
	for i, index := range order {
		ex := p.stage.cards[index].ex
		minimum := ex.MinCols
		if minimum <= 0 {
			minimum = 24
		}
		if maxW < min(minimum, p.area.w) {
			return composition{}, false
		}
		cols := maxW
		if ex.PreferredCols > 0 {
			cols = min(cols, ex.PreferredCols)
		}
		size := p.measure(index, cols, maxH)
		if size.Cols == 0 || size.Rows == 0 {
			return composition{}, false
		}
		item := placement{index: index, box: rect{w: size.Cols, h: min(size.Rows, maxH)}, cut: size.Rows > maxH}
		items = append(items, item)
		heights[i/columns] = max(heights[i/columns], item.box.h)
	}
	used := 0
	for _, height := range heights {
		used += height
	}
	for used > budget {
		tallest := 0
		for i := range heights {
			if heights[i] > heights[tallest] {
				tallest = i
			}
		}
		heights[tallest]--
		used--
	}
	// Refit scalable content to its final row budget. Short rows have
	// already returned their spare space to the taller rows.
	for i := range items {
		item := &items[i]
		size := p.measure(item.index, item.box.w, heights[i/columns])
		item.box.w, item.box.h = size.Cols, min(size.Rows, heights[i/columns])
		item.cut = size.Rows > heights[i/columns]
		widths[i%columns] = max(widths[i%columns], item.box.w)
	}
	totalW, totalH := (columns-1)*exhibitGapCols, (rows-1)*exhibitGapRows
	for _, width := range widths {
		totalW += width
	}
	for _, height := range heights {
		totalH += height
	}
	x0, y := area.x+(area.w-totalW)/2, area.y+(area.h-totalH)/2
	score := speaker.score + 400
	for row := range rows {
		x := x0
		for column := range columns {
			i := row*columns + column
			if i >= len(items) {
				break
			}
			items[i].box.x, items[i].box.y = x, y
			if columns == 1 {
				items[i].box.x += (widths[column] - items[i].box.w) / 2
			}
			score += p.value(items[i])
			x += widths[column] + exhibitGapCols
		}
		y += heights[row] + exhibitGapRows
	}
	return composition{block: speaker.block, placed: items, score: score}, true
}
