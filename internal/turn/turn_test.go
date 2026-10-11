package turn_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/quinnovator/contactd/internal/core"
	fakeinfer "github.com/quinnovator/contactd/internal/infer/fake"
	fakellm "github.com/quinnovator/contactd/internal/llm/fake"
	"github.com/quinnovator/contactd/internal/obs"
	"github.com/quinnovator/contactd/internal/plan"
	"github.com/quinnovator/contactd/internal/recall"
	"github.com/quinnovator/contactd/internal/turn"
)

func TestControl(t *testing.T) {
	t.Parallel()

	t.Run("before a tool starts, an interrupt cancels", func(t *testing.T) {
		t.Parallel()
		var c turn.Control
		if !c.Interrupt() {
			t.Fatal("Interrupt during planning should ask for cancellation")
		}
		if !c.Interrupt() {
			t.Error("a second Interrupt should still report cancelled")
		}
	})

	// The two outcomes are exclusive however the calls interleave: either
	// the actor is told to cancel, or the turn is told to re-plan.
	t.Run("interrupt and tool phase never both lose", func(t *testing.T) {
		t.Parallel()
		for range 2000 {
			var c turn.Control
			var cancelled, began, replan bool
			var wg sync.WaitGroup
			wg.Go(func() { cancelled = c.Interrupt() })
			wg.Go(func() {
				if began = turn.BeginTool(&c); began {
					replan = turn.EndTool(&c)
				}
			})
			wg.Wait()
			switch {
			case cancelled && replan:
				t.Fatal("turn was both cancelled and told to re-plan")
			case !cancelled && !replan:
				t.Fatal("interrupt was lost: neither cancelled nor re-planned")
			case cancelled && began:
				// The tool finished before the interrupt landed; the actor
				// cancels a turn that is back to waiting on the model.
			}
		}
	})

	t.Run("no tool starts after cancellation", func(t *testing.T) {
		t.Parallel()
		var c turn.Control
		c.Cancel()
		if turn.BeginTool(&c) {
			t.Error("a tool began after the turn was cancelled")
		}
	})
}

// leaser is a scriptable environment provider.
type leaser struct {
	mu        sync.Mutex
	snapWait  time.Duration
	leaseWait time.Duration
	leaseErr  error
	execErr   error
	leases    []core.SnapshotID // the resume ID of every Lease call
	live      int
	snaps     int
}

