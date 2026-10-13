package recall

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/obs"
)

// Scored is a recall candidate after reranking.
type Scored struct {
	Ep      *Episode
	Rerank  float32 // raw reranker logit before recency
	Stage1  float32
	Score   float32 // rerank logit plus the recency bonus
	Passage string  // what the reranker read, and what tier 2 will show
}

// Query bundles what one recall needs.
type Query struct {
	Contact   core.ContactID
	Text      string    // the new message plus the previous agent message
	Vec       []float32 // Text, embedded
	TailStart uint64    // first seq already in context
	Now       int64     // unix ms
}

// Recall runs both stages and returns the shortlist sorted by descending
// score. Callers keep at most K of those above Theta; often nothing clears
// it, and recalling nothing is correct: a person doesn't bring up unrelated
// history.
//
// degraded is true when the reranker failed and the result is the best two
// stage-1 hits instead, scored exactly at Theta.
func Recall(ctx context.Context, snap *Snapshot, q Query, log EventReader, rr Reranker, p Params) (out []Scored, degraded bool, err error) {
	start := time.Now()
	cands := snap.TopK(q.Vec, p.Shortlist, q.TailStart)
	obs.RecallStage1MS.Observe(obs.SinceMS(start))
	if len(cands) == 0 {
		return nil, false, nil
	}

	out = make([]Scored, len(cands))
	passages := make([]string, len(cands))
	for i, c := range cands {
		text, err := Passage(ctx, log, q.Contact, c.Ep, p.PassageTokens)
		if err != nil {
			return nil, false, err
		}
		passages[i] = text
		out[i] = Scored{Ep: c.Ep, Stage1: c.Stage1, Passage: text}
	}

	start = time.Now()
	scores, err := rr.Score(ctx, q.Text, passages)
	obs.RerankMS.Observe(obs.SinceMS(start))
	obs.RerankBatchSize.Observe(float64(len(passages)))
	if err != nil {
		if ctx.Err() != nil {
			return nil, false, fmt.Errorf("rerank: %w", ctx.Err())
		}
		// Degrade gracefully: stage 1 alone is worse, but better than
		// forgetting everything because a model call failed.
		out = out[:min(len(out), 2)]
		for i := range out {
			out[i].Score = p.Theta
		}
		return out, true, nil
	}
	if len(scores) != len(out) {
		return nil, false, fmt.Errorf("reranker returned %d scores for %d passages", len(scores), len(out))
	}
	for i := range out {
		age := float64(max(q.Now-out[i].Ep.LastTS, 0))
		out[i].Rerank = scores[i]
		out[i].Score = scores[i] + p.Lambda*float32(math.Exp(-age/float64(p.HalfLife)))
	}
	slices.SortStableFunc(out, func(a, b Scored) int {
		switch {
		case a.Score > b.Score:
			return -1
		case a.Score < b.Score:
			return 1
		default:
			return 0
		}
	})
	return out, false, nil
}

// excerptEdge is how many events are read from each end of an episode that is
// too long to quote whole.
const excerptEdge = 16

// Passage renders an episode for the reranker and for tier 2: its summary if
// one exists, otherwise an excerpt. A short episode is quoted whole; a long
// one is quoted from its start and its end, half the budget each, since that
// is where a conversation is opened and resolved. Bounding the passage bounds
// rerank cost. The text depends only on immutable log entries, so it is
// byte-stable until a summary lands.
func Passage(ctx context.Context, log EventReader, contact core.ContactID, ep *Episode, maxTokens int) (string, error) {
	date := time.UnixMilli(ep.LastTS).UTC().Format("2006-01-02")
	budget := maxTokens * 4 // bytes; the same 4-bytes-per-token estimate as the planner
	if ep.SummarySeq != 0 {
		ev, err := log.Event(ctx, contact, ep.SummarySeq)
		if err != nil {
			return "", fmt.Errorf("load summary of episode %d: %w", ep.ID, err)
		}
		var body core.SummaryBody
		if err := json.Unmarshal(ev.Body, &body); err != nil {
			return "", fmt.Errorf("decode summary of episode %d: %w", ep.ID, err)
		}
		return "[" + date + "] " + truncate(body.Text, budget), nil
	}

	head, err := episodeLines(ctx, log, contact, ep.Start, ep.End)
	if err != nil {
		return "", fmt.Errorf("load episode %d: %w", ep.ID, err)
	}
	whole := ep.End-ep.Start < excerptEdge
	if text := strings.Join(head, "\n"); whole && len(text) <= budget {
		return "[" + date + "] " + text, nil
	}
	tail := head
	if !whole {
		if tail, err = episodeLines(ctx, log, contact, ep.End-excerptEdge+1, ep.End); err != nil {
			return "", fmt.Errorf("load end of episode %d: %w", ep.ID, err)
		}
	}
	return "[" + date + "] " + takeHead(head, budget/2) + "\n...\n" + takeTail(tail, budget/2), nil
}

