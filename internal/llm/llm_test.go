package llm_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/quinnovator/contactd/internal/llm"
	"github.com/quinnovator/contactd/internal/llm/fake"
	"github.com/quinnovator/contactd/internal/obs"
	"github.com/quinnovator/contactd/internal/plan"
)

func TestNearestEffort(t *testing.T) {
	t.Parallel()
	deepseek := llm.Capabilities{Efforts: []llm.Effort{llm.EffortLow, llm.EffortHigh, llm.EffortMax}, ReasoningOff: true}
	noOff := llm.Capabilities{Efforts: []llm.Effort{llm.EffortLow, llm.EffortMedium, llm.EffortHigh}}
	offOnly := llm.Capabilities{ReasoningOff: true}
	var none llm.Capabilities
	cases := []struct {
		name string
		caps *llm.Capabilities
		want map[llm.Effort]llm.Effort
	}{
		{"low/high/max with off", &deepseek, map[llm.Effort]llm.Effort{
			llm.EffortDefault: llm.EffortDefault,
			llm.EffortNone:    llm.EffortNone,
			llm.EffortLow:     llm.EffortLow,
			llm.EffortMedium:  llm.EffortHigh, // a tie goes to the stronger
			llm.EffortHigh:    llm.EffortHigh,
			llm.EffortXHigh:   llm.EffortMax,
			llm.EffortMax:     llm.EffortMax,
		}},
		{"low/medium/high, cannot switch off", &noOff, map[llm.Effort]llm.Effort{
			llm.EffortNone:  llm.EffortLow, // the weakest it accepts
			llm.EffortXHigh: llm.EffortHigh,
			llm.EffortMax:   llm.EffortHigh,
		}},
		{"on or off only", &offOnly, map[llm.Effort]llm.Effort{
			llm.EffortNone: llm.EffortNone,
			llm.EffortHigh: llm.EffortDefault,
		}},
		{"no control", &none, map[llm.Effort]llm.Effort{
			llm.EffortNone: llm.EffortDefault,
			llm.EffortMax:  llm.EffortDefault,
		}},
	}
	for _, tc := range cases {
		for want, got := range tc.want {
			if n := tc.caps.Nearest(want); n != got {
				t.Errorf("%s: Nearest(%q) = %q, want %q", tc.name, want, n, got)
			}
		}
	}
	for _, name := range []string{"", "none", "low", "medium", "high", "xhigh", "max"} {
		e, err := llm.ParseEffort(name)
		if err != nil || e.String() != name {
			t.Errorf("ParseEffort(%q) = %q, %v", name, e, err)
		}
	}
	if _, err := llm.ParseEffort("extreme"); err == nil {
		t.Error("an unknown effort name must be an error")
	}
}

func TestPredictCached(t *testing.T) {
	t.Parallel()
	mark := func(h uint64, tokens int, bp bool) plan.Mark {
		return plan.Mark{Hash: h, Tokens: tokens, Breakpoint: bp}
	}
	// Previous request: static(bp) recall(bp) tail tail(bp) live.
	prev := []plan.Mark{mark(1, 100, true), mark(2, 300, true), mark(3, 400, false), mark(4, 500, true), mark(5, 520, false)}
	// Same through the first tail message, then different.
	cur := []plan.Mark{mark(1, 100, true), mark(2, 300, true), mark(3, 400, false), mark(40, 505, true), mark(50, 530, false)}

	if got := llm.PredictCached(llm.CacheAutoPrefix, prev, cur); got != 400 {
		t.Errorf("automatic prefix: %d, want 400 (the whole shared prefix)", got)
	}
	if got := llm.PredictCached(llm.CacheBreakpoints, prev, cur); got != 300 {
		t.Errorf("breakpoints: %d, want 300 (back to the last breakpoint the previous request wrote)", got)
	}
	if got := llm.PredictCached(llm.CacheNone, prev, cur); got != 0 {
		t.Errorf("no cache: %d, want 0", got)
	}
	if got := llm.PredictCached(llm.CacheAutoPrefix, nil, cur); got != 0 {
		t.Errorf("cold: %d, want 0", got)
	}
	if got := llm.PredictCached(llm.CacheAutoPrefix, prev, prev); got != 520 {
		t.Errorf("identical: %d, want 520", got)
	}
	other := []plan.Mark{mark(9, 100, true)}
	if got := llm.PredictCached(llm.CacheBreakpoints, prev, other); got != 0 {
		t.Errorf("nothing shared: %d, want 0", got)
	}
}

