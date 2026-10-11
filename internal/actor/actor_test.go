package actor_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/quinnovator/contactd/internal/actor"
	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/turn"
)

const ms = time.Millisecond

// Timers run on the real time API inside a synctest bubble, so these tests
// assert exact instants without a Clock interface in production code.

func TestBurstIsCoalescedIntoOneTurn(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.say("hey")
		time.Sleep(300 * ms)
		h.say("are you around?")
		time.Sleep(300 * ms)
		h.say("I have a question about the migration")
		time.Sleep(5 * time.Second)

		calls := h.llm.seen()
		if len(calls) != 1 {
			t.Fatalf("%d model calls for one burst, want 1", len(calls))
		}
		// 700 ms of quiet after the last message at 600 ms.
		if calls[0].at != 1300*ms {
			t.Errorf("turn started at %v, want 1.3s", calls[0].at)
		}
		if calls[0].shape != "UUUN" {
			t.Errorf("prompt shape %q, want all three messages in the live tier", calls[0].shape)
		}
		if got := h.kinds(); got != "UUUA" {
			t.Errorf("log = %s, want UUUA", got)
		}
	})
}

func TestDebounceHasAHardCap(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		// Someone who never pauses for 700 ms must still get a reply.
		for range 12 {
			h.say("and another thing")
			time.Sleep(500 * ms)
		}
		time.Sleep(5 * time.Second)
		calls := h.llm.seen()
		if len(calls) == 0 || calls[0].at != 3*time.Second {
			t.Fatalf("first turn: %+v, want it exactly 3s after the first message", calls)
		}
	})
}

func TestTypingHoldsTheReplyBack(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.say("so here is the thing")
		for range 2 {
			time.Sleep(500 * ms)
			if err := h.reg.Typing(t.Context(), h.id); err != nil {
				t.Fatal(err)
			}
		}
		time.Sleep(5 * time.Second)
		if calls := h.llm.seen(); len(calls) != 1 || calls[0].at != 1700*ms {
			t.Fatalf("calls = %+v, want one turn at 1.7s (700 ms after the last typing indicator)", calls)
		}
	})
}

func TestTypingNeverWakesAContact(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		if err := h.reg.Typing(t.Context(), h.id); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if n := h.reg.Hydrated(); n != 0 {
			t.Errorf("a typing indicator hydrated %d contact(s)", n)
		}
	})
}

func TestInterruptBeforeFirstTokenIsSilent(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, func(o *options) { o.llm.FirstToken = 2 * time.Second })
		h.say("what's the status?") // turn starts at 0.7s, first token due at 2.7s
		time.Sleep(1500 * ms)
		h.say("of the deploy, I mean") // arrives while waiting for the first token
		time.Sleep(10 * time.Second)

		calls := h.llm.seen()
		if len(calls) != 2 {
			t.Fatalf("%d model calls, want 2 (one cancelled, one answered)", len(calls))
		}
		if calls[1].at != 2200*ms || calls[1].shape != "UUN" {
			t.Errorf("second call at %v with shape %q; want 2.2s answering both messages", calls[1].at, calls[1].shape)
		}
		// Nothing was said yet, so nothing is recorded for the cancelled turn.
		if got := h.kinds(); got != "UUA" {
			t.Errorf("log = %s, want UUA", got)
		}
	})
}