// episodeLines renders up to excerptEdge events of a seq range as lines.
func episodeLines(ctx context.Context, log EventReader, contact core.ContactID, from, to uint64) ([]string, error) {
	events, err := log.EventsRange(ctx, contact, from, to, excerptEdge)
	if err != nil {
		return nil, err //nolint:wrapcheck // the caller names the episode
	}
	lines := make([]string, 0, len(events))
	for _, ev := range events {
		if !ev.Kind.IsMessage() {
			continue
		}
		line, err := messageLine(ev)
		if err != nil {
			return nil, err
		}
		lines = append(lines, line)
	}
	return lines, nil
}

// takeHead joins lines from the front until the budget is spent, always
// including at least a truncated first line.
func takeHead(lines []string, budget int) string {
	var b strings.Builder
	for i, line := range lines {
		if i > 0 && b.Len()+1+len(line) > budget {
			break
		}
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(truncate(line, budget))
	}
	return b.String()
}

// takeTail is takeHead from the back.
func takeTail(lines []string, budget int) string {
	start, used := len(lines), 0
	for start > 0 {
		n := len(lines[start-1]) + 1
		if start < len(lines) && used+n > budget {
			break
		}
		used += n
		start--
	}
	kept := slices.Clone(lines[start:])
	if len(kept) > 0 {
		kept[0] = truncate(kept[0], budget)
	}
	return strings.Join(kept, "\n")
}

// Transcript renders an episode's messages in full, for summarization.
func Transcript(ctx context.Context, log EventReader, contact core.ContactID, ep *Episode, maxTokens int) (string, error) {
	events, err := log.EventsRange(ctx, contact, ep.Start, ep.End, 1000)
	if err != nil {
		return "", fmt.Errorf("load episode %d: %w", ep.ID, err)
	}
	var b strings.Builder
	for _, ev := range events {
		if !ev.Kind.IsMessage() {
			continue
		}
		line, err := messageLine(ev)
		if err != nil {
			return "", err
		}
		if b.Len()+len(line) > maxTokens*4 {
			b.WriteString("...\n")
			break
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String(), nil
}

func messageLine(ev core.Event) (string, error) {
	var body core.MsgBody
	if err := json.Unmarshal(ev.Body, &body); err != nil {
		return "", fmt.Errorf("decode message seq %d: %w", ev.Seq, err)
	}
	who := "contact"
	if ev.Kind == core.KindAgentMsg {
		who = "you"
	}
	return who + ": " + body.Text, nil
}

// truncate cuts s to at most n bytes on a rune boundary.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

// QueryText builds the recall query: the new message alone is often too
// vague ("ok do that again"), so it is joined with the previous agent
// message and bounded to about maxTokens.
func QueryText(newMsgs []string, prevAgent string, maxTokens int) string {
	budget := maxTokens * 4
	text := strings.Join(newMsgs, "\n")
	if len(text) >= budget || prevAgent == "" {
		return truncate(text, budget)
	}
	// The new message matters most; the previous reply gets what is left.
	return truncate(prevAgent, budget-len(text)-1) + "\n" + text
}
