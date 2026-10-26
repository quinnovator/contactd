package stage

import "slices"

func padded(r rect) rect {
	return rect{r.x - exhibitGapCols, r.y - exhibitGapRows, r.w + 2*exhibitGapCols, r.h + 2*exhibitGapRows}
}

func contains(a, b rect) bool {
	return a.x <= b.x && a.y <= b.y && a.x+a.w >= b.x+b.w && a.y+a.h >= b.y+b.h
}

// subtract keeps maximal free rectangles. They may overlap each other, but
// never an occupied box; every subsequent placement subtracts from them all.
func subtract(free []rect, used rect) []rect {
	var out []rect
	for _, r := range free {
		if !r.meets(used) {
			out = append(out, r)
			continue
		}
		for _, part := range []rect{
			{r.x, r.y, used.x - r.x, r.h},
			{used.x + used.w, r.y, r.x + r.w - used.x - used.w, r.h},
			{r.x, r.y, r.w, used.y - r.y},
			{r.x, used.y + used.h, r.w, r.y + r.h - used.y - used.h},
		} {
			if part.w > 0 && part.h > 0 && contains(r, part) {
				out = append(out, part)
			}
		}
	}
	var kept []rect
	for i, r := range out {
		covered := false
		for j, other := range out {
			if i != j && contains(other, r) && (other != r || j < i) {
				covered = true
				break
			}
		}
		if !covered {
			kept = append(kept, r)
		}
	}
	return kept
}

func locations(area rect, w, h int, anchor Anchor) []rect {
	x := area.x + (area.w-w)/2
	y := area.y + (area.h-h)/2
	switch anchor.across() {
	case -1:
		x = area.x
	case 1:
		x = area.x + area.w - w
	}
	switch anchor.down() {
	case -1:
		y = area.y
	case 1:
		y = area.y + area.h - h
	}
	out := []rect{{x, y, w, h}}
	if anchor == Auto {
		for _, b := range []rect{
			{area.x, area.y, w, h},
			{area.x + area.w - w, area.y, w, h},
			{area.x, area.y + area.h - h, w, h},
			{area.x + area.w - w, area.y + area.h - h, w, h},
		} {
			if !slices.Contains(out, b) {
				out = append(out, b)
			}
		}
	}
	return out
}
