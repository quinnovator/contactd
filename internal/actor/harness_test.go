package actor_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quinnovator/contactd/internal/actor"
	"github.com/quinnovator/contactd/internal/core"
	fakeinfer "github.com/quinnovator/contactd/internal/infer/fake"
	"github.com/quinnovator/contactd/internal/llm"
	fakellm "github.com/quinnovator/contactd/internal/llm/fake"
	"github.com/quinnovator/contactd/internal/obs"
	"github.com/quinnovator/contactd/internal/plan"
	"github.com/quinnovator/contactd/internal/recall"
	"github.com/quinnovator/contactd/internal/turn"
)

// memLog is an in-memory event log. Besides standing in for SQLite it checks
// the invariant everything depends on: each contact's events arrive with
// consecutive sequence numbers, which only holds while the contact has
// exactly one writer.
type memLog struct {
	t  *testing.T
	mu sync.Mutex

	contacts map[core.ContactID]core.Contact
	events   map[core.ContactID][]core.Event
	vectors  map[core.ContactID][]core.Vector
	episodes map[core.ContactID]map[uint32]core.EpisodeRow
	// fail, when set, decides whether a write is rejected.
	fail func(core.Write) error
	// after, when set, runs once a write's Done callback has returned, on the
	// submitter's goroutine. Tests use it to hold the actor at a known point.
	after func(core.Write)
}

func newMemLog(t *testing.T) *memLog {
	t.Helper()
	return &memLog{
		t:        t,
		contacts: make(map[core.ContactID]core.Contact),
		events:   make(map[core.ContactID][]core.Event),
		vectors:  make(map[core.ContactID][]core.Vector),
		episodes: make(map[core.ContactID]map[uint32]core.EpisodeRow),
	}
}

func (l *memLog) addContact(agent core.AgentConfig, profile string) core.ContactID {
	l.mu.Lock()
	defer l.mu.Unlock()
	id := core.ContactID(len(l.contacts) + 1)
	l.contacts[id] = core.Contact{ID: id, Agent: agent, Profile: profile}
	return id
}

// CreateContact implements actor.Creator.
func (l *memLog) CreateContact(_ context.Context, agent core.AgentConfig, profile string, _ int64) (core.ContactID, error) {
	return l.addContact(agent, profile), nil
}

func (l *memLog) Submit(w core.Write) {
	err := l.apply(w)
	if w.Done != nil {
		w.Done(err)
	}
	l.mu.Lock()
	after := l.after
	l.mu.Unlock()
	if after != nil {
		after(w)
	}
}

func (l *memLog) setHooks(fail func(core.Write) error, after func(core.Write)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fail, l.after = fail, after
}

func (l *memLog) apply(w core.Write) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fail != nil {
		if err := l.fail(w); err != nil {
			return err
		}
	}
	if ev := w.Event; ev != nil {
		if want := uint64(len(l.events[ev.Contact]) + 1); ev.Seq != want {
			l.t.Errorf("contact %d: event appended with seq %d, want %d (two writers?)", ev.Contact, ev.Seq, want)
			return fmt.Errorf("seq %d out of order", ev.Seq)
		}
		l.events[ev.Contact] = append(l.events[ev.Contact], *ev)
	}
	if v := w.Vector; v != nil {
		l.vectors[w.Contact] = append(l.vectors[w.Contact], *v)
	}
	if ep := w.Episode; ep != nil {
		if l.episodes[w.Contact] == nil {
			l.episodes[w.Contact] = make(map[uint32]core.EpisodeRow)
		}
		l.episodes[w.Contact][ep.ID] = *ep
	}
	return nil
}

func (l *memLog) Hydrate(_ context.Context, id core.ContactID, tail int) (core.Hydrated, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	c, ok := l.contacts[id]
	if !ok {
		return core.Hydrated{}, fmt.Errorf("contact %d: %w", id, core.ErrContactNotFound)
	}
	all := l.events[id]
	h := core.Hydrated{Contact: c, NextSeq: uint64(len(all) + 1)}
	h.Tail = append(h.Tail, all[max(len(all)-tail, 0):]...)
	for ep := uint32(0); ; ep++ {
		row, ok := l.episodes[id][ep]
		if !ok {
			break
		}
		h.Episodes = append(h.Episodes, row)
	}
	return h, nil
}

