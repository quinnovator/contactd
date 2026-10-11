package plan

import "slices"

// Scored is a recall candidate that cleared the relevance threshold, with its
// reranked score.
type Scored struct {
	ID    uint32
	Score float32
}

// ChooseRecall picks the episodes for tier 2 (Rule 1). fresh must be sorted by
// descending score.
//
// While the cache is probably warm it keeps last turn's set, as long as every
// member is still in the fresh top 2K and no newcomer beats them all by
// Delta: a slightly better recall is not worth re-prefilling tiers 2-4.
// Otherwise it takes the fresh top K, in episode order so the bytes do not
// depend on score jitter.
func ChooseRecall(prev *PromptPlan, fresh []Scored, now int64, pr Params) []uint32 {
	if !pr.NoHysteresis && prev != nil && now-prev.BuiltAt < pr.CacheTTL &&
		allIn(prev.Recall, topN(fresh, 2*pr.K)) &&
		!beatsAll(fresh, prev.Recall, pr.Delta) {
		return prev.Recall
	}
	top := topN(fresh, pr.K)
	ids := make([]uint32, len(top))
	for i := range top {
		ids[i] = top[i].ID
	}
	slices.Sort(ids)
	return ids
}

func topN(s []Scored, n int) []Scored {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func allIn(ids []uint32, set []Scored) bool {
	for _, id := range ids {
		if !slices.ContainsFunc(set, func(s Scored) bool { return s.ID == id }) {
			return false
		}
	}
	return true
}

// beatsAll reports whether some episode outside held outscores every member
// of held by more than delta. With nothing held, any candidate qualifies.
func beatsAll(fresh []Scored, held []uint32, delta float32) bool {
	best, found := float32(0), false
	for _, s := range fresh {
		if slices.Contains(held, s.ID) && (!found || s.Score > best) {
			best, found = s.Score, true
		}
	}
	for _, s := range fresh {
		if slices.Contains(held, s.ID) {
			continue
		}
		if !found || s.Score > best+delta {
			return true
		}
	}
	return false
}
