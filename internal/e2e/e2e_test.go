//go:build integration

package e2e_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"

	"github.com/quinnovator/contactd/internal/actor"
	"github.com/quinnovator/contactd/internal/api"
	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/eventlog"
	"github.com/quinnovator/contactd/internal/infer"
	fakeinfer "github.com/quinnovator/contactd/internal/infer/fake"
	fakellm "github.com/quinnovator/contactd/internal/llm/fake"
	"github.com/quinnovator/contactd/internal/llm/openai"
	"github.com/quinnovator/contactd/internal/obs"
	"github.com/quinnovator/contactd/internal/plan"
	"github.com/quinnovator/contactd/internal/turn"
)

// stack is the production wiring of cmd/contactd with the fake model server
// in place of a provider.
type stack struct {
	url  string
	stop func()
}

func start(t *testing.T, dbPath string, llmOpt fakellm.Options, tune func(*actor.Config, *plan.Params)) *stack {
	t.Helper()
	provider := httptest.NewServer(fakellm.Handler(llmOpt))

	store, err := eventlog.Open(t.Context(), dbPath, eventlog.Options{})
	if err != nil {
		t.Fatal(err)
	}
	writerCtx, stopWriter := context.WithCancel(context.Background())
	base, cancel := context.WithCancel(context.Background())
	var bg sync.WaitGroup
	var writer sync.WaitGroup
	writer.Go(func() { store.Run(writerCtx) })

	embedB := infer.NewBatcher[string, []float32]("embedder", fakeinfer.Embedder{}, 2)
	rerankB := infer.NewBatcher[infer.Pair, float32]("reranker", fakeinfer.Reranker{}, 2)
	bg.Go(func() { embedB.Run(base) })
	bg.Go(func() { rerankB.Run(base) })
	embedder, reranker := infer.NewEmbedder(embedB), infer.NewReranker(rerankB)

	llm, err := openai.New(openai.Config{BaseURLs: []string{provider.URL + "/v1"}, Model: "fake", Logger: obs.Discard()})
	if err != nil {
		t.Fatal(err)
	}
	cfg, pp := actor.DefaultConfig(), plan.DefaultParams()
	cfg.Debounce, cfg.DebounceCap = 15*time.Millisecond, 60*time.Millisecond
	cfg.Recall.Tau = 0.2
	if tune != nil {
		tune(&cfg, &pp)
	}
	reg := actor.New(base, actor.Deps{
		Log: store, Hydrate: store, Repair: store, Embed: embedder, Summarize: llm, Snapshots: store,
		Turn: &turn.Deps{
			Embed: embedder, Rerank: reranker, Log: store, LLM: llm, Logger: obs.Discard(),
			Recall: cfg.Recall, Plan: pp, MaxToolSteps: 4, MaxToolOutput: 4096, QueryTokens: 128,
		},
	}, cfg, obs.Discard())
	a := api.New(store, reg, obs.Discard())
	srv := httptest.NewServer(a.Handler())

	var once sync.Once
	s := &stack{url: srv.URL}
	s.stop = func() {
		once.Do(func() {
			// The production shutdown order.
			a.Drain()
			grace, done := context.WithTimeout(base, 10*time.Second)
			reg.Shutdown(grace)
			done()
			srv.Close()
			cancel()
			bg.Wait()
			stopWriter()
			writer.Wait()
			if err := store.Close(); err != nil {
				t.Errorf("close store: %v", err)
			}
			provider.Close()
		})
	}
	t.Cleanup(s.stop)
	return s
}

func (s *stack) post(t *testing.T, path, body string) map[string]any {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, s.url+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode >= 300 {
		t.Fatalf("POST %s: %d %s", path, resp.StatusCode, raw)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return out
}

func (s *stack) contact(t *testing.T) string {
	t.Helper()
	return fmt.Sprint(s.post(t, "/v1/contacts", `{"agent":{"system":"You are Ada, a long-standing colleague. Answer briefly."}}`)["id"])
}

// stream opens the SSE stream and waits until the server has confirmed it.
func (s *stack) stream(t *testing.T, id string) <-chan core.Delta {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, s.url+"/v1/contacts/"+id+"/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req) //nolint:bodyclose // closed by the reader goroutine below
	if err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{})
	out := make(chan core.Delta, 1024)
	go func() {
		defer func() { _ = resp.Body.Close() }()
		defer close(out)
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
		first := true
		for sc.Scan() {
			if first {
				close(ready) // the ": connected" comment
				first = false
			}
			if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				var d core.Delta
				if json.Unmarshal([]byte(data), &d) == nil {
					out <- d
				}
			}
		}
	}()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("stream did not open")
	}
	return out
}

func reply(t *testing.T, deltas <-chan core.Delta) core.Delta {
	t.Helper()
	deadline := time.After(20 * time.Second)
	for {
		select {
		case d, ok := <-deltas:
			if !ok {
				t.Fatal("stream ended before the reply")
			}
			if d.Type == core.DeltaError {
				t.Fatalf("turn failed: %s", d.Text)
			}
			if d.Type == core.DeltaMessage {
				return d
			}
		case <-deadline:
			t.Fatal("no reply within 20s")
		}
	}
}

func (s *stack) say(t *testing.T, id, text string, deltas <-chan core.Delta) core.Delta {
	t.Helper()
	body, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		t.Fatal(err)
	}
	s.post(t, "/v1/contacts/"+id+"/messages", string(body))
	return reply(t, deltas)
}

