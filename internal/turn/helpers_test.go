package turn_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/llm"
	fakellm "github.com/quinnovator/contactd/internal/llm/fake"
	"github.com/quinnovator/contactd/internal/turn"
)

type helperFake struct{}

func (helperFake) Start(context.Context, core.ContactID, turn.HelperStart) (turn.HelperInfo, error) {
	return turn.HelperInfo{Handle: 7, State: "TASK_STATE_WORKING"}, nil
}

func (helperFake) Message(context.Context, core.ContactID, uint64, string) (turn.HelperInfo, error) {
	return turn.HelperInfo{Handle: 7}, nil
}

func (helperFake) Stop(context.Context, core.ContactID, uint64) (turn.HelperInfo, error) {
	return turn.HelperInfo{Handle: 7}, nil
}

func (helperFake) List(context.Context, core.ContactID) ([]turn.HelperInfo, error) { return nil, nil }

type pageFake struct{}

func (pageFake) Fetch(context.Context, string) (turn.Page, error) {
	return turn.Page{URL: "https://example.com", Status: 200, Text: strings.Repeat("visible ", 200)}, nil
}

func TestHelperToolDefinitions(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		agent core.AgentConfig
	}{{"requester", core.AgentConfig{Helpers: true}}, {"helper", core.AgentConfig{Helper: true}}, {"both", core.AgentConfig{Helpers: true, Helper: true}}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := turn.HelperTools(&tc.agent, &turn.Deps{Helpers: helperFake{}, Fetch: pageFake{}})
			raw, err := json.MarshalIndent(a.Tools, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join("testdata", tc.name+"-tools.golden.json")
			if os.Getenv("UPDATE_HELPER_GOLDENS") == "1" {
				if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(want) != string(raw)+"\n" {
				t.Fatalf("tool definition bytes changed: %s", raw)
			}
			again := turn.HelperTools(a, &turn.Deps{Helpers: helperFake{}, Fetch: pageFake{}})
			if len(again.Tools) != len(a.Tools) {
				t.Fatal("tools duplicated")
			}
		})
	}
	sub := core.AgentConfig{Helpers: true, Parent: 1}
	if parent, child := turn.HelperTools(&core.AgentConfig{Helpers: true}, &turn.Deps{Helpers: helperFake{}}), turn.HelperTools(&sub, &turn.Deps{Helpers: helperFake{}}); len(parent.Tools) != len(child.Tools) {
		t.Fatal("a sub-agent's tool list must match its parent's")
	}
	a := &core.AgentConfig{System: "existing"}
	if turn.HelperTools(a, &turn.Deps{}) != a {
		t.Fatal("ordinary contact changed")
	}
}

func TestHelperToolValidation(t *testing.T) {
	t.Parallel()
	d := &turn.Deps{Helpers: helperFake{}, Fetch: pageFake{}, MaxToolOutput: 80}
	for _, tc := range []struct {
		name, args string
		agent      core.AgentConfig
		bad        bool
	}{
		{turn.StartHelperTool, `{"title":"watch","brief":"a page","every":"oops"}`, core.AgentConfig{Helpers: true}, true},
		{turn.StartHelperTool, `{"title":"watch"}`, core.AgentConfig{Helpers: true}, true},
		{turn.StartHelperTool, `{"title":"watch","brief":"a page"}`, core.AgentConfig{Helper: true, Helpers: true}, true},
		{turn.StartHelperTool, `{"title":"watch","brief":"a page"}`, core.AgentConfig{Helpers: true}, false},
		{turn.StartHelperTool, `{"title":"watch","brief":"a page"}`, core.AgentConfig{Helpers: true, Parent: 1}, true},
		{turn.ListHelpersTool, `{}`, core.AgentConfig{Helpers: true, Parent: 1}, true},
		{turn.StayQuietTool, `{}`, core.AgentConfig{Helpers: true, Parent: 1}, true},
		{turn.FetchURLTool, `{"url":"https://example.com"}`, core.AgentConfig{}, true},
		{turn.FetchURLTool, `{"url":"https://example.com"}`, core.AgentConfig{Helper: true}, false},
		{turn.AskRequesterTool, `{"question":"which?"}`, core.AgentConfig{}, true},
		{turn.FinishTool, `{"summary":"done"}`, core.AgentConfig{}, true},
		{turn.StayQuietTool, `{}`, core.AgentConfig{}, true},
	} {
		r := turn.HelperTool(t.Context(), d, turn.Input{Agent: &tc.agent}, core.ToolCall{ID: "c", Name: tc.name, Args: json.RawMessage(tc.args)})
		if r.IsError != tc.bad {
			t.Fatalf("%s %s: %+v", tc.name, tc.args, r)
		}
		if tc.name == turn.FetchURLTool && !tc.bad && len(r.Output) > 120 {
			t.Fatalf("unbounded page: %d", len(r.Output))
		}
	}
}

func TestTerminalHelperToolProcessesSiblings(t *testing.T) {
	t.Parallel()
	for _, name := range []string{turn.StayQuietTool, turn.AskRequesterTool, turn.FinishTool} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := &sink{}
			in := input(s, &leaser{}, "check")
			in.Agent.Helper = true
			args := `{"reason":"same","question":"which?","summary":"done"}`
			d := deps(fakellm.New(fakellm.Options{Caps: caps(func(c *llm.Capabilities) { c.SingleToolCall = false }), Rules: []fakellm.Rule{{Match: regexp.MustCompile("check"), Tool: name, Code: args, Calls: 2}}}))
			res, err := turn.Run(t.Context(), in, d)
			if err != nil || res.Final != "" || res.End.Tool != name {
				t.Fatalf("ending: %+v %v", res, err)
			}
			if len(s.results) != 2 {
				t.Fatalf("sibling results: %+v", s.results)
			}
		})
	}
}
