package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
)

// message is one line of a transcript fixture.
type message struct {
	T    int64  `json:"t"`    // seconds since the transcript began
	Role string `json:"role"` // "user" or "agent"
	Text string `json:"text"`
	// Topic labels what the message is about. It is ground truth for the
	// benchmark and is never shown to the system under test.
	Topic string `json:"topic"`
	// Callback marks a user message that returns to a topic from an earlier
	// conversation ("back to the Postgres thing"). The right behaviour is to
	// recall an episode about Topic.
	Callback bool `json:"callback,omitempty"`
}

// topic is the raw material for synthetic conversations.
type topic struct {
	name      string
	user      []string
	agent     []string
	callbacks []string
}

func topics() []topic {
	return []topic{
		{
			name: "postgres",
			user: []string{
				"the postgres migration is stuck on the orders table again",
				"it says lock timeout while adding the index to orders",
				"should I run the migration with a lower lock_timeout and retry",
				"the orders table has about forty million rows in postgres",
				"can we create the index concurrently instead of inside the migration",
				"autovacuum was running on orders when the migration started",
			},
			agent: []string{
				"A lock timeout on orders usually means a long transaction is holding the table; check pg_stat_activity first.",
				"Create the index concurrently outside the migration transaction so writes to orders are not blocked.",
				"Set lock_timeout to a few seconds and retry in a loop rather than waiting on the lock indefinitely.",
				"Forty million rows is fine for a concurrent index build; expect it to take a while and watch replication lag.",
			},
			callbacks: []string{
				"back to the postgres thing, did we decide to build the orders index concurrently",
				"about that postgres migration from before, what lock_timeout did you suggest",
			},
		},
		{
			name: "lisbon",
			user: []string{
				"I am booking flights to lisbon for the conference in may",
				"which neighbourhood in lisbon should I stay in, alfama or baixa",
				"the conference venue is near the river in lisbon",
				"is the tram from the airport to alfama easy with luggage",
				"I want one free day to visit sintra from lisbon",
				"any restaurant in alfama you would book ahead",
			},
			agent: []string{
				"Alfama is charming but steep; Baixa is flatter and closer to the river venue.",
				"Take the metro from the airport rather than the tram if you have luggage.",
				"Sintra is an easy day trip by train from Rossio station; go early to beat the crowds.",
				"Book the flights soon, May fares to Lisbon rise quickly around conference dates.",
			},
			callbacks: []string{
				"going back to the lisbon trip, which neighbourhood did you recommend over alfama",
				"remind me about lisbon, how do I get from the airport with luggage",
			},
		},
		{
			name: "sourdough",
			user: []string{
				"my sourdough starter is not rising after feeding",
				"I feed the starter equal parts flour and water every morning",
				"the kitchen is cold, maybe that slows the sourdough",
				"the loaf came out dense with a gummy crumb",
				"should I use bread flour or whole wheat for the starter",
				"how long should the bulk ferment be for sourdough",
			},
			agent: []string{
				"A cold kitchen slows a starter a lot; keep it somewhere around twenty five degrees.",
				"A dense gummy crumb usually means the dough was underproofed during the bulk ferment.",
				"Whole wheat flour wakes a sluggish starter up faster than white bread flour.",
				"Bulk ferment until the dough has grown by about half, not by the clock.",
			},
			callbacks: []string{
				"back to the sourdough, why did you say my loaf came out gummy",
				"about the sourdough starter again, which flour wakes it up faster",
			},
		},
		{
			name: "marathon",
			user: []string{
				"I signed up for a marathon in october and need a training plan",
				"my long run is at eighteen kilometres right now",
				"how fast should the long run pace be compared to race pace",
				"my left knee aches after the interval sessions",
				"should I taper for two weeks or three before the marathon",
				"what should I eat during the long run",
			},
			agent: []string{
				"Run the long run about a minute per kilometre slower than marathon race pace.",
				"A knee that aches after intervals wants a lighter week and some strength work.",
				"A two week taper suits most first marathons; cut volume and keep a little intensity.",
				"Take carbohydrate every forty minutes on the long run and practise it before race day.",
			},
			callbacks: []string{
				"back to the marathon training, how much slower should the long run be",
				"about my marathon again, how long did you say the taper should be",
			},
		},
		{
			name: "kubernetes",
			user: []string{
				"the kubernetes pods keep getting oomkilled in production",
				"memory limits are set to two gigabytes on the api deployment",
				"the node shows memory pressure in kubectl describe",
				"should I raise the limit or fix the leak in the api service",
				"the horizontal pod autoscaler is not scaling the deployment",
				"we rolled back the deployment and the oomkills stopped",
			},
			agent: []string{
				"An OOMKilled pod at exactly its limit points to a leak or an undersized limit; look at the heap profile.",
				"Raising the limit buys time, but the rollback fixing it means the new release leaks memory.",
				"The autoscaler scales on CPU by default, so memory pressure alone will not trigger it.",
				"Set requests close to real usage so the scheduler does not overpack the node.",
			},
			callbacks: []string{
				"back to the kubernetes oomkilled problem, why was the autoscaler not helping",
				"about those kubernetes pods again, what did the rollback tell us",
			},
		},
		{
			name: "taxes",
			user: []string{
				"I need to file my tax return and I have freelance income this year",
				"can I deduct the home office if I rent the apartment",
				"I have receipts for a laptop and a monitor",
				"when is the deadline for quarterly estimated tax payments",
				"my accountant asked for a mileage log",
				"should the laptop be deducted at once or depreciated",
			},
			agent: []string{
				"A home office is deductible when the space is used regularly and only for work, renting or owning.",
				"Quarterly estimated payments are due in April, June, September and January.",
				"A laptop under the threshold can usually be deducted in the year you bought it.",
				"Keep a mileage log with date, destination and purpose; reconstructing it later is painful.",
			},
			callbacks: []string{
				"back to the tax return, can I deduct the home office while renting",
				"about my taxes again, when are the quarterly payments due",
			},
		},
		{
			name: "guitar",
			user: []string{
				"I am learning guitar and the barre chords hurt my hand",
				"the f major barre chord always buzzes on the b string",
				"how long should I practise guitar each day",
				"should I learn scales or songs first",
				"my guitar action feels high near the twelfth fret",
				"which strings are easier for a beginner",
			},
			agent: []string{
				"A buzzing barre chord usually means the index finger is too far from the fret; roll it onto its edge.",
				"Twenty focused minutes of guitar a day beats one long session a week.",
				"High action near the twelfth fret makes barre chords much harder; a setup is worth it.",
				"Lighter gauge strings are kinder to a beginner's fingers.",
			},
			callbacks: []string{
				"back to the guitar, why does my barre chord buzz",
				"about guitar practice again, how many minutes a day did you suggest",
			},
		},
		{
			name: "garden",
			user: []string{
				"the tomato plants in my garden have yellow leaves at the bottom",
				"I water the tomatoes every evening",
				"the soil in the raised bed stays wet for days",
				"should I prune the lower leaves off the tomato plants",
				"something is eating holes in the basil",
				"when should I add compost to the raised bed",
			},
			agent: []string{
				"Yellow lower leaves on tomatoes with wet soil point to overwatering rather than a deficiency.",
				"Water tomatoes deeply twice a week in the morning instead of a little every evening.",
				"Prune the leaves that touch the soil; it improves airflow and slows blight.",
				"Holes in basil are usually slugs; check after dark with a torch.",
			},
			callbacks: []string{
				"back to the garden, why are the tomato leaves turning yellow",
				"about the tomatoes again, how often did you say to water them",
			},
		},
	}
}

