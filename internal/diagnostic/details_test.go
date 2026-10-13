package diagnostic_test

import (
	"strings"
	"testing"

	"github.com/quinnovator/contactd/internal/diagnostic"
)

func TestDetailOwnershipEvictionAndProgress(t *testing.T) {
	t.Parallel()
	b := diagnostic.New(1)
	s := b.Begin(diagnostic.Snapshot{Blocks: []diagnostic.Block{{ID: "a"}}})
	d := diagnostic.Details{Wire: "exact request", Blocks: []diagnostic.Content{{ID: "a", Text: strings.Repeat("x", 3<<20)}}}
	b.StoreDetails(s.Run, s.Call, d)
	_, got, ok := b.Details(s.Run, s.Call)
	if !ok || !got.Truncated || len(got.Blocks[0].Text) > 4096 {
		t.Fatal("oversized capture not bounded")
	}
	if len(d.Blocks[0].Text) != 3<<20 {
		t.Fatal("truncation mutated caller")
	}
	got.Blocks[0].Text = "mutation"
	_, again, _ := b.Details(s.Run, s.Call)
	if again.Blocks[0].Text == "mutation" {
		t.Fatal("details share mutable state")
	}
	s.OutputBytes = 123
	b.Update(s)
	h, _, stop := b.Subscribe()
	defer stop()
	if h[0].Done || h[0].OutputBytes != 123 {
		t.Fatal("progress completed the call")
	}
	b.Finish(s)
	s.OutputBytes = 999
	b.Update(s)
	h, _, stop2 := b.Subscribe()
	defer stop2()
	if h[0].OutputBytes != 123 {
		t.Fatal("late progress replaced completion")
	}
	b.Begin(diagnostic.Snapshot{})
	if _, _, ok := b.Details(s.Run, s.Call); ok {
		t.Fatal("evicted metadata retained prompt")
	}
}

func TestDetailByteBudget(t *testing.T) {
	t.Parallel()
	b := diagnostic.New(40)
	var first, last diagnostic.Snapshot
	for i := range 35 {
		s := b.Begin(diagnostic.Snapshot{})
		if i == 0 {
			first = s
		}
		last = s
		b.StoreDetails(s.Run, s.Call, diagnostic.Details{Wire: strings.Repeat("x", 1<<20)})
	}
	if _, _, ok := b.Details(first.Run, first.Call); ok {
		t.Fatal("byte budget did not evict oldest contents")
	}
	if _, _, ok := b.Details(last.Run, last.Call); !ok {
		t.Fatal("byte budget discarded newest capture")
	}
}