func (l *leaser) Lease(ctx context.Context, _ core.ContactID, _ core.EnvSpec, resume core.SnapshotID) (turn.Lease, error) {
	if l.leaseWait > 0 {
		select {
		case <-time.After(l.leaseWait):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.leaseErr != nil {
		return nil, l.leaseErr
	}
	l.leases = append(l.leases, resume)
	l.live++
	return &lease{l: l}, nil
}

type lease struct {
	l    *leaser
	once sync.Once
}

func (x *lease) Exec(_ context.Context, code string) (turn.ExecResult, error) {
	x.l.mu.Lock()
	defer x.l.mu.Unlock()
	if x.l.execErr != nil {
		return turn.ExecResult{}, x.l.execErr
	}
	return turn.ExecResult{Stdout: "out:" + code}, nil
}

func (x *lease) Snapshot(ctx context.Context) (core.SnapshotID, error) {
	if x.l.snapWait > 0 {
		select {
		case <-time.After(x.l.snapWait):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	x.l.mu.Lock()
	defer x.l.mu.Unlock()
	x.l.snaps++
	return core.SnapshotID(fmt.Sprintf("s%d", x.l.snaps)), nil
}

func (x *lease) Release() {
	x.once.Do(func() {
		x.l.mu.Lock()
		x.l.live--
		x.l.mu.Unlock()
	})
}

func (l *leaser) state() (leases []core.SnapshotID, live, snaps int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]core.SnapshotID(nil), l.leases...), l.live, l.snaps
}

// snapLog answers LatestOfKind from a fixed list, newest first.
type snapLog []core.EnvSnapshotBody

func (s snapLog) LatestOfKind(context.Context, core.ContactID, core.EventKind, int) ([]core.Event, error) {
	out := make([]core.Event, len(s))
	for i, b := range s {
		raw, err := json.Marshal(b)
		if err != nil {
			return nil, err
		}
		out[i] = core.Event{Seq: uint64(100 - i), Kind: core.KindEnvSnapshot, Body: raw}
	}
	return out, nil
}

func pySpec() core.EnvSpec    { return core.EnvSpec{Tier: 0, Base: "python-3.12"} }
func jsSpec() core.EnvSpec    { return core.EnvSpec{Tier: 0, Base: "quickjs"} }
func shellSpec() core.EnvSpec { return core.EnvSpec{Tier: 1, Base: "debian"} }

func TestEnvLeasesLazilyAndReuses(t *testing.T) {
	t.Parallel()
	l := &leaser{}
	e := turn.NewEnv(turn.EnvConfig{Contact: 1, Leasers: map[uint8]turn.EnvLeaser{0: l}})
	if leases, _, _ := l.state(); len(leases) != 0 {
		t.Fatal("an environment existed before any tool call")
	}
	for range 3 {
		if _, err := e.Exec(t.Context(), pySpec(), "1+1"); err != nil {
			t.Fatal(err)
		}
	}
	if leases, live, _ := l.state(); len(leases) != 1 || live != 1 {
		t.Fatalf("%d leases, %d live after three calls; want one reused lease", len(leases), live)
	}

	// A different environment parks the current one as a snapshot.
	if _, err := e.Exec(t.Context(), jsSpec(), "1+1"); err != nil {
		t.Fatal(err)
	}
	if leases, live, snaps := l.state(); len(leases) != 2 || live != 1 || snaps != 1 {
		t.Fatalf("after switching base: %d leases, %d live, %d snapshots", len(leases), live, snaps)
	}
	// Going back resumes from that snapshot rather than starting fresh.
	if _, err := e.Exec(t.Context(), pySpec(), "x"); err != nil {
		t.Fatal(err)
	}
	if leases, _, _ := l.state(); leases[2] != "s1" {
		t.Errorf("returning to python resumed from %q, want s1", leases[2])
	}

	snaps := e.Hibernate(t.Context())
	if _, live, _ := l.state(); live != 0 {
		t.Errorf("%d leases still live after Hibernate", live)
	}
	if len(snaps) != 3 {
		t.Errorf("Hibernate returned %d unrecorded snapshots, want 3", len(snaps))
	}
	if again := e.TakeSnapshots(); len(again) != 0 {
		t.Errorf("snapshots were handed out twice: %v", again)
	}
}

func TestEnvResumesFromTheLog(t *testing.T) {
	t.Parallel()
	l := &leaser{}
	e := turn.NewEnv(turn.EnvConfig{
		Contact: 1,
		Leasers: map[uint8]turn.EnvLeaser{0: l},
		Log: snapLog{
			{Spec: jsSpec(), Snapshot: "js-new"},
			{Spec: pySpec(), Snapshot: "py-new"},
			{Spec: pySpec(), Snapshot: "py-old"},
		},
	})
	if _, err := e.Exec(t.Context(), pySpec(), "x"); err != nil {
		t.Fatal(err)
	}
	if leases, _, _ := l.state(); leases[0] != "py-new" {
		t.Errorf("resumed from %q, want the newest python snapshot", leases[0])
	}
}

func TestEnvFailures(t *testing.T) {
	t.Parallel()

	t.Run("no provider for the tier", func(t *testing.T) {
		t.Parallel()
		e := turn.NewEnv(turn.EnvConfig{Contact: 1, Leasers: map[uint8]turn.EnvLeaser{0: &leaser{}}})
		if _, err := e.Exec(t.Context(), shellSpec(), "ls"); !errors.Is(err, core.ErrNoEnvProvider) {
			t.Errorf("err = %v, want ErrNoEnvProvider", err)
		}
	})

	t.Run("a broken environment is dropped, not reused", func(t *testing.T) {
		t.Parallel()
		l := &leaser{execErr: errors.New("instance crashed")}
		e := turn.NewEnv(turn.EnvConfig{Contact: 1, Leasers: map[uint8]turn.EnvLeaser{0: l}})
		if _, err := e.Exec(t.Context(), pySpec(), "x"); err == nil {
			t.Fatal("want the environment's error")
		}
		if _, live, _ := l.state(); live != 0 {
			t.Errorf("the failed lease was kept (%d live)", live)
		}
		l.mu.Lock()
		l.execErr = nil
		l.mu.Unlock()
		if _, err := e.Exec(t.Context(), pySpec(), "x"); err != nil {
			t.Fatalf("the next call should start clean: %v", err)
		}
	})
}

func TestSpeculativeRestore(t *testing.T) {
	t.Parallel()

	t.Run("a prewarmed lease is adopted without leasing again", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			l := &leaser{leaseWait: 400 * time.Millisecond}
			e := turn.NewEnv(turn.EnvConfig{Contact: 1, Leasers: map[uint8]turn.EnvLeaser{1: l}})
			start := time.Now()
			e.Prewarm(t.Context(), shellSpec()) // the tool name just streamed in
			time.Sleep(300 * time.Millisecond)  // the arguments finish streaming
			if _, err := e.Exec(t.Context(), shellSpec(), "ls"); err != nil {
				t.Fatal(err)
			}
			// The restore overlapped the stream: 400 ms total, not 700.
			if got := time.Since(start); got != 400*time.Millisecond {
				t.Errorf("tool ran after %v, want 400ms", got)
			}
			if leases, _, _ := l.state(); len(leases) != 1 {
				t.Errorf("%d leases, want the prewarmed one only", len(leases))
			}
			e.Hibernate(t.Context())
		})
	})

	t.Run("a prewarm nobody uses is released", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			l := &leaser{leaseWait: 100 * time.Millisecond}
			e := turn.NewEnv(turn.EnvConfig{Contact: 1, Leasers: map[uint8]turn.EnvLeaser{0: l, 1: l}})
			e.Prewarm(t.Context(), shellSpec())
			// The model ended up calling a different tool.
			if _, err := e.Exec(t.Context(), pySpec(), "x"); err != nil {
				t.Fatal(err)
			}
			e.Hibernate(t.Context())
			synctest.Wait()
			if _, live, _ := l.state(); live != 0 {
				t.Errorf("%d leases leaked", live)
			}
		})
	})
}