func TestInterruptWhileStreamingPersistsThePartialReply(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, func(o *options) {
			o.llm.PerChunk = 100 * ms
			o.llm.ReplyWords = 40 // about 4s of streaming
		})
		sub, err := h.reg.Subscribe(t.Context(), h.id)
		if err != nil {
			t.Fatal(err)
		}
		h.say("tell me everything about the outage")
		time.Sleep(700*ms + 1050*ms) // ten chunks in
		h.say("wait, just the summary")
		time.Sleep(10 * time.Second)

		if got := h.kinds(); got != "UaUA" {
			t.Fatalf("log = %s, want UaUA (the cut-off reply is kept, flagged)", got)
		}
		partial := string(h.log.all(h.id)[1].Body)
		if !strings.Contains(partial, "Got it: tell me everything") || strings.Count(partial, " ok") >= 30 {
			t.Errorf("partial reply body = %s", partial)
		}
		// The model is told it was cut off: the next prompt shows U, the
		// partial A, then the new U.
		calls := h.llm.seen()
		if len(calls) != 2 || calls[1].shape != "UAUN" {
			t.Errorf("calls = %+v, want the second prompt shaped UAUN", calls)
		}
		ds, _ := drain(sub)
		var sawInterrupted bool
		for _, d := range ds {
			if d.Type == core.DeltaInterrupted && d.Seq == 2 {
				sawInterrupted = true
			}
		}
		if !sawInterrupted {
			t.Errorf("subscribers were not told the reply was interrupted: %s", types(ds))
		}
	})
}

func TestMessageDuringToolRunReplansAfterIt(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, func(o *options) { o.execDelay = 5 * time.Second })
		h.say("!python slow_job()") // tool starts at 0.7s and runs until 5.7s
		time.Sleep(2 * time.Second)
		h.say("also, how are you?")
		time.Sleep(20 * time.Second)

		// The tool was never killed: it may have side effects.
		if _, _, _, killed, _ := h.env.stats(); killed != 0 {
			t.Fatalf("the running tool was killed %d time(s)", killed)
		}
		// Call, then the message that arrived mid-run, then the result.
		if got := h.kinds(); got != "UCURA" {
			t.Fatalf("log = %s, want UCURA", got)
		}
		calls := h.llm.seen()
		if len(calls) != 2 {
			t.Fatalf("%d model calls, want 2: the tool call and the re-plan (no continuation of the stale turn)", len(calls))
		}
		// Re-planned the moment the tool returned, and rendered in the order
		// the model experienced it: call, result, then the new message.
		if calls[1].at != 5700*ms || calls[1].shape != "UCRUN" {
			t.Errorf("re-plan at %v with shape %q; want 5.7s and UCRUN", calls[1].at, calls[1].shape)
		}
		last := h.log.all(h.id)[4]
		if !strings.Contains(string(last.Body), "also, how are you?") {
			t.Errorf("the reply answers the wrong thing: %s", last.Body)
		}
	})
}

func TestToolTurnRunsToCompletion(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, func(o *options) { o.execDelay = 200 * ms })
		sub, err := h.reg.Subscribe(t.Context(), h.id)
		if err != nil {
			t.Fatal(err)
		}
		h.say("!python print(6*7)")
		time.Sleep(5 * time.Second)

		if got := h.kinds(); got != "UCRA" {
			t.Fatalf("log = %s, want UCRA", got)
		}
		events := h.log.all(h.id)
		if !strings.Contains(string(events[2].Body), "ran print(6*7)") {
			t.Errorf("tool result body = %s", events[2].Body)
		}
		if !strings.Contains(string(events[3].Body), "The result is: ran print(6*7)") {
			t.Errorf("final reply = %s", events[3].Body)
		}
		// In-turn appends keep every earlier byte: the second model call of
		// the turn sees what the first saw (U and the time note), then C, R.
		if calls := h.llm.seen(); len(calls) != 2 || calls[1].shape != "UNCR" {
			t.Errorf("calls = %+v", calls)
		}
		ds, _ := drain(sub)
		want := "typing=true tool_start tool_call tool_result text text text text text message typing=false"
		if got := types(ds); got != want {
			t.Errorf("stream = %s\n   want %s", got, want)
		}
	})
}

