package actor_test

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/llm"
	fakellm "github.com/quinnovator/contactd/internal/llm/fake"
)

// These tests pin defects an independent review found in the provider-core
// change. Each failed before its fix.

// A sub-agent that finishes while the server is shutting down cannot reach
// its parent. The parent's log still shows it as started and not settled,
// so the parent's next actor asks it to report again.
func TestASettlementLostAtShutdownIsRedelivered(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		log := newMemLog(t)
		slow := func(o *options) { o.llm.PerChunk = 2 * time.Second }
		h := newHarnessOn(t, log, routeCaps(nil), delegating, slow)
		h.say("!delegate a job")
		time.Sleep(3 * time.Second) // the parent and the child are both mid-turn
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
		defer cancel()
		h.reg.Shutdown(ctx) // both turns finish inside the grace period
		if got := kindsOf(log.all(2)); got != "UA" {
			t.Fatalf("setup: child log = %s, want it to have finished", got)
		}
		if got := strings.ReplaceAll(h.kinds(), "E", ""); got != "UCRA" {
			t.Fatalf("setup: parent log = %s, want no notice yet", got)
		}

		h2 := newHarnessOn(t, log, routeCaps(nil), delegating)
		h2.id = h.id
		h2.say("did the sub-agent finish?")
		time.Sleep(time.Minute)
		got := strings.ReplaceAll(h2.kinds(), "E", "")
		if !strings.Contains(got, "n") {
			t.Fatalf("parent log = %s; the settlement was never delivered", got)
		}
		var notice core.NoticeBody
		for _, ev := range log.all(h.id) {
			if ev.Kind == core.KindNotice {
				if err := json.Unmarshal(ev.Body, &notice); err != nil {
					t.Fatal(err)
				}
			}
		}
		if notice.Child != 2 || !strings.Contains(notice.Text, "Sub-agent 2 finished") || !strings.Contains(notice.Text, "<task> a job") {
			t.Errorf("notice = %+v, want the child's answer, read back from its log", notice)
		}
		// Asked once more, nothing is delivered twice.
		h2.reg.Shutdown(t.Context())
		h3 := newHarnessOn(t, log, routeCaps(nil), delegating)
		h3.id = h.id
		h3.say("anything else?")
		time.Sleep(time.Minute)
		if n := strings.Count(h3.kinds(), "n"); n != 1 {
			t.Errorf("%d notices for one sub-agent", n)
		}
	})
}

// A sub-agent that was cut off mid-task by a restart reports that, instead
// of leaving its parent waiting for good.
func TestASubAgentCutOffByARestartSettlesAsFailed(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		log := newMemLog(t)
		// The sub-agent's first move is a tool run that takes an hour.
		h := newHarnessOn(t, log, routeCaps(nil), delegating, func(o *options) {
			o.execDelay = time.Hour
			o.llm.Rules = []fakellm.Rule{{Match: regexp.MustCompile(`^<task>`), Tool: "python", Code: "work()"}}
		})
		h.say("!delegate a very long job")
		time.Sleep(30 * time.Second)
		if got := kindsOf(log.all(2)); got != "UC" {
			t.Fatalf("setup: child log = %s, want it mid-tool", got)
		}
		ctx, cancel := context.WithTimeout(t.Context(), time.Second) // the grace period runs out
		defer cancel()
		h.reg.Shutdown(ctx)

		h2 := newHarnessOn(t, log, routeCaps(nil), delegating)
		h2.id = h.id
		h2.say("hello again")
		time.Sleep(time.Minute)
		var notice core.NoticeBody
		for _, ev := range log.all(h.id) {
			if ev.Kind == core.KindNotice {
				if err := json.Unmarshal(ev.Body, &notice); err != nil {
					t.Fatal(err)
				}
			}
		}
		if notice.Child != 2 || !strings.Contains(notice.Text, "failed (stop: error)") || !strings.Contains(notice.Text, "restarted") {
			t.Errorf("notice = %+v, want a failure that says the sub-agent was cut off", notice)
		}
	})
}

