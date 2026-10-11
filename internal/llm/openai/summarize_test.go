package openai

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestSummarize(t *testing.T) {
	t.Parallel()
	api := newFakeAPI(t,
		step{status: http.StatusInternalServerError, file: "error_503.json"},
		step{file: "summary.json"},
	)
	c := newClient(t, Config{BaseURLs: []string{api.url}, SummaryModel: "gpt-small", PromptCacheKey: true})
	const transcript = "user: What is 6 times 7?\nagent: It is 42."

	got, err := c.Summarize(t.Context(), transcript)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if want := "Sam asked for the product of 6 and 7. The agent computed 42 with Python."; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}

	reqs := api.seen()
	if len(reqs) != 2 {
		t.Fatalf("server saw %d requests, want a retry after the 500", len(reqs))
	}
	if reqs[1].path != "/chat/completions" {
		t.Errorf("path = %q", reqs[1].path)
	}
	req := decode(t, reqs[1].body)
	if req.Model != "gpt-small" || req.Stream || req.StreamOptions != nil || len(req.Tools) != 0 ||
		req.PromptCacheKey != "" {
		t.Errorf("request = %+v, want a plain non-streaming call to the summary model", req)
	}
	if len(req.Messages) != 2 || req.Messages[0].Role != "system" || req.Messages[1].Role != "user" {
		t.Fatalf("messages = %+v", req.Messages)
	}
	if !strings.Contains(content(req.Messages[0]), "no preamble") {
		t.Errorf("system = %q", content(req.Messages[0]))
	}
	if got := content(req.Messages[1]); got != "<excerpt>\n"+transcript+"\n</excerpt>" {
		t.Errorf("user = %q", got)
	}
}

func TestSummarizeErrors(t *testing.T) {
	t.Parallel()

	t.Run("empty transcript", func(t *testing.T) {
		t.Parallel()
		api := newFakeAPI(t, step{file: "summary.json"})
		if _, err := clientFor(t, api).Summarize(t.Context(), "  \n"); err == nil {
			t.Error("Summarize accepted an empty transcript")
		}
		if n := len(api.seen()); n != 0 {
			t.Errorf("server saw %d requests, want 0", n)
		}
	})

	t.Run("api error", func(t *testing.T) {
		t.Parallel()
		api := newFakeAPI(t, step{status: http.StatusBadRequest, file: "error_400.json"})
		_, err := clientFor(t, api).Summarize(t.Context(), "user: hi")
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
			t.Errorf("error = %v, want a 400 APIError", err)
		}
	})

	t.Run("no model", func(t *testing.T) {
		t.Parallel()
		c, err := New(Config{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Summarize(t.Context(), "user: hi"); !errors.Is(err, ErrNoModel) {
			t.Errorf("error = %v, want ErrNoModel", err)
		}
	})

	t.Run("summary model falls back to the reply model", func(t *testing.T) {
		t.Parallel()
		api := newFakeAPI(t, step{file: "summary.json"})
		if _, err := clientFor(t, api).Summarize(t.Context(), "user: hi"); err != nil {
			t.Fatal(err)
		}
		if req := decode(t, api.seen()[0].body); req.Model != "gpt-test" {
			t.Errorf("model = %q, want gpt-test", req.Model)
		}
	})
}
