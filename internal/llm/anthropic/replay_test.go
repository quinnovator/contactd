package anthropic

import (
	"bytes"
	"slices"
	"testing"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/plan"
)

// wantReplay is the assistant turn of thinking_tool.sse as it has to go back:
// both thinking blocks with their signatures (the first with the empty text
// the default display gives), the text, the block that arrived whole, and
// the tool call.
const wantReplay = `[` +
	`{"type":"thinking","thinking":"","signature":"EqQBCgIYAhIM1gbcDa9GJwZA2b3hGgxBdjrkzLoky3dl1pkiMOYds+/=="},` +
	`{"type":"text","text":"Let me check <that>."},` +
	`{"type":"thinking","thinking":"Multiplying 7 by 8 with a <script> & \"quotes\".","signature":"ErUBCkYIBxgCIkD9second"},` +
	`{"type":"redacted_thinking","data":"EmwKAhgBEgy3va3pzix/LafPsn4aDFIT2Xlxh0L5L8rLVyIw"},` +
	`{"type":"tool_use","id":"toolu_01B","name":"python","input":{"code":"print(7 * 8)  # <b>"}}` +
	`]`

// streamToolCall runs one fixture through the adapter and returns the call
// it stopped on, with Text filled in the way the turn loop does.
func streamToolCall(t *testing.T, file string) core.ToolCall {
	t.Helper()
	api := newFakeAPI(t, step{file: file})
	s, err := newClient(t, api.url).Stream(t.Context(), fullPlan(t))
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	collect(t, s)
	stop, err := s.Stop()
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if stop.ToolCall == nil {
		t.Fatal("no tool call")
	}
	call := *stop.ToolCall
	call.Text = stop.Text
	return call
}

// openMessages is a request body without the brackets that close the
// messages array and the request, so a later body can be checked against it.
func openMessages(t *testing.T, raw []byte) []byte {
	t.Helper()
	open, ok := bytes.CutSuffix(raw, []byte(`]}`))
	if !ok || !bytes.Contains(open, []byte(`"messages":[`)) {
		t.Fatalf("request does not end with its messages: %s", raw)
	}
	return open
}

func TestReplayCapturesAssistantTurn(t *testing.T) {
	t.Parallel()
	call := streamToolCall(t, "thinking_tool.sse")
	if call.ID != "toolu_01B" || call.Name != "python" || call.Code != "print(7 * 8)  # <b>" {
		t.Errorf("tool call = %+v", call)
	}
	if call.Text != "Let me check <that>." {
		t.Errorf("text = %q", call.Text)
	}
	if got := string(call.Replay); got != wantReplay {
		t.Errorf("replay differs\n got: %s\nwant: %s", got, wantReplay)
	}
}

func TestReplayRoundTrip(t *testing.T) {
	t.Parallel()
	c := newClient(t, "")
	p := buildPlan(t, fullOpts(t))
	step1 := body(t, c, p)

	call := streamToolCall(t, "thinking_tool.sse")
	p.AppendTail(call, core.ToolResult{ID: call.ID, Output: "56\n"})
	step2 := body(t, c, p)

	// The blocks go back byte for byte, as the whole content of the turn:
	// not rebuilt from Text and Code, and with no cache_control added.
	turn := `{"role":"assistant","content":` + wantReplay + `},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01B","content":"56\n"}]}]}`
	want := append(openMessages(t, step1), ","+turn...)
	if !bytes.Equal(step2, want) {
		t.Errorf("step 2 request\n got: %s\nwant: %s", step2, want)
	}

	// A second tool step: everything sent so far is again an exact prefix.
	next := streamToolCall(t, "tool.sse")
	p.AppendTail(next, core.ToolResult{ID: next.ID, Output: "42\n<done>\n", IsError: true})
	step3 := body(t, c, p)
	if !bytes.HasPrefix(step3, openMessages(t, step2)) {
		t.Errorf("step 3 rewrote what step 2 sent\nstep 2: %s\nstep 3: %s", step2, step3)
	}
	if n := bytes.Count(step3, []byte(wantReplay)); n != 1 {
		t.Errorf("step 3 carries the first turn's blocks %d times, want once", n)
	}

	// The breakpoints are where they were before the tool steps.
	before, after := decode(t, step1), decode(t, step3)
	if got, want := breakpoints(&after), breakpoints(&before); len(got) != len(want) || len(got) != 4 {
		t.Errorf("breakpoints = %v, want the four of step 1 %v", got, want)
	}
	if bytes.Contains(call.Replay, []byte("cache_control")) {
		t.Errorf("replay carries cache_control: %s", call.Replay)
	}
}