// A sub-agent whose log cannot be written dies with its state. It must still
// tell its parent, or the parent stays in memory waiting and its slot is
// never given back.
func TestASubAgentThatDiesOnAWriteFailureStillSettles(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		log := newMemLog(t)
		h := newHarnessOn(t, log, routeCaps(nil), delegating, func(o *options) {
			o.cfg.MaxChildren = 1
			o.cfg.IdleTTL = time.Minute
			o.llm.Rules = []fakellm.Rule{{Match: regexp.MustCompile(`^<task>`), Tool: "python", Code: "work()"}}
		})
		log.setHooks(func(w core.Write) error {
			if w.Contact == 2 && w.Event != nil && w.Event.Kind == core.KindToolCall {
				return errDisk
			}
			return nil
		}, nil)
		h.say("!delegate a job")
		time.Sleep(time.Minute)
		log.setHooks(nil, nil)

		got := h.kinds()
		if !strings.HasPrefix(got, "UCRAn") {
			t.Fatalf("parent log = %s, want a failure notice after its reply", got)
		}
		if body := string(log.all(h.id)[4].Body); !strings.Contains(body, "failed (stop: error)") || !strings.Contains(body, "could not be written") {
			t.Errorf("notice = %s", body)
		}
		// The slot is free again, and the parent can go back to sleep.
		h.say("!delegate another job")
		time.Sleep(time.Minute)
		results := 0
		last := ""
		for _, ev := range log.all(h.id) {
			if ev.Kind == core.KindToolResult {
				results++
				last = string(ev.Body)
			}
		}
		if results != 2 || !strings.Contains(last, "started") {
			t.Errorf("second delegation = %s; the first sub-agent's slot was not given back", last)
		}
		time.Sleep(6 * time.Hour)
		synctest.Wait()
		if n := h.reg.Hydrated(); n != 0 {
			t.Errorf("%d actors still in memory six hours later", n)
		}
	})
}

// A checkpoint is written once and stands for a long time. A contact that
// hibernates long after its last fold must come back with the summary still
// at the head of its prompt, even though the checkpoint event is far outside
// the window of recent events that hydration loads.
func TestACheckpointOutlivesTheHydrationWindow(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		log := newMemLog(t)
		small := func(o *options) {
			o.cfg.TailEvents = 2 // hydration loads one turn
			o.cfg.IdleTTL = time.Minute
		}
		h := newHarnessOn(t, log, routeCaps(nil), smallTail, small)
		// At this sizing a fold lands after turns four and six, so after
		// turn seven the newest checkpoint has one whole turn logged after
		// it: it is outside the window hydration will load.
		chat(h, 7)
		if got := h.kinds(); !strings.HasSuffix(got, "KUA") {
			t.Fatalf("setup: log = %s, want it to end with a checkpoint and one turn", got)
		}
		before := h.llm.seen()
		if shape := before[len(before)-1].shape; !strings.HasPrefix(shape, "K") {
			t.Fatalf("setup: prompt %q", shape)
		}
		time.Sleep(5 * time.Minute)
		synctest.Wait()
		if h.reg.Hydrated() != 0 {
			t.Fatal("setup: the contact should have hibernated")
		}

		h.say("back again")
		time.Sleep(5 * time.Second)
		after := h.llm.seen()
		if got := after[len(after)-1].shape; got != "KUAUN" {
			t.Errorf("after hibernation the prompt is %q, want the summary, the one turn in the window, and the new message", got)
		}
	})
}

// The hot window is counted in events of the conversation. Attempt records,
// one per model call, must not use it up.
func TestAttemptRecordsDoNotShrinkTheHydratedWindow(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		log := newMemLog(t)
		small := func(o *options) {
			o.cfg.TailEvents = 8
			o.cfg.IdleTTL = time.Minute
			o.cfg.Checkpoint = false
		}
		h := newHarnessOn(t, log, routeCaps(nil), small)
		for i := range 6 {
			h.say("message " + string(rune('A'+i)))
			time.Sleep(5 * time.Second)
		}
		time.Sleep(5 * time.Minute)
		synctest.Wait()
		h.say("one more")
		time.Sleep(5 * time.Second)
		calls := h.llm.seen()
		// Eight events of history (four turns), then the new message. With
		// attempts counted, only two and a half turns would have fitted.
		if got := calls[len(calls)-1].shape; got != "UAUAUAUAUN" {
			t.Errorf("prompt after hydration = %q, want four turns of history", got)
		}
	})
}

