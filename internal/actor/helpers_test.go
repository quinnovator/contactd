package actor_test

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/quinnovator/contactd/internal/actor"
	"github.com/quinnovator/contactd/internal/core"
	fakellm "github.com/quinnovator/contactd/internal/llm/fake"
	"github.com/quinnovator/contactd/internal/turn"
)

type helperHook struct {
	out chan actor.HelperOutcome
	log *memLog
}

func (h helperHook) HelperTurnEnded(_ context.Context, id core.ContactID, o actor.HelperOutcome) {
	if o.Kind == actor.OutcomeReplied {
		found := false
		for _, ev := range h.log.all(id) {
			if ev.Seq == o.Seq && ev.Kind == core.KindAgentMsg {
				found = true
			}
		}
		if !found {
			o.Text = "reply not durable"
		}
	}
	h.out <- o
}

func helperContact(o *options) {
	o.agent = func(a *core.AgentConfig) { a.Helper = true }
	o.llm.FirstToken = time.Second
}

func TestHelperChecksAndDurableOutcome(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, helperContact)
		hook := helperHook{out: make(chan actor.HelperOutcome, 8), log: h.log}
		h.reg.SetHelperHook(hook)
		if _, err := h.reg.Check(t.Context(), h.id, "Check 1"); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if h.reg.Turns() != 1 {
			t.Fatal("check was debounced")
		}
		if _, err := h.reg.Check(t.Context(), h.id, "Check 2"); !errors.Is(err, core.ErrBusy) {
			t.Fatalf("busy check: %v", err)
		}
		if err := h.reg.Interrupt(t.Context(), h.id); err != nil {
			t.Fatal(err)
		}
		time.Sleep(3 * time.Second)
		select {
		case o := <-hook.out:
			if o.Kind != actor.OutcomeReplied || o.Text != "" {
				t.Fatalf("outcome: %+v", o)
			}
		default:
			t.Fatal("missing durable reply outcome")
		}
		if len(sessionNotices(t, h)) != 0 {
			t.Fatal("helper got a visit")
		}
		if _, err := h.reg.MessageFrom(t.Context(), h.id, "wrapped", "original", "message-7"); err != nil {
			t.Fatal(err)
		}
		for _, ev := range h.log.all(h.id) {
			if ev.Kind == core.KindUserMsg {
				var b core.MsgBody
				if err := json.Unmarshal(ev.Body, &b); err != nil {
					t.Fatal(err)
				}
				if b.MessageID != "message-7" || b.Raw != "original" {
					t.Fatalf("message identity: %+v", b)
				}
			}
		}
	})
}

func TestHelperNoticeWaitsBehindReply(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, func(o *options) { o.llm.FirstToken = time.Second; o.llm.PerChunk = time.Second })
		h.say("hello")
		time.Sleep(time.Second)
		ack := make(chan error, 1)
		go func() {
			_, err := h.reg.HelperNotice(t.Context(), h.id, core.NoticeBody{Source: "helper-update", Helper: 7, Text: "new report"})
			ack <- err
		}()
		synctest.Wait()
		select {
		case err := <-ack:
			t.Fatalf("notice acknowledged before reply: %v", err)
		default:
		}
		time.Sleep(time.Minute)
		if err := <-ack; err != nil {
			t.Fatal(err)
		}
		evs := h.log.all(h.id)
		reply := false
		found := false
		for _, ev := range evs {
			if ev.Kind == core.KindAgentMsg {
				reply = true
			}
			if ev.Kind == core.KindNotice {
				var b core.NoticeBody
				if err := json.Unmarshal(ev.Body, &b); err != nil {
					t.Fatal(err)
				}
				if b.Helper == 7 {
					found = true
					if !reply {
						t.Fatal("notice precedes reply")
					}
				}
			}
		}
		if !found {
			t.Fatal("notice was not logged")
		}
	})
}

func TestHelperObserveDoesNotHydrate(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		sub, err := h.reg.Subscribe(t.Context(), h.id)
		if err != nil {
			t.Fatal(err)
		}
		defer h.reg.Unsubscribe(h.id, sub)
		<-sub.C
		for range 300 {
			if err := h.reg.Observe(t.Context(), h.id, core.HelperDelta{Handle: 1, Event: "push"}); err != nil {
				t.Fatal(err)
			}
		}
		if h.reg.Hydrated() != 0 {
			t.Fatal("observation hydrated contact")
		}
		if got := <-sub.C; got.Type != core.DeltaHelper || got.Helper.Handle != 1 {
			t.Fatalf("delta: %+v", got)
		}
	})
}

func TestHelperTerminalOutcomesAndLiveArgs(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		tool, args string
		want       actor.OutcomeKind
	}{{turn.StayQuietTool, `{"reason":"same"}`, actor.OutcomeQuiet}, {turn.AskRequesterTool, `{"question":"which?"}`, actor.OutcomeAsked}, {turn.FinishTool, `{"summary":"done"}`, actor.OutcomeFinished}} {
		t.Run(tc.tool, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				h := newHarness(t, helperContact, func(o *options) {
					o.llm.Rules = []fakellm.Rule{{Match: regexp.MustCompile("check"), Tool: tc.tool, Code: tc.args}}
				})
				hook := helperHook{out: make(chan actor.HelperOutcome, 8), log: h.log}
				h.reg.SetHelperHook(hook)
				sub, err := h.reg.Subscribe(t.Context(), h.id)
				if err != nil {
					t.Fatal(err)
				}
				defer h.reg.Unsubscribe(h.id, sub)
				if _, err := h.reg.Check(t.Context(), h.id, "check"); err != nil {
					t.Fatal(err)
				}
				time.Sleep(5 * time.Second)
				select {
				case o := <-hook.out:
					if o.Kind != tc.want {
						t.Fatalf("outcome: %+v", o)
					}
				default:
					t.Fatal("missing outcome")
				}
				if strings.Contains(h.kinds(), "A") {
					t.Fatalf("terminal tool wrote reply: %s", h.kinds())
				}
				found := false
				for len(sub.C) > 0 {
					d := <-sub.C
					if d.Type == core.DeltaToolCall {
						found = true
						if d.Text != tc.args {
							t.Fatalf("live args: %s", d.Text)
						}
					}
				}
				if !found {
					t.Fatal("no live tool call")
				}
			})
		})
	}
}

func TestHelperFailureOutcomes(t *testing.T) {
	t.Parallel()
	for _, stepLimit := range []bool{false, true} {
		name := "provider error"
		if stepLimit {
			name = "step limit"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				h := newHarness(t, helperContact, func(o *options) {
					if stepLimit {
						o.turn = func(d *turn.Deps) { d.MaxToolSteps = 1 }
						o.llm.Rules = []fakellm.Rule{{Match: regexp.MustCompile("check"), Tool: "python", Code: "1"}}
					} else {
						o.llm.Fail = func(int, []byte) error { return errors.New("unavailable") }
					}
				})
				hook := helperHook{out: make(chan actor.HelperOutcome, 8), log: h.log}
				h.reg.SetHelperHook(hook)
				if _, err := h.reg.Check(t.Context(), h.id, "check"); err != nil {
					t.Fatal(err)
				}
				time.Sleep(time.Minute)
				select {
				case o := <-hook.out:
					if o.Kind != actor.OutcomeFailed {
						t.Fatalf("outcome: %+v", o)
					}
				default:
					t.Fatal("missing failure")
				}
			})
		})
	}
}
