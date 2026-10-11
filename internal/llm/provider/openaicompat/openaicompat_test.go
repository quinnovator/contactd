package openaicompat

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/llm"
	"github.com/quinnovator/contactd/internal/llm/conform"
	"github.com/quinnovator/contactd/internal/plan"
)

// fixedNow is the clock every test plan is built with.
const fixedNow = int64(1_760_000_000_000)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return data
}

func TestConformance(t *testing.T) {
	t.Parallel()
	fx := conform.Fixture{
		Reply: conform.Response{
			Body: fixture(t, "reply.sse"),
			Text: "Six times seven is 42.", Input: 2312, Cached: 2048, Output: 9,
		},
		Tool: conform.Response{
			Body: fixture(t, "tool.sse"),
			Text: "Let me check.", Code: "print(6 * 7)", Input: 1520, Cached: 1500, Output: 41,
			// The body carries reasoning_content; the route replays none, so
			// the suite expects no payload to be kept.
			Reasoning: true,
		},
	}
	for name, cfg := range map[string]Config{
		"listed model":       {Models: []Model{{Name: "qwen-test", ContextWindow: 131_072, MaxOutput: 8192}, {Name: "other"}}},
		"default model only": {DefaultModel: "qwen-test"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			conform.Run(t, func(t *testing.T, baseURL string) llm.Provider {
				t.Helper()
				cfg.APIKey, cfg.BaseURLs = "test-key", []string{baseURL}
				p, err := New(cfg)
				if err != nil {
					t.Fatalf("New: %v", err)
				}
				return p
			}, "qwen-test", fx)
		})
	}
}