// A message that arrives at the very instant a tool finishes must not cost
// the tool's result. The tool ran; its result is logged, whatever happens to
// the turn next.
func TestAMessageRacingTheEndOfAToolNeverLosesTheResult(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		log := newMemLog(t)
		h := newHarnessOn(t, log, routeCaps(nil), func(o *options) { o.execDelay = time.Second })
		const contacts = 400
		ids := make([]core.ContactID, contacts)
		for i := range ids {
			ids[i] = log.addContact(pythonAgent(), "")
			if _, err := h.reg.Message(t.Context(), ids[i], "!python job()"); err != nil {
				t.Fatal(err)
			}
		}
		time.Sleep(1700 * time.Millisecond) // debounce 700 ms + tool 1 s: the same instant
		for _, id := range ids {
			if _, err := h.reg.Message(t.Context(), id, "one more thing"); err != nil {
				t.Fatal(err)
			}
		}
		time.Sleep(30 * time.Second)
		lost := 0
		for _, id := range ids {
			calls, results := 0, 0
			for _, ev := range log.all(id) {
				switch ev.Kind { //nolint:exhaustive // only these two are counted
				case core.KindToolCall:
					calls++
				case core.KindToolResult:
					results++
				}
			}
			if results < calls {
				lost++
				t.Logf("contact %d: %s", id, kindsOf(log.all(id)))
			}
		}
		if lost != 0 {
			t.Errorf("%d of %d contacts logged a tool call with no result", lost, contacts)
		}
	})
}

// A contact moved to another model keeps its history. Where the new route
// must be given back the reasoning behind every tool call, the tool calls
// the old route made cannot be shown to it as tool calls: it has no
// reasoning of its own for them, and the provider would reject the request
// on every turn from then on.
func TestMovingAContactToARouteThatReplaysReasoning(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		allTurns := routeCaps(func(c *llm.Capabilities) { c.Replay = llm.ReplayAllTurns })
		two := func(o *options) { o.llm.Models = []string{"fake-1", "fake-2"} }
		log := newMemLog(t)
		h := newHarnessOn(t, log, allTurns, two)
		h.say("!python print(1)")
		time.Sleep(5 * time.Second)
		h.reg.Shutdown(t.Context())

		log.setAgent(h.id, func(a *core.AgentConfig) { a.Model = "fake-2" })
		h2 := newHarnessOn(t, log, allTurns, two)
		h2.id = h.id
		h2.say("hello again")
		time.Sleep(5 * time.Second)
		if got := strings.ReplaceAll(h2.kinds(), "E", ""); got != "UCRAUA" {
			t.Fatalf("log = %s; the contact is stuck on its new route", got)
		}
		for _, a := range log.attempts(h.id) {
			if a.Error != "" {
				t.Errorf("attempt on %s failed: %s", a.Route, a.Error)
			}
		}
		// The old exchange is still there for the model to read, as text.
		calls := h2.llm.seen()
		if shape := calls[len(calls)-1].shape; strings.ContainsAny(shape, "CR") {
			t.Errorf("prompt shape %q still carries the other route's tool call", shape)
		}
		body := string(h2.llm.requests("hello again")[0].Body)
		if !strings.Contains(body, "[ran the python tool]") || !strings.Contains(body, "[result of the python tool]") {
			t.Errorf("the earlier tool run is not in the prompt:\n%s", body)
		}
		// Its own tool calls from here on are ordinary ones.
		h2.say("!python print(2)")
		time.Sleep(5 * time.Second)
		h2.say("and once more")
		time.Sleep(5 * time.Second)
		if got := strings.ReplaceAll(h2.kinds(), "E", ""); got != "UCRAUAUCRAUA" {
			t.Errorf("log = %s", got)
		}
		calls = h2.llm.seen()
		if shape := calls[len(calls)-1].shape; strings.Count(shape, "C") != 1 {
			t.Errorf("prompt shape %q: the new route's own tool call should stay a tool call", shape)
		}
	})
}