func (l *memLog) MessagesAfter(_ context.Context, id core.ContactID, after uint64, limit int) ([]core.Event, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []core.Event
	for _, ev := range l.events[id] {
		if ev.Seq > after && ev.Kind.IsMessage() && len(out) < limit {
			out = append(out, ev)
		}
	}
	return out, nil
}

func (l *memLog) Vectors(_ context.Context, id core.ContactID, from, to uint64) ([]core.Vector, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []core.Vector
	for _, v := range l.vectors[id] {
		if v.Seq >= from && v.Seq <= to {
			out = append(out, v)
		}
	}
	return out, nil
}

func (l *memLog) ReplaceEpisodes(_ context.Context, id core.ContactID, rows []core.EpisodeRow) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.episodes[id] = make(map[uint32]core.EpisodeRow)
	for _, r := range rows {
		l.episodes[id][r.ID] = r
	}
	return nil
}

func (l *memLog) Event(_ context.Context, id core.ContactID, seq uint64) (core.Event, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if seq == 0 || seq > uint64(len(l.events[id])) {
		return core.Event{}, fmt.Errorf("contact %d has no event %d", id, seq)
	}
	return l.events[id][seq-1], nil
}

func (l *memLog) EventsRange(_ context.Context, id core.ContactID, from, to uint64, limit int) ([]core.Event, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []core.Event
	for _, ev := range l.events[id] {
		if ev.Seq >= from && ev.Seq <= to && len(out) < limit {
			out = append(out, ev)
		}
	}
	return out, nil
}

func (l *memLog) LatestOfKind(_ context.Context, id core.ContactID, kind core.EventKind, limit int) ([]core.Event, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []core.Event
	all := l.events[id]
	for i := len(all) - 1; i >= 0 && len(out) < limit; i-- {
		if all[i].Kind == kind {
			out = append(out, all[i])
		}
	}
	return out, nil
}

// all returns a contact's log as a conversation: every event except the
// per-call attempt records, which attempts returns.
func (l *memLog) all(id core.ContactID) []core.Event {
	return l.filter(id, func(k core.EventKind) bool { return k != core.KindAttempt })
}

func (l *memLog) attempts(id core.ContactID) []core.AttemptBody {
	events := l.filter(id, func(k core.EventKind) bool { return k == core.KindAttempt })
	out := make([]core.AttemptBody, 0, len(events))
	for _, ev := range events {
		var a core.AttemptBody
		if err := json.Unmarshal(ev.Body, &a); err != nil {
			l.t.Fatalf("decode attempt: %v", err)
		}
		out = append(out, a)
	}
	return out
}

func (l *memLog) filter(id core.ContactID, keep func(core.EventKind) bool) []core.Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []core.Event
	for _, ev := range l.events[id] {
		if keep(ev.Kind) {
			out = append(out, ev)
		}
	}
	return out
}

func (l *memLog) episodeCount(id core.ContactID) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.episodes[id])
}

func (l *memLog) vectorCount(id core.ContactID) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.vectors[id])
}

// directEmbed and directRerank call the fake models without a batcher. delay
// makes the embedder slow, to move background indexing to a chosen moment.
type directEmbed struct{ delay time.Duration }

