package deepseek

import (
	"context"
	"net/http"

	"github.com/quinnovator/contactd/internal/llm"
	"github.com/quinnovator/contactd/internal/llm/wire/chat"
	"github.com/quinnovator/contactd/internal/plan"
)

const (
	// WireChat selects DeepSeek's own Chat Completions endpoint.
	WireChat = "chat"

	chatBaseURL = "https://api.deepseek.com"
	chatPath    = "/chat/completions"
)

// chatCodec is the profile over DeepSeek's Chat Completions endpoint.
//
// This is the endpoint DeepSeek documents most fully: the cache fields
// (prompt_cache_hit_tokens) and the replay rule (reasoning_content must come
// back on every later request when tools are set, or HTTP 400) are both
// stated for it, where on the Messages endpoint they are assumed. The
// Messages endpoint stays the default because it is the one DeepSeek's own
// harness uses; the probe is what should settle the choice.
func chatCodec(cfg Config, hc *http.Client) (codec, error) {
	base, err := baseURL(cfg.BaseURL, chatBaseURL)
	if err != nil {
		return codec{}, err
	}
	header := http.Header{}
	header.Set("Authorization", "Bearer "+cfg.APIKey)
	d := &chat.Dialect{
		URL:    base + chatPath,
		Header: header,
		// thinking: {"type": "enabled" | "disabled"} plus reasoning_effort.
		Effort:           chat.EffortThinkingToggle,
		Reasoning:        chat.ReasoningContent,
		DefaultMaxOutput: defaultMaxOutput,
	}
	return codec{
		wire: llm.WireChat,
		caps: chatCaps,
		prepare: func(r llm.Route, p *plan.PromptPlan, o llm.Options) (*llm.Frozen, error) {
			return chat.Prepare(d, r, p, o)
		},
		send: func(ctx context.Context, f *llm.Frozen) (llm.Stream, error) {
			return chat.Send(ctx, hc, f)
		},
	}, nil
}

// chatCaps declares the same model behaviour as the Messages route. What
// differs between the two endpoints is the wire, not the model: the cache is
// automatic by prefix, reasoning is replayed on every turn, and nothing
// holds the model to one tool call per step (the docs list no
// parallel_tool_calls switch for this endpoint, so none is declared).
func chatCaps() llm.Capabilities {
	c := messagesCaps()
	// tool_choice is documented for this endpoint. The codec does not send
	// it either way; the declaration is for the probe to hold up.
	c.ToolChoice = true
	return c
}