func TestIdleContactIsHibernatedAndWakesIntact(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, func(o *options) { o.execDelay = 10 * ms })
		sub, err := h.reg.Subscribe(t.Context(), h.id)
		if err != nil {
			t.Fatal(err)
		}
		h.say("!python x = 1")
		time.Sleep(5 * time.Second)
		if h.reg.Hydrated() != 1 {
			t.Fatalf("hydrated = %d, want 1 while active", h.reg.Hydrated())
		}

		time.Sleep(actor.DefaultConfig().IdleTTL + time.Second)
		if h.reg.Hydrated() != 0 {
			t.Fatalf("hydrated = %d after the idle TTL, want 0", h.reg.Hydrated())
		}
		// Hibernation parked the environment as a snapshot in the log.
		leases, snaps, released, _, _ := h.env.stats()
		if leases != 1 || snaps != 1 || released != 1 {
			t.Errorf("env: %d leases, %d snapshots, %d releases; want 1 of each", leases, snaps, released)
		}
		if got := h.kinds(); got != "UCRAE" {
			t.Fatalf("log = %s, want UCRAE", got)
		}
		drain(sub)

		// Waking up: sequence numbers continue (memLog checks), the stream
		// that was open before hibernation still works, and the environment
		// resumes from its snapshot.
		h.say("!python print(x)")
		time.Sleep(5 * time.Second)
		if got := h.kinds(); got != "UCRAEUCRA" {
			t.Fatalf("log after waking = %s, want UCRAEUCRA", got)
		}
		if _, _, _, _, resumes := h.env.stats(); len(resumes) != 2 || resumes[1] != "snap-1" {
			t.Errorf("leases resumed from %q, want the second to resume snap-1", resumes)
		}
		if ds, closed := drain(sub); closed || len(ds) == 0 {
			t.Errorf("the subscription did not survive hibernation: closed=%t, %d deltas", closed, len(ds))
		}
	})
}

func TestIdleEnvironmentIsReleasedWhileActorStaysHydrated(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.say("!python x = 1")
		time.Sleep(actor.DefaultConfig().EnvIdleTTL + 10*time.Second)

		if h.reg.Hydrated() != 1 {
			t.Fatalf("the actor should still be hydrated")
		}
		if _, snaps, released, _, _ := h.env.stats(); snaps != 1 || released != 1 {
			t.Errorf("after 2 idle minutes: %d snapshots, %d releases; want 1 and 1", snaps, released)
		}
		if got := h.kinds(); got != "UCRAE" {
			t.Errorf("log = %s, want the snapshot recorded as an event (UCRAE)", got)
		}
	})
}

func TestMemoryPressureHibernatesTheLeastRecentlyActive(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		const limit = 8
		h := newHarness(t, func(o *options) { o.cfg.MaxHydrated = limit })
		ids := make([]core.ContactID, 40)
		for i := range ids {
			ids[i] = h.log.addContact(pythonAgent(), "")
			if _, err := h.reg.Message(t.Context(), ids[i], "hello"); err != nil {
				t.Fatal(err)
			}
			time.Sleep(2 * time.Second) // let each finish its turn and go idle
		}
		time.Sleep(time.Second)
		// Eviction samples, so it is approximate; it must still hold the
		// count near the limit rather than letting all 40 pile up.
		if n := h.reg.Hydrated(); n > 2*limit {
			t.Errorf("%d contacts hydrated with a limit of %d", n, limit)
		}
		// Nothing was lost on the way: every contact got its reply.
		for _, id := range ids {
			if got := kindsOf(h.log.all(id)); got != "UA" {
				t.Errorf("contact %d: log = %s, want UA", id, got)
			}
		}
	})
}

