package actor_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/llm"
	"github.com/quinnovator/contactd/internal/turn"
)

// routeCaps declares a fake route that caches by prefix, like DeepSeek, and
// applies mod on top.
func routeCaps(mod func(*llm.Capabilities)) func(*options) {
	return func(o *options) {
		c := llm.Capabilities{
			ContextWindow: 1_000_000, MaxOutput: 8192,
			Cache: llm.CacheAutoPrefix, CacheTTL: time.Hour, CacheUsage: true,
			ToolChoice: true,
		}
		if mod != nil {
			mod(&c)
		}
		o.llm.Caps = &c
	}
}

func delegating(o *options) { o.agent = func(a *core.AgentConfig) { a.Delegate = true } }

// TestStoredReasoningSurvivesTurnsAndRestarts is the DeepSeek rule: with
// tools set, every later request must carry the reasoning of every earlier
// tool call, or the provider rejects it. Before reasoning was logged, the
// second turn of any tool-using contact would have failed.
func TestStoredReasoningSurvivesTurnsAndRestarts(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		allTurns := routeCaps(func(c *llm.Capabilities) { c.Replay = llm.ReplayAllTurns })
		log := newMemLog(t)
		h := newHarnessOn(t, log, allTurns)
		for _, text := range []string{"!python print(1)", "thanks, and what about tomorrow?", "!python print(2)"} {
			h.say(text)
			time.Sleep(5 * time.Second)
		}
		if got := h.kinds(); got != "UCRAUAUCRA" {
			t.Fatalf("log = %s, want three answered turns (UCRAUAUCRA); a rejected request shows as a missing reply", got)
		}
		var call core.ToolCall
		var reply core.MsgBody
		events := log.all(h.id)
		if err := json.Unmarshal(events[1].Body, &call); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(events[3].Body, &reply); err != nil {
			t.Fatal(err)
		}
		if call.Reasoning == nil || call.Reasoning.Route != "fake/fake-1" || reply.Reasoning == nil {
			t.Errorf("reasoning was not stored: call %+v, reply %+v", call.Reasoning, reply.Reasoning)
		}

		// A new process has only the log to go on.
		h.reg.Shutdown(t.Context())
		h2 := newHarnessOn(t, log, allTurns)
		h2.id = h.id
		h2.say("!python print(3)")
		time.Sleep(5 * time.Second)
		// (E is the environment snapshot the shutdown recorded.)
		if got := strings.ReplaceAll(h2.kinds(), "E", ""); got != "UCRAUAUCRAUCRA" {
			t.Errorf("after a restart the log = %s; the stored reasoning should have been replayed", got)
		}
	})
}

func TestSameTurnReasoningIsNeverStored(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, routeCaps(func(c *llm.Capabilities) { c.Replay = llm.ReplaySameTurn }))
		h.say("!python print(1)")
		time.Sleep(5 * time.Second)
		for _, ev := range h.log.all(h.id) {
			if bytes.Contains(ev.Body, []byte(`"reasoning"`)) {
				t.Errorf("%s event stores reasoning on a route that only needs it within the turn: %s", ev.Kind, ev.Body)
			}
		}
	})
}

