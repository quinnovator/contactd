package tui

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/quinnovator/contactd/internal/client"
	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
)

// What the client does most often, timed with a long conversation open: a
// key, a piece of a reply, a screen with nothing to draw, and a new size.
// None of it may grow with the conversation, which TestWorkDoesNotGrowWithHistory
// holds it to by counting; these say what it costs. Nothing asserts a time.

// benchItems is the length of the conversation the benchmarks open.
const benchItems = 10000

// benchChat is a conversation of benchItems messages, questions and answers
// in turn, in a terminal of 100 columns by 30 rows that shows every colour,
// drawn once. It returns the interface and the conversation's session.
func benchChat(b *testing.B) (*model, *fakeSession) {
	b.Helper()
	back := &fakeBackend{}
	m := newModel(b.Context(), Options{
		Backend: back, Now: fixedNow, Location: time.UTC,
		Env:       termcaps.Env{Term: "xterm-256color", Detected: termcaps.ColorTrue},
		Overrides: termcaps.Overrides{Glyphs: "unicode", Images: "text", Theme: "dark", NoProbe: true},
	})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.open(client.Contact{ID: 1, Name: "ada", Provider: "deepseek", Model: "deepseek-flash"})
	sess := back.sessions[0]
	events := make([]core.Event, 0, benchItems)
	at := fixedNow().Add(-time.Hour).UnixMilli()
	for seq := uint64(1); seq <= benchItems; seq++ {
		kind, text := core.KindUserMsg, fmt.Sprintf("question %d: how did the latency do last week?", seq)
		if seq%2 == 0 {
			kind, text = core.KindAgentMsg, fmt.Sprintf("Answer %d: p99 **peaked** on `Thursday` at 7.2 s, and was under two seconds on every other day.", seq)
		}
		body, err := json.Marshal(core.MsgBody{Text: text})
		if err != nil {
			b.Fatal(err)
		}
		events = append(events, core.Event{Contact: 1, Seq: seq, TS: at, Kind: kind, Body: body})
	}
	m.Update(updatesMsg{sess: sess, batch: []client.Update{live(), {Kind: client.UpdateEvents, Events: events}}})
	if got := m.chat.list.Len(); got < benchItems {
		b.Fatalf("the conversation has %d blocks", got)
	}
	m.View()
	return m, sess
}

// One key typed into the box, and the screen drawn for it.
func BenchmarkKeyPress(b *testing.B) {
	m, _ := benchChat(b)
	keys := []tea.Msg{tea.KeyPressMsg{Code: 'x', Text: "x"}, tea.KeyPressMsg{Code: tea.KeyBackspace}}
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		m.Update(keys[i%2])
		m.View()
	}
}

// A batch of three pieces of a reply as the stream hands them over, and the
// screen drawn for it. The reply starts again every 64 batches, so that what
// is timed is a reply of a few lines and not one that never ends.
func BenchmarkStreamedBatch(b *testing.B) {
	m, sess := benchChat(b)
	batch := updatesMsg{sess: sess, closed: true, batch: []client.Update{
		delta(core.Delta{Type: core.DeltaText, Text: "p99 peaked "}),
		delta(core.Delta{Type: core.DeltaText, Text: "on Thursday "}),
		delta(core.Delta{Type: core.DeltaText, Text: "at 7.2 s. "}),
	}}
	again := updatesMsg{sess: sess, closed: true, batch: []client.Update{
		delta(core.Delta{Type: core.DeltaTyping}), delta(core.Delta{Type: core.DeltaTyping, Typing: true}),
	}}
	m.Update(again)
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		if i%64 == 63 {
			m.Update(again)
		}
		m.Update(batch)
		m.View()
	}
}

// The screen asked for again with nothing changed since it was drawn.
func BenchmarkViewUnchanged(b *testing.B) {
	m, _ := benchChat(b)
	b.ReportAllocs()
	for b.Loop() {
		m.View()
	}
}

// The terminal given a new size, and the screen drawn for it: every block in
// view is drawn again, at the new width.
func BenchmarkResize(b *testing.B) {
	m, _ := benchChat(b)
	sizes := []tea.Msg{tea.WindowSizeMsg{Width: 120, Height: 40}, tea.WindowSizeMsg{Width: 100, Height: 30}}
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		m.Update(sizes[i%2])
		m.View()
	}
}