func TestStreamLifecycle(t *testing.T) {
	t.Parallel()

	t.Run("subscribing does not hydrate, and deltas arrive in order", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			h := newHarness(t)
			sub, err := h.reg.Subscribe(t.Context(), h.id)
			if err != nil {
				t.Fatal(err)
			}
			synctest.Wait()
			if h.reg.Hydrated() != 0 {
				t.Fatal("opening a stream woke the contact")
			}
			seq := h.say("hello there")
			time.Sleep(5 * time.Second)
			ds, _ := drain(sub)
			if len(ds) < 3 || ds[0].Type != core.DeltaTyping || !ds[0].Typing {
				t.Fatalf("stream = %s", types(ds))
			}
			var text strings.Builder
			var final core.Delta
			for _, d := range ds {
				if d.Type == core.DeltaText {
					text.WriteString(d.Text)
				}
				if d.Type == core.DeltaMessage {
					final = d
				}
			}
			if final.Text == "" || final.Text != text.String() || final.Seq != seq+1 {
				t.Errorf("streamed %q, committed %q at seq %d", text.String(), final.Text, final.Seq)
			}
			if last := ds[len(ds)-1]; last.Type != core.DeltaTyping || last.Typing {
				t.Errorf("stream should end with typing=false, got %s", types(ds))
			}
		})
	})

	t.Run("a subscriber that falls behind is dropped, not waited for", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			h := newHarness(t, func(o *options) {
				o.cfg.SubBuffer = 4
				o.llm.ReplyWords = 50
			})
			slow, _ := h.reg.Subscribe(t.Context(), h.id)
			h.say("go")
			time.Sleep(5 * time.Second)
			if _, closed := drain(slow); !closed {
				t.Error("the lagging subscriber's stream was not closed")
			}
			if got := h.kinds(); got != "UA" {
				t.Errorf("log = %s; a slow reader must not affect the turn", got)
			}
			h.reg.Unsubscribe(h.id, slow) // already gone: must be harmless
		})
	})

	t.Run("unsubscribing closes the stream, live or parked", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			h := newHarness(t)
			parked, _ := h.reg.Subscribe(t.Context(), h.id)
			h.reg.Unsubscribe(h.id, parked)
			if _, closed := drain(parked); !closed {
				t.Error("parked subscription not closed")
			}
			h.say("hi")
			live, _ := h.reg.Subscribe(t.Context(), h.id)
			synctest.Wait()
			h.reg.Unsubscribe(h.id, live)
			synctest.Wait()
			if _, closed := drain(live); !closed {
				t.Error("live subscription not closed")
			}
		})
	})
}

func TestUnknownContact(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		_, err := h.reg.Message(t.Context(), 9999, "anyone there?")
		if !errors.Is(err, core.ErrContactNotFound) {
			t.Fatalf("err = %v, want ErrContactNotFound", err)
		}
		synctest.Wait()
		if h.reg.Hydrated() != 0 {
			t.Error("a failed hydration left an actor behind")
		}
	})
}

func TestWriteFailureDropsStateAndRecovers(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.say("first")
		time.Sleep(5 * time.Second)

		h.log.mu.Lock()
		h.log.fail = func(w core.Write) error {
			if w.Event != nil && w.Event.Kind == core.KindUserMsg {
				return errDisk
			}
			return nil
		}
		h.log.mu.Unlock()
		if _, err := h.reg.Message(t.Context(), h.id, "lost"); !errors.Is(err, errDisk) {
			t.Fatalf("err = %v, want the disk error surfaced to the sender", err)
		}
		synctest.Wait()
		if h.reg.Hydrated() != 0 {
			t.Fatal("the actor kept state that never reached disk")
		}

		h.log.mu.Lock()
		h.log.fail = nil
		h.log.mu.Unlock()
		// The next message rehydrates from what actually committed; memLog
		// would flag a sequence gap if the lost event's number were skipped.
		h.say("second")
		time.Sleep(5 * time.Second)
		if got := h.kinds(); got != "UAUA" {
			t.Errorf("log = %s, want UAUA", got)
		}
	})
}