func TestEveryModelCallLeavesAnAttemptRecord(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		fails := 0
		h := newHarness(t, routeCaps(nil), func(o *options) {
			o.llm.Fail = func(_ int, _ []byte) error {
				if fails++; fails == 1 {
					return &llm.Error{Class: llm.ErrOverloaded, Status: 529, Provider: "fake", Err: errors.New("busy")}
				}
				return nil
			}
		})
		h.say("!python print(6*7)")
		time.Sleep(5 * time.Second)
		if got := h.kinds(); got != "UCRA" {
			t.Fatalf("log = %s; a call that failed before streaming should have been retried", got)
		}
		atts := h.log.attempts(h.id)
		if len(atts) != 2 {
			t.Fatalf("%d attempt records for 2 model calls", len(atts))
		}
		if atts[0].Tries != 2 || atts[1].Tries != 1 {
			t.Errorf("tries = %d and %d, want 2 (one retry) and 1", atts[0].Tries, atts[1].Tries)
		}
		if atts[0].Turn != 1 || atts[0].Step != 0 || atts[1].Step != 1 || atts[0].Stop != "tool" || atts[1].Stop != "end" {
			t.Errorf("attempts = %+v", atts)
		}
		// The second call extends the first, and the provider's report
		// matches the prediction.
		if atts[1].Predicted == 0 || atts[1].Cached != atts[1].Predicted {
			t.Errorf("second call: predicted %d cached tokens, provider reported %d", atts[1].Predicted, atts[1].Cached)
		}
		// The record is for the log; it never enters the prompt.
		h.say("and again")
		time.Sleep(5 * time.Second)
		if calls := h.llm.seen(); calls[len(calls)-1].shape != "UCRAUN" {
			t.Errorf("prompt shape = %q", calls[len(calls)-1].shape)
		}
	})
}

func TestCachePredictionCarriesAcrossTurns(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, routeCaps(nil))
		h.say("hello there")
		time.Sleep(10 * time.Second)
		h.say("one more thing")
		time.Sleep(10 * time.Second)
		// Longer than this route's declared cache lifetime.
		time.Sleep(2 * time.Hour)
		h.say("are you still there")
		time.Sleep(10 * time.Second)

		atts := h.log.attempts(h.id)
		if len(atts) != 3 {
			t.Fatalf("%d attempts", len(atts))
		}
		if atts[0].Predicted != 0 {
			t.Errorf("first turn predicted %d", atts[0].Predicted)
		}
		if atts[1].Predicted == 0 || atts[1].Cached != atts[1].Predicted {
			t.Errorf("second turn: predicted %d, reported %d", atts[1].Predicted, atts[1].Cached)
		}
		if atts[2].Predicted != 0 || atts[2].Cached != 0 {
			t.Errorf("after the cache lifetime: predicted %d, reported %d; want a cold start on both sides", atts[2].Predicted, atts[2].Cached)
		}
	})
}

func TestAnUnknownModelIsReportedOnTheStream(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, func(o *options) { o.agent = func(a *core.AgentConfig) { a.Model = "retired-model" } })
		sub, err := h.reg.Subscribe(t.Context(), h.id)
		if err != nil {
			t.Fatal(err)
		}
		h.say("hello")
		time.Sleep(5 * time.Second)
		ds, _ := drain(sub)
		var reason string
		for _, d := range ds {
			if d.Type == core.DeltaError {
				reason = d.Text
			}
		}
		if reason != "unknown_model" {
			t.Errorf("stream = %s, error reason %q; want unknown_model", types(ds), reason)
		}
		if got := h.kinds(); got != "U" {
			t.Errorf("log = %s; nothing should have been sent to any model", got)
		}
		if h.llm.Sends() != 0 {
			t.Error("a request was sent for a model the provider does not serve")
		}
	})
}

// smallTail sizes things so that a turn is about 80 tokens and the tail's
// budget is under three of them: the fourth turn's prompt is over budget
// (240 > 220) and still short of the hard budget (330). A checkpoint summary
// from the fake model is about 45 tokens.
func smallTail(o *options) {
	o.llm.ReplyWords = 80
	o.turn = func(td *turn.Deps) { td.Plan.TailBudget = 220 }
}

func chat(h *harness, n int) {
	for i := range n {
		h.say("tell me about topic " + string(rune('A'+i)) + " in detail")
		time.Sleep(5 * time.Second)
	}
}

