package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/quinnovator/contactd/internal/client"
	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
)

// The interface is driven the way Bubble Tea drives it: messages into
// Update, and the screen read back from View. No terminal is involved.

type fakeSession struct {
	updates    chan client.Update
	said       []string
	typings    int
	interrupts int
	olders     int
	closed     bool
}

func (s *fakeSession) Updates() <-chan client.Update { return s.updates }
func (s *fakeSession) Say(text string)               { s.said = append(s.said, text) }
func (s *fakeSession) Typing()                       { s.typings++ }
func (s *fakeSession) Interrupt()                    { s.interrupts++ }
func (s *fakeSession) Older()                        { s.olders++ }
func (s *fakeSession) Close()                        { s.closed = true }

type fakeBackend struct {
	contacts []client.Contact
	created  []client.NewContact
	sessions []*fakeSession
	// files are the files of every contact's working directory, by path,
	// and failing the ones that cannot be fetched right now, with why.
	files   map[string]client.File
	failing map[string]error
	err     error

	// mu guards what is written down about File, which commands call.
	mu sync.Mutex
	// asked is every file that was asked for, in order. onLoop are the ones
	// asked for while Update ran, which must never happen: loop says it runs.
	asked  []fileAsk
	loop   bool
	onLoop []string
	// flying is how many fetches are on their way, and most the most there
	// were at one moment.
	flying, most int
	// hold, when set, keeps a fetch waiting until it is closed or the caller
	// gives up. Each fetch that starts to wait says so on waiting, and gaveUp
	// then tells whether the caller of the last one has given up.
	hold    chan struct{}
	waiting chan struct{}
	gaveUp  func() error
}

// fileAsk is one request for a file: whose, which, and the tag of the copy
// the caller said it holds.
type fileAsk struct {
	id         core.ContactID
	path, etag string
}

func (b *fakeBackend) Contacts(context.Context) ([]client.Contact, error) { return b.contacts, b.err }

func (b *fakeBackend) Contact(_ context.Context, id core.ContactID) (client.Contact, error) {
	for _, c := range b.contacts {
		if c.ID == id {
			return c, nil
		}
	}
	return client.Contact{}, core.ErrContactNotFound
}

func (b *fakeBackend) Create(_ context.Context, nc client.NewContact) (core.ContactID, error) {
	if b.err != nil {
		return 0, b.err
	}
	b.created = append(b.created, nc)
	id := core.ContactID(len(b.contacts) + 1)
	b.contacts = append(b.contacts, client.Contact{ID: id, Name: nc.Name, Provider: "fake", Model: "fake-1"})
	return id, nil
}

func (b *fakeBackend) Rename(_ context.Context, id core.ContactID, name string) (client.Contact, error) {
	if b.err != nil {
		return client.Contact{}, b.err
	}
	for i := range b.contacts {
		if b.contacts[i].ID == id {
			b.contacts[i].Name = name
			return b.contacts[i], nil
		}
	}
	return client.Contact{}, core.ErrContactNotFound
}

func (b *fakeBackend) Follow(context.Context, core.ContactID) Session {
	s := &fakeSession{updates: make(chan client.Update)}
	b.sessions = append(b.sessions, s)
	return s
}

func (b *fakeBackend) File(ctx context.Context, id core.ContactID, path, etag string) (client.File, error) {
	defer b.fetching(id, path, etag)()
	if b.hold != nil {
		b.mu.Lock()
		b.gaveUp = ctx.Err
		b.mu.Unlock()
		if b.waiting != nil {
			b.waiting <- struct{}{}
		}
		select {
		case <-b.hold:
		case <-ctx.Done():
			return client.File{}, fmt.Errorf("fetch %s: %w", path, ctx.Err())
		}
	}
	f, ok := b.files[path]
	switch {
	case b.failing[path] != nil:
		return client.File{}, b.failing[path]
	case !ok:
		return client.File{}, client.ErrFileNotFound
	case etag != "" && etag == f.ETag:
		return client.File{ETag: f.ETag, Modified: f.Modified, NotModified: true}, nil
	default:
		return f, nil
	}
}