func TestRoute(t *testing.T) {
	t.Parallel()
	p, err := New(Config{
		Models:       []Model{{Name: "big", ContextWindow: 200_000, MaxOutput: 4096}, {Name: "small"}},
		DefaultModel: "small",
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.ID() != "openai" {
		t.Errorf("ID = %q; stored agent configs name this provider openai", p.ID())
	}
	big, err := p.Route(t.Context(), "big")
	if err != nil {
		t.Fatal(err)
	}
	want := llm.Capabilities{
		ContextWindow: 200_000, MaxOutput: 4096,
		Cache: llm.CacheAutoPrefix, CacheTTL: 5 * time.Minute, Affinity: llm.AffinityHeader, CacheUsage: true,
		Replay: llm.ReplayNone, ToolChoice: true, SingleToolCall: true,
	}
	if big.ID() != "openai/big" || big.Wire != llm.WireChat || !reflect.DeepEqual(big.Caps, want) {
		t.Errorf("route = %+v\nwant caps %+v", big, want)
	}
	def, err := p.Route(t.Context(), "")
	if err != nil || def.Model != "small" || def.Caps.ContextWindow != DefaultContextWindow || def.Caps.MaxOutput != 0 {
		t.Errorf("default route = %+v, %v", def, err)
	}
	// A name the operator did not list is never passed through.
	if _, err := p.Route(t.Context(), "gpt-4o"); !errors.Is(err, llm.ErrUnknownModel) {
		t.Errorf("unlisted model: err = %v, want ErrUnknownModel", err)
	}

	noDefault, err := New(Config{Models: []Model{{Name: "only"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := noDefault.Route(t.Context(), ""); !errors.Is(err, llm.ErrUnknownModel) {
		t.Errorf("no default model: err = %v, want ErrUnknownModel", err)
	}
}

func TestNewValidates(t *testing.T) {
	t.Parallel()
	for name, cfg := range map[string]Config{
		"no models":            {},
		"bad base url":         {DefaultModel: "m", BaseURLs: []string{"localhost:8000/v1"}},
		"duplicate url":        {DefaultModel: "m", BaseURLs: []string{"http://a:8000/v1", "http://a:8000/v1/"}},
		"unnamed model":        {Models: []Model{{ContextWindow: 8192}}},
		"model listed twice":   {Models: []Model{{Name: "m"}, {Name: "m"}}},
		"negative output cap":  {Models: []Model{{Name: "m", MaxOutput: -1}}},
		"default not in list":  {Models: []Model{{Name: "m"}}, DefaultModel: "other"},
		"unparseable base url": {DefaultModel: "m", BaseURLs: []string{"http://[::1"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := New(cfg); err == nil {
				t.Error("New accepted an invalid config")
			}
		})
	}
	p, err := New(Config{DefaultModel: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.replicas) != 1 || p.replicas[0].dialect.URL != "https://api.openai.com/v1/chat/completions" {
		t.Errorf("replicas = %+v", p.replicas)
	}
}

// event, fullPlan and the golden file are the old adapter's: the same
// logical input must freeze to the bytes it sent.

func event(t *testing.T, seq uint64, kind core.EventKind, body any) core.Event {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encode event body: %v", err)
	}
	return core.Event{Contact: 42, Seq: seq, TS: fixedNow - 60_000 + int64(seq), Kind: kind, Body: raw}
}

func fullPlan(t *testing.T) *plan.PromptPlan {
	t.Helper()
	in := plan.Input{
		Agent: &core.AgentConfig{
			System: "You are Ada, a careful assistant.",
			Tools: []core.ToolDef{{
				Name:        "python",
				Description: "Run Python 3.12 and return what it prints.",
				Env:         core.EnvSpec{Tier: 0, Base: "python-3.12"},
			}},
		},
		Profile: "Name: Sam. Prefers short answers.",
		Tail: []core.Event{
			event(t, 1, core.KindUserMsg, core.MsgBody{Text: "What is 6 times 7?"}),
			event(t, 2, core.KindToolCall, core.ToolCall{
				ID: "call_01", Name: "python", Code: "print(6 * 7)", Text: "Let me compute that.",
			}),
			event(t, 3, core.KindToolResult, core.ToolResult{ID: "call_01", Output: "42\n"}),
			event(t, 4, core.KindAgentMsg, core.MsgBody{Text: "It is 42."}),
			event(t, 5, core.KindUserMsg, core.MsgBody{Text: "And <b>7 times 8</b>? Say \"café\" & show the code."}),
		},
	}
	recall := []plan.Recalled{{ID: 3, Text: "Sam asked about Postgres vacuum settings."}}
	p, err := plan.Build(nil, in, recall, fixedNow, plan.DefaultParams())
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	p.AppendTail(
		core.ToolCall{ID: "call_02", Name: "python", Code: "print(7 * 8)"},
		core.ToolResult{ID: "call_02", Output: "56\n"},
	)
	return p
}

func TestRequestGolden(t *testing.T) {
	t.Parallel()
	p, err := New(Config{APIKey: "test-key", DefaultModel: "gpt-test"})
	if err != nil {
		t.Fatal(err)
	}
	route, err := p.Route(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	want := fixture(t, "request_full.golden.json")
	// The pinning key picks a replica and travels in a header; it must not
	// move a byte of the body.
	for _, o := range []llm.Options{{}, {Affinity: "0123456789abcdef0123456789abcdef"}} {
		f, err := p.Prepare(t.Context(), route, fullPlan(t), o)
		if err != nil {
			t.Fatalf("Prepare: %v", err)
		}
		if !bytes.Equal(f.Body, want) {
			t.Errorf("request body differs from the golden file\n got: %s\nwant: %s", f.Body, want)
		}
		if f.Header.Get("Authorization") != "Bearer test-key" || f.Header.Get("X-Contactd-Affinity") != o.Affinity {
			t.Errorf("headers = %v", f.Header)
		}
	}

	// A server that checks no key is sent none.
	open, err := New(Config{DefaultModel: "gpt-test"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := open.Prepare(t.Context(), route, fullPlan(t), llm.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := f.Header["Authorization"]; ok {
		t.Error("sent an Authorization header with no API key configured")
	}

	// A cap is sent under the name newer servers require, and held to the
	// ceiling the operator configured for the model.
	capped, err := New(Config{Models: []Model{{Name: "gpt-test", MaxOutput: 99}}})
	if err != nil {
		t.Fatal(err)
	}
	cr, err := capped.Route(t.Context(), "gpt-test")
	if err != nil {
		t.Fatal(err)
	}
	cf, err := capped.Prepare(t.Context(), cr, fullPlan(t), llm.Options{MaxOutput: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(cf.Body, []byte(`"parallel_tool_calls":false,"max_completion_tokens":99,"stream":true`)) {
		t.Errorf("capped body = %s", cf.Body)
	}
}

const routedContacts = 20_000

func replicaURLs(n int) []string {
	urls := make([]string, n)
	for i := range urls {
		urls[i] = fmt.Sprintf("http://gpu-%d.inference.internal:8000/v1", i)
	}
	return urls
}

// key stands in for llm.Client.Affinity: an opaque string per contact.
func key(contact int) string { return fmt.Sprintf("contact-key-%d", contact) }

func assignments(t *testing.T, urls []string) []string {
	t.Helper()
	p, err := New(Config{DefaultModel: "m", BaseURLs: urls})
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, routedContacts)
	for i := range out {
		out[i] = p.pick(key(i + 1)).dialect.URL
	}
	return out
}

func TestRendezvousIsStable(t *testing.T) {
	t.Parallel()
	urls := replicaURLs(4)
	first := assignments(t, urls)

	// A second provider, as after a restart, agrees with the first.
	for i, got := range assignments(t, urls) {
		if got != first[i] {
			t.Fatalf("contact %d moved from %s to %s between providers", i+1, first[i], got)
		}
	}
	// The order replicas are listed in must not matter either: a config edit
	// that only reorders the list should not cool every cache.
	reordered := []string{urls[2], urls[0], urls[3], urls[1]}
	for i, got := range assignments(t, reordered) {
		if got != first[i] {
			t.Fatalf("contact %d moved from %s to %s when the list was reordered", i+1, first[i], got)
		}
	}
}

func TestRendezvousSpreadsContacts(t *testing.T) {
	t.Parallel()
	const n = 4
	counts := map[string]int{}
	for _, endpoint := range assignments(t, replicaURLs(n)) {
		counts[endpoint]++
	}
	if len(counts) != n {
		t.Fatalf("contacts landed on %d of %d replicas", len(counts), n)
	}
	// Keys that differ in a trailing digit are the worst case for a weak
	// hash. An even split is 5000 each; allow 10% either way.
	want := routedContacts / n
	for endpoint, got := range counts {
		if got < want*9/10 || got > want*11/10 {
			t.Errorf("%s serves %d contacts, want about %d", endpoint, got, want)
		}
	}
}

func TestRendezvousAddingReplicaMovesFew(t *testing.T) {
	t.Parallel()
	const n = 4
	urls := replicaURLs(n + 1)
	before := assignments(t, urls[:n])
	after := assignments(t, urls)
	added := urls[n] + "/chat/completions"

	moved := 0
	for i := range before {
		if before[i] == after[i] {
			continue
		}
		moved++
		// A contact only ever leaves for the new replica; nothing shuffles
		// between the old ones.
		if after[i] != added {
			t.Fatalf("contact %d moved from %s to %s, not to the new replica", i+1, before[i], after[i])
		}
	}
	// The newcomer should take its fair share, 1/(n+1) = 20%, and no more.
	share := float64(moved) / routedContacts
	if share < 0.17 || share > 0.23 {
		t.Errorf("%.1f%% of contacts moved, want about %.0f%%", 100*share, 100.0/(n+1))
	}
}

func TestSingleReplicaAndNoKey(t *testing.T) {
	t.Parallel()
	one, err := New(Config{DefaultModel: "m", BaseURLs: []string{"http://only:8000/v1"}})
	if err != nil {
		t.Fatal(err)
	}
	for id := range 100 {
		if got := one.pick(key(id)).dialect.URL; got != "http://only:8000/v1/chat/completions" {
			t.Fatalf("contact %d routed to %s", id, got)
		}
	}
	// A call that belongs to no contact takes the first replica listed.
	urls := replicaURLs(3)
	many, err := New(Config{DefaultModel: "m", BaseURLs: urls})
	if err != nil {
		t.Fatal(err)
	}
	if got := many.pick("").dialect.URL; got != urls[0]+"/chat/completions" {
		t.Errorf("no key routed to %s, want the first replica", got)
	}
}

func TestSendRoutesByAffinity(t *testing.T) {
	t.Parallel()
	reply := fixture(t, "reply.sse")
	const replicas = 3
	var hits [replicas]atomic.Int64
	urls := make([]string, replicas)
	for i := range urls {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			hits[i].Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write(reply)
		}))
		t.Cleanup(srv.Close)
		urls[i] = srv.URL
	}
	p, err := New(Config{DefaultModel: "m", BaseURLs: urls})
	if err != nil {
		t.Fatal(err)
	}
	route, err := p.Route(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}

	const contacts, rounds = 12, 3
	for round := range rounds {
		for id := range contacts {
			f, err := p.Prepare(t.Context(), route, fullPlan(t), llm.Options{Affinity: key(id)})
			if err != nil {
				t.Fatal(err)
			}
			s, err := p.Send(t.Context(), f)
			if err != nil {
				t.Fatalf("round %d contact %d: %v", round, id, err)
			}
			for range s.Events() { //nolint:revive // draining
			}
			if res, err := s.Result(); err != nil || res.Text != "Six times seven is 42." {
				t.Fatalf("round %d contact %d: %+v, %v", round, id, res, err)
			}
		}
	}

	total, used := int64(0), 0
	for i := range hits {
		n := hits[i].Load()
		total += n
		if n > 0 {
			used++
		}
		// Every contact returned to its replica each round, so each replica
		// saw a whole number of contacts, every round.
		if n%rounds != 0 {
			t.Errorf("replica %d saw %d requests, not a multiple of %d", i, n, rounds)
		}
	}
	if total != contacts*rounds {
		t.Errorf("replicas saw %d requests in total, want %d", total, contacts*rounds)
	}
	if used < 2 {
		t.Errorf("only %d of %d replicas were used", used, replicas)
	}
}
