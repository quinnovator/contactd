package requester

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestWireRedactsBeforeClipping(t *testing.T) {
	t.Parallel()
	got := wire(map[string]any{"nested": []any{map[string]any{"credentials": "very-secret", "token": "also-secret", "text": strings.Repeat("界", 1000)}}})
	if strings.Contains(got, "secret") || !utf8.ValidString(got) || len(got) > 2048 {
		t.Fatalf("unsafe wire: %q", got)
	}
}