func TestEnvIdleExpiry(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		l := &leaser{}
		notified := 0
		e := turn.NewEnv(turn.EnvConfig{
			Contact: 1, Leasers: map[uint8]turn.EnvLeaser{0: l},
			IdleTTL: 2 * time.Minute, OnIdle: func() { notified++ },
		})
		if _, err := e.Exec(t.Context(), pySpec(), "x"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(90 * time.Second)
		if _, err := e.Exec(t.Context(), pySpec(), "y"); err != nil { // use resets the countdown
			t.Fatal(err)
		}
		time.Sleep(90 * time.Second)
		if _, live, _ := l.state(); live != 1 {
			t.Fatal("the lease was released while still in use within the TTL")
		}
		time.Sleep(31 * time.Second)
		synctest.Wait()
		if _, live, snaps := l.state(); live != 0 || snaps != 1 {
			t.Fatalf("after 2 idle minutes: %d live, %d snapshots; want 0 and 1", live, snaps)
		}
		if notified != 1 {
			t.Errorf("OnIdle called %d times, want 1", notified)
		}
		if got := e.TakeSnapshots(); len(got) != 1 || got[0].Snapshot != "s1" || got[0].Spec.Base != pySpec().Base {
			t.Errorf("TakeSnapshots = %+v", got)
		}
	})
}

// --- Run ---