func (e directEmbed) Embed(ctx context.Context, texts []string, _ recall.Priority) ([][]float32, error) {
	if e.delay > 0 {
		select {
		case <-time.After(e.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = fakeinfer.Embed(t)
	}
	return out, nil
}

type directRerank struct{}

func (directRerank) Score(_ context.Context, query string, passages []string) ([]float32, error) {
	out := make([]float32, len(passages))
	for i, p := range passages {
		out[i] = fakeinfer.Score(query, p)
	}
	return out, nil
}

// modelCall is what the recording LLM saw for one model call.
type modelCall struct {
	at     time.Duration // since the harness started
	shape  string        // the conversation as roles: U A C(all) R(esult) N(ote)
	recall []uint32
}

// recLLM wraps the fake provider and records every model call a turn makes.
// Summaries and checkpoints go through it too but are not recorded: a turn's
// prompt is the one that ends with the time note.
type recLLM struct {
	*fakellm.Provider
	start time.Time
	mu    sync.Mutex
	calls []modelCall
	// frozen holds every request, turn or not, in the order it was frozen.
	frozen []*llm.Frozen
}

func (r *recLLM) Prepare(ctx context.Context, route llm.Route, p *plan.PromptPlan, o llm.Options) (*llm.Frozen, error) {
	var shape strings.Builder
	isTurn := false
	for _, tier := range []plan.TierKind{plan.TierTail, plan.TierLive} {
		for _, m := range p.Tiers[tier].Msgs {
			switch {
			case m.ToolCall != nil:
				shape.WriteByte('C')
			case m.ToolResult != nil:
				shape.WriteByte('R')
			case strings.HasPrefix(m.Text, "<now>"):
				shape.WriteByte('N')
				isTurn = true
			case strings.HasPrefix(m.Text, "<notice"):
				shape.WriteByte('n')
			case strings.HasPrefix(m.Text, "[Summary of the conversation"):
				shape.WriteByte('K')
			case m.Role == core.RoleUser:
				shape.WriteByte('U')
			case m.Role == core.RoleAssistant:
				shape.WriteByte('A')
			case m.Role == core.RoleSystem, m.Role == core.RoleTool:
				shape.WriteByte('?')
			}
		}
	}
	f, err := r.Provider.Prepare(ctx, route, p, o)
	r.mu.Lock()
	if isTurn {
		r.calls = append(r.calls, modelCall{
			at:     time.Since(r.start),
			shape:  shape.String(),
			recall: append([]uint32(nil), p.Recall...),
		})
	}
	if f != nil {
		r.frozen = append(r.frozen, f)
	}
	r.mu.Unlock()
	return f, err
}

// requests returns every request frozen so far whose body contains marker.
func (r *recLLM) requests(marker string) []*llm.Frozen {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*llm.Frozen
	for _, f := range r.frozen {
		if strings.Contains(string(f.Body), marker) {
			out = append(out, f)
		}
	}
	return out
}

func (r *recLLM) seen() []modelCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]modelCall(nil), r.calls...)
}

// fakeLeaser is a tier-0 environment provider whose Exec takes a fixed time
// and can be killed through its context.
type fakeLeaser struct {
	execDelay time.Duration

	mu        sync.Mutex
	leases    int
	resumes   []core.SnapshotID
	execs     []string
	snapshots int
	released  int
	killed    int
}

func (f *fakeLeaser) Lease(_ context.Context, _ core.ContactID, _ core.EnvSpec, resume core.SnapshotID) (turn.Lease, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.leases++
	f.resumes = append(f.resumes, resume)
	return &fakeLease{f: f}, nil
}

type fakeLease struct {
	f    *fakeLeaser
	once sync.Once
}

func (l *fakeLease) Exec(ctx context.Context, code string) (turn.ExecResult, error) {
	if l.f.execDelay > 0 {
		timer := time.NewTimer(l.f.execDelay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			l.f.mu.Lock()
			l.f.killed++
			l.f.mu.Unlock()
			return turn.ExecResult{}, ctx.Err()
		}
	}
	l.f.mu.Lock()
	l.f.execs = append(l.f.execs, code)
	l.f.mu.Unlock()
	return turn.ExecResult{Stdout: "ran " + code}, nil
}

func (l *fakeLease) Snapshot(context.Context) (core.SnapshotID, error) {
	l.f.mu.Lock()
	defer l.f.mu.Unlock()
	l.f.snapshots++
	return core.SnapshotID(fmt.Sprintf("snap-%d", l.f.snapshots)), nil
}

func (l *fakeLease) Release() {
	l.once.Do(func() {
		l.f.mu.Lock()
		l.f.released++
		l.f.mu.Unlock()
	})
}

func (f *fakeLeaser) stats() (leases, snapshots, released, killed int, resumes []core.SnapshotID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.leases, f.snapshots, f.released, f.killed, append([]core.SnapshotID(nil), f.resumes...)
}

// harness wires a registry to fakes for one test.
type harness struct {
	t     *testing.T
	reg   *actor.Registry
	log   *memLog
	llm   *recLLM
	env   *fakeLeaser
	id    core.ContactID
	start time.Time
}

type options struct {
	cfg       actor.Config
	llm       fakellm.Options
	execDelay time.Duration
	embedWait time.Duration
	summarize bool
	turn      func(*turn.Deps)
	agent     func(*core.AgentConfig)
}