func TestReplayIsOnlyForTheCurrentTurn(t *testing.T) {
	t.Parallel()
	c := newClient(t, "")

	t.Run("history from the log is rebuilt from text and code", func(t *testing.T) {
		t.Parallel()
		// Replay is never logged, so a tool call read back in a later turn
		// has none and renders as before.
		req := decode(t, body(t, c, buildPlan(t, fullOpts(t))))
		got, err := encode(req.Messages[1])
		if err != nil {
			t.Fatal(err)
		}
		const want = `{"role":"assistant","content":[{"type":"text","text":"Let me compute that."},` +
			`{"type":"tool_use","id":"toolu_01","name":"python","input":{"code":"print(6 * 7)"}}]}`
		if string(got) != want {
			t.Errorf("tail tool call\n got: %s\nwant: %s", got, want)
		}
	})

	t.Run("an in-turn call without replay is rebuilt too", func(t *testing.T) {
		t.Parallel()
		for name, replay := range map[string][]byte{
			"none":         nil,
			"not an array": []byte(`{"type":"text","text":"x"}`),
			"empty array":  []byte(`[]`),
			"untyped":      []byte(`[{"text":"x"}]`),
			"truncated":    []byte(`[{"type":"thinking","thinking":""`),
		} {
			p := buildPlan(t, fullOpts(t))
			p.AppendTail(
				core.ToolCall{ID: "toolu_02", Name: "python", Code: "print(7 * 8)", Text: "On it.", Replay: replay},
				core.ToolResult{ID: "toolu_02", Output: "56\n"},
			)
			const want = `{"role":"assistant","content":[{"type":"text","text":"On it."},` +
				`{"type":"tool_use","id":"toolu_02","name":"python","input":{"code":"print(7 * 8)"}}]}`
			if raw := body(t, c, p); !bytes.Contains(raw, []byte(want)) {
				t.Errorf("%s: call was not rebuilt from text and code: %s", name, raw)
			}
		}
	})

	t.Run("a replayed turn in the tail takes no breakpoint", func(t *testing.T) {
		t.Parallel()
		// It cannot happen through the log, but if a tail ended on a replayed
		// turn the signed blocks must still go out untouched, even though
		// that costs the tail its breakpoint.
		p := buildPlan(t, fullOpts(t))
		tail := &p.Tiers[plan.TierTail]
		tail.Msgs = append(slices.Clip(tail.Msgs),
			core.Message{Role: core.RoleUser, Text: "Go on."},
			core.Message{
				Role:     core.RoleAssistant,
				ToolCall: &core.ToolCall{ID: "toolu_01B", Name: "python", Replay: []byte(wantReplay)},
			},
		)
		raw := body(t, c, p)
		if !bytes.Contains(raw, []byte(`{"role":"assistant","content":`+wantReplay+`}`)) {
			t.Errorf("replayed blocks were altered: %s", raw)
		}
		req := decode(t, raw)
		if got, want := breakpoints(&req), []string{"system[0]", "system[1]", "system[2]"}; !slices.Equal(got, want) {
			t.Errorf("breakpoints = %v, want %v", got, want)
		}
	})
}

func TestReplayAfterFallback(t *testing.T) {
	t.Parallel()
	// A classifier declined mid-message and another model finished it. The
	// declined model's reasoning and tool call must not go back; its text,
	// the fallback marker and everything after the boundary do.
	call := streamToolCall(t, "fallback_tool.sse")
	if call.ID != "toolu_kept" || call.Code != "y" || call.Text != "Before. After." {
		t.Errorf("tool call = %+v", call)
	}
	const want = `[` +
		`{"type":"text","text":"Before. "},` +
		`{"type":"fallback","from":{"model":"claude-opus-5-5"},"to":{"model":"claude-opus-5"}},` +
		`{"type":"thinking","thinking":"","signature":"fallbackModelSig"},` +
		`{"type":"text","text":"After."},` +
		`{"type":"tool_use","id":"toolu_kept","name":"python","input":{"code":"y"}}` +
		`]`
	if got := string(call.Replay); got != want {
		t.Errorf("replay differs\n got: %s\nwant: %s", got, want)
	}
}

func TestReplayStopsAtTheFirstToolCall(t *testing.T) {
	t.Parallel()
	// The second call is never run, so it could not be answered: sending it
	// back would leave a tool_use without a result.
	call := streamToolCall(t, "two_tools.sse")
	const want = `[{"type":"tool_use","id":"toolu_first","name":"python","input":{"code":"a = 1"}}]`
	if got := string(call.Replay); got != want {
		t.Errorf("replay = %s, want %s", got, want)
	}
}