// fetching writes down that a file is asked for. What it returns is called
// when the fetch is over.
func (b *fakeBackend) fetching(id core.ContactID, path, etag string) func() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.asked = append(b.asked, fileAsk{id: id, path: path, etag: etag})
	if b.loop {
		b.onLoop = append(b.onLoop, path)
	}
	b.flying++
	b.most = max(b.most, b.flying)
	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.flying--
	}
}

// fetched is the files asked for so far, in order.
func (b *fakeBackend) fetched() []fileAsk {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.asked)
}

func (b *fakeBackend) Where() string { return "/data/contactd.sock" }

// noon on a Monday, UTC.
func fixedNow() time.Time { return time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC) }

type harness struct {
	t   *testing.T
	m   *model
	b   *fakeBackend
	now time.Time
	// With keep, the commands Update answers with are kept, for a test that
	// runs every one of them. around, when set, is called before and after
	// each Update.
	keep   bool
	kept   []tea.Cmd
	around func(in bool)
}

// The four terminals the client is drawn for in tests: the best one, a
// 16-colour one on a light background, a 256-colour one that never said what
// its background is, and a serial console.
func trueDark() termcaps.Caps {
	return termcaps.Caps{
		Color: termcaps.ColorTrue, Background: termcaps.BgDark, Glyphs: termcaps.GlyphsUnicode,
		Images: termcaps.ImagesText, Width: termcaps.WidthWc, CellW: 8, CellH: 16, Settled: true,
	}
}

func light16() termcaps.Caps {
	c := trueDark()
	c.Color, c.Background = termcaps.Color16, termcaps.BgLight
	return c
}

func unknown256() termcaps.Caps {
	c := trueDark()
	c.Color, c.Background = termcaps.Color256, termcaps.BgUnknown
	return c
}

func noneASCII() termcaps.Caps {
	c := trueDark()
	c.Color, c.Background, c.Glyphs = termcaps.ColorNone, termcaps.BgUnknown, termcaps.GlyphsASCII
	return c
}

// terminal is an environment and settings from which the client works out
// caps, for a terminal that is asked nothing: the colours as detected, the
// rest as set.
func terminal(t *testing.T, caps termcaps.Caps) (termcaps.Env, termcaps.Overrides) {
	t.Helper()
	env := termcaps.Env{Term: "xterm-256color", Detected: caps.Color}
	over := termcaps.Overrides{Glyphs: caps.Glyphs.String(), Images: caps.Images.String(), NoProbe: true}
	if caps.Background != termcaps.BgUnknown {
		over.Theme = caps.Background.String()
	}
	if got := termcaps.Resolve(env, over, termcaps.Replies{}); got != caps {
		t.Fatalf("the environment and settings give\n%+v, want\n%+v", got, caps)
	}
	return env, over
}

// newHarness makes the interface for a terminal that can do caps, 80
// columns by 24 rows, over a server that has these contacts.
func newHarness(t *testing.T, caps termcaps.Caps, contacts ...client.Contact) *harness {
	t.Helper()
	env, over := terminal(t, caps)
	return newHarnessWith(t, Options{Env: env, Overrides: over}, contacts...)
}

// newHarnessWith is newHarness for the terminal and the settings opt holds.
// The server, the clock and the time zone are the harness's own.
func newHarnessWith(t *testing.T, opt Options, contacts ...client.Contact) *harness {
	t.Helper()
	h := &harness{t: t, b: &fakeBackend{contacts: contacts}, now: fixedNow()}
	opt.Backend, opt.Now, opt.Location = h.b, func() time.Time { return h.now }, time.UTC
	h.m = newModel(t.Context(), opt)
	h.size(80, 24)
	return h
}

// size tells the interface the terminal's size.
func (h *harness) size(width, height int) {
	h.t.Helper()
	h.send(tea.WindowSizeMsg{Width: width, Height: height})
}

// send delivers one message and returns the command Update answered with.
func (h *harness) send(msg tea.Msg) tea.Cmd {
	h.t.Helper()
	if h.around != nil {
		h.around(true)
		defer h.around(false)
	}
	_, cmd := h.m.Update(msg)
	if h.keep && cmd != nil {
		h.kept = append(h.kept, cmd)
	}
	return cmd
}

