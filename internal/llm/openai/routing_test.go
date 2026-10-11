package openai

import (
	"fmt"
	"testing"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/plan"
)

const routedContacts = 20_000

func replicaURLs(n int) []string {
	urls := make([]string, n)
	for i := range urls {
		urls[i] = fmt.Sprintf("http://gpu-%d.inference.internal:8000/v1", i)
	}
	return urls
}

func assignments(t *testing.T, urls []string) []string {
	t.Helper()
	c := newClient(t, Config{BaseURLs: urls})
	out := make([]string, routedContacts)
	for i := range out {
		out[i] = c.replicaFor(core.ContactID(i + 1)).endpoint
	}
	return out
}

func TestRendezvousIsStable(t *testing.T) {
	t.Parallel()
	urls := replicaURLs(4)
	first := assignments(t, urls)

	// A second client, as after a restart, agrees with the first.
	for i, got := range assignments(t, urls) {
		if got != first[i] {
			t.Fatalf("contact %d moved from %s to %s between clients", i+1, first[i], got)
		}
	}
	// The order replicas are listed in must not matter either: a config edit
	// that only reorders the list should not cool every cache.
	reordered := []string{urls[2], urls[0], urls[3], urls[1]}
	for i, got := range assignments(t, reordered) {
		if got != first[i] {
			t.Fatalf("contact %d moved from %s to %s when the list was reordered", i+1, first[i], got)
		}
	}
}

func TestRendezvousSpreadsContacts(t *testing.T) {
	t.Parallel()
	const n = 4
	counts := map[string]int{}
	for _, endpoint := range assignments(t, replicaURLs(n)) {
		counts[endpoint]++
	}
	if len(counts) != n {
		t.Fatalf("contacts landed on %d of %d replicas", len(counts), n)
	}
	// Consecutive IDs are the worst case for a weak hash. An even split is
	// 5000 each; allow 10% either way.
	want := routedContacts / n
	for endpoint, got := range counts {
		if got < want*9/10 || got > want*11/10 {
			t.Errorf("%s serves %d contacts, want about %d", endpoint, got, want)
		}
	}
}

func TestRendezvousAddingReplicaMovesFew(t *testing.T) {
	t.Parallel()
	const n = 4
	urls := replicaURLs(n + 1)
	before := assignments(t, urls[:n])
	after := assignments(t, urls)
	added := urls[n] + "/chat/completions"

	moved := 0
	for i := range before {
		if before[i] == after[i] {
			continue
		}
		moved++
		// A contact only ever leaves for the new replica; nothing shuffles
		// between the old ones.
		if after[i] != added {
			t.Fatalf("contact %d moved from %s to %s, not to the new replica", i+1, before[i], after[i])
		}
	}
	// The newcomer should take its fair share, 1/(n+1) = 20%, and no more.
	share := float64(moved) / routedContacts
	if share < 0.17 || share > 0.23 {
		t.Errorf("%.1f%% of contacts moved, want about %.0f%%", 100*share, 100.0/(n+1))
	}
}

func TestSingleReplicaTakesEverything(t *testing.T) {
	t.Parallel()
	c := newClient(t, Config{BaseURLs: []string{"http://only:8000/v1"}})
	for id := range core.ContactID(100) {
		if got := c.replicaFor(id).endpoint; got != "http://only:8000/v1/chat/completions" {
			t.Fatalf("contact %d routed to %s", id, got)
		}
	}
}

func TestStreamRoutesByContact(t *testing.T) {
	t.Parallel()
	apis := []*fakeAPI{
		newFakeAPI(t, step{file: "text.sse"}),
		newFakeAPI(t, step{file: "text.sse"}),
		newFakeAPI(t, step{file: "text.sse"}),
	}
	urls := make([]string, len(apis))
	for i, api := range apis {
		urls[i] = api.url
	}
	c := newClient(t, Config{BaseURLs: urls})

	const contacts, rounds = 12, 3
	for round := range rounds {
		for id := core.ContactID(1); id <= contacts; id++ {
			o := fullOpts(t)
			in := plan.Input{Contact: id, Agent: &core.AgentConfig{System: o.system}, Tail: o.events}
			p, err := plan.Build(nil, in, nil, fixedNow, plan.DefaultParams())
			if err != nil {
				t.Fatal(err)
			}
			s, err := c.Stream(t.Context(), p)
			if err != nil {
				t.Fatalf("round %d contact %d: %v", round, id, err)
			}
			collect(t, s)
			if _, err := s.Stop(); err != nil {
				t.Fatalf("round %d contact %d: %v", round, id, err)
			}
		}
	}

	total, used := 0, 0
	for i, api := range apis {
		n := len(api.seen())
		total += n
		if n > 0 {
			used++
		}
		// Every contact returned to its replica each round, so each replica
		// saw a whole number of contacts, every round.
		if n%rounds != 0 {
			t.Errorf("replica %d saw %d requests, not a multiple of %d", i, n, rounds)
		}
	}
	if total != contacts*rounds {
		t.Errorf("replicas saw %d requests in total, want %d", total, contacts*rounds)
	}
	if used < 2 {
		t.Errorf("only %d of %d replicas were used", used, len(apis))
	}
}
