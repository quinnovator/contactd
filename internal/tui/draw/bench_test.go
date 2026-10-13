package draw_test

import (
	"strings"
	"testing"

	"github.com/quinnovator/contactd/internal/transcript"
)

// BenchmarkReply draws a reply with every kind of Markdown at 100 columns:
// what a block costs when it enters the window.
func BenchmarkReply(b *testing.B) {
	r := trueDark().renderer(100)
	it := agent(1, rich)
	b.ReportAllocs()
	for b.Loop() {
		r.Item(it)
	}
}

// BenchmarkReplyASCII is the same reply shown as written.
func BenchmarkReplyASCII(b *testing.B) {
	r := noneASCII().renderer(100)
	it := agent(1, rich)
	b.ReportAllocs()
	for b.Loop() {
		r.Item(it)
	}
}

// BenchmarkLiveFragment adds one word to a reply of 16 kB in flight: what a
// streamed batch costs.
func BenchmarkLiveFragment(b *testing.B) {
	r := trueDark().renderer(100)
	base := strings.Repeat(strings.Repeat("words that fill a line and more ", 8)+"\n\n", 64)
	texts := []string{base + "one", base + "one two"}
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		r.Live(transcript.Live{Typing: true, Text: texts[i%2]}, 0)
		i++
	}
}

// BenchmarkFoldedOutput draws a megabyte of tool output folded to four rows.
func BenchmarkFoldedOutput(b *testing.B) {
	r := trueDark().renderer(100)
	it := &transcript.Item{Kind: transcript.ToolResult, Seq: 1, Text: strings.Repeat(strings.Repeat("0123456789", 12)+"\n", 8192)}
	b.ReportAllocs()
	for b.Loop() {
		r.Item(it)
	}
}
