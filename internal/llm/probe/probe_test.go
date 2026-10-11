package probe_test

import (
	"bytes"
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/quinnovator/contactd/internal/llm"
	"github.com/quinnovator/contactd/internal/llm/fake"
	"github.com/quinnovator/contactd/internal/llm/probe"
	"github.com/quinnovator/contactd/internal/plan"
)

// upstream is a fake endpoint that behaves like a reasoning model with an
// automatic prefix cache: it returns reasoning, rejects a tool loop that
// leaves it out, and makes two tool calls when asked for two.
func upstream(caps llm.Capabilities, fail func(int, []byte) error) *fake.Provider {
	return fake.New(fake.Options{
		Caps: &caps,
		Fail: fail,
		Rules: []fake.Rule{{
			Match: regexp.MustCompile(`Use the python tool twice`),
			Tool:  "python", Code: "print(6*7)", Calls: 2,
		}},
	})
}

func honest() llm.Capabilities {
	return llm.Capabilities{
		ContextWindow: 1_000_000, MaxOutput: 8192,
		Cache: llm.CacheAutoPrefix, CacheTTL: time.Hour, CacheUsage: true,
		Efforts: []llm.Effort{llm.EffortLow, llm.EffortHigh, llm.EffortMax}, ReasoningOff: true,
		Replay: llm.ReplayAllTurns,
	}
}

func byCheck(t *testing.T, fs []probe.Finding) map[string]probe.Finding {
	t.Helper()
	out := make(map[string]probe.Finding, len(fs))
	for _, f := range fs {
		if _, dup := out[f.Check]; dup {
			t.Errorf("check %q reported twice", f.Check)
		}
		out[f.Check] = f
	}
	return out
}

func TestAnHonestProfilePasses(t *testing.T) {
	t.Parallel()
	p := upstream(honest(), nil)
	rep, err := probe.Run(t.Context(), p, "", probe.Options{Now: 1_760_000_000_000})
	if err != nil {
		t.Fatal(err)
	}
	fs := rep.Findings
	got := byCheck(t, fs)
	for _, check := range []string{
		"unknown model name", "plain reply", "repeated prefix", "tool call", "calls per step",
		"reasoning payload", "tool loop continues", "reasoning left out",
		"effort none", "effort low", "effort high", "effort max",
	} {
		f, ok := got[check]
		if !ok {
			t.Errorf("no finding for %q", check)
			continue
		}
		if f.Verdict != probe.OK {
			t.Errorf("%s: %s (declared %q, observed %q)", check, f.Verdict, f.Declared, f.Observed)
		}
	}
	if probe.AnyDiffers(fs) {
		t.Error("an honest profile was reported as differing")
	}
	if !strings.Contains(got["calls per step"].Observed, "2 calls") || !strings.Contains(got["reasoning left out"].Observed, "rejected") {
		t.Errorf("observations: %q / %q", got["calls per step"].Observed, got["reasoning left out"].Observed)
	}

	var buf bytes.Buffer
	if err := probe.Write(&buf, rep); err != nil {
		t.Fatal(err)
	}
	report := buf.String()
	if rep.Requests != p.Sends() || rep.Requests < 9 || rep.Usage.Input == 0 || rep.Usage.Cached == 0 {
		t.Errorf("the report says %d requests and %+v; the provider saw %d", rep.Requests, rep.Usage, p.Sends())
	}
	if !strings.Contains(report, "fake/fake-1") || !strings.Contains(report, "requests") || !strings.Contains(report, "repeated prefix") || strings.Contains(report, "DIFFERS") {
		t.Errorf("report:\n%s", report)
	}
}

// liar declares one set of capabilities while its upstream has another.
type liar struct {
	*fake.Provider
	declared llm.Capabilities
	actual   llm.Capabilities
	anyModel bool
}

func (l *liar) Route(ctx context.Context, model string) (llm.Route, error) {
	if l.anyModel {
		model = ""
	}
	r, err := l.Provider.Route(ctx, model)
	r.Caps = l.declared
	return r, err
}