type sink struct {
	mu      sync.Mutex
	text    strings.Builder
	calls   []core.ToolCall
	results []core.ToolResult
}

func (s *sink) Delta(_ context.Context, d core.Delta) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d.Type == core.DeltaText {
		s.text.WriteString(d.Text)
	}
	return nil
}

func (s *sink) ToolCall(_ context.Context, c core.ToolCall, enter func() bool) error {
	if !enter() {
		return context.Canceled
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, c)
	return nil
}

func (s *sink) ToolResult(_ context.Context, _ string, r core.ToolResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.results = append(s.results, r)
	return nil
}

type embedder struct {
	err     error
	queries *[]string // when set, records every text embedded
}

func (e embedder) Embed(_ context.Context, texts []string, _ recall.Priority) ([][]float32, error) {
	if e.err != nil {
		return nil, e.err
	}
	if e.queries != nil {
		*e.queries = append(*e.queries, texts...)
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = fakeinfer.Embed(t)
	}
	return out, nil
}

type reranker struct{}

func (reranker) Score(_ context.Context, q string, ps []string) ([]float32, error) {
	out := make([]float32, len(ps))
	for i, p := range ps {
		out[i] = fakeinfer.Score(q, p)
	}
	return out, nil
}

func userEvent(seq uint64, text string) core.Event {
	raw, err := json.Marshal(core.MsgBody{Text: text})
	if err != nil {
		panic(err)
	}
	return core.Event{Contact: 1, Seq: seq, TS: int64(seq) * 1000, Kind: core.KindUserMsg, Body: raw}
}

func input(s *sink, l *leaser, text string) turn.Input {
	agent := &core.AgentConfig{
		System: "s",
		Tools:  []core.ToolDef{{Name: "python", Description: "Run Python.", Env: pySpec()}, {Name: "shell", Description: "Run a shell command.", Env: shellSpec()}},
	}
	return turn.Input{
		Contact: 1, TurnID: 1, Agent: agent,
		Tail:    []core.Event{userEvent(1, text)},
		Env:     turn.NewEnv(turn.EnvConfig{Contact: 1, Leasers: map[uint8]turn.EnvLeaser{0: l}}),
		Ctl:     new(turn.Control),
		Sink:    s,
		Started: time.Now(),
	}
}

func deps(llm turn.LLM) *turn.Deps {
	return &turn.Deps{
		Embed: embedder{}, Rerank: reranker{}, LLM: llm, Logger: obs.Discard(),
		Recall: recall.DefaultParams(), Plan: plan.DefaultParams(),
		MaxToolSteps: 3, MaxToolOutput: 200, QueryTokens: 128,
	}
}