// run delivers a message and then whatever its command produces, the way
// the program's loop would. Only commands that finish are run this way.
func (h *harness) run(msg tea.Msg) {
	h.t.Helper()
	if cmd := h.send(msg); cmd != nil {
		h.send(cmd())
	}
}

func (h *harness) key(k tea.KeyPressMsg) tea.Cmd { return h.send(k) }

func (h *harness) typeText(s string) {
	for _, r := range s {
		h.send(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func enter() tea.KeyPressMsg         { return tea.KeyPressMsg{Code: tea.KeyEnter} }
func esc() tea.KeyPressMsg           { return tea.KeyPressMsg{Code: tea.KeyEscape} }
func ctrl(r rune) tea.KeyPressMsg    { return tea.KeyPressMsg{Code: r, Mod: tea.ModCtrl} }
func special(c rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: c} }

// screen is what is drawn, without colours.
func (h *harness) screen() string { return ansi.Strip(h.m.View().Content) }

func (h *harness) wantOnScreen(parts ...string) {
	h.t.Helper()
	screen := h.screen()
	for _, part := range parts {
		if !strings.Contains(screen, part) {
			h.t.Errorf("the screen lacks %q:\n%s", part, screen)
		}
	}
}

func (h *harness) wantNotOnScreen(parts ...string) {
	h.t.Helper()
	screen := h.screen()
	for _, part := range parts {
		if strings.Contains(screen, part) {
			h.t.Errorf("the screen should not show %q:\n%s", part, screen)
		}
	}
}

// openChat opens a conversation with ada and returns its session.
func (h *harness) openChat() *fakeSession {
	h.t.Helper()
	h.m.open(client.Contact{ID: 1, Name: "ada", Provider: "fake", Model: "fake-1"})
	return h.b.sessions[len(h.b.sessions)-1]
}

// from delivers a batch of updates as coming from the open session.
func (h *harness) from(sess *fakeSession, updates ...client.Update) {
	h.t.Helper()
	h.send(updatesMsg{sess: sess, batch: updates})
}

func live() client.Update { return client.Update{Kind: client.UpdateState, State: client.StateLive} }

func delta(d core.Delta) client.Update { return client.Update{Kind: client.UpdateDelta, Delta: d} }

func logged(t *testing.T, seq uint64, at time.Time, kind core.EventKind, body any) core.Event {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return core.Event{Contact: 1, Seq: seq, TS: at.UnixMilli(), Kind: kind, Body: raw}
}

func TestChatShowsHistoryThenAReplyArriving(t *testing.T) {
	t.Parallel()
	h := newHarness(t, trueDark())
	sess := h.openChat()
	h.wantOnScreen("ada", "fake/fake-1", "● connecting")

	at := fixedNow().Add(-time.Hour)
	h.from(sess, client.Update{Kind: client.UpdateEvents, Start: true, Events: []core.Event{
		logged(t, 1, at, core.KindUserMsg, core.MsgBody{Text: "what is 6*7?"}),
		logged(t, 2, at, core.KindToolCall, core.ToolCall{ID: "c1", Name: "python", Code: "print(6*7)", Text: "Let me check."}),
		logged(t, 3, at, core.KindToolResult, core.ToolResult{ID: "c1", Output: "42\n"}),
		logged(t, 4, at, core.KindAttempt, core.AttemptBody{Route: "fake/fake-1", Input: 2000, Cached: 1500, MS: 1234}),
		logged(t, 5, at, core.KindAgentMsg, core.MsgBody{Text: "It is 42."}),
	}}, live())
	h.wantOnScreen(
		"the beginning", "Mon 12 Oct 2026",
		"❯ what is 6*7?", "Let me check.", "⏺ python", "print(6*7)", "⎿ 42", "It is 42.",
		"● live │ fake-1 │ cache 75% │ 2.0k tok │ 1.2s",
		"Message ada",
	)
	h.wantNotOnScreen("connecting", "replying", "esc stop")

	h.from(sess,
		delta(core.Delta{Type: core.DeltaUser, Text: "and 6*8?", Seq: 6}),
		delta(core.Delta{Type: core.DeltaTyping, Typing: true}),
		delta(core.Delta{Type: core.DeltaText, Text: "That would "}),
		delta(core.Delta{Type: core.DeltaText, Text: "be"}),
	)
	h.wantOnScreen("❯ and 6*8?", "That would be", "replying │ esc stop")

	h.from(sess,
		delta(core.Delta{Type: core.DeltaText, Text: " 48."}),
		delta(core.Delta{Type: core.DeltaMessage, Text: "That would be 48.", Seq: 8}),
		delta(core.Delta{Type: core.DeltaTyping}),
	)
	h.wantOnScreen("That would be 48.")
	h.wantNotOnScreen("replying", "esc stop")
	if n := strings.Count(h.screen(), "That would be 48."); n != 1 {
		t.Errorf("the reply is on screen %d times", n)
	}
}

func TestSendingAMessage(t *testing.T) {
	t.Parallel()
	h := newHarness(t, trueDark())
	sess := h.openChat()

	// Not connected yet: nothing is sent, and nothing typed is lost.
	h.typeText("hello")
	h.key(enter())
	if len(sess.said) != 0 {
		t.Fatalf("sent %q before the connection was up", sess.said)
	}
	h.wantOnScreen("not sent: not connected", "❯ hello")

	h.from(sess, live())
	h.wantNotOnScreen("not sent")
	h.key(enter())
	if fmt.Sprint(sess.said) != "[hello]" {
		t.Fatalf("said = %q", sess.said)
	}
	h.wantOnScreen("❯ hello", "sending…", "Message ada")

	h.from(sess, client.Update{Kind: client.UpdateAck, Text: "hello", Seq: 1})
	h.wantOnScreen("❯ hello")
	h.wantNotOnScreen("sending…")

	// An empty box sends nothing, and neither does one with only spaces.
	h.key(enter())
	h.typeText("   ")
	h.key(enter())
	if len(sess.said) != 1 {
		t.Errorf("said = %q after sending nothing", sess.said)
	}
}

func TestAMessageThatDidNotArriveComesBack(t *testing.T) {
	t.Parallel()

	t.Run("refused", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, trueDark())
		sess := h.openChat()
		h.from(sess, live())
		h.typeText("one more")
		h.key(enter())
		h.typeText("next thought")
		h.from(sess, client.Update{Kind: client.UpdateRefused, Text: "one more", Reason: "too many unanswered messages; wait for a reply"})
		h.wantOnScreen("not sent: too many unanswered messages", "❯ one more", "next thought")
		h.wantNotOnScreen("sending…")
		if got := h.m.chat.input.Value(); got != "one more\nnext thought" {
			t.Errorf("the box holds %q", got)
		}
	})

	t.Run("lost when the connection dropped", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, trueDark())
		sess := h.openChat()
		h.from(sess, live())
		h.typeText("made it")
		h.key(enter())
		h.typeText("did not")
		h.key(enter())
		h.from(sess,
			client.Update{Kind: client.UpdateState, State: client.StateDown, Err: errors.New("connection reset")},
		)
		h.wantOnScreen("● offline, retrying", "disconnected: connection reset")
		// On reconnect the log has the first message and not the second.
		h.from(sess,
			client.Update{Kind: client.UpdateEvents, Events: []core.Event{logged(t, 1, fixedNow(), core.KindUserMsg, core.MsgBody{Text: "made it"})}},
			client.Update{Kind: client.UpdateSynced},
			live(),
		)
		if got := h.m.chat.input.Value(); got != "did not" {
			t.Errorf("the box holds %q, want the message that was lost", got)
		}
		h.wantOnScreen("❯ made it", "not sent: the connection dropped first")
		h.wantNotOnScreen("sending…", "disconnected", "offline")
	})
}

