package anthropic

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestSummarize(t *testing.T) {
	t.Parallel()
	api := newFakeAPI(t,
		step{status: http.StatusInternalServerError, file: "error_529.json"},
		step{file: "summary.json"},
	)
	c := newClient(t, api.url)
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
	req := decode(t, reqs[1].body)
	if req.Model != "claude-haiku-5-5" || req.Stream || req.Fallbacks != "" || len(req.Tools) != 0 {
		t.Errorf("request = %+v, want a plain non-streaming call to the summary model", req)
	}
	if reqs[1].header.Get("Anthropic-Beta") != "" {
		t.Errorf("summary call sent a beta header")
	}
	if len(req.System) != 1 || !strings.Contains(req.System[0].Text, "no preamble") {
		t.Errorf("system = %+v", req.System)
	}
	if len(req.Messages) != 1 || req.Messages[0].Role != "user" ||
		req.Messages[0].Content[0].Text != "<excerpt>\n"+transcript+"\n</excerpt>" {
		t.Errorf("messages = %+v", req.Messages)
	}
}

func TestSummarizeErrors(t *testing.T) {
	t.Parallel()

	t.Run("empty transcript", func(t *testing.T) {
		t.Parallel()
		api := newFakeAPI(t, step{file: "summary.json"})
		if _, err := newClient(t, api.url).Summarize(t.Context(), "  \n"); err == nil {
			t.Error("Summarize accepted an empty transcript")
		}
		if n := len(api.seen()); n != 0 {
			t.Errorf("server saw %d requests, want 0", n)
		}
	})

	t.Run("api error", func(t *testing.T) {
		t.Parallel()
		api := newFakeAPI(t, step{status: http.StatusBadRequest, file: "error_400.json"})
		_, err := newClient(t, api.url).Summarize(t.Context(), "user: hi")
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
			t.Errorf("error = %v, want a 400 APIError", err)
		}
	})
}
