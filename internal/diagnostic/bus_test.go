package diagnostic_test

import (
	"sync"
	"testing"

	"github.com/quinnovator/contactd/internal/diagnostic"
)

func TestRetainsLatestStateWithoutAliasing(t *testing.T) {
	t.Parallel()
	b := diagnostic.New(2)
	first := b.Begin(diagnostic.Snapshot{Tiers: []diagnostic.Tier{{Tokens: 10}}, Recall: []uint32{1}, Events: []string{"recall"}})
	second := b.Begin(diagnostic.Snapshot{})
	first.Reported, first.ReportedCached = true, 5
	b.Finish(first) // completion arrived after another contact's plan
	first.Tiers[0].Tokens, first.Recall[0], first.Events[0] = 99, 99, "changed"
	h, updates, cancel := b.Subscribe()
	defer cancel()
	if len(h) != 2 || !h[0].Done || h[0].Call != first.Call || h[1].Call != second.Call || h[0].ReportedCached != 5 {
		t.Fatalf("history lost or reordered completion: %+v", h)
	}
	if h[0].Tiers[0].Tokens != 10 || h[0].Recall[0] != 1 || h[0].Events[0] != "recall" {
		t.Fatal("publisher mutated retained history")
	}
	h[0].Tiers[0].Tokens = 500
	third := b.Begin(diagnostic.Snapshot{})
	got := <-updates
	if got.Call != third.Call {
		t.Fatal("missed a live update")
	}
	b.Finish(first) // evicted calls must not displace newer ones
	h, _, stop := b.Subscribe()
	defer stop()
	if len(h) != 2 || h[0].Call != second.Call || h[1].Call != third.Call {
		t.Fatalf("eviction order: %+v", h)
	}
}

func TestSlowSubscriberReconnectsWithoutStallingPublisher(t *testing.T) {
	t.Parallel()
	b := diagnostic.New(3)
	_, slow, cancel := b.Subscribe()
	defer cancel()
	for range 1000 {
		b.Begin(diagnostic.Snapshot{})
	}
	count := 0
	for range slow {
		count++
	}
	if count != 64 {
		t.Fatalf("slow reader buffered %d updates, want 64", count)
	}
	h, live, stop := b.Subscribe()
	defer stop()
	if len(h) != 3 || h[2].Call != 1000 {
		t.Fatalf("reconnected history: %+v", h)
	}
	b.Close()
	b.Close()
	if _, ok := <-live; ok {
		t.Fatal("close did not release a stream")
	}
}

func TestConcurrentPublishAndSubscribe(t *testing.T) {
	t.Parallel()
	b := diagnostic.New(20)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 100 {
				s := b.Begin(diagnostic.Snapshot{})
				_, _, stop := b.Subscribe()
				b.Finish(s)
				stop()
			}
		})
	}
	wg.Wait()
	b.Close()
}