func TestEscStopsAReplyAndOnlyAReply(t *testing.T) {
	t.Parallel()
	h := newHarness(t, trueDark())
	sess := h.openChat()
	h.from(sess, live())
	h.key(esc())
	if sess.interrupts != 0 {
		t.Fatal("esc sent an interrupt with nothing in flight")
	}
	h.from(sess, delta(core.Delta{Type: core.DeltaTyping, Typing: true}), delta(core.Delta{Type: core.DeltaText, Text: "It began on a Tuesday"}))
	h.key(esc())
	if sess.interrupts != 1 {
		t.Fatalf("interrupts = %d, want 1", sess.interrupts)
	}
	h.from(sess, delta(core.Delta{Type: core.DeltaInterrupted, Seq: 2}), delta(core.Delta{Type: core.DeltaTyping}))
	h.wantOnScreen("It began on a Tuesday", "⏹ interrupted")
	h.wantNotOnScreen("replying")
}

func TestCtrlCClearsTheBoxBeforeItQuits(t *testing.T) {
	t.Parallel()
	h := newHarness(t, trueDark())
	h.openChat()
	h.typeText("half a thought")
	if cmd := h.key(ctrl('c')); cmd != nil {
		t.Fatal("ctrl+c with something typed did more than clear it")
	}
	if got := h.m.chat.input.Value(); got != "" {
		t.Fatalf("the box still holds %q", got)
	}
	cmd := h.key(ctrl('c'))
	if cmd == nil {
		t.Fatal("ctrl+c on an empty box did nothing")
	}
	if _, quit := cmd().(tea.QuitMsg); !quit {
		t.Error("ctrl+c on an empty box did not quit")
	}
}