func TestRun(t *testing.T) {
	t.Parallel()

	t.Run("plain reply", func(t *testing.T) {
		t.Parallel()
		s := &sink{}
		res, err := turn.Run(t.Context(), input(s, &leaser{}, "hello there"), deps(fakellm.New(fakellm.Options{})))
		if err != nil {
			t.Fatal(err)
		}
		if res.Final == "" || res.Final != s.text.String() || res.Replan || res.StepLimit {
			t.Errorf("result = %+v, streamed %q", res, s.text.String())
		}
		if res.Usage.InputTokens == 0 || res.Usage.OutputTokens == 0 {
			t.Errorf("usage not reported: %+v", res.Usage)
		}
	})

	t.Run("tool call then reply", func(t *testing.T) {
		t.Parallel()
		s, l := &sink{}, &leaser{}
		res, err := turn.Run(t.Context(), input(s, l, "!python print(1)"), deps(fakellm.New(fakellm.Options{})))
		if err != nil {
			t.Fatal(err)
		}
		if len(s.calls) != 1 || s.calls[0].Env.Base != pySpec().Base || s.calls[0].Code != "print(1)" {
			t.Fatalf("tool calls = %+v, want one python call with its environment filled in", s.calls)
		}
		if len(s.results) != 1 || s.results[0].ID != s.calls[0].ID || s.results[0].Output != "out:print(1)" {
			t.Errorf("tool results = %+v", s.results)
		}
		if !strings.Contains(res.Final, "out:print(1)") {
			t.Errorf("final = %q", res.Final)
		}
	})

	t.Run("a tool with no provider becomes an error result, not a failed turn", func(t *testing.T) {
		t.Parallel()
		s := &sink{}
		res, err := turn.Run(t.Context(), input(s, &leaser{}, "!shell ls"), deps(fakellm.New(fakellm.Options{})))
		if err != nil {
			t.Fatal(err)
		}
		if len(s.results) != 1 || !s.results[0].IsError || !strings.Contains(s.results[0].Output, "not available") {
			t.Errorf("results = %+v", s.results)
		}
		if res.Final == "" {
			t.Error("the model should still get to reply")
		}
	})

	t.Run("a failing environment becomes an error result", func(t *testing.T) {
		t.Parallel()
		s := &sink{}
		l := &leaser{leaseErr: errors.New("sandbox service down")}
		if _, err := turn.Run(t.Context(), input(s, l, "!python 1"), deps(fakellm.New(fakellm.Options{}))); err != nil {
			t.Fatal(err)
		}
		if len(s.results) != 1 || !s.results[0].IsError || strings.Contains(s.results[0].Output, "sandbox service down") {
			t.Errorf("results = %+v; want a generic error that does not leak internals", s.results)
		}
	})

	t.Run("a model that only ever calls tools hits the step limit", func(t *testing.T) {
		t.Parallel()
		s := &sink{}
		res, err := turn.Run(t.Context(), input(s, &leaser{}, "go"), deps(loopLLM{}))
		if err != nil {
			t.Fatal(err)
		}
		if !res.StepLimit || len(s.calls) != 3 {
			t.Errorf("StepLimit=%t after %d calls; want the limit of 3", res.StepLimit, len(s.calls))
		}
	})

	t.Run("an embedder outage does not stop the reply", func(t *testing.T) {
		t.Parallel()
		s := &sink{}
		d := deps(fakellm.New(fakellm.Options{}))
		d.Embed = embedder{err: errors.New("onnx runtime crashed")}
		in := input(s, &leaser{}, "hello")
		// One prior episode, so recall has something to look for.
		x, err := recall.NewIndex(recall.DefaultParams(), nil)
		if err != nil {
			t.Fatal(err)
		}
		x.OnEmbedded(core.Event{Seq: 1, TS: 1}, fakeinfer.Embed("old topic"))
		in.Episodes = x.Snapshot()
		res, err := turn.Run(t.Context(), in, d)
		if err != nil || res.Final == "" {
			t.Fatalf("res=%+v err=%v; the turn should degrade to no recall", res, err)
		}
	})

	t.Run("cancellation stops the turn", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err := turn.Run(ctx, input(&sink{}, &leaser{}, "hello"), deps(fakellm.New(fakellm.Options{})))
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	})

	t.Run("long tool output is clipped to its budget", func(t *testing.T) {
		t.Parallel()
		s := &sink{}
		big := "!python " + strings.Repeat("x", 5000)
		if _, err := turn.Run(t.Context(), input(s, &leaser{}, big), deps(fakellm.New(fakellm.Options{}))); err != nil {
			t.Fatal(err)
		}
		out := s.results[0].Output
		if len(out) > 400 || !strings.Contains(out, "bytes omitted") {
			t.Errorf("tool output is %d bytes: %.80q...", len(out), out)
		}
	})
}

// loopLLM calls a tool on every step, forever.
type loopLLM struct{}

func (loopLLM) Stream(ctx context.Context, _ *plan.PromptPlan) (turn.Stream, error) {
	call := &core.ToolCall{ID: "c", Name: "python", Code: "pass"}
	return fakellm.NewStream(ctx, fakellm.Response{Tool: call}, 0), nil
}