// generate writes n synthetic transcripts into dir. Each is a run of
// conversations about different topics, hours or days apart, some of which
// open by calling back to an earlier one.
func generate(dir string, n int, seed uint64) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	all := topics()
	for i := range n {
		rng := rand.New(rand.NewPCG(seed, uint64(i))) //nolint:gosec // fixtures must be reproducible, not unpredictable
		msgs := transcript(rng, all, 14+rng.IntN(8))
		path := filepath.Join(dir, fmt.Sprintf("contact-%02d.jsonl", i+1))
		if err := writeTranscript(path, msgs); err != nil {
			return err
		}
	}
	return nil
}

func transcript(rng *rand.Rand, all []topic, sessions int) []message {
	var now int64
	out := make([]message, 0, sessions*8)
	visited := make([]int, 0, sessions) // topics already discussed, by index
	for s := range sessions {
		// After a few sessions, a third of them return to an old topic.
		ti := rng.IntN(len(all))
		callback := s >= 3 && len(visited) > 0 && rng.IntN(3) == 0
		if callback {
			ti = visited[rng.IntN(len(visited))]
		}
		tp := all[ti]
		visited = append(visited, ti)

		user := rng.Perm(len(tp.user))
		agent := rng.Perm(len(tp.agent))
		exchanges := 2 + rng.IntN(3)
		for e := range exchanges {
			text := tp.user[user[e%len(user)]]
			isCallback := callback && e == 0
			if isCallback {
				text = tp.callbacks[rng.IntN(len(tp.callbacks))]
			}
			out = append(out, message{T: now, Role: "user", Text: text, Topic: tp.name, Callback: isCallback})
			now += 4 + rng.Int64N(20)
			out = append(out, message{T: now, Role: "agent", Text: tp.agent[agent[e%len(agent)]], Topic: tp.name})
			now += 20 + rng.Int64N(160)
		}
		// Between conversations: an hour to four days.
		now += 3600 + rng.Int64N(4*24*3600)
	}
	return out
}

func writeTranscript(path string, msgs []message) (err error) {
	f, err := os.Create(path) //nolint:gosec // the path comes from the operator's flag
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer func() {
		if cerr := f.Close(); err == nil && cerr != nil {
			err = fmt.Errorf("close %s: %w", path, cerr)
		}
	}()
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	for _, m := range msgs {
		if err := enc.Encode(m); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func readTranscript(path string) ([]message, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // the path comes from the operator's flag
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var out []message
	for i, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var m message
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			return nil, fmt.Errorf("%s line %d: %w", path, i+1, err)
		}
		out = append(out, m)
	}
	return out, nil
}