func TestToolOutputFoldsUntilAsked(t *testing.T) {
	t.Parallel()
	h := newHarness(t, trueDark())
	sess := h.openChat()
	var out strings.Builder
	for i := 1; i <= 9; i++ {
		fmt.Fprintf(&out, "row %d\n", i)
	}
	h.from(sess, live(), client.Update{Kind: client.UpdateEvents, Events: []core.Event{
		logged(t, 1, fixedNow(), core.KindToolCall, core.ToolCall{ID: "c1", Name: "python", Code: "for i in range(9): print(i)"}),
		logged(t, 2, fixedNow(), core.KindToolResult, core.ToolResult{ID: "c1", Output: out.String(), IsError: true}),
	}})
	h.wantOnScreen("⎿ row 1", "row 4", "… 5 more rows (ctrl+o)")
	h.wantNotOnScreen("row 5")
	h.key(ctrl('o'))
	h.wantOnScreen("row 5", "row 9")
	h.wantNotOnScreen("more rows")
	h.key(ctrl('o'))
	h.wantNotOnScreen("row 5")
}

func TestScrollingToTheTopLoadsEarlierHistory(t *testing.T) {
	t.Parallel()
	h := newHarness(t, trueDark())
	sess := h.openChat()
	page := func(from, to uint64) []core.Event {
		var events []core.Event
		for seq := from; seq <= to; seq++ {
			events = append(events, logged(t, seq, fixedNow(), core.KindUserMsg, core.MsgBody{Text: fmt.Sprintf("message %d", seq)}))
		}
		return events
	}
	h.from(sess, client.Update{Kind: client.UpdateEvents, Events: page(21, 40)}, live())
	h.wantOnScreen("message 40")
	h.wantNotOnScreen("message 21", "the beginning")

	// Up to the top of what is loaded, and once more: that asks for more,
	// once, however many times the key is pressed while it loads.
	for range 12 {
		h.key(special(tea.KeyPgUp))
	}
	if sess.olders != 1 {
		t.Fatalf("asked for earlier history %d times, want once", sess.olders)
	}
	h.wantOnScreen("message 21", "loading earlier")
	// From the first message down: the day line above it is about to move.
	rows := func() string {
		lines := strings.Split(h.screen(), "\n")
		for i, line := range lines {
			if strings.Contains(line, "message 21") {
				return fmt.Sprintf("row %d:\n%s", i, strings.Join(lines[i:i+8], "\n"))
			}
		}
		return "message 21 is not on screen"
	}
	was := rows()

	// The page lands above; what was on screen stays where it was.
	h.from(sess, client.Update{Kind: client.UpdateOlder, Events: page(1, 20), Start: true})
	if got := rows(); got != was {
		t.Errorf("the view jumped when earlier history arrived:\n%s\n--- was ---\n%s", got, was)
	}
	h.wantOnScreen("message 20")
	h.wantNotOnScreen("loading earlier")
	for range 30 {
		h.key(special(tea.KeyPgUp))
	}
	h.wantOnScreen("the beginning", "message 1")
	if sess.olders != 1 {
		t.Errorf("asked again past the beginning: %d requests", sess.olders)
	}

	// Sending from up here comes back down to the newest message.
	h.typeText("back down")
	h.key(enter())
	h.wantOnScreen("message 40", "❯ back down")
}

