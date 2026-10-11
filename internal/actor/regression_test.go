package actor_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/quinnovator/contactd/internal/core"
)

// Each test here pins a defect found in review. The scenario is in the
// comment; the assertions are what must stay true.

// A message that arrives while the model is still streaming the tool call
// must cancel the turn, not be treated as "arrived during the tool". The
// turn enters its tool phase on the actor goroutine, in mailbox order, so a
// message queued ahead of the call always wins.
func TestMessageQueuedAheadOfToolCallCancelsTheTurn(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, func(o *options) {
			o.llm.PerChunk = 200 * ms
			o.embedWait = 800 * ms // the first message's vector lands at 0.8s
			o.execDelay = time.Second
		})
		// Hold the actor goroutine from 0.8s, when it writes that vector.
		gate := make(chan struct{})
		var once sync.Once
		h.log.setHooks(nil, func(w core.Write) {
			if w.Vector != nil {
				once.Do(func() { <-gate })
			}
		})
		h.say("!python job()") // turn starts at 0.7s; the call's arguments finish at 1.1s
		time.Sleep(900 * ms)
		sent := make(chan error, 1)
		go func() {
			_, err := h.reg.Message(t.Context(), h.id, "also, how are you?")
			sent <- err
		}()
		time.Sleep(300 * ms) // t=1.2s: mailbox holds the message, then the tool call
		close(gate)
		if err := <-sent; err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Second)

		if _, _, _, _, resumes := h.env.stats(); len(resumes) != 0 {
			t.Errorf("a tool ran for a turn the new message should have cancelled")
		}
		if got := h.kinds(); got != "UUA" {
			t.Fatalf("log = %s, want UUA: both messages, then one reply to them", got)
		}
		events := h.log.all(h.id)
		if !strings.Contains(string(events[2].Body), "also, how are you?") {
			t.Errorf("the reply does not answer the newest message: %s", events[2].Body)
		}
	})
}

// A message delivered behind a write failure must be refused, not left
// unanswered when the actor drops its state.
func TestMessageBehindAWriteFailureIsAnswered(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.say("first")
		time.Sleep(5 * time.Second)

		// Fail the write of "lost", and hold the actor right after the
		// failure has been posted to its own mailbox.
		held, proceed := make(chan struct{}), make(chan struct{})
		h.log.setHooks(failLost, func(w core.Write) {
			if isLost(w) {
				close(held)
				<-proceed
			}
		})
		lost := make(chan error, 1)
		go func() {
			_, err := h.reg.Message(t.Context(), h.id, "lost")
			lost <- err
		}()
		<-held
		// Queue another message behind the failure while the actor is busy.
		behind := make(chan error, 1)
		go func() {
			_, err := h.reg.Message(t.Context(), h.id, "behind the failure")
			behind <- err
		}()
		synctest.Wait()
		close(proceed)
		synctest.Wait()

		if err := <-lost; !errors.Is(err, errDisk) {
			t.Fatalf("err = %v, want the disk error", err)
		}
		select {
		case err := <-behind:
			if err == nil {
				t.Error("the message queued behind the failure was acknowledged by an actor that dropped its state")
			}
		default:
			t.Fatal("the message queued behind the write failure was neither acknowledged nor refused")
		}

		// The contact recovers, and its environment was released, not leaked.
		h.log.setHooks(nil, nil)
		h.say("second")
		time.Sleep(5 * time.Second)
		if got := h.kinds(); got != "UAUA" {
			t.Errorf("log = %s, want UAUA", got)
		}
	})
}

func TestWriteFailureReleasesTheEnvironment(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.say("!python x = 1")
		time.Sleep(5 * time.Second)
		h.log.setHooks(func(w core.Write) error {
			if w.Event != nil && w.Event.Kind == core.KindUserMsg {
				return errDisk
			}
			return nil
		}, nil)
		if _, err := h.reg.Message(t.Context(), h.id, "lost"); !errors.Is(err, errDisk) {
			t.Fatalf("err = %v", err)
		}
		synctest.Wait()
		if leases, _, released, _, _ := h.env.stats(); leases != 1 || released != 1 {
			t.Errorf("%d lease(s), %d released; the dropped actor's environment must not stay open", leases, released)
		}
	})
}

