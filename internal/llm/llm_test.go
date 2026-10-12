package llm_test

import (
	"context"
	"errors"
	"iter"
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

// gauge measures how many requests are in flight at the provider: from the
// moment one is sent until its stream has been asked how it ended.
type gauge struct {
	*fake.Provider
	live, peak atomic.Int32
}

type gauged struct {
	llm.Stream
	done func()
}

func (g gauged) Result() (llm.Result, error) {
	defer g.done()
	return g.Stream.Result()
}

func (g *gauge) Send(ctx context.Context, f *llm.Frozen) (llm.Stream, error) {
	n := g.live.Add(1)
	for {
		old := g.peak.Load()
		if n <= old || g.peak.CompareAndSwap(old, n) {
			break
		}
	}
	s, err := g.Provider.Send(ctx, f)
	if err != nil {
		g.live.Add(-1)
		return nil, err
	}
	return gauged{Stream: s, done: sync.OnceFunc(func() { g.live.Add(-1) })}, nil
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
		p := &gauge{Provider: fake.New(fake.Options{FirstToken: time.Second})}
		c := client(t, llm.ClientConfig{Providers: []llm.Provider{p}, Limits: map[string]int{"fake": 2}})
		r, _ := c.Route(t.Context(), "", "")
		var wg sync.WaitGroup
		for range 6 {
			wg.Go(func() {
				call, err := c.Call(t.Context(), r, plan.Prompt("s", "hello"), llm.Options{})
				if err != nil {
					t.Error(err)
					return
				}
				drain(t, call)
			})
		}
		start := time.Now()
		wg.Wait()
		if got := p.peak.Load(); got != 2 {
			t.Errorf("%d requests were in flight at the provider at once, want 2", got)
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

// lateFailure accepts each request, then fails some of them after the
// response has started: before any event (the first failBefore sends) or
// after the first one.
type lateFailure struct {
	*fake.Provider
	failBefore, failAfter int
	sends                 atomic.Int32
}

type brokenStream struct {
	events []llm.Event
	err    error
}

func (s brokenStream) Events() iter.Seq[llm.Event] {
	return func(yield func(llm.Event) bool) {
		for _, ev := range s.events {
			if !yield(ev) {
				return
			}
		}
	}
}

func (s brokenStream) Result() (llm.Result, error) { return llm.Result{}, s.err }

func (p *lateFailure) Send(ctx context.Context, f *llm.Frozen) (llm.Stream, error) {
	n := int(p.sends.Add(1))
	overloaded := &llm.Error{Class: llm.ErrOverloaded, Provider: "fake", Err: errors.New("overloaded_error")}
	switch {
	case n <= p.failBefore:
		return brokenStream{err: overloaded}, nil
	case n <= p.failBefore+p.failAfter:
		return brokenStream{events: []llm.Event{{Kind: llm.TextDelta, Text: "Got"}}, err: overloaded}, nil
	}
	return p.Provider.Send(ctx, f)
}

func TestAFailureBeforeTheFirstEventIsRetried(t *testing.T) {
	t.Parallel()
	// HTTP 200, then an error frame or a dropped connection before any
	// event: nothing has reached a subscriber, so it is retried.
	p := &lateFailure{Provider: fake.New(fake.Options{}), failBefore: 2}
	c := client(t, llm.ClientConfig{
		Providers: []llm.Provider{p},
		Retry:     llm.RetryPolicy{Attempts: 4, Base: time.Millisecond, Max: time.Millisecond},
	})
	r, _ := c.Route(t.Context(), "", "")
	call, err := c.Call(t.Context(), r, plan.Prompt("s", "hello"), llm.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	for ev := range call.Events() {
		text.WriteString(ev.Text)
	}
	res, err := call.Result()
	if err != nil || res.Text == "" || text.String() != res.Text {
		t.Fatalf("result %q (streamed %q), err %v", res.Text, text.String(), err)
	}
	if got := call.Attempt(); got.Tries != 3 || p.sends.Load() != 3 {
		t.Errorf("tries = %d, sends = %d; want 3 of each", got.Tries, p.sends.Load())
	}
}

func TestAFailureAfterTheFirstEventIsNotRetried(t *testing.T) {
	t.Parallel()
	// Once text has gone out, sending the request again would say it twice.
	p := &lateFailure{Provider: fake.New(fake.Options{}), failAfter: 1}
	c := client(t, llm.ClientConfig{Providers: []llm.Provider{p}})
	r, _ := c.Route(t.Context(), "", "")
	call, err := c.Call(t.Context(), r, plan.Prompt("s", "hello"), llm.Options{})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for range call.Events() {
		n++
	}
	if _, err := call.Result(); llm.ClassOf(err) != llm.ErrOverloaded {
		t.Errorf("err = %v, want the overloaded error passed through", err)
	}
	if n != 1 || p.sends.Load() != 1 || call.Attempt().Tries != 1 || call.Attempt().Class != llm.ErrOverloaded {
		t.Errorf("%d events, %d sends, attempt %+v", n, p.sends.Load(), call.Attempt())
	}
}

func TestRetriesBeforeTheFirstEventRunOut(t *testing.T) {
	t.Parallel()
	p := &lateFailure{Provider: fake.New(fake.Options{}), failBefore: 99}
	c := client(t, llm.ClientConfig{
		Providers: []llm.Provider{p},
		Retry:     llm.RetryPolicy{Attempts: 3, Base: time.Millisecond, Max: time.Millisecond},
		Limits:    map[string]int{"fake": 1},
	})
	r, _ := c.Route(t.Context(), "", "")
	for range 2 { // twice: the slot must come back each time
		call, err := c.Call(t.Context(), r, plan.Prompt("s", "hello"), llm.Options{})
		if err != nil {
			t.Fatal(err)
		}
		for range call.Events() {
			t.Error("a failed stream yielded an event")
		}
		if _, err := call.Result(); llm.ClassOf(err) != llm.ErrOverloaded || call.Attempt().Tries != 3 {
			t.Errorf("err = %v after %d tries", err, call.Attempt().Tries)
		}
	}
}

func TestResultOnAFailedCallReleasesItsSlotOnce(t *testing.T) {
	t.Parallel()
	fails := true
	p := fake.New(fake.Options{Fail: func(int, []byte) error {
		if fails {
			return &llm.Error{Class: llm.ErrAuth, Provider: "fake"}
		}
		return nil
	}})
	c := client(t, llm.ClientConfig{Providers: []llm.Provider{p}, Limits: map[string]int{"fake": 1}})
	r, _ := c.Route(t.Context(), "", "")
	call, err := c.Call(t.Context(), r, plan.Prompt("s", "hello"), llm.Options{})
	if llm.ClassOf(err) != llm.ErrAuth {
		t.Fatalf("err = %v", err)
	}
	// Asking a failed call for its result must not take a second slot back.
	done := make(chan struct{})
	go func() { _, _ = call.Result(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Result on a failed call never returned")
	}
	// And the one slot is still exactly one slot.
	fails = false
	first, err := c.Call(t.Context(), r, plan.Prompt("s", "a"), llm.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if _, err := c.Call(ctx, r, plan.Prompt("s", "b"), llm.Options{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a second call got in beside the first: %v", err)
	}
	drain(t, first)
}