func TestCheckpointFoldsTheOldestTurns(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		log := newMemLog(t)
		h := newHarnessOn(t, log, routeCaps(nil), smallTail)
		chat(h, 4)
		// The fourth turn's prompt held three turns, which is over budget:
		// a checkpoint was written off the turn's path once it committed.
		if got := h.kinds(); got != "UAUAUAUAK" {
			t.Fatalf("log = %s, want four turns then a checkpoint (K)", got)
		}
		events := log.all(h.id)
		var ck core.CheckpointBody
		if err := json.Unmarshal(events[8].Body, &ck); err != nil {
			t.Fatal(err)
		}
		// Turns one and two: with attempt records in between, seqs 1..6.
		if ck.From != 1 || ck.To != 6 || !strings.Contains(ck.Text, "topic A") {
			t.Errorf("checkpoint = %+v, want the first two turns", ck)
		}

		// The request that produced it was the live prompt cut short, so it
		// read the folded turns from cache: only the instruction was new.
		var fold *core.AttemptBody
		atts := log.attempts(h.id)
		for i := range atts {
			if atts[i].Purpose == "checkpoint" {
				fold = &atts[i]
			}
		}
		if fold == nil {
			t.Fatal("no attempt record for the checkpoint request")
		}
		if fold.Cached == 0 || fold.Input-fold.Cached > 150 {
			t.Errorf("checkpoint request: %d of %d input tokens from cache", fold.Cached, fold.Input)
		}

		// The next turn sees the summary in place of the two turns.
		h.say("tell me about topic E in detail")
		time.Sleep(5 * time.Second)
		calls := h.llm.seen()
		if got := calls[len(calls)-1].shape; got != "KUAUAUN" {
			t.Errorf("prompt shape = %q, want the summary, turns three and four, and the new message", got)
		}
		// The folded events are still in the log.
		if got := h.kinds(); got != "UAUAUAUAKUA" {
			t.Errorf("log = %s", got)
		}
		if n := strings.Count(h.kinds(), "K"); n != 1 {
			t.Errorf("%d checkpoints after five turns, want 1: the tail is back under its budget", n)
		}

		// One more turn and the tail (summary, turns three to five) is over
		// budget again. The second checkpoint absorbs the first: it covers
		// everything from the very start through turn four.
		chat(h, 1)
		events = log.all(h.id)
		last := events[len(events)-1]
		if last.Kind != core.KindCheckpoint {
			t.Fatalf("log = %s, want a second checkpoint after the sixth turn", h.kinds())
		}
		var ck2 core.CheckpointBody
		if err := json.Unmarshal(last.Body, &ck2); err != nil {
			t.Fatal(err)
		}
		if ck2.From != 1 || ck2.To <= ck.To {
			t.Errorf("second checkpoint = seqs %d..%d, want it to start at 1 and reach past %d", ck2.From, ck2.To, ck.To)
		}
		h.say("and a seventh thing")
		time.Sleep(5 * time.Second)
		calls = h.llm.seen()
		if got := calls[len(calls)-1].shape; got != "KUAUAUN" {
			t.Errorf("prompt shape after the second checkpoint = %q, want one summary, turns five and six, and the new message", got)
		}

		// A new process lays the prompt out the same way from the log.
		h.reg.Shutdown(t.Context())
		h2 := newHarnessOn(t, log, routeCaps(nil), smallTail)
		h2.id = h.id
		h2.say("back again")
		time.Sleep(5 * time.Second)
		if calls := h2.llm.seen(); !strings.HasPrefix(calls[0].shape, "KUA") {
			t.Errorf("after a restart the prompt shape = %q, want it to open with the summary", calls[0].shape)
		}
	})
}

func TestAFailedCheckpointLeavesTheConversationAlone(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, routeCaps(nil), smallTail, func(o *options) {
			o.llm.Fail = func(_ int, body []byte) error {
				if bytes.Contains(body, []byte("<instruction>")) {
					return &llm.Error{Class: llm.ErrAuth, Status: 401, Provider: "fake", Err: errors.New("no")}
				}
				return nil
			}
		})
		chat(h, 8)
		if got := h.kinds(); got != strings.Repeat("UA", 8) {
			t.Fatalf("log = %s, want eight answered turns and no checkpoint", got)
		}
		failed := 0
		for _, a := range h.log.attempts(h.id) {
			if a.Purpose == "checkpoint" && a.Error == "auth" {
				failed++
			}
		}
		if failed == 0 {
			t.Error("the failed checkpoint requests left no record")
		}
		// Without a checkpoint the tail is cut at the hard budget instead,
		// so the prompt stays bounded.
		calls := h.llm.seen()
		if shape := calls[len(calls)-1].shape; len(shape) > 10 || strings.Contains(shape, "K") {
			t.Errorf("last prompt shape = %q; the hard cut should have bounded it", shape)
		}
	})
}

