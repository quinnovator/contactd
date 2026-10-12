package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/infer"
	fakellm "github.com/quinnovator/contactd/internal/llm/fake"
	"github.com/quinnovator/contactd/internal/plan"
	"github.com/quinnovator/contactd/internal/recall"
)

// epoch anchors transcript offsets to wall-clock time.
const epoch = int64(1_760_000_000_000)

// models is the inference backend a simulation uses. Embeddings are memoized
// because every variant replays the same texts.
type models struct {
	embed  infer.Pipeline[string, []float32]
	rerank infer.Pipeline[infer.Pair, float32]
	memo   map[string][]float32
}

func (m *models) vec(ctx context.Context, text string) ([]float32, error) {
	if v, ok := m.memo[text]; ok {
		return v, nil
	}
	out, err := m.embed.Run(ctx, []string{text})
	if err != nil {
		return nil, fmt.Errorf("embed: %w", err)
	}
	m.memo[text] = out[0]
	return out[0], nil
}

// Score implements recall.Reranker.
func (m *models) Score(ctx context.Context, query string, passages []string) ([]float32, error) {
	pairs := make([]infer.Pair, len(passages))
	for i, p := range passages {
		pairs[i] = infer.Pair{Query: query, Passage: p}
	}
	out, err := m.rerank.Run(ctx, pairs)
	if err != nil {
		return nil, fmt.Errorf("rerank: %w", err)
	}
	return out, nil
}

// memLog is the simulated contact's event log.
type memLog struct {
	events []core.Event
	topics []string // ground truth, parallel to events
}

func (l *memLog) add(kind core.EventKind, ts int64, text, topic string) core.Event {
	raw, err := json.Marshal(core.MsgBody{Text: text})
	if err != nil {
		panic(err) // a string always marshals
	}
	ev := core.Event{Contact: 1, Seq: uint64(len(l.events) + 1), TS: ts, Kind: kind, Body: raw}
	l.events = append(l.events, ev)
	l.topics = append(l.topics, topic)
	return ev
}

func (l *memLog) Event(_ context.Context, _ core.ContactID, seq uint64) (core.Event, error) {
	if seq == 0 || seq > uint64(len(l.events)) {
		return core.Event{}, fmt.Errorf("no event %d", seq)
	}
	return l.events[seq-1], nil
}

func (l *memLog) EventsRange(_ context.Context, _ core.ContactID, from, to uint64, limit int) ([]core.Event, error) {
	var out []core.Event
	for _, ev := range l.events {
		if ev.Seq >= from && ev.Seq <= to && len(out) < limit {
			out = append(out, ev)
		}
	}
	return out, nil
}

// about reports whether an episode contains a message on the given topic.
func (l *memLog) about(ep *recall.Episode, topic string) bool {
	for seq := ep.Start; seq <= ep.End; seq++ {
		if l.topics[seq-1] == topic {
			return true
		}
	}
	return false
}

// variant is one planner configuration under test.
type variant struct {
	Name         string `json:"name"`
	Slide        bool   `json:"slide"`
	NoHysteresis bool   `json:"no_hysteresis"`
}

// result is what one variant measured over every transcript.
type result struct {
	Variant variant `json:"variant"`
	Turns   int     `json:"turns"`
	// Cached share of input tokens, as the simulated provider reports it.
	InputTokens  int64   `json:"input_tokens"`
	CachedTokens int64   `json:"cached_tokens"`
	CachedShare  float64 `json:"cached_share"`
	// The same over warm turns only: those within the cache TTL of the turn
	// before, which is the steady state the 80% target is about.
	WarmTurns       int     `json:"warm_turns"`
	WarmInput       int64   `json:"warm_input_tokens"`
	WarmCached      int64   `json:"warm_cached_tokens"`
	WarmCachedShare float64 `json:"warm_cached_share"`
	// What the planner predicted for the same turns.
	PredictedShare float64 `json:"predicted_share"`
	// BreakTier[i] counts turns whose first changed tier was i (5 = none).
	BreakTier [plan.NumTiers + 1]int `json:"break_tier"`

	// Recall quality on labelled callbacks.
	Callbacks     int     `json:"callbacks"`   // callbacks whose episode had left the tail
	Stage1Hits    int     `json:"stage1_hits"` // right episode in the bi-encoder top 4
	RerankHits    int     `json:"rerank_hits"` // right episode in the reranked top 4 above theta
	RecallAt4S1   float64 `json:"recall_at_4_stage1"`
	RecallAt4RR   float64 `json:"recall_at_4_rerank"`
	QuietTurns    int     `json:"quiet_turns"`    // non-callback turns
	QuietRecalled int     `json:"quiet_recalled"` // episodes recalled on them (lower is better)
	Episodes      int     `json:"episodes"`

	predicted int64
}

func (r *result) finish() {
	r.CachedShare = ratio(r.CachedTokens, r.InputTokens)
	r.WarmCachedShare = ratio(r.WarmCached, r.WarmInput)
	r.PredictedShare = ratio(r.predicted, r.InputTokens)
	r.RecallAt4S1 = ratio(int64(r.Stage1Hits), int64(r.Callbacks))
	r.RecallAt4RR = ratio(int64(r.RerankHits), int64(r.Callbacks))
}

func ratio(a, b int64) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}

// params bundles the knobs a run sweeps.
type params struct {
	recall recall.Params
	plan   plan.Params
	mode   fakellm.CacheMode
	agent  core.AgentConfig
}