func client(t *testing.T, cfg llm.ClientConfig) *llm.Client {
	t.Helper()
	cfg.Logger = obs.Discard()
	cfg.AffinitySecret = []byte("secret")
	c, err := llm.NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func drain(t *testing.T, call *llm.Call) llm.Result {
	t.Helper()
	for range call.Events() { //nolint:revive // only the result matters
	}
	res, err := call.Result()
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestClientRoutesStrictly(t *testing.T) {
	t.Parallel()
	a := fake.New(fake.Options{ID: "a", Models: []string{"a-1", "a-2"}})
	b := fake.New(fake.Options{ID: "b", Models: []string{"b-1"}})
	c := client(t, llm.ClientConfig{Providers: []llm.Provider{a, b}, Default: "b"})

	for _, tc := range []struct{ provider, model, want string }{
		{"", "", "b/b-1"},
		{"a", "", "a/a-1"},
		{"a", "a-2", "a/a-2"},
	} {
		r, err := c.Route(t.Context(), tc.provider, tc.model)
		if err != nil || r.ID() != tc.want {
			t.Errorf("Route(%q, %q) = %q, %v; want %q", tc.provider, tc.model, r.ID(), err, tc.want)
		}
	}
	if _, err := c.Route(t.Context(), "a", "b-1"); !errors.Is(err, llm.ErrUnknownModel) {
		t.Errorf("a model from another provider: %v, want ErrUnknownModel", err)
	}
	if _, err := c.Route(t.Context(), "nope", ""); !errors.Is(err, llm.ErrUnknownProvider) {
		t.Errorf("unknown provider: %v, want ErrUnknownProvider", err)
	}
	if _, err := llm.NewClient(llm.ClientConfig{Providers: []llm.Provider{a, a}, Logger: obs.Discard()}); err == nil {
		t.Error("the same provider twice must be refused")
	}
	if _, err := llm.NewClient(llm.ClientConfig{Providers: []llm.Provider{a}, Default: "zzz", Logger: obs.Discard()}); !errors.Is(err, llm.ErrUnknownProvider) {
		t.Errorf("unknown default provider: %v", err)
	}
}

func TestAffinityIsAKeyedHash(t *testing.T) {
	t.Parallel()
	c := client(t, llm.ClientConfig{Providers: []llm.Provider{fake.New(fake.Options{})}})
	k1, k2 := c.Affinity(1234567), c.Affinity(1234568)
	if k1 == k2 || k1 != c.Affinity(1234567) {
		t.Errorf("keys must be stable and distinct: %q %q", k1, k2)
	}
	if strings.Contains(k1, "1234567") || len(k1) != 32 {
		t.Errorf("key %q must not reveal the contact ID", k1)
	}
	other, err := llm.NewClient(llm.ClientConfig{Providers: []llm.Provider{fake.New(fake.Options{})}, AffinitySecret: []byte("another"), Logger: obs.Discard()})
	if err != nil {
		t.Fatal(err)
	}
	if other.Affinity(1234567) == k1 {
		t.Error("the key must depend on the deployment's secret")
	}
}

func TestClientDropsAffinityWhereTheRouteTakesNone(t *testing.T) {
	t.Parallel()
	for _, mode := range []llm.Affinity{llm.AffinityNone, llm.AffinityHeader} {
		caps := llm.Capabilities{ContextWindow: 1000, Affinity: mode}
		rec := &recorder{Provider: fake.New(fake.Options{Caps: &caps})}
		c := client(t, llm.ClientConfig{Providers: []llm.Provider{rec}})
		r, err := c.Route(t.Context(), "", "")
		if err != nil {
			t.Fatal(err)
		}
		call, err := c.Call(t.Context(), r, plan.Prompt("s", "hi"), llm.Options{Affinity: "k3y"})
		if err != nil {
			t.Fatal(err)
		}
		drain(t, call)
		got := rec.frozen[0].Header.Get(fake.AffinityHeader)
		if want := map[llm.Affinity]string{llm.AffinityNone: "", llm.AffinityHeader: "k3y"}[mode]; got != want {
			t.Errorf("affinity mode %d: header = %q, want %q", mode, got, want)
		}
	}
}

// recorder keeps every frozen request it is asked to send.
type recorder struct {
	*fake.Provider
	mu     sync.Mutex
	frozen []*llm.Frozen
}

func (r *recorder) Send(ctx context.Context, f *llm.Frozen) (llm.Stream, error) {
	r.mu.Lock()
	r.frozen = append(r.frozen, f)
	r.mu.Unlock()
	return r.Provider.Send(ctx, f)
}

func TestClientRetriesOnlyBeforeTheStreamStarts(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		fails := []error{
			&llm.Error{Class: llm.ErrRateLimited, RetryAfter: 3 * time.Second, Provider: "fake"},
			&llm.Error{Class: llm.ErrOverloaded, Provider: "fake"},
			nil,
		}
		rec := &recorder{Provider: fake.New(fake.Options{Fail: func(n int, _ []byte) error { return fails[n] }})}
		c := client(t, llm.ClientConfig{
			Providers: []llm.Provider{rec},
			Retry:     llm.RetryPolicy{Attempts: 4, Base: time.Second, Max: 8 * time.Second, MaxRetryAfter: 30 * time.Second},
		})
		r, _ := c.Route(t.Context(), "", "")
		start := time.Now()
		call, err := c.Call(t.Context(), r, plan.Prompt("s", "hello"), llm.Options{})
		if err != nil {
			t.Fatal(err)
		}
		res := drain(t, call)
		att := call.Attempt()
		if att.Tries != 3 || res.Text == "" || att.Stop != llm.StopEnd || att.Class != llm.ErrUnknown {
			t.Errorf("attempt = %+v, text %q; want 3 tries and a reply", att, res.Text)
		}
		// The provider's hint is honoured for the first wait; the second
		// backs off from the base with jitter (1s to 2s for try 2).
		if waited := time.Since(start); waited < 4*time.Second || waited > 5*time.Second {
			t.Errorf("waited %v, want the 3s hint plus 1-2s of backoff", waited)
		}
		// A retry resends the frozen request itself, not a rebuilt one.
		if len(rec.frozen) != 3 || rec.frozen[0] != rec.frozen[1] || rec.frozen[1] != rec.frozen[2] {
			t.Fatalf("%d sends; every retry must send the same frozen request", len(rec.frozen))
		}
		if att.BodyHash == "" || att.Bytes != len(rec.frozen[0].Body) || att.Route != "fake/fake-1" {
			t.Errorf("attempt record incomplete: %+v", att)
		}
	})
}

