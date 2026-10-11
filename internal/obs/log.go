package obs

import (
	"io"
	"log/slog"
	"strings"
)

// NewLogger builds the process logger. format is "json" or "text"; level is a
// slog level name. Every line in a turn carries contact_id and turn_id.
// Message bodies are never logged: they are user data and already in the log
// store.
func NewLogger(w io.Writer, format, level string) *slog.Logger {
	var lv slog.Level
	if err := lv.UnmarshalText([]byte(level)); err != nil {
		lv = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lv}
	if strings.EqualFold(format, "text") {
		return slog.New(slog.NewTextHandler(w, opts))
	}
	return slog.New(slog.NewJSONHandler(w, opts))
}

// Discard returns a logger that drops everything, for tests and tools.
func Discard() *slog.Logger { return slog.New(slog.DiscardHandler) }
