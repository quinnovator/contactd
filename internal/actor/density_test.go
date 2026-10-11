package actor_test

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/quinnovator/contactd/internal/actor"
	"github.com/quinnovator/contactd/internal/core"
	fakeinfer "github.com/quinnovator/contactd/internal/infer/fake"
	fakellm "github.com/quinnovator/contactd/internal/llm/fake"
	"github.com/quinnovator/contactd/internal/obs"
	"github.com/quinnovator/contactd/internal/plan"
	"github.com/quinnovator/contactd/internal/recall"
	"github.com/quinnovator/contactd/internal/turn"
)

// BenchmarkDensity hydrates 10k contacts with realistic tails and episode
// counts and reports the memory each one holds. The result is what
// config.BytesPerActor should be set to: MaxHydrated is derived from
// GOMEMLIMIT divided by it.
func BenchmarkDensity(b *testing.B) {
	const (
		contacts = 10_000
		tailMsgs = 60 // 30 exchanges in the hot window
		episodes = 40 // a few months of conversations
	)
	// One prototype history, shared by every contact: the bytes are
	// immutable, so only the actors' own copies are measured.
	index, err := recall.NewIndex(recall.DefaultParams(), nil)
	if err != nil {
		b.Fatal(err)
	}
	for e := range episodes {
		index.OnEmbedded(core.Event{Seq: uint64(e + 1), TS: int64(e) * 86_400_000}, fakeinfer.Embed(fmt.Sprintf("topic %d words", e)))
	}
	rows := index.Rows()
	text := "a message of fairly ordinary length, the kind people actually send to one another all day"

	for b.Loop() {
		b.StopTimer()
		log := &memLog{
			contacts: make(map[core.ContactID]core.Contact, contacts),
			events:   make(map[core.ContactID][]core.Event, contacts),
			vectors:  make(map[core.ContactID][]core.Vector),
			episodes: make(map[core.ContactID]map[uint32]core.EpisodeRow, contacts),
		}
		agent := core.AgentConfig{System: "You are a helpful colleague."}
		for c := core.ContactID(1); c <= contacts; c++ {
			log.contacts[c] = core.Contact{ID: c, Agent: agent, Profile: "Prefers short answers."}
			evs := make([]core.Event, tailMsgs)
			for i := range evs {
				kind := core.KindUserMsg
				if i%2 == 1 {
					kind = core.KindAgentMsg
				}
				body, err := json.Marshal(core.MsgBody{Text: fmt.Sprintf("%s (%d)", text, i)})
				if err != nil {
					b.Fatal(err)
				}
				// Sequence numbers continue after the indexed history.
				evs[i] = core.Event{Contact: c, Seq: uint64(i + 1), TS: 1, Kind: kind, Body: body}
			}
			log.events[c] = evs
			m := make(map[uint32]core.EpisodeRow, len(rows))
			for _, r := range rows {
				// Episodes cover the same seq range as the tail here, which
				// only matters to recall, not to memory.
				r.Start, r.End = uint64(r.ID+1), uint64(r.ID+1)
				m[r.ID] = r
			}
			log.episodes[c] = m
		}
		cfg := actor.DefaultConfig()
		cfg.TailEvents = tailMsgs
		models := fakellm.NewClient(fakellm.New(fakellm.Options{}))
		ctx, cancel := context.WithCancel(context.Background())
		reg := actor.New(ctx, actor.Deps{
			Log: log, Hydrate: log, Repair: log, Embed: directEmbed{}, Snapshots: log,
			Turn: &turn.Deps{
				Embed: directEmbed{}, Rerank: directRerank{}, Log: log, Models: models, Logger: obs.Discard(),
				Recall: cfg.Recall, Plan: plan.DefaultParams(), MaxToolSteps: 4, MaxToolOutput: 4096, QueryTokens: 128,
			},
		}, cfg, obs.Discard())

		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		b.StartTimer()
		for c := core.ContactID(1); c <= contacts; c++ {
			// A typing indicator sent directly hydrates without starting a turn.
			if err := reg.Send(ctx, c, actor.Typing{}); err != nil {
				b.Fatal(err)
			}
		}
		for reg.Hydrated() < contacts {
			time.Sleep(time.Millisecond)
		}
		b.StopTimer()
		time.Sleep(50 * time.Millisecond) // let the last hydrations finish
		runtime.GC()
		runtime.ReadMemStats(&after)
		heap := float64(after.HeapAlloc-before.HeapAlloc) / contacts
		stack := float64(after.StackInuse-before.StackInuse) / contacts
		b.ReportMetric(heap, "heap-B/actor")
		b.ReportMetric(stack, "stack-B/actor")
		b.ReportMetric(heap+stack, "B/actor")

		reg.Shutdown(ctx)
		cancel()
		b.StartTimer()
	}
}
