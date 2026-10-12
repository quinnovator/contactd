package actor_test

import (
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/quinnovator/contactd/internal/actor"
	"github.com/quinnovator/contactd/internal/core"
)

// A client that opens a contact reads the log and then follows the stream.
// These tests pin what the stream says first, which is what makes the two
// halves meet without a gap.

func TestStreamOpenedMidReplyStartsWithWhatWasSaid(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, func(o *options) {
			o.llm.PerChunk = 100 * ms
			o.llm.ReplyWords = 40
		})
		seq := h.say("tell me everything about the outage")
		time.Sleep(700*ms + 1050*ms) // ten chunks in

		late, err := h.reg.Subscribe(t.Context(), h.id)
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		opening, _ := drain(late)
		if len(opening) != 3 {
			t.Fatalf("stream opened with %s; want ready, typing=true, text", types(opening))
		}
		// The message is the only event so far: the model call is recorded
		// when it ends, and the reply when it is whole.
		if opening[0] != (core.Delta{Type: core.DeltaReady, Seq: seq}) {
			t.Errorf("ready = %+v, want seq %d", opening[0], seq)
		}
		if opening[1].Type != core.DeltaTyping || !opening[1].Typing {
			t.Errorf("second delta = %+v, want typing=true", opening[1])
		}
		soFar := opening[2]
		if soFar.Type != core.DeltaText || !strings.HasPrefix(soFar.Text, "Got it: tell me everything") {
			t.Fatalf("third delta = %+v, want the reply so far", soFar)
		}

		time.Sleep(10 * time.Second)
		rest, _ := drain(late)
		var text strings.Builder
		text.WriteString(soFar.Text)
		var final core.Delta
		for _, d := range rest {
			if d.Type == core.DeltaText {
				text.WriteString(d.Text)
			}
			if d.Type == core.DeltaMessage {
				final = d
			}
		}
		if final.Text == "" || text.String() != final.Text {
			t.Errorf("a late subscriber pieced together %q; the committed reply is %q", text.String(), final.Text)
		}
	})
}

func TestInterruptStopsTheReplyAndStartsNothing(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, func(o *options) {
			o.llm.PerChunk = 100 * ms
			o.llm.ReplyWords = 40
		})
		sub, err := h.reg.Subscribe(t.Context(), h.id)
		if err != nil {
			t.Fatal(err)
		}
		h.say("tell me everything about the outage")
		time.Sleep(700*ms + 1050*ms)
		if err := h.reg.Interrupt(t.Context(), h.id); err != nil {
			t.Fatal(err)
		}
		time.Sleep(30 * time.Second)

		// The cut-off reply is kept and flagged, and nothing answers in its
		// place: nobody said anything new.
		if got := h.kinds(); got != "Ua" {
			t.Fatalf("log = %s, want Ua", got)
		}
		if n := len(h.llm.seen()); n != 1 {
			t.Errorf("%d model calls after an interrupt, want 1", n)
		}
		ds, _ := drain(sub)
		if got := types(ds); !strings.HasSuffix(got, "text interrupted typing=false") {
			t.Errorf("stream ended %s; want text interrupted typing=false", got)
		}

		// The contact is not wedged: the next message is answered, and the
		// model is shown the message it never finished answering.
		h.say("just the summary then")
		time.Sleep(30 * time.Second)
		if got := h.kinds(); got != "UaUA" {
			t.Fatalf("log = %s, want UaUA", got)
		}
		if calls := h.llm.seen(); len(calls) != 2 || calls[1].shape != "UAUN" {
			t.Errorf("calls = %+v, want the second prompt shaped UAUN", calls)
		}
	})
}

func TestInterruptDuringToolRunLetsTheToolFinish(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, func(o *options) { o.execDelay = 5 * time.Second })
		h.say("!python slow_job()") // the tool starts at 0.7s and runs until 5.7s
		time.Sleep(2 * time.Second)
		if err := h.reg.Interrupt(t.Context(), h.id); err != nil {
			t.Fatal(err)
		}
		time.Sleep(30 * time.Second)

		if _, _, _, killed, _ := h.env.stats(); killed != 0 {
			t.Fatalf("the running tool was killed %d time(s)", killed)
		}
		// Its result is on record, and the turn ended there.
		if got := h.kinds(); got != "UCR" {
			t.Fatalf("log = %s, want UCR", got)
		}
		if n := len(h.llm.seen()); n != 1 {
			t.Errorf("%d model calls, want 1: the model is not asked to go on", n)
		}

		h.say("never mind, how are you?")
		time.Sleep(30 * time.Second)
		if got := h.kinds(); got != "UCRUA" {
			t.Fatalf("log = %s, want UCRUA", got)
		}
	})
}

func TestInterruptWithNothingInFlight(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		if err := h.reg.Interrupt(t.Context(), h.id); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if h.reg.Hydrated() != 0 {
			t.Fatal("an interrupt woke a contact that had nothing to stop")
		}

		h.say("hello")
		time.Sleep(10 * time.Second)
		if err := h.reg.Interrupt(t.Context(), h.id); err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Second)
		if got := h.kinds(); got != "UA" {
			t.Errorf("log = %s, want UA: an interrupt after the reply changes nothing", got)
		}
	})
}

func TestStreamOpenedOnAHibernatedContactOpensWithReady(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, func(o *options) { o.cfg.IdleTTL = time.Minute })
		h.say("hello")
		time.Sleep(10 * time.Minute) // answered, then hibernated
		if h.reg.Hydrated() != 0 {
			t.Fatal("the contact did not hibernate")
		}
		subs := make([]*actor.Subscription, 0, 3)
		for range 3 {
			sub, err := h.reg.Subscribe(t.Context(), h.id)
			if err != nil {
				t.Fatal(err)
			}
			subs = append(subs, sub)
		}
		synctest.Wait()
		for i, sub := range subs {
			if ds, _ := drain(sub); len(ds) != 1 || ds[0].Type != core.DeltaReady {
				t.Errorf("stream %d opened with %s, want ready", i, types(ds))
			}
		}
	})
}