// A tool's output, a model's reply or a contact's name can carry terminal
// escape sequences. None of them may reach the terminal.
func TestEscapeSequencesFromTheServerAreNotPrinted(t *testing.T) {
	t.Parallel()
	hostile := "\x1b]0;owned\x07\x1b[2J\x1b[31mred\u202e alert\r\x00"
	h := newHarness(t, trueDark())
	h.m.open(client.Contact{ID: 1, Name: "ad\x1b[5ma", Provider: "fa\x1bke", Model: "m"})
	sess := h.b.sessions[0]
	h.from(sess, live(), client.Update{Kind: client.UpdateEvents, Events: []core.Event{
		logged(t, 1, fixedNow(), core.KindUserMsg, core.MsgBody{Text: hostile}),
		logged(t, 2, fixedNow(), core.KindToolCall, core.ToolCall{ID: "c", Name: "py\x1b[2Jthon", Code: hostile, Text: hostile}),
		logged(t, 3, fixedNow(), core.KindToolResult, core.ToolResult{ID: "c", Output: hostile}),
		logged(t, 4, fixedNow(), core.KindAgentMsg, core.MsgBody{Text: hostile}),
		logged(t, 5, fixedNow(), core.KindNotice, core.NoticeBody{Text: hostile}),
	}},
		delta(core.Delta{Type: core.DeltaTyping, Typing: true}),
		delta(core.Delta{Type: core.DeltaText, Text: hostile}),
		delta(core.Delta{Type: core.DeltaToolStart, Tool: hostile}),
		delta(core.Delta{Type: core.DeltaError, Text: hostile}),
	)
	raw := h.m.View().Content
	for _, bad := range []string{"\x1b]", "\x07", "\x1b[2J", "\x1b[31mred", "\u202e", "\x00", "\r"} {
		if strings.Contains(raw, bad) {
			t.Errorf("the screen contains %q", bad)
		}
	}
	// The escape character is dropped; the printable rest is shown as text.
	if !strings.Contains(h.screen(), "red alert") || !strings.Contains(h.screen(), "ad[5ma  fa") {
		t.Errorf("the harmless text was lost:\n%s", h.screen())
	}
	if title := h.m.View().WindowTitle; strings.ContainsRune(title, '\x1b') {
		t.Errorf("window title = %q", title)
	}
}

func TestADayLineSeparatesDays(t *testing.T) {
	t.Parallel()
	h := newHarness(t, trueDark())
	sess := h.openChat()
	h.from(sess, live(), client.Update{Kind: client.UpdateEvents, Events: []core.Event{
		logged(t, 1, fixedNow().Add(-48*time.Hour), core.KindUserMsg, core.MsgBody{Text: "saturday"}),
		logged(t, 2, fixedNow().Add(-47*time.Hour), core.KindAgentMsg, core.MsgBody{Text: "also saturday"}),
		logged(t, 3, fixedNow(), core.KindUserMsg, core.MsgBody{Text: "monday"}),
	}})
	screen := h.screen()
	sat, mon := strings.Index(screen, "Sat 10 Oct 2026"), strings.Index(screen, "Mon 12 Oct 2026")
	if sat < 0 || mon < sat || strings.Count(screen, "Oct 2026") != 2 {
		t.Errorf("want one line for each of the two days, in order:\n%s", screen)
	}
}

