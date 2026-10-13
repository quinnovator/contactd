package plan

import (
	"testing"

	"github.com/quinnovator/contactd/internal/core"
)

func TestBlocksRetainOffsetsProvenanceAndPromptBytes(t *testing.T) {
	t.Parallel()
	log := new(logBuilder).user("old question").agent("old answer").call("tool-1", "print(42)").result("tool-1", "42").user("new question")
	pr := DefaultParams()
	p := mustBuild(t, nil, Input{Agent: testAgent(), Profile: "Jack", Tail: log.events}, []Recalled{{ID: 7, Text: "remember"}}, t0, pr)
	before := dump(p)
	blocks := p.Blocks(pr)
	sum := 0
	seen := map[string]bool{}
	for _, b := range blocks {
		if b.Offset != sum {
			t.Fatalf("offset %d != %d", b.Offset, sum)
		}
		sum += b.Tokens
		seen[b.Kind] = true
		if b.Kind == "tool_result" && b.Seq != 4 {
			t.Fatalf("tool provenance: %+v", b)
		}
		if b.Kind == "user" && b.Tier == 4 && b.Seq != 5 {
			t.Fatalf("live provenance: %+v", b)
		}
	}
	if sum != p.InputTokens() {
		t.Fatalf("blocks %d != plan %d", sum, p.InputTokens())
	}
	for _, kind := range []string{"system", "tools", "profile", "recall", "tool_call", "tool_result", "time"} {
		if !seen[kind] {
			t.Errorf("missing %s", kind)
		}
	}
	if dump(p) != before {
		t.Fatal("inspection changed prompt bytes or hashes")
	}
	p.AppendTail(core.ToolCall{ID: "next", Name: "python", Code: "1"}, core.ToolResult{ID: "next", Output: "1"})
	if got := p.Blocks(pr); got[len(got)-1].Kind != "tool_result" || got[len(got)-1].Seq != 0 {
		t.Fatal("new tool source must remain explicitly unknown")
	}
}

func TestCheckpointBlockCoversOriginalSpan(t *testing.T) {
	t.Parallel()
	log := new(logBuilder).user("old").agent("answer").add(core.KindCheckpoint, core.CheckpointBody{From: 1, To: 2, Text: "summary"}).user("new")
	p := mustBuild(t, nil, Input{Agent: testAgent(), Tail: log.events}, nil, t0, DefaultParams())
	for _, b := range p.Blocks(DefaultParams()) {
		if b.Kind == "checkpoint" {
			if b.Seq != 3 || b.From != 1 || b.To != 2 {
				t.Fatalf("wrong checkpoint source: %+v", b)
			}
			return
		}
	}
	t.Fatal("checkpoint block missing")
}