// TestConversationSurvivesRestart runs a conversation, restarts the server on
// the same database, and continues it. It also checks the headline metric
// end to end: after the first turn the provider reports cached input tokens,
// which only happens if the bytes the OpenAI adapter sends really are a
// stable prefix from turn to turn.
//
//nolint:paralleltest // reads process-wide Prometheus counters before and after
func TestConversationSurvivesRestart(t *testing.T) {
	db := filepath.Join(t.TempDir(), "contacts.db")
	s := start(t, db, fakellm.Options{}, nil)
	id := s.contact(t)
	deltas := s.stream(t, id)

	inputBefore := testutil.ToFloat64(obs.ProviderInputTokens)
	cachedBefore := testutil.ToFloat64(obs.ProviderCachedTokens)
	for i, text := range []string{
		"the postgres migration is stuck on the orders table",
		"it says lock timeout while adding the index",
		"should I create the index concurrently",
		"ok, doing that now",
	} {
		d := s.say(t, id, text, deltas)
		if d.Seq != uint64(2*i+2) || d.Text == "" {
			t.Fatalf("turn %d: reply %+v, want seq %d", i, d, 2*i+2)
		}
	}
	input := testutil.ToFloat64(obs.ProviderInputTokens) - inputBefore
	cached := testutil.ToFloat64(obs.ProviderCachedTokens) - cachedBefore
	t.Logf("4 turns: %.0f input tokens, %.0f served from the provider cache (%.0f%%)", input, cached, 100*cached/input)
	// The fake server caches at message granularity, and on this wire format
	// the newest user message and the time note share one message, so each
	// turn reuses everything up to the turn before last. That is a floor:
	// real providers match token by token and reuse more. Four short turns
	// should still come in well above a third.
	if cached < 0.35*input {
		t.Errorf("only %.0f of %.0f input tokens were cached; the prompt prefix is not stable across turns", cached, input)
	}

	s.stop()

	s2 := start(t, db, fakellm.Options{}, nil)
	deltas2 := s2.stream(t, id)
	d := s2.say(t, id, "and we are back after the restart", deltas2)
	if d.Seq != 10 || !strings.Contains(d.Text, "and we are back") {
		t.Fatalf("after restart: reply %+v, want seq 10 answering the new message", d)
	}
}

// TestPlatformOverheadUnderLoad drives many contacts at once and reports
// turn_overhead_ms: the time from the debounce firing to the model request
// being sent, which is pure platform cost (hydration, query embedding,
// recall with rerank, planning). The design's starting target is a p99
// under 100 ms.
//
//nolint:paralleltest // reads the process-wide overhead histogram
func TestPlatformOverheadUnderLoad(t *testing.T) {
	const contacts, turns = 150, 4
	s := start(t, filepath.Join(t.TempDir(), "contacts.db"), fakellm.Options{FirstToken: 5 * time.Millisecond}, func(_ *actor.Config, pp *plan.Params) {
		pp.TailBudget = 200 // tiny, so later turns really do recall and rerank
	})
	before := overhead(t)

	topics := []string{"postgres migration orders index", "lisbon flights alfama hotel", "sourdough starter flour", "marathon long run pace"}
	var wg sync.WaitGroup
	started := time.Now()
	for c := range contacts {
		wg.Go(func() {
			id := s.contact(t)
			deltas := s.stream(t, id)
			for i := range turns {
				s.say(t, id, fmt.Sprintf("%s question %d from contact %d", topics[(c+i)%len(topics)], i, c), deltas)
			}
		})
	}
	wg.Wait()
	after := overhead(t)

	n := after.count - before.count
	if n < contacts*turns {
		t.Fatalf("%d turns observed, want at least %d", n, contacts*turns)
	}
	p50, p99 := quantile(before, after, 0.50), quantile(before, after, 0.99)
	t.Logf("%d turns across %d concurrent contacts in %v: turn_overhead_ms p50 <= %.1f, p99 <= %.1f (bucket upper bounds; fake inference)",
		n, contacts, time.Since(started).Round(time.Millisecond), p50, p99)
	if p99 > 250 {
		t.Errorf("p99 platform overhead is above 250 ms even with fake inference: %.1f ms", p99)
	}
}

type histogram struct {
	count   uint64
	buckets map[float64]uint64 // upper bound -> cumulative count
}

func overhead(t *testing.T) histogram {
	t.Helper()
	var m dto.Metric
	if err := obs.TurnOverheadMS.Write(&m); err != nil {
		t.Fatal(err)
	}
	h := histogram{count: m.GetHistogram().GetSampleCount(), buckets: map[float64]uint64{}}
	for _, b := range m.GetHistogram().GetBucket() {
		h.buckets[b.GetUpperBound()] = b.GetCumulativeCount()
	}
	return h
}

// quantile returns the upper bound of the bucket holding quantile q of the
// observations made between two snapshots.
func quantile(before, after histogram, q float64) float64 {
	bounds := make([]float64, 0, len(after.buckets))
	for le := range after.buckets {
		bounds = append(bounds, le)
	}
	sort.Float64s(bounds)
	want := q * float64(after.count-before.count)
	for _, le := range bounds {
		if float64(after.buckets[le]-before.buckets[le]) >= want {
			return le
		}
	}
	return bounds[len(bounds)-1] * 2 // beyond the largest finite bucket
}