// A checkpoint's text becomes an episode's summary only when it covers that
// episode and nothing else. An episode keeps the first summary it is given,
// so a wrong one would stay.
func TestACheckpointIsNotFiledAsTheSummaryOfHalfAnEpisode(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, routeCaps(nil), smallTail)
		h.say("tell me about topic A in detail")
		time.Sleep(time.Hour) // past the episode gap: the next message opens a new episode
		for _, topic := range []string{"B", "C", "D"} {
			h.say("tell me about topic " + topic + " in detail")
			time.Sleep(5 * time.Second)
		}
		got := h.kinds()
		if !strings.Contains(got, "K") {
			t.Fatalf("setup: log = %s, want a checkpoint", got)
		}
		// The span covers the first episode and the start of the second.
		if strings.Contains(got, "S") {
			t.Errorf("log = %s: a summary of two conversations was filed as the summary of one", got)
		}
	})
}

// With a small budget a summary can be half the tail by itself. Folding
// "the summary and nothing else" covers nothing new, can never be applied,
// and would be asked for again on every turn.
func TestAFoldAlwaysCoversARealTurn(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		log := newMemLog(t)
		h := newHarnessOn(t, log, routeCaps(nil), smallTail, func(o *options) { o.llm.ReplyWords = 20 })
		// A long first message makes the first summary long.
		h.say(strings.TrimSpace(strings.Repeat(strings.Repeat("x", 40)+" ", 12)))
		time.Sleep(5 * time.Second)
		chat(h, 12)

		var spans []core.CheckpointBody
		for _, ev := range log.all(h.id) {
			if ev.Kind == core.KindCheckpoint {
				var ck core.CheckpointBody
				if err := json.Unmarshal(ev.Body, &ck); err != nil {
					t.Fatal(err)
				}
				spans = append(spans, ck)
			}
		}
		if len(spans) < 2 {
			t.Fatalf("setup: %d checkpoints, want several", len(spans))
		}
		for i := 1; i < len(spans); i++ {
			if spans[i].To <= spans[i-1].To {
				t.Errorf("checkpoint %d covers seqs %d..%d, no further than the one before it (..%d)",
					i, spans[i].From, spans[i].To, spans[i-1].To)
			}
		}
		// And the prompt stays bounded instead of growing turn after turn.
		calls := h.llm.seen()
		if shape := calls[len(calls)-1].shape; len(shape) > 12 {
			t.Errorf("last prompt shape = %q; the tail is not being folded", shape)
		}
	})
}

// A tool that runs for longer than the route keeps a prefix leaves nothing
// in the cache. The step after it must not predict a hit, or every slow tool
// would raise a false alarm.
func TestNoCacheHitIsPredictedAfterAToolOutlivesTheCache(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t,
			routeCaps(func(c *llm.Capabilities) { c.CacheTTL = 5 * time.Minute }),
			func(o *options) { o.execDelay = 20 * time.Minute })
		h.say("!python slow()")
		time.Sleep(30 * time.Minute)
		atts := h.log.attempts(h.id)
		if len(atts) != 2 {
			t.Fatalf("%d attempts", len(atts))
		}
		if atts[1].Predicted != 0 || atts[1].Cached != 0 {
			t.Errorf("after a 20-minute tool on a 5-minute cache: predicted %d, reported %d", atts[1].Predicted, atts[1].Cached)
		}
	})
}

// The task a sub-agent is given says where it came from, so that a fork's
// starting prompt can be found again in its parent's log.
func TestATaskRecordsWhereItCameFrom(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, routeCaps(nil), delegating)
		h.say("we are planning the autumn release")
		time.Sleep(5 * time.Second)
		h.say(`!delegate {"task":"draft the announcement","mode":"fork"}`)
		time.Sleep(10 * time.Second)
		var m core.MsgBody
		if err := json.Unmarshal(h.log.all(2)[0].Body, &m); err != nil {
			t.Fatal(err)
		}
		if m.Task == nil || m.Task.Parent != h.id || !m.Task.Fork || m.Task.Turn != 2 || m.Task.TailFrom != 1 {
			t.Errorf("task reference = %+v", m.Task)
		}
		var res core.ToolResult
		if err := json.Unmarshal(h.log.all(h.id)[4].Body, &res); err != nil {
			t.Fatal(err)
		}
		if res.Child != 2 {
			t.Errorf("the delegation's result does not record its child: %+v", res)
		}
	})
}