func TestClientDoesNotRetryWhatCannotSucceed(t *testing.T) {
	t.Parallel()
	for _, class := range []llm.ErrorClass{llm.ErrAuth, llm.ErrBadRequest, llm.ErrQuota, llm.ErrContextOverflow} {
		p := fake.New(fake.Options{Fail: func(int, []byte) error { return &llm.Error{Class: class, Provider: "fake"} }})
		c := client(t, llm.ClientConfig{Providers: []llm.Provider{p}})
		r, _ := c.Route(t.Context(), "", "")
		call, err := c.Call(t.Context(), r, plan.Prompt("s", "hello"), llm.Options{})
		if llm.ClassOf(err) != class {
			t.Errorf("%s: err = %v", class, err)
		}
		if p.Sends() != 1 {
			t.Errorf("%s: sent %d times, want 1", class, p.Sends())
		}
		// The failed call is still described, so it can be logged.
		if call == nil || call.Attempt().Class != class || call.Attempt().Tries != 1 {
			t.Errorf("%s: failed call not recorded: %+v", class, call)
		}
		if _, err := call.Result(); llm.ClassOf(err) != class {
			t.Errorf("%s: Result on a failed call = %v", class, err)
		}
	}
}

func TestClientGivesUpAfterItsAttempts(t *testing.T) {
	t.Parallel()
	p := fake.New(fake.Options{Fail: func(int, []byte) error { return &llm.Error{Class: llm.ErrOverloaded, Provider: "fake"} }})
	c := client(t, llm.ClientConfig{
		Providers: []llm.Provider{p},
		Retry:     llm.RetryPolicy{Attempts: 3, Base: time.Millisecond, Max: time.Millisecond},
	})
	r, _ := c.Route(t.Context(), "", "")
	call, err := c.Call(t.Context(), r, plan.Prompt("s", "hello"), llm.Options{})
	if llm.ClassOf(err) != llm.ErrOverloaded || p.Sends() != 3 || call.Attempt().Tries != 3 {
		t.Errorf("err = %v after %d sends (attempt %+v); want overloaded after 3", err, p.Sends(), call.Attempt())
	}
}