func TestCheckpointsCanBeTurnedOff(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, routeCaps(nil), smallTail, func(o *options) { o.cfg.Checkpoint = false })
		chat(h, 6)
		if got := h.kinds(); strings.Contains(got, "K") {
			t.Errorf("log = %s", got)
		}
		for _, a := range h.log.attempts(h.id) {
			if a.Purpose != "" {
				t.Errorf("a %s request was sent with checkpoints off", a.Purpose)
			}
		}
	})
}

func messagesOf(t *testing.T, f *llm.Frozen) []json.RawMessage {
	t.Helper()
	var req struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(f.Body, &req); err != nil {
		t.Fatal(err)
	}
	return req.Messages
}

func TestSpawnedSubAgentSettlesAsANotice(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, routeCaps(func(c *llm.Capabilities) { c.Affinity = llm.AffinityHeader }), delegating)
		sub, err := h.reg.Subscribe(t.Context(), h.id)
		if err != nil {
			t.Fatal(err)
		}
		h.say("!delegate count the open tickets")
		time.Sleep(10 * time.Second)

		// The parent: message, the delegate call and its immediate result,
		// its reply, then the settlement notice and its answer to that.
		if got := h.kinds(); got != "UCRAnA" {
			t.Fatalf("parent log = %s, want UCRAnA", got)
		}
		parent := h.log.all(h.id)
		if !strings.Contains(string(parent[2].Body), "Sub-agent 2 started") {
			t.Errorf("delegate result = %s", parent[2].Body)
		}
		var notice core.NoticeBody
		if err := json.Unmarshal(parent[4].Body, &notice); err != nil {
			t.Fatal(err)
		}
		if notice.Source != "subagent-settled" || !strings.Contains(notice.Text, "Sub-agent 2 finished (stop: end)") ||
			!strings.Contains(notice.Text, "<result>\nGot it: <task> count the open tickets") {
			t.Errorf("notice = %+v", notice)
		}

		// The child is a contact of its own, with its own log.
		const child = core.ContactID(2)
		if got := kindsOf(h.log.all(child)); got != "UA" {
			t.Errorf("child log = %s, want the task and its answer", got)
		}
		cc, err := h.log.Hydrate(t.Context(), child, 8)
		if err != nil {
			t.Fatal(err)
		}
		if cc.Contact.Agent.Parent != h.id || cc.Contact.Agent.System != pythonAgent().System {
			t.Errorf("child agent = %+v", cc.Contact.Agent)
		}

		// The notice reached the model in the user's position, wrapped so it
		// cannot be mistaken for the contact, and subscribers were told.
		calls := h.llm.seen()
		if last := calls[len(calls)-1].shape; last != "UCRAnN" {
			t.Errorf("the parent's last prompt = %q, want the notice as the thing to answer", last)
		}
		ds, _ := drain(sub)
		if !strings.Contains(types(ds), "notice") {
			t.Errorf("stream = %s, want a notice delta", types(ds))
		}

		// Parent and child share one affinity key, so a gateway keeps them
		// on the same backend; an unrelated contact gets another.
		other := h.log.addContact(pythonAgent(), "")
		if _, err := h.reg.Message(t.Context(), other, "hi"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Second)
		key := func(marker string) string {
			reqs := h.llm.requests(marker)
			if len(reqs) == 0 {
				t.Fatalf("no request containing %q", marker)
			}
			return reqs[0].Header.Get("X-Fake-Session")
		}
		parentKey, childKey, otherKey := key("!delegate count"), key("<task>"), key(`"text":"hi"`)
		if parentKey == "" || childKey != parentKey || otherKey == parentKey {
			t.Errorf("affinity keys: parent %q, child %q, unrelated %q", parentKey, childKey, otherKey)
		}
	})
}

