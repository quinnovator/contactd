package chat

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/quinnovator/contactd/internal/llm"
	"github.com/quinnovator/contactd/internal/llm/wire"
)

// refine improves on the status-based class of a failed response where the
// body says more than the status does.
func refine(status int, body []byte) llm.ErrorClass {
	if status < 400 || status >= 500 {
		return llm.ErrUnknown
	}
	return classifyText(body)
}

// classifyText recognises the two failures this format reports in words: a
// prompt too long for the model, which arrives as a plain 400, and an
// account out of quota or credit, which OpenAI reports as a 429 that must
// not be retried.
func classifyText(b []byte) llm.ErrorClass {
	s := bytes.ToLower(b)
	switch {
	case bytes.Contains(s, []byte("context_length_exceeded")), bytes.Contains(s, []byte("maximum context length")):
		return llm.ErrContextOverflow
	case bytes.Contains(s, []byte("insufficient_quota")):
		return llm.ErrQuota
	default:
		return llm.ErrUnknown
	}
}

// errorEnvelope covers the two error shapes in use: OpenAI, OpenRouter and
// current vLLM nest the details under "error"; older self-hosted servers put
// them at the top level.
type errorEnvelope struct {
	Error   *errorBody      `json:"error"`
	Message string          `json:"message"`
	Type    string          `json:"type"`
	Code    json.RawMessage `json:"code"`
}

type errorBody struct {
	Message string          `json:"message"`
	Type    string          `json:"type"`
	Code    json.RawMessage `json:"code"` // a string from OpenAI, a number from vLLM and OpenRouter
}

// streamError turns an error object sent in place of a chunk into an
// *llm.Error, or returns nil when the chunk holds none. Such an error has no
// HTTP status of its own: the response was a 200.
func (env *errorEnvelope) streamError(provider string) error {
	var kind, code, msg string
	switch {
	case env.Error != nil:
		kind, code, msg = env.Error.Type, codeString(env.Error.Code), env.Error.Message
	case env.Message != "":
		kind, code, msg = env.Type, codeString(env.Code), env.Message
	default:
		return nil
	}
	class := classifyText([]byte(kind + " " + code + " " + msg))
	if class == llm.ErrUnknown {
		class = classifyKind(kind, code)
	}
	detail := strings.TrimSpace(kind + " " + code)
	if detail != "" {
		detail += ": "
	}
	return &llm.Error{
		Class:    class,
		Provider: provider,
		Err:      errors.New("stream error: " + wire.Excerpt([]byte(detail+msg))),
	}
}

// classifyKind reads the kind of a mid-stream error. Servers disagree on
// whether it goes in type or code, and some put an HTTP status in code.
func classifyKind(kind, code string) llm.ErrorClass {
	if n, err := strconv.Atoi(code); err == nil && n >= http.StatusBadRequest {
		return wire.Classify(n)
	}
	for _, k := range []string{kind, code} {
		switch strings.ToLower(k) {
		case "server_error", "overloaded_error", "service_unavailable", "internalservererror", "serviceunavailableerror":
			return llm.ErrOverloaded
		case "rate_limit_exceeded", "rate_limit_error":
			return llm.ErrRateLimited
		case "authentication_error", "invalid_api_key":
			return llm.ErrAuth
		case "invalid_request_error":
			return llm.ErrBadRequest
		}
	}
	return llm.ErrUnknown
}

func codeString(raw json.RawMessage) string {
	s := strings.Trim(string(raw), `"`)
	if s == "null" {
		return ""
	}
	return s
}