func TestGracefulShutdown(t *testing.T) {
	t.Parallel()

	t.Run("a turn that finishes within the grace period commits normally", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			h := newHarness(t, func(o *options) { o.llm.PerChunk = 100 * ms })
			h.say("hello")
			time.Sleep(800 * ms) // streaming has begun
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			h.reg.Shutdown(ctx)
			if got := h.kinds(); got != "UA" {
				t.Errorf("log = %s, want UA", got)
			}
			if _, err := h.reg.Message(t.Context(), h.id, "too late"); !errors.Is(err, core.ErrClosed) {
				t.Errorf("message after shutdown: err = %v, want ErrClosed", err)
			}
		})
	})

	t.Run("a turn that outlives the grace period is cut and kept as interrupted", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			h := newHarness(t, func(o *options) {
				o.llm.PerChunk = time.Second
				o.llm.ReplyWords = 60
			})
			sub, _ := h.reg.Subscribe(t.Context(), h.id)
			h.say("hello")
			time.Sleep(3 * time.Second)
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			h.reg.Shutdown(ctx)
			if got := h.kinds(); got != "Ua" {
				t.Errorf("log = %s, want Ua", got)
			}
			if h.reg.Hydrated() != 0 {
				t.Errorf("%d actors survived shutdown", h.reg.Hydrated())
			}
			if _, closed := drain(sub); !closed {
				t.Error("streams were not closed at shutdown")
			}
		})
	})

	t.Run("a running tool is stopped at the deadline", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			h := newHarness(t, func(o *options) { o.execDelay = time.Hour })
			h.say("!python forever()")
			time.Sleep(2 * time.Second)
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			h.reg.Shutdown(ctx)
			if _, _, _, killed, _ := h.env.stats(); killed != 1 {
				t.Errorf("killed = %d, want the tool stopped", killed)
			}
			// The call is durable even though its result never arrived; the
			// planner gives it a placeholder result on the next turn.
			if got := h.kinds(); got != "UC" {
				t.Errorf("log = %s, want UC", got)
			}
		})
	})
}

func TestMessagesAreIndexedInTheBackground(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.say("the postgres migration is stuck on the orders table")
		time.Sleep(5 * time.Second)
		// Both the message and the reply were embedded and segmented.
		if got := h.log.vectorCount(h.id); got != 2 {
			t.Errorf("%d vectors, want 2", got)
		}
		if got := h.log.episodeCount(h.id); got != 1 {
			t.Errorf("%d episodes, want 1", got)
		}
	})
}

func TestHydrationEmbedsWhatWasMissed(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		// Events that reached the log but were never embedded, as after a
		// crash between append and indexing.
		for i, text := range []string{"lisbon flights in may", "lisbon hotel in alfama"} {
			body := fmt.Sprintf(`{"text":%q}`, text)
			h.log.Submit(core.Write{Contact: h.id, Event: &core.Event{
				Contact: h.id, Seq: uint64(i + 1), TS: time.Now().UnixMilli(), Kind: core.KindUserMsg, Body: []byte(body),
			}})
		}
		h.say("and a rental car")
		time.Sleep(5 * time.Second)
		if got := h.log.vectorCount(h.id); got != 4 {
			t.Errorf("%d vectors, want 4 (two caught up, the new message, the reply)", got)
		}
	})
}

