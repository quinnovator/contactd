// Package conform is the conformance suite every provider profile runs.
//
// A capability is a tested claim: whatever a profile declares, this suite
// exercises against recorded upstream responses. It checks the properties
// the core relies on and cannot see for itself: that freezing a request is
// deterministic, that a turn's later request extends its earlier one, that
// reasoning payloads round-trip only to the route that produced them, that
// usage and errors are normalized, and that the declared capabilities are
// honoured on the wire.
package conform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/llm"
	"github.com/quinnovator/contactd/internal/plan"
)

// Response is one recorded upstream response and what a codec must make of
// it.
type Response struct {
	// Body is the raw response body, normally a Server-Sent Events stream.
	Body []byte
	// ContentType defaults to text/event-stream.
	ContentType string
	// Text is the reply text the codec must return.
	Text string
	// Code is the code of the first tool call, or "" for a plain reply.
	Code string
	// Input, Cached and Output are the usage the codec must report.
	Input, Cached, Output int
	// Reasoning says the body carries reasoning the codec must capture.
	Reasoning bool
}

// Fixture is the set of recorded responses a profile supplies.
type Fixture struct {
	// Reply is a plain text reply.
	Reply Response
	// Tool is a response in which the model reasons (where the route does),
	// then calls the tool named "python".
	Tool Response
}

// NewProvider builds the profile under test against a stand-in upstream.
type NewProvider func(t *testing.T, baseURL string) llm.Provider