func TestForkedSubAgentReadsItsParentsCache(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, routeCaps(nil), delegating)
		h.say("we are planning the autumn release")
		time.Sleep(5 * time.Second)
		h.say("the date is the third of November")
		time.Sleep(5 * time.Second)
		h.say(`!delegate {"task":"draft the announcement","mode":"fork"}`)
		time.Sleep(10 * time.Second)
		if got := h.kinds(); got != "UAUAUCRAnA" {
			t.Fatalf("parent log = %s", got)
		}

		parentReq := h.llm.requests(`!delegate {\"task`)[0]
		childReq := h.llm.requests("<task>")[0]
		pm, cm := messagesOf(t, parentReq), messagesOf(t, childReq)
		shared := 0
		for shared < len(pm) && shared < len(cm) && bytes.Equal(pm[shared], cm[shared]) {
			shared++
		}
		// The system message and both finished turns: everything the parent
		// had sent before its unfinished turn.
		if shared != 5 {
			t.Fatalf("the fork shares %d leading messages with its parent's request, want 5", shared)
		}
		if strings.Contains(string(childReq.Body), "!delegate") {
			t.Error("the fork was shown the parent's unfinished turn")
		}
		// And the provider served that shared prefix from the parent's cache.
		child := h.log.attempts(2)
		if len(child) == 0 || child[0].Cached == 0 || child[0].Predicted != 0 {
			// The child has sent nothing before, so it predicts nothing;
			// the hit is the parent's doing.
			t.Errorf("child's first call: %+v; want a cache hit it did not have to predict", child)
		}

		// A spawned child, by contrast, starts from the instructions alone.
		h.say("!delegate tidy the changelog")
		time.Sleep(10 * time.Second)
		spawned := messagesOf(t, h.llm.requests(`<task>\ntidy the changelog`)[0])
		if len(spawned) != 3 {
			t.Errorf("a spawned sub-agent's first request has %d messages, want the system prompt, the task and the time note", len(spawned))
		}
	})
}

func TestSubAgentLimitAndDepth(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, routeCaps(nil), delegating, func(o *options) {
			o.cfg.MaxChildren = 2
			o.llm.PerChunk = 2 * time.Second // children take a while
		})
		// Three delegations in one step: the third is over the limit.
		h.say("!delegate job one ;; !delegate job two ;; !delegate job three")
		time.Sleep(30 * time.Second)
		parent := h.log.all(h.id)
		if !strings.HasPrefix(kindsOf(parent), "UCRCRCR") {
			t.Fatalf("parent log = %s", kindsOf(parent))
		}
		if !strings.Contains(string(parent[2].Body), "started") || !strings.Contains(string(parent[4].Body), "started") {
			t.Errorf("the first two delegations should start: %s / %s", parent[2].Body, parent[4].Body)
		}
		if !strings.Contains(string(parent[6].Body), "too many sub-agents") {
			t.Errorf("the third delegation = %s, want it refused", parent[6].Body)
		}
		// Once they settle the slots are free again.
		time.Sleep(5 * time.Minute)
		h.say("!delegate job four")
		time.Sleep(time.Minute)
		log := h.log.all(h.id)
		last := ""
		for _, ev := range log {
			if ev.Kind == core.KindToolResult {
				last = string(ev.Body)
			}
		}
		if !strings.Contains(last, "started") {
			t.Errorf("after the first two settled, a new delegation = %s", last)
		}

		// A sub-agent that tries to delegate is refused: one level only.
		h2 := newHarness(t, routeCaps(nil), delegating)
		h2.say("!delegate !delegate go one deeper")
		time.Sleep(time.Minute)
		child := h2.log.all(2)
		if got := kindsOf(child); got != "UA" {
			// The child's task text does not start with "!", so the scripted
			// model answers it in words. Depth is enforced in the turn loop
			// and covered there; here the point is that no third contact
			// was ever created.
			t.Errorf("child log = %s", got)
		}
		if len(h2.log.all(3)) != 0 {
			t.Error("a sub-agent started a sub-agent")
		}
	})
}

