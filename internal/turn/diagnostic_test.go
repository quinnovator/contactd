package turn_test

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"testing"

	"github.com/cespare/xxhash/v2"

	"github.com/quinnovator/contactd/internal/diagnostic"
	"github.com/quinnovator/contactd/internal/llm"
	fakellm "github.com/quinnovator/contactd/internal/llm/fake"
	"github.com/quinnovator/contactd/internal/plan"
	"github.com/quinnovator/contactd/internal/turn"
)

type watchingModels struct {
	turn.Models
	before func(*plan.PromptPlan, llm.Options)
}

func (m watchingModels) Call(ctx context.Context, r llm.Route, p *plan.PromptPlan, o llm.Options) (*llm.Call, error) {
	m.before(p, o)
	return m.Models.Call(ctx, r, p, o)
}

func TestDiagnosticsPrecedeSendAndMatchBothToolSteps(t *testing.T) {
	t.Parallel()
	b := diagnostic.New(10)
	d := deps(fakellm.New(fakellm.Options{Mode: fakellm.CachePrefix}))
	d.Diagnostics = b
	s := &sink{}
	in := input(s, &leaser{}, "!python print(42)")
	in.Name = "ada"
	d.Models = watchingModels{Models: d.Models, before: func(p *plan.PromptPlan, o llm.Options) {
		h, _, stop := b.Subscribe()
		defer stop()
		last := h[len(h)-1]
		if last.Done || last.Total() != p.InputTokens() || last.Name != "ada" || len(last.Tiers) != plan.NumTiers {
			t.Fatalf("pre-send snapshot does not describe this prompt: %+v", last)
		}
		if last.PredictedCached != llm.PredictCached(llm.CacheAutoPrefix, o.Prev, p.PrefixMarks()) {
			t.Fatal("wrong pre-send prediction")
		}
	}}
	if _, err := turn.Run(t.Context(), in, d); err != nil {
		t.Fatal(err)
	}
	h, _, stop := b.Subscribe()
	defer stop()
	if len(h) != 2 || len(s.attempts) != 2 {
		t.Fatalf("%d snapshots for %d attempts", len(h), len(s.attempts))
	}
	for i, got := range h {
		a := s.attempts[i]
		if !got.Done || !got.Reported || got.Step != i+1 || got.PredictedCached != a.Predicted || got.ReportedInput != a.Input || got.ReportedCached != a.Cached {
			t.Errorf("snapshot %+v disagrees with attempt %+v", got, a)
		}
	}
	if !slices.Contains(h[1].Events, "tool") || h[1].Tiers[4].Tokens <= h[0].Tiers[4].Tokens {
		t.Fatal("tool step was not an append in the live tier")
	}
}

func TestDiagnosticsCompleteFailedCallsWithoutInventingUsage(t *testing.T) {
	t.Parallel()
	b := diagnostic.New(2)
	d := deps(fakellm.New(fakellm.Options{Fail: func(int, []byte) error { return errors.New("private provider error") }}))
	d.Diagnostics = b
	if _, err := turn.Run(t.Context(), input(&sink{}, &leaser{}, "hello"), d); err == nil {
		t.Fatal("expected failed call")
	}
	h, _, stop := b.Subscribe()
	defer stop()
	if len(h) != 1 || !h[0].Done || h[0].Reported || h[0].Error == "" || h[0].Error == "private provider error" {
		t.Fatalf("wrong failed-call metadata: %+v", h)
	}
}

func TestBackgroundSummaryHasContactScope(t *testing.T) {
	t.Parallel()
	b := diagnostic.New(2)
	m := &turn.ModelSummarizer{Models: fakellm.NewClient(fakellm.New(fakellm.Options{})), Diagnostics: b}
	ctx := diagnostic.WithScope(t.Context(), diagnostic.Scope{Contact: 7, Name: "ada", Turn: 4})
	if _, err := m.Summarize(ctx, "Ada discussed the launch."); err != nil {
		t.Fatal(err)
	}
	h, _, stop := b.Subscribe()
	defer stop()
	if len(h) != 1 || !h[0].Done || h[0].Purpose != "summary" || h[0].Contact != 7 || h[0].Name != "ada" {
		t.Fatalf("summary scope missing: %+v", h)
	}
}

func TestDiagnosticWireCaptureMatchesSentRequestHash(t *testing.T) {
	t.Parallel()
	b := diagnostic.New(2)
	d := deps(fakellm.New(fakellm.Options{}))
	d.Diagnostics = b
	s := &sink{}
	if _, err := turn.Run(t.Context(), input(s, &leaser{}, "hello"), d); err != nil {
		t.Fatal(err)
	}
	h, _, stop := b.Subscribe()
	defer stop()
	_, detail, ok := b.Details(h[0].Run, h[0].Call)
	if !ok || detail.Wire == "" || detail.WireBytes != len(detail.Wire) {
		t.Fatalf("wire capture missing: %+v", detail)
	}
	if got := strconv.FormatUint(xxhash.Sum64String(detail.Wire), 16); got != s.attempts[0].BodyHash {
		t.Fatalf("capture hash %s differs from attempt %+v", got, s.attempts[0])
	}
}