// A client that sends faster than the model answers must hit a limit instead
// of growing the prompt, and the contact must not be wedged by it.
func TestBacklogIsBoundedAndClears(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, func(o *options) {
			o.cfg.MaxPendingBytes = 1000
			o.llm.FirstToken = 5 * time.Second // every reply is interrupted before it starts
		})
		text := strings.Repeat("lorem ipsum ", 20) // 240 bytes
		accepted, refused := 0, 0
		for range 30 {
			_, err := h.reg.Message(t.Context(), h.id, text)
			switch {
			case err == nil:
				accepted++
			case errors.Is(err, core.ErrBacklog):
				refused++
			default:
				t.Fatal(err)
			}
			time.Sleep(2 * time.Second)
		}
		if refused == 0 {
			t.Fatalf("all %d messages were accepted with a 1000-byte backlog limit", accepted)
		}
		// Refusing a message does not interrupt the turn in flight, so the
		// backlog gets answered and no prompt ever carries more than the
		// limit's worth of unanswered messages (four of 240 bytes).
		for _, call := range h.llm.seen() {
			if pending := strings.Count(call.shape[strings.LastIndexAny(call.shape, "AR")+1:], "U"); pending > 4 {
				t.Fatalf("a prompt carried %d unanswered messages (shape %s); the limit allows 4", pending, call.shape)
			}
		}
		if !strings.Contains(h.kinds(), "A") {
			t.Fatalf("log = %s; no reply ever got through while the client kept sending", h.kinds())
		}
		// Once the sender pauses, the queued messages get their reply and the
		// contact accepts messages again.
		time.Sleep(30 * time.Second)
		if got := h.kinds(); !strings.HasSuffix(got, "UA") {
			t.Fatalf("log = %s; the backlog was never answered", got)
		}
		if _, err := h.reg.Message(t.Context(), h.id, "thanks"); err != nil {
			t.Errorf("after the backlog cleared: %v", err)
		}
	})
}

// A message acknowledged just before shutdown is still inside its debounce
// window. The grace period must cover its reply.
func TestShutdownAnswersAnAcknowledgedMessage(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.say("hello")
		time.Sleep(300 * ms) // the 700 ms debounce has not fired yet
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		h.reg.Shutdown(ctx)
		if got := h.kinds(); got != "UA" {
			t.Errorf("log = %s, want UA: the acknowledged message must be answered within the grace period", got)
		}
	})
}

// A send that races Shutdown must not leave behind an actor that Shutdown
// never told to stop: it would sit idle for the whole idle TTL while
// Shutdown waited for it. Real time, many rounds, to exercise the race.
func TestShutdownDoesNotHangOnALateSpawn(t *testing.T) {
	t.Parallel()
	const rounds, senders = 150, 16
	for round := range rounds {
		log := newMemLog(t)
		h := newHarnessOn(t, log) // default config: a 10 minute idle TTL
		ids := make([]core.ContactID, senders)
		for i := range ids {
			ids[i] = log.addContact(core.AgentConfig{System: "s"}, "")
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		for _, id := range ids {
			wg.Go(func() {
				<-start
				ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
				defer cancel()
				_, _ = h.reg.Message(ctx, id, "hi") // refused or acknowledged; both are fine
			})
		}
		stopped := make(chan struct{})
		go func() {
			<-start
			if round%2 == 0 {
				time.Sleep(time.Duration(round%50) * time.Microsecond)
			}
			h.reg.Shutdown(t.Context())
			close(stopped)
		}()
		close(start)
		wg.Wait()
		select {
		case <-stopped:
		case <-time.After(5 * time.Second):
			t.Fatalf("round %d: Shutdown still blocked 5s after the last sender returned; %d actor(s) hydrated", round, h.reg.Hydrated())
		}
	}
}

// A stream opened just as its contact's actor is forced out must be handed
// to the next actor, not dropped with the mailbox.
func TestForcedRetirementKeepsQueuedStreams(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.say("first")
		time.Sleep(5 * time.Second)

		held, proceed := make(chan struct{}), make(chan struct{})
		h.log.setHooks(failLost, func(w core.Write) {
			if isLost(w) {
				close(held)
				<-proceed
			}
		})
		go func() { _, _ = h.reg.Message(t.Context(), h.id, "lost") }()
		<-held
		// Subscribe while the failure is queued and the actor is busy.
		sub, err := h.reg.Subscribe(t.Context(), h.id)
		if err != nil {
			t.Fatal(err)
		}
		close(proceed)
		synctest.Wait()
		h.log.setHooks(nil, nil)

		h.say("second")
		time.Sleep(5 * time.Second)
		ds, closed := drain(sub)
		if closed || len(ds) == 0 {
			t.Errorf("the stream opened behind the failure was lost: closed=%t, %d deltas", closed, len(ds))
		}
	})
}

func isLost(w core.Write) bool {
	return w.Event != nil && strings.Contains(string(w.Event.Body), "lost")
}

func failLost(w core.Write) error {
	if isLost(w) {
		return errDisk
	}
	return nil
}