// Run checks a profile against the contract. model is a model the profile
// serves, or "" for its default.
func Run(t *testing.T, newProvider NewProvider, model string, fx Fixture) {
	t.Helper()
	up := newUpstream(t)
	p := newProvider(t, up.URL)
	ctx := t.Context()
	route, err := p.Route(ctx, model)
	if err != nil {
		t.Fatalf("Route(%q): %v", model, err)
	}

	t.Run("route", func(t *testing.T) {
		if route.Provider != p.ID() || route.Model == "" || route.Wire == 0 {
			t.Errorf("route = %+v; it must name its provider, a model and a wire", route)
		}
		if route.Caps.ContextWindow <= 0 {
			t.Error("a route must declare its context window")
		}
		if _, err := p.Route(ctx, "no-such-model-xyzzy"); !errors.Is(err, llm.ErrUnknownModel) {
			t.Errorf("unknown model: err = %v, want ErrUnknownModel (never a silent default)", err)
		}
		for _, e := range route.Caps.Efforts {
			if e == llm.EffortDefault || e == llm.EffortNone {
				t.Errorf("Efforts lists %q; switching reasoning off is ReasoningOff, not an effort", e)
			}
		}
	})

	t.Run("freezing is deterministic", func(t *testing.T) {
		pl := turnOne(t)
		a := prepare(ctx, t, p, route, pl, llm.Options{})
		b := prepare(ctx, t, p, route, pl, llm.Options{})
		if !bytes.Equal(a.Body, b.Body) {
			t.Error("the same plan froze to different bytes")
		}
		if a.URL == "" || !strings.HasPrefix(a.URL, up.URL) {
			t.Errorf("URL = %q, want it under the configured base %q", a.URL, up.URL)
		}
		if len(a.Marks) != len(pl.PrefixMarks()) {
			t.Errorf("Frozen.Marks has %d marks, the plan has %d", len(a.Marks), len(pl.PrefixMarks()))
		}
		if !json.Valid(a.Body) {
			t.Error("the frozen body is not valid JSON")
		}
	})

	t.Run("a tool step extends the request it follows", func(t *testing.T) {
		pl := turnOne(t)
		before := messages(t, prepare(ctx, t, p, route, pl, llm.Options{}).Body)
		pl.AppendTail(
			core.ToolCall{ID: "call_conform_1", Name: "python", Code: "print(6 * 7)"},
			core.ToolResult{ID: "call_conform_1", Output: "42"},
		)
		after := messages(t, prepare(ctx, t, p, route, pl, llm.Options{}).Body)
		if len(after) <= len(before) {
			t.Fatalf("%d messages before the tool step, %d after", len(before), len(after))
		}
		for i := range before {
			if !bytes.Equal(before[i], after[i]) {
				t.Fatalf("message %d changed when a tool step was appended; the cached prefix is lost:\n before %s\n after  %s", i, before[i], after[i])
			}
		}
	})

	t.Run("cache prediction", func(t *testing.T) {
		pl := turnOne(t)
		prev := pl.PrefixMarks()
		pl.AppendTail(
			core.ToolCall{ID: "call_conform_1", Name: "python", Code: "x"},
			core.ToolResult{ID: "call_conform_1", Output: "y"},
		)
		f := prepare(ctx, t, p, route, pl, llm.Options{Prev: prev})
		if want := llm.PredictCached(route.Caps.Cache, prev, pl.PrefixMarks()); f.Predict != want {
			t.Errorf("Predict = %d, want %d for cache mode %d", f.Predict, want, route.Caps.Cache)
		}
		if cold := prepare(ctx, t, p, route, pl, llm.Options{}); cold.Predict != 0 {
			t.Errorf("Predict = %d with no previous request, want 0", cold.Predict)
		}
	})

	t.Run("affinity key", func(t *testing.T) {
		const key = "aff1n1tyk3yc0nf0rm"
		f := prepare(ctx, t, p, route, turnOne(t), llm.Options{Affinity: key})
		inHeader := false
		for _, vs := range f.Header {
			for _, v := range vs {
				inHeader = inHeader || strings.Contains(v, key)
			}
		}
		inBody := bytes.Contains(f.Body, []byte(key))
		switch route.Caps.Affinity {
		case llm.AffinityNone:
			if inHeader || inBody {
				t.Error("the route declares no affinity key but sent one")
			}
		case llm.AffinityHeader:
			if !inHeader || inBody {
				t.Errorf("AffinityHeader: key in header=%t, in body=%t", inHeader, inBody)
			}
		case llm.AffinityBody:
			if !inBody || inHeader {
				t.Errorf("AffinityBody: key in header=%t, in body=%t", inHeader, inBody)
			}
		}
	})

	t.Run("effort", func(t *testing.T) {
		base := prepare(ctx, t, p, route, turnOne(t), llm.Options{})
		if base.Effort != llm.EffortDefault {
			t.Errorf("no effort requested, but Frozen.Effort = %q", base.Effort)
		}
		for e := llm.EffortNone; e <= llm.EffortMax; e++ {
			f := prepare(ctx, t, p, route, turnOne(t), llm.Options{Effort: e})
			if want := route.Caps.Nearest(e); f.Effort != want {
				t.Errorf("effort %q froze as %q, want the nearest accepted %q", e, f.Effort, want)
			}
			if f.Effort == llm.EffortDefault && !bytes.Equal(f.Body, base.Body) {
				t.Errorf("effort %q maps to no control on this route, yet it changed the request", e)
			}
		}
	})

	t.Run("plain reply", func(t *testing.T) {
		up.serve(fx.Reply)
		res := send(ctx, t, p, route, turnOne(t))
		if res.Text != fx.Reply.Text || len(res.ToolCalls) != 0 || res.Stop != llm.StopEnd {
			t.Errorf("result = text %q, %d tool calls, stop %s; want %q, none, end", res.Text, len(res.ToolCalls), res.Stop, fx.Reply.Text)
		}
		checkUsage(t, res.Usage, fx.Reply)
	})

	t.Run("tool call and reasoning round trip", func(t *testing.T) {
		up.serve(fx.Tool)
		pl := turnOne(t)
		res := send(ctx, t, p, route, pl)
		if len(res.ToolCalls) == 0 || res.Stop != llm.StopTool {
			t.Fatalf("result = %d tool calls, stop %s; want a tool call", len(res.ToolCalls), res.Stop)
		}
		call := res.ToolCalls[0]
		if call.Name != "python" || call.Code != fx.Tool.Code || call.ID == "" {
			t.Errorf("tool call = %+v, want python running %q with an ID", call, fx.Tool.Code)
		}
		if res.Text != fx.Tool.Text {
			t.Errorf("text before the call = %q, want %q", res.Text, fx.Tool.Text)
		}
		checkUsage(t, res.Usage, fx.Tool)

		wantPayload := fx.Tool.Reasoning && route.Caps.Replay != llm.ReplayNone
		switch {
		case wantPayload && res.Reasoning == nil:
			t.Fatal("the response carried reasoning and the route replays it, but no payload was captured")
		case !wantPayload && res.Reasoning != nil:
			t.Fatalf("a payload was captured on a route with Replay=%d and fixture reasoning=%t", route.Caps.Replay, fx.Tool.Reasoning)
		case !wantPayload:
			return
		}
		if res.Reasoning.Route != route.ID() || len(res.Reasoning.Data) == 0 {
			t.Fatalf("payload tagged %q with %d bytes, want route %q", res.Reasoning.Route, len(res.Reasoning.Data), route.ID())
		}

		// Returned to its own route, the payload changes the request; handed
		// to another route's request, it must not appear at all.
		result := core.ToolResult{ID: call.ID, Output: "42"}
		with, without, foreign := turnOne(t), turnOne(t), turnOne(t)
		own := call
		own.Reasoning = res.Reasoning
		with.AppendTail(own, result)
		bare := call
		bare.Reasoning = nil
		without.AppendTail(bare, result)
		other := call
		other.Reasoning = &core.Reasoning{Route: "some-other/route", Data: res.Reasoning.Data}
		foreign.AppendTail(other, result)

		bodyWith := prepare(ctx, t, p, route, with, llm.Options{}).Body
		bodyWithout := prepare(ctx, t, p, route, without, llm.Options{}).Body
		bodyForeign := prepare(ctx, t, p, route, foreign, llm.Options{}).Body
		if bytes.Equal(bodyWith, bodyWithout) {
			t.Error("a payload tagged for this route was not sent back")
		}
		if !bytes.Equal(bodyForeign, bodyWithout) {
			t.Error("a payload tagged for another route was sent to this one")
		}
		// And it survives the trip unchanged: freezing twice is stable.
		if !bytes.Equal(bodyWith, prepare(ctx, t, p, route, with, llm.Options{}).Body) {
			t.Error("replaying a payload is not deterministic")
		}
	})

	t.Run("errors are classified", func(t *testing.T) {
		cases := []struct {
			status int
			header string
			want   llm.ErrorClass
		}{
			{http.StatusTooManyRequests, "2", llm.ErrRateLimited},
			{http.StatusUnauthorized, "", llm.ErrAuth},
			{http.StatusInternalServerError, "", llm.ErrOverloaded},
			{http.StatusBadRequest, "", llm.ErrBadRequest},
		}
		for _, tc := range cases {
			up.fail(tc.status, tc.header)
			f := prepare(ctx, t, p, route, turnOne(t), llm.Options{})
			_, err := p.Send(ctx, f)
			e, ok := errors.AsType[*llm.Error](err)
			if !ok {
				t.Errorf("HTTP %d: err = %v, want an *llm.Error", tc.status, err)
				continue
			}
			if e.Class != tc.want || e.Status != tc.status || e.Provider != p.ID() {
				t.Errorf("HTTP %d: classified %s (status %d, provider %q), want %s", tc.status, e.Class, e.Status, e.Provider, tc.want)
			}
			if tc.header != "" && e.RetryAfter != 2*time.Second {
				t.Errorf("HTTP %d: RetryAfter = %v, want the provider's 2s hint", tc.status, e.RetryAfter)
			}
		}
	})

	t.Run("cancellation ends a stream promptly", func(t *testing.T) {
		up.stall(fx.Reply)
		cctx, cancel := context.WithCancel(ctx)
		f := prepare(ctx, t, p, route, turnOne(t), llm.Options{})
		stream, err := p.Send(cctx, f)
		if err != nil {
			t.Fatalf("Send: %v", err)
		}
		done := make(chan error, 1)
		go func() {
			for range stream.Events() { //nolint:revive // draining
			}
			_, err := stream.Result()
			done <- err
		}()
		time.Sleep(50 * time.Millisecond)
		cancel()
		select {
		case err := <-done:
			if err == nil {
				t.Error("a cancelled stream reported success")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the stream did not end within 5s of cancellation")
		}
	})
}

func checkUsage(t *testing.T, got llm.Usage, want Response) {
	t.Helper()
	if got.Input != want.Input || got.Cached != want.Cached || got.Output != want.Output {
		t.Errorf("usage = input %d, cached %d, output %d; want %d, %d, %d",
			got.Input, got.Cached, got.Output, want.Input, want.Cached, want.Output)
	}
	if len(got.Raw) == 0 {
		t.Error("the provider's raw usage object was not kept")
	}
}

func prepare(ctx context.Context, t *testing.T, p llm.Provider, r llm.Route, pl *plan.PromptPlan, o llm.Options) *llm.Frozen {
	t.Helper()
	f, err := p.Prepare(ctx, r, pl, o)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	return f
}

func send(ctx context.Context, t *testing.T, p llm.Provider, r llm.Route, pl *plan.PromptPlan) llm.Result {
	t.Helper()
	stream, err := p.Send(ctx, prepare(ctx, t, p, r, pl, llm.Options{}))
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	for range stream.Events() { //nolint:revive // draining
	}
	res, err := stream.Result()
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	return res
}

// messages returns the elements of a request's "messages" array, which both
// the Chat Completions and Messages formats use for the conversation.
func messages(t *testing.T, body []byte) []json.RawMessage {
	t.Helper()
	var req struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if len(req.Messages) == 0 {
		t.Fatal("the request has no messages array")
	}
	return req.Messages
}

// turnOne builds a fresh plan with every tier populated: a system prompt and
// one tool, a profile, a recalled episode, a finished exchange in the tail
// and a new user message.
func turnOne(t *testing.T) *plan.PromptPlan {
	t.Helper()
	event := func(seq uint64, kind core.EventKind, body any) core.Event {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		return core.Event{Contact: 77, Seq: seq, TS: 1_760_000_000_000 + int64(seq)*1000, Kind: kind, Body: raw} //nolint:gosec // seq is 1, 2 or 3
	}
	in := plan.Input{
		Agent: &core.AgentConfig{
			System: "You are Ada, a careful colleague.",
			Tools:  []core.ToolDef{{Name: "python", Description: "Run Python 3.12 and return what it prints.", Env: core.EnvSpec{Base: "python-3.12"}}},
		},
		Profile: "Prefers short answers.",
		Tail: []core.Event{
			event(1, core.KindUserMsg, core.MsgBody{Text: "hello"}),
			event(2, core.KindAgentMsg, core.MsgBody{Text: "Hi. What do you need?"}),
			event(3, core.KindUserMsg, core.MsgBody{Text: "What is 6 times 7? Use the tool."}),
		},
	}
	pl, err := plan.Build(nil, in, []plan.Recalled{{ID: 1, Text: "[2025-10-01] contact: asked about vacuum settings"}}, 1_760_000_100_000, plan.DefaultParams())
	if err != nil {
		t.Fatal(err)
	}
	return pl
}

// upstream is a stand-in provider endpoint that replays one programmed
// behaviour at a time.
type upstream struct {
	*httptest.Server
	mode atomic.Pointer[behaviour]
}

type behaviour struct {
	resp   Response
	status int
	retry  string
	stall  bool
}

func newUpstream(t *testing.T) *upstream {
	t.Helper()
	u := &upstream{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := u.mode.Load()
		if b == nil {
			http.Error(w, "no behaviour programmed", http.StatusTeapot)
			return
		}
		if b.status != 0 {
			if b.retry != "" {
				w.Header().Set("Retry-After", b.retry)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(b.status)
			_, _ = w.Write([]byte(`{"error":{"type":"conformance","message":"programmed failure"}}`))
			return
		}
		ct := b.resp.ContentType
		if ct == "" {
			ct = "text/event-stream"
		}
		w.Header().Set("Content-Type", ct)
		w.WriteHeader(http.StatusOK)
		if !b.stall {
			_, _ = w.Write(b.resp.Body)
			return
		}
		// Send the first half, then hold the connection open.
		_, _ = w.Write(b.resp.Body[:len(b.resp.Body)/2])
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
		<-r.Context().Done()
	}))
	t.Cleanup(u.Close)
	return u
}

func (u *upstream) serve(r Response) { u.mode.Store(&behaviour{resp: r}) }
func (u *upstream) stall(r Response) { u.mode.Store(&behaviour{resp: r, stall: true}) }
func (u *upstream) fail(status int, retry string) {
	u.mode.Store(&behaviour{status: status, retry: retry})
}