func (l *liar) Prepare(ctx context.Context, r llm.Route, p *plan.PromptPlan, o llm.Options) (*llm.Frozen, error) {
	r.Caps = l.actual // the upstream does what it does, whatever was declared
	return l.Provider.Prepare(ctx, r, p, o)
}

func TestWrongDeclarationsAreReported(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		declare func(*llm.Capabilities)
		any     bool
		check   string
	}{
		"cache hits are reported though usage is declared absent": {
			declare: func(c *llm.Capabilities) { c.CacheUsage = false }, check: "repeated prefix",
		},
		"a cache is declared and never hit": {
			// The upstream has no cache; the profile says it does.
			declare: func(c *llm.Capabilities) {}, check: "repeated prefix",
		},
		"several calls come back from a route declared single-call": {
			declare: func(c *llm.Capabilities) { c.SingleToolCall = true }, check: "calls per step",
		},
		"reasoning is required by a route declared as not replaying": {
			declare: func(c *llm.Capabilities) { c.Replay = llm.ReplayNone }, check: "reasoning payload",
		},
		"unknown model names are passed through": {
			declare: func(c *llm.Capabilities) {}, any: true, check: "unknown model name",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			actual := honest()
			if name == "a cache is declared and never hit" {
				actual.Cache = llm.CacheNone
			}
			declared := honest()
			tc.declare(&declared)
			p := &liar{Provider: upstream(actual, nil), declared: declared, actual: actual, anyModel: tc.any}
			rep, err := probe.Run(t.Context(), p, "", probe.Options{})
			if err != nil {
				t.Fatal(err)
			}
			fs := rep.Findings
			f := byCheck(t, fs)[tc.check]
			if f.Verdict != probe.Differs {
				t.Errorf("%s: verdict %s (declared %q, observed %q), want DIFFERS", tc.check, f.Verdict, f.Declared, f.Observed)
			}
			if !probe.AnyDiffers(fs) {
				t.Error("AnyDiffers missed it")
			}
		})
	}
}

func TestWhatCannotBeSettledIsNotCalledWrong(t *testing.T) {
	t.Parallel()
	// A model that makes one call when asked for two proves nothing about a
	// route that allows several; and where leaving reasoning out is accepted,
	// the stricter declaration stands but is flagged for a human.
	caps := honest()
	caps.Replay = llm.ReplaySameTurn // the fake enforces only all-turns replay
	p := fake.New(fake.Options{Caps: &caps, Rules: []fake.Rule{{
		Match: regexp.MustCompile(`Use the python tool twice`), Tool: "python", Code: "print(6*7)",
	}}})
	rep, err := probe.Run(t.Context(), p, "", probe.Options{})
	if err != nil {
		t.Fatal(err)
	}
	fs := rep.Findings
	got := byCheck(t, fs)
	for _, check := range []string{"calls per step", "reasoning left out"} {
		if got[check].Verdict != probe.Inconclusive {
			t.Errorf("%s: %s, want inconclusive (%q)", check, got[check].Verdict, got[check].Observed)
		}
	}
	if probe.AnyDiffers(fs) {
		t.Error("an inconclusive probe must not be reported as a contradiction")
	}
}

func TestAFailingEndpointIsReportedNotFatal(t *testing.T) {
	t.Parallel()
	p := upstream(honest(), func(int, []byte) error {
		return &llm.Error{Class: llm.ErrAuth, Status: 401, Provider: "fake", Err: errors.New("invalid api key")}
	})
	rep, err := probe.Run(t.Context(), p, "", probe.Options{})
	if err != nil {
		t.Fatal(err)
	}
	fs := rep.Findings
	failed := 0
	for _, f := range fs {
		if f.Verdict == probe.Failed {
			failed++
			if !strings.Contains(f.Observed, "auth") {
				t.Errorf("%s: observed %q, want the error class", f.Check, f.Observed)
			}
		}
	}
	if failed < 4 {
		t.Errorf("%d checks failed, want every request-backed check to say so: %+v", failed, fs)
	}
	if _, err := probe.Run(t.Context(), p, "no-such-model", probe.Options{}); !errors.Is(err, llm.ErrUnknownModel) {
		t.Errorf("probing an unknown model: %v", err)
	}
}
