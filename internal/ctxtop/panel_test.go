package ctxtop

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/diagnostic"
)

func panelFixture() *model {
	m := viewer()
	m.width, m.height = 130, 30
	s := sample()
	s.Call = 2
	s.Blocks = []diagnostic.Block{
		{ID: "sys", Tier: 0, Kind: "system", Tokens: 1300, Hash: "a", Preview: "You are Ada, a thoughtful colleague.", Fate: "retained until configuration changes"},
		{ID: "profile", Tier: 1, Offset: 1300, Kind: "profile", Tokens: 100, Hash: "b", Preview: "Name: Jack"},
		{ID: "recall", Tier: 2, Offset: 1400, Kind: "recall", Tokens: 400, Hash: "c", Preview: "Earlier conversation: baseline evaluation"},
		{ID: "ckpt", Tier: 3, Offset: 1800, Kind: "checkpoint", Tokens: 200, Seq: 7, From: 1, To: 6, Hash: "d", Preview: "Jack ran eval.py with twelve shards."},
		{ID: "result", Tier: 3, Offset: 2000, Kind: "tool_result", Tokens: 14000, Seq: 8, Episode: 37, Hash: "e", Preview: "step 412 loss 1.88", Fate: "selected for the next fold"},
		{ID: "live", Tier: 4, Offset: 16000, Kind: "user", Tokens: 1200, Seq: 9, Episode: 37, Hash: "f", Preview: "Compare the result against the baseline."},
	}
	previous := s
	previous.Call = 1
	previous.Blocks = append([]diagnostic.Block(nil), s.Blocks...)
	previous.Blocks[5].Hash = "older"
	m.accept(previous)
	m.accept(s)
	m.row[0] = 8
	d := diagnostic.Details{Wire: `{"model":"deepseek-flash","messages":[{"role":"user","content":"Compare the result"}]}`, Episodes: []diagnostic.Episode{{ID: 36, Start: 1, End: 6}, {ID: 37, Start: 8, End: 9}}, Recall: &diagnostic.Recall{Query: "compare against baseline", Threshold: .5, Margin: .08, Warm: []uint32{36}, Chosen: []uint32{36}, Status: "reranked", Candidates: []diagnostic.Candidate{{Episode: 36, Cosine: .71, Rerank: .82, Score: .9, Reason: "selected", Passage: "Jack evaluated the baseline."}, {Episode: 12, Cosine: .69, Rerank: .79, Score: .85, Reason: "warm set retained by hysteresis", Passage: "First evaluation harness."}}}}
	for _, b := range s.Blocks {
		d.Blocks = append(d.Blocks, diagnostic.Content{ID: b.ID, Text: b.Preview, Canonical: `{"text":"` + b.Preview + `"}`})
	}
	m.captures[2] = d
	old := d
	old.Wire = `{"model":"deepseek-flash","messages":[{"role":"user","content":"Run the evaluation"}]}`
	old.Blocks = append([]diagnostic.Content(nil), d.Blocks...)
	old.Blocks[5].Canonical = `{"text":"Run the evaluation"}`
	m.captures[1] = old
	ev := []core.Event{{Seq: 1, Kind: core.KindUserMsg, Body: []byte(`{"text":"Run eval.py with 12 shards and seed 42"}`)}, {Seq: 2, Kind: core.KindAgentMsg, Body: []byte(`{"text":"Loss is 1.88"}`)}}
	m.events[eventKey(s, 1, 6)] = ev
	return m
}
func press(m *model, key string) { m.Update(tea.KeyPressMsg{Code: []rune(key)[0], Text: key}) }

func TestPanelNavigationPreservesSourceSelection(t *testing.T) {
	t.Parallel()
	m := panelFixture()
	m.rememberSelection()
	if m.selectionSeq != 8 {
		t.Fatal("block selection missing source")
	}
	press(m, "g")
	if m.screen != 1 || m.row[1] != 1 {
		t.Fatal("g did not jump to source episode")
	}
	press(m, "1")
	rows := m.items()
	if rows[m.row[0]].block.Seq != 8 {
		t.Fatal("source selection lost on return")
	}
	press(m, "s")
	rows = m.items()
	if rows[0].block.Tokens != 14000 {
		t.Fatal("size sort did not put heavy output first")
	}
	press(m, "/")
	press(m, "baseline")
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(m.items()) != 2 {
		t.Fatalf("search matched %d rows", len(m.items()))
	}
}

func TestAllPanelsFitAndSanitize(t *testing.T) {
	t.Parallel()
	m := panelFixture()
	for screen := range 6 {
		m.screen = screen
		for _, w := range []int{1, 20, 65, 100, 130} {
			for _, h := range []int{1, 10, 30} {
				m.width, m.height = w, h
				lines := strings.Split(ansi.Strip(m.View().Content), "\n")
				if len(lines) > h {
					t.Fatalf("screen %d exceeds height %d", screen, h)
				}
				for _, line := range lines {
					if ansi.StringWidth(line) > w {
						t.Fatalf("screen %d exceeds width %d: %s", screen, w, line)
					}
				}
			}
		}
	}
}

func TestDiffBytesAndToolCause(t *testing.T) {
	t.Parallel()
	if firstDifference("abc", "abcd") != 3 || firstDifference("abcX", "abcY") != 3 {
		t.Fatal("byte diff wrong")
	}
	a := diagnostic.Snapshot{Turn: 8, Step: 1, Blocks: []diagnostic.Block{{Tier: 4, Hash: "1"}}}
	b := diagnostic.Snapshot{Turn: 8, Step: 2, Blocks: []diagnostic.Block{{Tier: 4, Hash: "2"}}}
	if got := diffCause(a, b); got != "tool step appended in the live tier" {
		t.Fatal(got)
	}
	if got := missingWords("seed 42 in data/run.json", "seed"); !strings.Contains(got, "!42!") || !strings.Contains(got, "!data/run.json!") {
		t.Fatal(got)
	}
}