func TestTypingIsReportedButNotOnEveryKey(t *testing.T) {
	t.Parallel()
	h := newHarness(t, trueDark())
	sess := h.openChat()
	h.from(sess, live())
	h.typeText("a long sentence typed quickly")
	if sess.typings != 1 {
		t.Errorf("typing reported %d times within a moment, want once", sess.typings)
	}
	h.now = h.now.Add(time.Second)
	h.typeText("!")
	if sess.typings != 2 {
		t.Errorf("typing reported %d times, want a second one a second later", sess.typings)
	}
}

func TestContactListAndItsForms(t *testing.T) {
	t.Parallel()
	h := newHarness(t, trueDark(),
		client.Contact{ID: 1, Name: "ada", Provider: "deepseek", Model: "deepseek-flash", LastActive: fixedNow().Add(-3 * time.Minute).UnixMilli()},
		client.Contact{ID: 2, Provider: "fake", Model: "fake-1", LastActive: fixedNow().Add(-26 * time.Hour).UnixMilli()},
	)
	h.run(nil) // nothing: the list is loaded by Init in a real run
	h.send(h.m.showContacts()())
	h.wantOnScreen("Contacts", "/data/contactd.sock", "▸ ada", "deepseek/deepseek-flash", "3m ago", "#2", "1d ago")

	// Open the second contact, and come back.
	h.key(special(tea.KeyDown))
	h.key(enter())
	if len(h.b.sessions) != 1 || h.m.screen != screenChat || h.m.chat.contact.ID != 2 {
		t.Fatalf("enter did not open contact 2: %d sessions, screen %d", len(h.b.sessions), h.m.screen)
	}
	h.wantOnScreen("#2", "Message #2")
	h.send(h.key(ctrl('p'))())
	if !h.b.sessions[0].closed || h.m.screen != screenContacts {
		t.Error("leaving a conversation did not close its session")
	}

	// Name the unnamed one. A name that is taken is reported, and the
	// prompt stays open for another try.
	h.key(special(tea.KeyDown))
	h.typeText("r")
	h.wantOnScreen("Rename #2")
	h.typeText("bob")
	h.b.err = &client.Error{Status: 409, Message: "another contact has that name"}
	h.send(h.key(enter())())
	h.wantOnScreen("another contact has that name", "Rename #2")
	h.b.err = nil
	h.run(h.key(enter())())
	h.wantOnScreen("Contacts", "bob")

	// Make a new one: a name, then who it is.
	h.typeText("n")
	h.wantOnScreen("New contact", "System prompt")
	h.typeText("carol")
	h.key(enter())
	h.key(enter()) // no system prompt yet
	h.wantOnScreen("say what this contact is")
	h.typeText("You are Carol.")
	created := h.key(enter())
	h.send(created())
	if len(h.b.created) != 1 || h.b.created[0].Name != "carol" || h.b.created[0].Agent.System != "You are Carol." {
		t.Fatalf("created = %+v", h.b.created)
	}
	if h.m.screen != screenChat || h.m.chat.contact.Name != "carol" {
		t.Errorf("a new contact was not opened: screen %d", h.m.screen)
	}

	// The server being away is said plainly.
	h.send(h.key(ctrl('p'))())
	h.b.err = fmt.Errorf("%w at /data/contactd.sock: no such file", client.ErrUnavailable)
	h.typeText("g")
	h.send(h.m.showContacts()())
	h.wantOnScreen("contactd is not reachable", "running?)")
}

func TestAgo(t *testing.T) {
	t.Parallel()
	now := fixedNow()
	for _, tc := range []struct {
		back time.Duration
		want string
	}{
		{10 * time.Second, "just now"},
		{5 * time.Minute, "5m ago"},
		{3 * time.Hour, "3h ago"},
		{50 * time.Hour, "2d ago"},
		{40 * 24 * time.Hour, "2 Sep 2026"},
	} {
		if got := ago(now, now.Add(-tc.back).UnixMilli()); got != tc.want {
			t.Errorf("ago(%v) = %q, want %q", tc.back, got, tc.want)
		}
	}
	if got := ago(now, 0); got != "" {
		t.Errorf("ago of no time = %q", got)
	}
}