// TestRecallQueryIsTheMessageBeingAnswered covers the cases where the new
// message is not the last raw event in the log: it arrived while a tool was
// running (so the tool result follows it), or a bookkeeping event was
// appended after it. The query must still be built from that message.
func TestRecallQueryIsTheMessageBeingAnswered(t *testing.T) {
	t.Parallel()
	const question = "back to the postgres migration: did the orders table finish?"
	event := func(seq uint64, kind core.EventKind, body any) core.Event {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		return core.Event{Contact: 1, Seq: seq, TS: int64(seq) * 1000, Kind: kind, Body: raw}
	}
	cases := map[string][]core.Event{
		"message logged during a tool run": {
			userEvent(1, "!python slow_job()"),
			event(2, core.KindToolCall, core.ToolCall{ID: "c1", Name: "python", Code: "slow_job()"}),
			userEvent(3, question),
			event(4, core.KindToolResult, core.ToolResult{ID: "c1", Output: "done"}),
		},
		"summary appended after the message": {
			userEvent(1, question),
			event(2, core.KindSummary, core.SummaryBody{Episode: 0, Start: 1, End: 1, Text: "old"}),
		},
		"environment snapshot appended after the message": {
			userEvent(1, question),
			event(2, core.KindEnvSnapshot, core.EnvSnapshotBody{Spec: pySpec(), Snapshot: "s1"}),
		},
	}
	for name, tail := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var queries []string
			d := deps(fakellm.New(fakellm.Options{}))
			d.Embed = embedder{queries: &queries}
			in := input(&sink{}, &leaser{}, "unused")
			in.Tail = tail
			// One earlier episode, so recall has something to look for.
			x, err := recall.NewIndex(recall.DefaultParams(), nil)
			if err != nil {
				t.Fatal(err)
			}
			x.OnEmbedded(core.Event{Seq: 1, TS: 1}, fakeinfer.Embed("old topic"))
			in.Episodes = x.Snapshot()
			if _, err := turn.Run(t.Context(), in, d); err != nil {
				t.Fatal(err)
			}
			if len(queries) != 1 || !strings.Contains(queries[0], "postgres migration") {
				t.Errorf("recall query = %q, want it built from the message being answered", queries)
			}
		})
	}
}

// TestCollectingSnapshotsNeverWaitsOnTheEnvironment pins what keeps the actor
// goroutine responsive: an idle snapshot may take a long time, and the actor
// collects finished snapshots from its own goroutine.
func TestCollectingSnapshotsNeverWaitsOnTheEnvironment(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		l := &leaser{snapWait: 20 * time.Second}
		e := turn.NewEnv(turn.EnvConfig{Contact: 1, Leasers: map[uint8]turn.EnvLeaser{0: l}, IdleTTL: time.Minute})
		if _, err := e.Exec(t.Context(), pySpec(), "x"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Minute + time.Second) // the idle snapshot is now in progress
		start := time.Now()
		if got := e.TakeSnapshots(); len(got) != 0 {
			t.Errorf("a snapshot was reported before it finished: %v", got)
		}
		if waited := time.Since(start); waited != 0 {
			t.Errorf("TakeSnapshots waited %v behind the snapshot in progress", waited)
		}
		time.Sleep(30 * time.Second)
		if got := e.TakeSnapshots(); len(got) != 1 {
			t.Errorf("after the snapshot finished: %d reported, want 1", len(got))
		}
	})
}

func TestAnUnusedPrewarmIsReleasedWhenIdle(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		l := &leaser{}
		e := turn.NewEnv(turn.EnvConfig{Contact: 1, Leasers: map[uint8]turn.EnvLeaser{1: l}, IdleTTL: 2 * time.Minute})
		e.Prewarm(t.Context(), shellSpec()) // the turn was then interrupted; no tool ever ran
		time.Sleep(3 * time.Minute)
		synctest.Wait()
		if _, live, _ := l.state(); live != 0 {
			t.Errorf("%d prewarmed environment(s) still held after the idle TTL", live)
		}
	})
}