func pythonAgent() core.AgentConfig {
	return core.AgentConfig{
		System: "You are a helpful colleague.",
		Tools:  []core.ToolDef{{Name: "python", Description: "Run Python.", Env: core.EnvSpec{Tier: 0, Base: "python-3.12"}}},
	}
}

// newHarness builds a registry and one contact. Call it inside a synctest
// bubble for tests that depend on time.
func newHarness(t *testing.T, mods ...func(*options)) *harness {
	t.Helper()
	return newHarnessOn(t, newMemLog(t), mods...)
}

// newHarnessOn is newHarness over a log the caller already has.
func newHarnessOn(t *testing.T, log *memLog, mods ...func(*options)) *harness {
	t.Helper()
	o := options{cfg: actor.DefaultConfig()}
	for _, mod := range mods {
		mod(&o)
	}
	h := &harness{
		t:     t,
		log:   log,
		llm:   &recLLM{Provider: fakellm.New(o.llm), start: time.Now()},
		env:   &fakeLeaser{execDelay: o.execDelay},
		start: time.Now(),
	}
	models := fakellm.NewClient(h.llm)
	td := &turn.Deps{
		Embed:         directEmbed{},
		Rerank:        directRerank{},
		Log:           log,
		Models:        models,
		Checkpoint:    o.cfg.Checkpoint,
		Logger:        obs.Discard(),
		Recall:        o.cfg.Recall,
		Plan:          plan.DefaultParams(),
		MaxToolSteps:  4,
		MaxToolOutput: 4096,
		QueryTokens:   128,
	}
	if o.turn != nil {
		o.turn(td)
	}
	deps := actor.Deps{
		Log:       log,
		Create:    log,
		Hydrate:   log,
		Repair:    log,
		Embed:     directEmbed{delay: o.embedWait},
		Envs:      map[uint8]turn.EnvLeaser{0: h.env},
		Snapshots: log,
		Turn:      td,
	}
	if o.summarize {
		deps.Summarize = &turn.ModelSummarizer{Models: models}
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.reg = actor.New(ctx, deps, o.cfg, obs.Discard())
	td.Delegate = h.reg
	agent := pythonAgent()
	if o.agent != nil {
		o.agent(&agent)
	}
	h.id = log.addContact(agent, "")
	t.Cleanup(func() {
		h.reg.Shutdown(ctx)
		cancel()
	})
	return h
}

// say sends a user message and fails the test if it is not acknowledged.
func (h *harness) say(text string) uint64 {
	h.t.Helper()
	seq, err := h.reg.Message(h.t.Context(), h.id, text)
	if err != nil {
		h.t.Fatalf("Message(%q): %v", text, err)
	}
	return seq
}

// kinds renders a contact's log as one letter per event: U(ser), A(gent),
// a(gent, interrupted), C(all), R(esult), S(ummary), E(nv snapshot).
func (h *harness) kinds() string { return kindsOf(h.log.all(h.id)) }

func kindsOf(events []core.Event) string {
	var b strings.Builder
	for _, ev := range events {
		switch ev.Kind {
		case core.KindUserMsg:
			b.WriteByte('U')
		case core.KindAgentMsg:
			if strings.Contains(string(ev.Body), `"interrupted":true`) {
				b.WriteByte('a')
			} else {
				b.WriteByte('A')
			}
		case core.KindToolCall:
			b.WriteByte('C')
		case core.KindToolResult:
			b.WriteByte('R')
		case core.KindSummary:
			b.WriteByte('S')
		case core.KindEnvSnapshot:
			b.WriteByte('E')
		case core.KindCheckpoint:
			b.WriteByte('K')
		case core.KindNotice:
			b.WriteByte('n')
		case core.KindAttempt:
			// One per model call; left out so the log reads as a conversation.
		}
	}
	return b.String()
}

// drain returns everything currently buffered on a subscription.
func drain(sub *actor.Subscription) (out []core.Delta, closed bool) {
	for {
		select {
		case d, ok := <-sub.C:
			if !ok {
				return out, true
			}
			out = append(out, d)
		default:
			return out, false
		}
	}
}

func types(ds []core.Delta) string {
	parts := make([]string, len(ds))
	for i, d := range ds {
		parts[i] = string(d.Type)
		if d.Type == core.DeltaTyping {
			parts[i] += fmt.Sprintf("=%t", d.Typing)
		}
	}
	return strings.Join(parts, " ")
}

var errDisk = errors.New("disk full")