// simulate replays one transcript through the same recall and planning code
// the server runs, against the fake provider's prefix cache, and adds what
// it measures to res.
func simulate(ctx context.Context, msgs []message, m *models, pr params, v variant, res *result) error {
	pp := pr.plan
	pp.Slide, pp.NoHysteresis = v.Slide, v.NoHysteresis

	log := new(memLog)
	index, err := recall.NewIndex(pr.recall, nil)
	if err != nil {
		return fmt.Errorf("new index: %w", err)
	}
	provider := fakellm.NewModel(fakellm.Options{Mode: pr.mode, CacheTTL: time.Duration(pp.CacheTTL) * time.Millisecond})
	var prev *plan.PromptPlan

	for i := 0; i < len(msgs); {
		// A turn answers a run of consecutive user messages.
		first := i
		var texts []string
		var fresh []core.Event
		for ; i < len(msgs) && msgs[i].Role == "user"; i++ {
			fresh = append(fresh, log.add(core.KindUserMsg, epoch+msgs[i].T*1000, msgs[i].Text, msgs[i].Topic))
			texts = append(texts, msgs[i].Text)
		}
		if len(fresh) == 0 {
			i++ // a stray agent line with no user message before it
			continue
		}
		now := fresh[len(fresh)-1].TS

		// Recall, exactly as turn.Run does it.
		lay, err := plan.Layout(prev, log.events, plan.Reader{}, pp)
		if err != nil {
			return fmt.Errorf("lay out tail: %w", err)
		}
		lead := ""
		if lay.TS-lay.LastReplyTS <= pr.recall.GapMS {
			lead = lay.LastReply
		}
		qText := recall.QueryText(lay.Texts, lead, 128)
		qVec, err := m.vec(ctx, qText)
		if err != nil {
			return err
		}
		tailStart := lay.TailStart
		snap := index.Snapshot()
		scored, _, err := recall.Recall(ctx, snap, recall.Query{
			Contact: 1, Text: qText, Vec: qVec, TailStart: tailStart, Now: now,
		}, log, m, pr.recall)
		if err != nil {
			return fmt.Errorf("recall: %w", err)
		}
		above := make([]plan.Scored, 0, len(scored))
		for _, s := range scored {
			if s.Score >= pr.recall.Theta {
				above = append(above, plan.Scored{ID: s.Ep.ID, Score: s.Score})
			}
		}

		if cb := msgs[first]; cb.Callback {
			scoreCallback(log, snap, qVec, tailStart, scored, pr, cb.Topic, res)
		} else {
			res.QuietTurns++
			res.QuietRecalled += min(len(above), pp.K)
		}

		ids := plan.ChooseRecall(prev, above, now, pp)
		recalled := make([]plan.Recalled, len(ids))
		for k, id := range ids {
			recalled[k] = plan.Recalled{ID: id}
			for _, s := range scored {
				if s.Ep.ID == id {
					recalled[k].Text = s.Passage
				}
			}
		}
		p, err := plan.Build(prev, plan.Input{Agent: &pr.agent, Tail: log.events}, recalled, now, pp)
		if err != nil {
			return fmt.Errorf("build plan: %w", err)
		}

		var prompt []core.Message
		for t := range p.Tiers {
			prompt = append(prompt, p.Tiers[t].Msgs...)
		}
		resp := provider.Respond(prompt, p.Tools, p.PrefixMarks(), time.UnixMilli(now))
		res.Turns++
		res.InputTokens += int64(resp.Usage.Input)
		res.CachedTokens += int64(resp.Usage.Cached)
		res.predicted += int64(p.PredictCached(prev, pp))
		res.BreakTier[p.FirstChanged(prev)]++
		if prev != nil && now-prev.BuiltAt < pp.CacheTTL {
			res.WarmTurns++
			res.WarmInput += int64(resp.Usage.Input)
			res.WarmCached += int64(resp.Usage.Cached)
		}

		// The reply: the fixture's if it has one, else the fake model's.
		reply, topic := resp.Text, msgs[first].Topic
		if i < len(msgs) && msgs[i].Role == "agent" {
			reply, topic = msgs[i].Text, msgs[i].Topic
			i++
		}
		agentEv := log.add(core.KindAgentMsg, now+2000, reply, topic)

		// Background indexing catches up before the next turn.
		for k, ev := range append(fresh, agentEv) {
			text := reply
			if k < len(texts) {
				text = texts[k]
			}
			vec, err := m.vec(ctx, text)
			if err != nil {
				return err
			}
			index.OnEmbedded(ev, vec)
		}
		prev = p
	}
	res.Episodes += index.Len()
	return nil
}

// scoreCallback records whether the episode a callback refers to was found,
// by the bi-encoder alone and after reranking. A callback whose topic is
// still entirely in the tail is not counted: the model can already see it.
func scoreCallback(log *memLog, snap *recall.Snapshot, q []float32, tailStart uint64, scored []recall.Scored, pr params, topic string, res *result) {
	recallable := false
	for _, c := range snap.TopK(q, 1<<20, tailStart) {
		if log.about(c.Ep, topic) {
			recallable = true
			break
		}
	}
	if !recallable {
		return
	}
	res.Callbacks++
	for _, c := range snap.TopK(q, pr.plan.K, tailStart) {
		if log.about(c.Ep, topic) {
			res.Stage1Hits++
			break
		}
	}
	kept := 0
	for _, s := range scored { // sorted by descending score
		if s.Score < pr.recall.Theta || kept == pr.plan.K {
			break
		}
		kept++
		if log.about(s.Ep, topic) {
			res.RerankHits++
			break
		}
	}
}