// TestRecallAcrossEpisodes drives the whole memory path: three separate
// conversations, then a callback that should bring exactly the right one
// back, a summary generated lazily because it was recalled, and a recall set
// held steady by hysteresis while the cache is warm.
func TestRecallAcrossEpisodes(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, func(o *options) {
			o.summarize = true
			o.cfg.Recall.Tau = 0.2
			// A small tail so old conversations leave the prompt and have to
			// be recalled rather than simply read.
			o.turn = func(d *turn.Deps) { d.Plan.TailBudget = 120 }
		})
		topics := [][]string{
			{"the postgres migration is stuck on the orders table", "postgres migration lock timeout on orders again"},
			{"booking flights to lisbon for the conference", "which lisbon hotel is near the venue in alfama"},
			{"my sourdough starter is not rising", "sourdough flour hydration ratio is too low"},
		}
		for _, topic := range topics {
			for _, text := range topic {
				h.say(text)
				time.Sleep(10 * time.Second)
			}
			time.Sleep(24 * time.Hour) // a day passes; the contact hibernates
		}
		if got := h.log.episodeCount(h.id); got < 3 {
			t.Fatalf("%d episodes after three conversations, want at least 3", got)
		}
		before := len(h.llm.seen())

		h.say("back to the postgres migration thing: did the orders table finish?")
		time.Sleep(10 * time.Second)
		calls := h.llm.seen()[before:]
		if len(calls) != 1 || len(calls[0].recall) != 1 || calls[0].recall[0] != 0 {
			t.Fatalf("callback recalled %+v, want exactly episode 0 (postgres)", calls)
		}
		// Recalling it triggered a lazy summary, appended as an event.
		if got := h.kinds(); !strings.Contains(got, "S") {
			t.Errorf("log = %s, want a summary event after the recall", got)
		}

		// A follow-up while the cache is warm keeps the same recall set even
		// though the wording changed.
		h.say("ok and the postgres lock timeout?")
		time.Sleep(10 * time.Second)
		calls = h.llm.seen()[before:]
		if len(calls) != 2 || len(calls[1].recall) != 1 || calls[1].recall[0] != 0 {
			t.Fatalf("follow-up recalled %+v, want episode 0 held", calls)
		}

		// An unrelated message recalls nothing: a person doesn't bring up
		// unrelated history.
		time.Sleep(24 * time.Hour)
		h.say("what should I cook tonight?")
		time.Sleep(10 * time.Second)
		last := h.llm.seen()
		if got := last[len(last)-1].recall; len(got) != 0 {
			t.Errorf("an unrelated message recalled %v", got)
		}
	})
}

// TestNoMessageIsLostWhenSendsRaceRetirement uses real time and an idle TTL
// so short that actors retire constantly while messages keep arriving. Every
// acknowledged message must be in the log exactly once, in order; memLog
// fails the test on any sequence gap or duplicate, which is what two writers
// for one contact would produce.
func TestNoMessageIsLostWhenSendsRaceRetirement(t *testing.T) {
	t.Parallel()
	h := newHarness(t, func(o *options) {
		o.cfg.IdleTTL = 200 * time.Microsecond
		o.cfg.Debounce = 100 * time.Microsecond
		o.cfg.DebounceCap = time.Millisecond
		o.cfg.InboxSize = 4
	})
	const contacts, perContact = 24, 60
	ids := make([]core.ContactID, contacts)
	for i := range ids {
		ids[i] = h.log.addContact(core.AgentConfig{System: "s"}, "")
	}
	var acked atomic.Int64
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Go(func() {
			for i := range perContact {
				if _, err := h.reg.Message(t.Context(), id, fmt.Sprintf("message %d", i)); err != nil {
					t.Errorf("contact %d message %d: %v", id, i, err)
					return
				}
				acked.Add(1)
				if i%7 == 0 {
					time.Sleep(300 * time.Microsecond) // let it retire sometimes
				}
			}
		})
	}
	wg.Wait()
	if acked.Load() != contacts*perContact {
		t.Fatalf("%d of %d messages acknowledged", acked.Load(), contacts*perContact)
	}
	for _, id := range ids {
		users := 0
		for _, ev := range h.log.all(id) {
			if ev.Kind == core.KindUserMsg {
				if want := fmt.Sprintf(`{"text":"message %d"}`, users); string(ev.Body) != want {
					t.Fatalf("contact %d: user message %d is %s, want %s", id, users, ev.Body, want)
				}
				users++
			}
		}
		if users != perContact {
			t.Errorf("contact %d: %d user messages in the log, want %d", id, users, perContact)
		}
	}
}
