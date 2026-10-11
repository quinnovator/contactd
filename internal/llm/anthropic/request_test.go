package anthropic

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/plan"
)

func TestRequestGolden(t *testing.T) {
	t.Parallel()
	c := newClient(t, "")
	golden(t, "request_full.golden.json", body(t, c, fullPlan(t)))
}

func TestRequestIsByteStable(t *testing.T) {
	t.Parallel()
	c := newClient(t, "")
	p := fullPlan(t)
	first := body(t, c, p)
	if again := body(t, c, p); !bytes.Equal(first, again) {
		t.Errorf("same plan marshalled twice differs:\n%s\n%s", first, again)
	}
	// A plan rebuilt from the same inputs, sent by another client, must match too.
	if rebuilt := body(t, newClient(t, ""), fullPlan(t)); !bytes.Equal(first, rebuilt) {
		t.Errorf("rebuilt plan differs:\n%s\n%s", first, rebuilt)
	}
}

// breakpoints lists where a request carries cache_control, in render order.
func breakpoints(req *request) []string {
	var at []string
	for i := range req.Tools {
		if req.Tools[i].CacheControl != nil {
			at = append(at, fmt.Sprintf("tools[%d]", i))
		}
	}
	for i := range req.System {
		if req.System[i].CacheControl != nil {
			at = append(at, fmt.Sprintf("system[%d]", i))
		}
	}
	for i := range req.Messages {
		for j := range req.Messages[i].Content {
			if req.Messages[i].Content[j].CacheControl != nil {
				at = append(at, fmt.Sprintf("messages[%d].content[%d]", i, j))
			}
		}
	}
	return at
}

func TestBreakpoints(t *testing.T) {
	t.Parallel()
	firstTurn := func(t *testing.T) []core.Event {
		t.Helper()
		return []core.Event{event(t, 1, core.KindUserMsg, core.MsgBody{Text: "Hi"})}
	}
	cases := []struct {
		name string
		edit func(t *testing.T, o *planOpts)
		want []string
	}{
		{
			name: "all tiers",
			edit: func(*testing.T, *planOpts) {},
			// messages[3] is the reply that ends the tail; messages[4] opens tier 4.
			want: []string{"system[0]", "system[1]", "system[2]", "messages[3].content[0]"},
		},
		{
			name: "no profile",
			edit: func(_ *testing.T, o *planOpts) { o.profile = "" },
			want: []string{"system[0]", "system[1]", "messages[3].content[0]"},
		},
		{
			name: "no recall",
			edit: func(_ *testing.T, o *planOpts) { o.recall = nil },
			want: []string{"system[0]", "system[1]", "messages[3].content[0]"},
		},
		{
			name: "no profile or recall",
			edit: func(_ *testing.T, o *planOpts) { o.profile, o.recall = "", nil },
			want: []string{"system[0]", "messages[3].content[0]"},
		},
		{
			name: "empty tail",
			edit: func(t *testing.T, o *planOpts) { t.Helper(); o.events = firstTurn(t) },
			want: []string{"system[0]", "system[1]", "system[2]"},
		},
		{
			name: "no system prompt puts the static breakpoint on the last tool",
			edit: func(_ *testing.T, o *planOpts) {
				o.system = ""
				o.tools = []core.ToolDef{pythonTool(), {Name: "shell", Description: "Run sh."}}
			},
			want: []string{"tools[1]", "system[0]", "system[1]", "messages[3].content[0]"},
		},
		{
			name: "nothing static",
			edit: func(t *testing.T, o *planOpts) {
				t.Helper()
				*o = planOpts{events: firstTurn(t)}
			},
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			o := fullOpts(t)
			tc.edit(t, &o)
			p := buildPlan(t, o)
			c := newClient(t, "")
			req := decode(t, body(t, c, p))
			if got := breakpoints(&req); !slices.Equal(got, tc.want) {
				t.Errorf("breakpoints = %v, want %v", got, tc.want)
			}

			// Tool steps inside the turn land in tier 4 and must not move or
			// add a breakpoint.
			p.AppendTail(
				core.ToolCall{ID: "toolu_09", Name: "python", Code: "1"},
				core.ToolResult{ID: "toolu_09", Output: "1"},
			)
			req = decode(t, body(t, c, p))
			if got := breakpoints(&req); !slices.Equal(got, tc.want) {
				t.Errorf("after a tool step: breakpoints = %v, want %v", got, tc.want)
			}
			if n := len(breakpoints(&req)); n > 4 {
				t.Errorf("%d breakpoints, the API allows 4", n)
			}
		})
	}
}

