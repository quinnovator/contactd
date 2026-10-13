package turn_test

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/diagnostic"
	"github.com/quinnovator/contactd/internal/llm/provider/ollamacloud"
	"github.com/quinnovator/contactd/internal/obs"
	"github.com/quinnovator/contactd/internal/turn"
)

// These calls share the real global collectors, so this test is deliberately
// sequential. Parallel turn tests run only after it has finished.
//
//nolint:paralleltest // reads process-wide Prometheus counters before and after
func TestOllamaCacheAccounting(t *testing.T) {
	metric := func(m prometheus.Metric) *dto.Metric {
		t.Helper()
		var out dto.Metric
		if err := m.Write(&out); err != nil {
			t.Fatal(err)
		}
		return &out
	}
	for _, tc := range []struct {
		name     string
		details  string
		cached   int
		reported bool
	}{
		{"cold", `,"prompt_tokens_details":{"cached_tokens":0}`, 0, true},
		{"warm", `,"prompt_tokens_details":{"cached_tokens":768}`, 768, true},
		{"omitted", "", 0, false},
		{"null", `,"prompt_tokens_details":{"cached_tokens":null}`, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Stream  bool `json:"stream"`
					Options struct {
						Usage bool `json:"include_usage"`
					} `json:"stream_options"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil || !request.Stream || !request.Options.Usage || r.URL.Path != "/v1/chat/completions" {
					t.Errorf("wrong streaming request: %+v, %v, %s", request, err, r.URL.Path)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprintf(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"},\"finish_reason\":\"stop\"}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":1000,\"completion_tokens\":1%s}}\n\ndata: [DONE]\n\n", tc.details)
			}))
			defer api.Close()
			p, err := ollamacloud.New(t.Context(), ollamacloud.Config{
				BaseURL: api.URL, DefaultModel: "glm-5.3-flash",
				Models: []ollamacloud.Model{{Name: "glm-5.3-flash"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			d := deps(p)
			d.Diagnostics = diagnostic.New(2)
			s := &sink{}
			before := metric(obs.ProviderCachedTokenRatio).GetHistogram()
			beforeCached := metric(obs.ProviderCachedTokens).GetCounter().GetValue()
			res, err := turn.Run(t.Context(), input(s, &leaser{}, "hello"), d)
			if err != nil {
				t.Fatal(err)
			}
			if res.Final != "Hello" || res.Usage.CacheReported != tc.reported || res.Params.CacheTTL != 0 {
				t.Fatalf("unexpected result: %+v", res)
			}
			if len(s.attempts) != 1 {
				t.Fatalf("attempts = %d", len(s.attempts))
			}
			a := s.attempts[0]
			if a.Input != 1000 || a.Cached != tc.cached || a.CacheReported != tc.reported || a.HasCacheReport() != tc.reported {
				t.Errorf("wrong persisted accounting: %+v", a)
			}
			// The serialized event must retain explicit zero vs missing usage.
			raw, err := json.Marshal(a)
			if err != nil {
				t.Fatal(err)
			}
			var restored core.AttemptBody
			if err := json.Unmarshal(raw, &restored); err != nil || restored.HasCacheReport() != tc.reported {
				t.Errorf("lost report presence in %s: %v", raw, err)
			}
			history, _, stop := d.Diagnostics.Subscribe()
			defer stop()
			if len(history) != 1 || !history[0].Done || history[0].Reported != tc.reported || history[0].ReportedCached != tc.cached {
				t.Errorf("diagnostic accounting disagrees: %+v", history)
			}
			after := metric(obs.ProviderCachedTokenRatio).GetHistogram()
			var samples uint64
			if tc.reported {
				samples = 1
			}
			if after.GetSampleCount()-before.GetSampleCount() != samples || math.Abs(after.GetSampleSum()-before.GetSampleSum()-float64(tc.cached)/1000) > 1e-9 {
				t.Errorf("cache metric recorded missing usage or lost a report: before %v, after %v", before, after)
			}
			if got := metric(obs.ProviderCachedTokens).GetCounter().GetValue() - beforeCached; got != float64(tc.cached) {
				t.Errorf("cached token metric delta = %v, want %d", got, tc.cached)
			}
		})
	}
}