func TestAFailedSubAgentSettlesWithoutFailingItsParent(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, routeCaps(nil), delegating, func(o *options) {
			o.llm.Fail = func(_ int, body []byte) error {
				if bytes.Contains(body, []byte("<task>")) {
					return &llm.Error{Class: llm.ErrQuota, Status: 402, Provider: "fake", Err: errors.New("out of credit")}
				}
				return nil
			}
		})
		h.say("!delegate do something expensive")
		time.Sleep(10 * time.Second)
		if got := h.kinds(); got != "UCRAnA" {
			t.Fatalf("parent log = %s; the parent's own turn must be unaffected", got)
		}
		var notice core.NoticeBody
		if err := json.Unmarshal(h.log.all(h.id)[4].Body, &notice); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(notice.Text, "Sub-agent 2 failed (stop: error)") || !strings.Contains(notice.Text, "out of credit") {
			t.Errorf("notice = %q", notice.Text)
		}
		if i, j := strings.Index(notice.Text, "<diagnostic>"), strings.Index(notice.Text, "</diagnostic>"); j-i > 4096+32 {
			t.Errorf("diagnostic is %d bytes, over the 4,096 limit", j-i)
		}
	})
}

func TestASettlementNeverInterruptsAReply(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, routeCaps(nil), delegating, func(o *options) { o.llm.PerChunk = time.Second })
		h.say("!delegate a quick job")
		// The parent is now streaming its (longer) reply to the tool result
		// while the child finishes first.
		time.Sleep(2 * time.Minute)
		got := h.kinds()
		if got != "UCRAnA" {
			t.Fatalf("parent log = %s, want the reply whole (A, not a), then the notice", got)
		}
		parent := h.log.all(h.id)
		child := h.log.all(2)
		childDone, parentReply := child[len(child)-1].TS, parent[3].TS
		if childDone >= parentReply {
			t.Skipf("setup: the child (done at %d) did not finish before the parent's reply (%d)", childDone, parentReply)
		}
		// The notice waited for the reply it would otherwise have cut off.
		if parent[4].Kind != core.KindNotice || parent[4].Seq < parent[3].Seq {
			t.Errorf("notice at seq %d, reply at seq %d", parent[4].Seq, parent[3].Seq)
		}
	})
}

func TestInterruptingTheParentDoesNotStopItsSubAgent(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, routeCaps(nil), delegating, func(o *options) { o.llm.PerChunk = time.Second })
		h.say("!delegate a long job")
		time.Sleep(4 * time.Second)
		h.say("actually, something else") // cuts the parent's reply off
		time.Sleep(3 * time.Minute)
		if got := kindsOf(h.log.all(2)); got != "UA" {
			t.Errorf("child log = %s; an interruption of the parent must not stop it", got)
		}
		got := h.kinds()
		if !strings.Contains(got, "a") || !strings.Contains(got, "n") {
			t.Errorf("parent log = %s, want an interrupted reply and, later, the settlement", got)
		}
	})
}

func TestAParentWithRunningSubAgentsStaysAwake(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, routeCaps(nil), delegating, func(o *options) {
			o.cfg.IdleTTL = 30 * time.Second
			o.llm.PerChunk = 10 * time.Second // the child takes minutes
		})
		h.say("!delegate a slow job")
		time.Sleep(90 * time.Second)
		synctest.Wait()
		// Past its idle TTL, but a child is still out: the parent holds the
		// slot count and must not retire with it.
		if h.reg.Hydrated() < 2 {
			t.Errorf("%d actors hydrated; the parent should stay while its sub-agent runs", h.reg.Hydrated())
		}
		time.Sleep(20 * time.Minute)
		synctest.Wait()
		if got := h.kinds(); !strings.Contains(got, "n") {
			t.Errorf("parent log = %s, want the settlement", got)
		}
		if h.reg.Hydrated() != 0 {
			t.Errorf("%d actors still hydrated long after everything settled", h.reg.Hydrated())
		}
	})
}