func TestTierLayout(t *testing.T) {
	t.Parallel()
	req := decode(t, body(t, newClient(t, ""), fullPlan(t)))

	if req.Model != defaultModel || req.MaxTokens != defaultMaxTokens || !req.Stream {
		t.Errorf("model %q, max_tokens %d, stream %v", req.Model, req.MaxTokens, req.Stream)
	}
	if req.Fallbacks != "default" {
		t.Errorf("fallbacks = %q, want default", req.Fallbacks)
	}
	if req.ToolChoice == nil || req.ToolChoice.Type != "auto" || !req.ToolChoice.DisableParallelToolUse {
		t.Errorf("tool_choice = %+v, want auto without parallel calls", req.ToolChoice)
	}
	if len(req.Tools) != 1 || req.Tools[0].Name != "python" || !req.Tools[0].EagerInputStreaming {
		t.Fatalf("tools = %+v", req.Tools)
	}
	if got := req.Tools[0].InputSchema.Required; !slices.Equal(got, []string{"code"}) {
		t.Errorf("required = %v", got)
	}
	if len(req.System) != 3 || req.System[0].Text != "You are Ada, a careful assistant." {
		t.Fatalf("system = %+v", req.System)
	}

	type shape struct{ role, blocks string }
	got := make([]shape, 0, len(req.Messages))
	for _, m := range req.Messages {
		kinds := make([]string, len(m.Content))
		for i, b := range m.Content {
			kinds[i] = b.Type
		}
		got = append(got, shape{m.Role, strings.Join(kinds, ",")})
	}
	want := []shape{
		{"user", "text"},
		{"assistant", "text,tool_use"},
		{"user", "tool_result"},
		{"assistant", "text"},
		// The new message and the time note share one user turn. The in-turn
		// tool exchange comes after the note: it is appended, never inserted.
		{"user", "text,text"},
		{"assistant", "tool_use"},
		{"user", "tool_result"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("messages = %v, want %v", got, want)
	}
	use := req.Messages[1].Content[1]
	if use.ID != "toolu_01" || use.Name != "python" || use.Input == nil || use.Input.Code != "print(6 * 7)" {
		t.Errorf("tool_use = %+v", use)
	}
	res := req.Messages[2].Content[0]
	if res.ToolUseID != "toolu_01" || res.Content != "42\n" || res.IsError {
		t.Errorf("tool_result = %+v", res)
	}
}

func TestPlanOverridesModelAndMaxTokens(t *testing.T) {
	t.Parallel()
	p := fullPlan(t)
	p.Model, p.MaxTokens = "claude-haiku-5-5", 1234
	req := decode(t, body(t, newClient(t, ""), p))
	if req.Model != "claude-haiku-5-5" || req.MaxTokens != 1234 {
		t.Errorf("model %q, max_tokens %d", req.Model, req.MaxTokens)
	}
	if req.Fallbacks != "" {
		t.Errorf("fallbacks = %q for a model with no fallback", req.Fallbacks)
	}
}

func TestNoFallbackConfig(t *testing.T) {
	t.Parallel()
	c, err := New(Config{APIKey: "k", NoFallback: true})
	if err != nil {
		t.Fatal(err)
	}
	if req := decode(t, body(t, c, fullPlan(t))); req.Fallbacks != "" {
		t.Errorf("fallbacks = %q with NoFallback set", req.Fallbacks)
	}
}

func TestMergeSameRole(t *testing.T) {
	t.Parallel()

	t.Run("orphaned tool result and the next user message", func(t *testing.T) {
		t.Parallel()
		// The tool never answered and the contact wrote again: the planner
		// renders a placeholder result, then the message. Both are user
		// turns on the wire, and the result is where the tail ends.
		p := buildPlan(t, planOpts{
			system: "sys",
			tools:  []core.ToolDef{pythonTool()},
			events: []core.Event{
				event(t, 1, core.KindUserMsg, core.MsgBody{Text: "Run it"}),
				event(t, 2, core.KindToolCall, core.ToolCall{ID: "toolu_01", Name: "python", Code: "while True: pass"}),
				event(t, 3, core.KindUserMsg, core.MsgBody{Text: "Still there?"}),
			},
		})
		req := decode(t, body(t, newClient(t, ""), p))
		if len(req.Messages) != 3 {
			t.Fatalf("got %d messages, want 3: %+v", len(req.Messages), req.Messages)
		}
		last := req.Messages[2]
		if last.Role != "user" || len(last.Content) != 3 {
			t.Fatalf("last message = %+v", last)
		}
		if last.Content[0].Type != "tool_result" || !last.Content[0].IsError || last.Content[0].CacheControl == nil {
			t.Errorf("first block = %+v, want the error result carrying the tail breakpoint", last.Content[0])
		}
		if last.Content[1].Text != "Still there?" || last.Content[1].CacheControl != nil {
			t.Errorf("second block = %+v", last.Content[1])
		}
		if last.Content[2].Type != "text" || last.Content[2].CacheControl != nil {
			t.Errorf("third block = %+v, want the time note", last.Content[2])
		}
	})

	t.Run("consecutive user messages", func(t *testing.T) {
		t.Parallel()
		p := buildPlan(t, planOpts{events: []core.Event{
			event(t, 1, core.KindUserMsg, core.MsgBody{Text: "one"}),
			event(t, 2, core.KindUserMsg, core.MsgBody{Text: "two"}),
		}})
		req := decode(t, body(t, newClient(t, ""), p))
		if len(req.Messages) != 1 || len(req.Messages[0].Content) != 3 {
			t.Fatalf("messages = %+v, want one user turn with three text blocks", req.Messages)
		}
	})

	t.Run("results move ahead of text only within a tier", func(t *testing.T) {
		t.Parallel()
		res := func(id string) *core.Message {
			return &core.Message{Role: core.RoleTool, ToolResult: &core.ToolResult{ID: id}}
		}
		var b builder
		b.add(&core.Message{Role: core.RoleUser, Text: "tail text"})
		b.endTier()
		b.add(&core.Message{Role: core.RoleUser, Text: "live text"})
		b.add(res("a"))
		b.add(res("b"))
		if len(b.msgs) != 1 {
			t.Fatalf("got %d messages, want 1", len(b.msgs))
		}
		got := make([]string, 0, len(b.msgs[0].Content))
		for _, bl := range b.msgs[0].Content {
			got = append(got, bl.Type+":"+bl.Text+bl.ToolUseID)
		}
		want := []string{"text:tail text", "tool_result:a", "tool_result:b", "text:live text"}
		if !slices.Equal(got, want) {
			t.Errorf("blocks = %v, want %v", got, want)
		}
	})

	t.Run("empty text is dropped", func(t *testing.T) {
		t.Parallel()
		var b builder
		b.add(&core.Message{Role: core.RoleUser, Text: "a"})
		b.add(&core.Message{Role: core.RoleAssistant, Text: " \n"})
		b.add(&core.Message{Role: core.RoleUser, Text: "b"})
		if len(b.msgs) != 1 || len(b.msgs[0].Content) != 2 {
			t.Errorf("messages = %+v, want the two user texts merged", b.msgs)
		}
	})
}

func TestTailGrowthKeepsPrefix(t *testing.T) {
	t.Parallel()
	// The next turn's request must start with every byte the previous
	// breakpoints covered, apart from the tail marker moving forward.
	c := newClient(t, "")
	o := fullOpts(t)
	prev := buildPlan(t, o)

	o.events = append(o.events,
		event(t, 6, core.KindAgentMsg, core.MsgBody{Text: "56."}),
		event(t, 7, core.KindUserMsg, core.MsgBody{Text: "Thanks"}),
	)
	in := plan.Input{Contact: 42, Agent: &core.AgentConfig{System: o.system, Tools: o.tools}, Profile: o.profile, Tail: o.events}
	next, err := plan.Build(prev, in, o.recall, fixedNow+60_000, plan.DefaultParams())
	if err != nil {
		t.Fatal(err)
	}

	a, b := decode(t, body(t, c, prev)), decode(t, body(t, c, next))
	strip := func(req *request) {
		for i := range req.Messages {
			for j := range req.Messages[i].Content {
				req.Messages[i].Content[j].CacheControl = nil
			}
		}
	}
	strip(&a)
	strip(&b)
	// Tier 3 of the earlier plan is its first four messages.
	a.Messages, b.Messages = a.Messages[:4], b.Messages[:4]
	rawA, err := marshal(&a)
	if err != nil {
		t.Fatal(err)
	}
	rawB, err := marshal(&b)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(rawA, rawB) {
		t.Errorf("cached prefix changed between turns:\n%s\n%s", rawA, rawB)
	}
}

func TestEffort(t *testing.T) {
	t.Parallel()
	const field = `"output_config"`

	t.Run("absent by default", func(t *testing.T) {
		t.Parallel()
		if raw := body(t, newClient(t, ""), fullPlan(t)); bytes.Contains(raw, []byte(field)) {
			t.Errorf("request sets output_config with no effort configured: %s", raw)
		}
	})

	t.Run("sent when configured", func(t *testing.T) {
		t.Parallel()
		c, err := New(Config{APIKey: "k", Effort: "low"})
		if err != nil {
			t.Fatal(err)
		}
		raw := body(t, c, fullPlan(t))
		if !bytes.Contains(raw, []byte(`"output_config":{"effort":"low"},`)) {
			t.Errorf("request does not carry the effort: %s", raw)
		}
		// Nothing else moves: dropping the field gives the default request.
		without := bytes.Replace(raw, []byte(`"output_config":{"effort":"low"},`), nil, 1)
		if want := body(t, newClient(t, ""), fullPlan(t)); !bytes.Equal(without, want) {
			t.Errorf("effort changed more than its own field:\n%s\n%s", without, want)
		}
	})
}