func TestAdmissionHoldsCallsToTheProvidersLimit(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		p := fake.New(fake.Options{FirstToken: time.Second})
		c := client(t, llm.ClientConfig{Providers: []llm.Provider{p}, Limits: map[string]int{"fake": 2}})
		r, _ := c.Route(t.Context(), "", "")
		var peak, live atomic.Int32
		var wg sync.WaitGroup
		for range 6 {
			wg.Go(func() {
				call, err := c.Call(t.Context(), r, plan.Prompt("s", "hello"), llm.Options{})
				if err != nil {
					t.Error(err)
					return
				}
				n := live.Add(1)
				for {
					old := peak.Load()
					if n <= old || peak.CompareAndSwap(old, n) {
						break
					}
				}
				drain(t, call)
				live.Add(-1)
			})
		}
		start := time.Now()
		wg.Wait()
		if peak.Load() != 2 {
			t.Errorf("%d calls were in flight at once, want 2", peak.Load())
		}
		// Six one-second calls, two at a time.
		if took := time.Since(start); took != 3*time.Second {
			t.Errorf("took %v, want 3s", took)
		}

		// A call waiting for a slot gives up when its context does.
		hold, err := c.Call(t.Context(), r, plan.Prompt("s", "a"), llm.Options{})
		if err != nil {
			t.Fatal(err)
		}
		hold2, err := c.Call(t.Context(), r, plan.Prompt("s", "b"), llm.Options{})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		defer cancel()
		if _, err := c.Call(ctx, r, plan.Prompt("s", "c"), llm.Options{}); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("a call that could not be admitted: %v", err)
		}
		drain(t, hold)
		drain(t, hold2)
	})
}

func TestARouteLimitWinsOverItsProviders(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		p := fake.New(fake.Options{Models: []string{"small", "big"}, FirstToken: time.Second})
		c := client(t, llm.ClientConfig{Providers: []llm.Provider{p}, Limits: map[string]int{"fake": 8, "fake/big": 1}})
		big, _ := c.Route(t.Context(), "", "big")
		start := time.Now()
		var wg sync.WaitGroup
		for range 3 {
			wg.Go(func() {
				call, err := c.Call(t.Context(), big, plan.Prompt("s", "hello"), llm.Options{})
				if err != nil {
					t.Error(err)
					return
				}
				drain(t, call)
			})
		}
		wg.Wait()
		if took := time.Since(start); took != 3*time.Second {
			t.Errorf("three calls on a route limited to one took %v, want 3s", took)
		}
	})
}

func TestErrorClasses(t *testing.T) {
	t.Parallel()
	for class, retry := range map[llm.ErrorClass]bool{
		llm.ErrRateLimited: true, llm.ErrOverloaded: true, llm.ErrTransport: true,
		llm.ErrContextOverflow: false, llm.ErrQuota: false, llm.ErrAuth: false, llm.ErrBadRequest: false, llm.ErrUnknown: false,
	} {
		if class.Retryable() != retry {
			t.Errorf("%s: Retryable = %t", class, class.Retryable())
		}
	}
	err := error(&llm.Error{Class: llm.ErrQuota, Status: 402, Provider: "p", Err: errors.New("out of credit")})
	if got := err.Error(); got != "p: quota (HTTP 402): out of credit" {
		t.Errorf("Error() = %q", got)
	}
	if llm.ClassOf(errors.Join(errors.New("wrapped"), err)) != llm.ErrQuota || llm.ClassOf(errors.New("plain")) != llm.ErrUnknown {
		t.Error("ClassOf must see through wrapping and default to unknown")
	}
}
