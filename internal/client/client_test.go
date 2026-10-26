package client_test

import (
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/quinnovator/contactd/internal/actor"
	"github.com/quinnovator/contactd/internal/api/apitest"
	"github.com/quinnovator/contactd/internal/client"
	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/transcript"
)

// These tests run the client against the real API, so they pin the two
// sides to each other: nothing here is a hand-written copy of a response.

func overSocket(t *testing.T, srv *apitest.Server) *client.Client {
	t.Helper()
	c, err := client.New(client.Options{Socket: srv.Socket})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func overTCP(t *testing.T, srv *apitest.Server) *client.Client {
	t.Helper()
	c, err := client.New(client.Options{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// quiet makes a server whose contacts never reply, so a test can fill a log
// with exactly the messages it sends.
func quiet(cfg *actor.Config) { cfg.Debounce, cfg.DebounceCap = time.Hour, time.Hour }

func create(t *testing.T, c *client.Client, name string) core.ContactID {
	t.Helper()
	id, err := c.Create(t.Context(), client.NewContact{Name: name, Agent: core.AgentConfig{System: "You are " + name + "."}})
	if err != nil {
		t.Fatalf("create %q: %v", name, err)
	}
	return id
}

func seqs(events []core.Event) string {
	parts := make([]string, len(events))
	for i, ev := range events {
		parts[i] = strconv.FormatUint(ev.Seq, 10)
	}
	return strings.Join(parts, " ")
}

func TestContactsAndNames(t *testing.T) {
	t.Parallel()
	for name, dial := range map[string]func(*testing.T, *apitest.Server) *client.Client{"unix socket": overSocket, "tcp": overTCP} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := dial(t, apitest.Start(t, apitest.Options{Actor: quiet}))
			if err := c.Health(t.Context()); err != nil {
				t.Fatalf("health: %v", err)
			}
			ada, bob := "ada", "bob"
			a, b := create(t, c, ada), create(t, c, bob)
			unnamed := create(t, c, "")

			for _, ref := range []string{ada, a.String(), "#" + a.String()} {
				got, err := c.Find(t.Context(), ref)
				if err != nil || got.ID != a || got.Name != ada {
					t.Errorf("Find(%q) = %+v, %v", ref, got, err)
				}
				if got.Agent == nil || got.Agent.System != "You are "+ada+"." || got.Provider != "fake" || got.Model == "" {
					t.Errorf("Find(%q) left out the agent or the route: %+v", ref, got)
				}
			}
			if _, err := c.Find(t.Context(), "nobody"); !errors.Is(err, core.ErrContactNotFound) {
				t.Errorf("Find(nobody) = %v, want ErrContactNotFound", err)
			}
			if _, err := c.Find(t.Context(), "99999"); !errors.Is(err, core.ErrContactNotFound) {
				t.Errorf("Find(99999) = %v, want ErrContactNotFound", err)
			}

			if _, err := c.Create(t.Context(), client.NewContact{Name: ada, Agent: core.AgentConfig{System: "x"}}); !client.IsStatus(err, http.StatusConflict) {
				t.Errorf("creating a second %q: %v, want a conflict", ada, err)
			}
			if _, err := c.Rename(t.Context(), b, ada); !client.IsStatus(err, http.StatusConflict) {
				t.Errorf("renaming to a name that is taken: %v, want a conflict", err)
			}
			for _, bad := range []string{"123", " padded", "tab\there", strings.Repeat("x", 65)} {
				if _, err := c.Rename(t.Context(), b, bad); !client.IsStatus(err, http.StatusBadRequest) {
					t.Errorf("Rename(%q) = %v, want a bad request", bad, err)
				}
			}
			renamed, err := c.Rename(t.Context(), unnamed, "carol")
			if err != nil || renamed.Name != "carol" || renamed.Label() != "carol" {
				t.Fatalf("Rename = %+v, %v", renamed, err)
			}
			if cleared, err := c.Rename(t.Context(), unnamed, ""); err != nil || cleared.Label() != "#"+unnamed.String() {
				t.Errorf("clearing a name: %+v, %v", cleared, err)
			}

			// The contact spoken to last is listed first.
			if _, err := c.Send(t.Context(), a, "hello"); err != nil {
				t.Fatal(err)
			}
			list, err := c.Contacts(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if len(list) != 3 || list[0].ID != a {
				t.Errorf("list = %+v, want %q first", list, ada)
			}
		})
	}
}

func TestLogPaging(t *testing.T) {
	t.Parallel()
	srv := apitest.Start(t, apitest.Options{Actor: quiet})
	c := overSocket(t, srv)
	id := create(t, c, "ada")
	for i := 1; i <= 7; i++ {
		if _, err := c.Send(t.Context(), id, fmt.Sprintf("message %d", i)); err != nil {
			t.Fatal(err)
		}
	}
	all := core.AllKinds

	tail, err := c.Tail(t.Context(), id, 3, all)
	if err != nil || seqs(tail.Events) != "6 7 8" || !tail.More {
		t.Fatalf("Tail(3) = %s more=%t, %v", seqs(tail.Events), tail.More, err)
	}
	older, err := c.Before(t.Context(), id, 5, 3, all)
	if err != nil || seqs(older.Events) != "2 3 4" || !older.More {
		t.Errorf("Before(5, 3) = %s more=%t, %v", seqs(older.Events), older.More, err)
	}
	first, err := c.Before(t.Context(), id, 2, 3, all)
	if err != nil || seqs(first.Events) != "1" || first.More {
		t.Errorf("Before(2, 3) = %s more=%t, %v; want the first event and no more", seqs(first.Events), first.More, err)
	}
	fwd, err := c.After(t.Context(), id, 4, 2, all)
	if err != nil || seqs(fwd.Events) != "5 6" || !fwd.More {
		t.Errorf("After(4, 2) = %s more=%t, %v", seqs(fwd.Events), fwd.More, err)
	}
	if ev := tail.Events[0]; ev.Kind != core.KindUserMsg || !strings.Contains(string(ev.Body), "message 5") || ev.TS == 0 {
		t.Errorf("event = %+v body %s", ev, ev.Body)
	}
	// A filter that matches nothing is an empty page, not an error.
	none, err := c.Tail(t.Context(), id, 3, core.KindSet(0).With(core.KindCheckpoint))
	if err != nil || len(none.Events) != 0 || none.More {
		t.Errorf("Tail of notices = %s, %v", seqs(none.Events), err)
	}
}

func TestNothingListening(t *testing.T) {
	t.Parallel()
	c, err := client.New(client.Options{Socket: filepath.Join(t.TempDir(), "nobody.sock")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Contacts(t.Context()); !errors.Is(err, client.ErrUnavailable) {
		t.Errorf("Contacts = %v, want ErrUnavailable", err)
	}
	if _, err := c.Live(t.Context(), 1); !errors.Is(err, client.ErrUnavailable) {
		t.Errorf("Live = %v, want ErrUnavailable", err)
	}
	sess := c.Follow(t.Context(), 1)
	defer sess.Close()
	down := waitFor(t, sess, nil, func(u client.Update) bool { return u.Kind == client.UpdateState && u.State == client.StateDown })
	if !errors.Is(down.Err, client.ErrUnavailable) {
		t.Errorf("session went down with %v, want ErrUnavailable", down.Err)
	}
	// A message cannot be sent, and the caller is told so.
	sess.Say("anyone?")
	refused := waitFor(t, sess, nil, func(u client.Update) bool { return u.Kind == client.UpdateRefused })
	if refused.Text != "anyone?" {
		t.Errorf("refused = %+v", refused)
	}

	if _, err := client.New(client.Options{}); err == nil {
		t.Error("a client with no server was accepted")
	}
	if _, err := client.New(client.Options{URL: "contactd.example"}); err == nil {
		t.Error("a URL with no scheme was accepted")
	}
}

// waitFor reads a session's updates, folding each into tr when one is given,
// until one satisfies done.
func waitFor(t *testing.T, sess *client.Session, tr *transcript.Transcript, done func(client.Update) bool) client.Update {
	t.Helper()
	timeout := time.After(20 * time.Second)
	for {
		select {
		case u, ok := <-sess.Updates():
			if !ok {
				t.Fatal("the session ended while the test was waiting on it")
			}
			if tr != nil {
				fold(tr, u)
			}
			if done(u) {
				return u
			}
		case <-timeout:
			t.Fatal("timed out waiting for the session")
		}
	}
}

// until reads a session's updates into tr until the transcript satisfies
// cond. Unlike waitFor it looks before it reads, for a condition that what
// has already arrived may meet.
func until(t *testing.T, sess *client.Session, tr *transcript.Transcript, cond func() bool) {
	t.Helper()
	for !cond() {
		waitFor(t, sess, tr, func(client.Update) bool { return true })
	}
}

// replies waits until the contact's log holds n whole replies.
func replies(t *testing.T, c *client.Client, id core.ContactID, n int) {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		page, err := c.Tail(t.Context(), id, 100, core.KindSet(0).With(core.KindAgentMsg))
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Events) >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the contact has %d replies; waited for %d", len(page.Events), n)
		}
	}
}

// fold applies an update the way a view does.
func fold(tr *transcript.Transcript, u client.Update) {
	switch u.Kind {
	case client.UpdateEvents, client.UpdateOlder:
		tr.Events(u.Events)
	case client.UpdateDelta:
		tr.Delta(u.Delta, 1)
	case client.UpdateAck:
		tr.Acked(u.Text, u.Seq, 1)
	case client.UpdateRefused:
		tr.Refused(u.Text)
	case client.UpdateSynced:
		tr.DropPending()
	case client.UpdateHelpers:
	case client.UpdateState:
	}
}

func isLive(u client.Update) bool { return u.Kind == client.UpdateState && u.State == client.StateLive }

func turnEnded(u client.Update) bool {
	return u.Kind == client.UpdateDelta && u.Delta.Type == core.DeltaTyping && !u.Delta.Typing
}

// texts renders a transcript's messages, one per line.
func texts(tr *transcript.Transcript) string {
	var b strings.Builder
	for _, it := range tr.Items() {
		switch it.Kind {
		case transcript.User:
			fmt.Fprintf(&b, "user: %s\n", it.Text)
		case transcript.Agent:
			if it.Interrupted {
				b.WriteString("agent (interrupted)\n")
			} else {
				fmt.Fprintf(&b, "agent: %s\n", it.Text)
			}
		case transcript.ToolCall, transcript.ToolResult, transcript.Notice, transcript.Checkpoint, transcript.Failure:
			fmt.Fprintf(&b, "%d: %s\n", it.Kind, it.Text)
		}
	}
	return b.String()
}

func TestSessionFollowsAConversation(t *testing.T) {
	t.Parallel()
	srv := apitest.Start(t, apitest.Options{})
	c := overSocket(t, srv)
	id := create(t, c, "ada")
	// Something said before the session opened: it has to come from the log.
	if _, err := c.Send(t.Context(), id, "before you arrived"); err != nil {
		t.Fatal(err)
	}
	replies(t, c, id, 1)

	var tr transcript.Transcript
	sess := c.Follow(t.Context(), id)
	defer sess.Close()
	waitFor(t, sess, &tr, isLive)
	if !strings.Contains(texts(&tr), "user: before you arrived") {
		t.Fatalf("history was not read on opening:\n%s", texts(&tr))
	}

	tr.Sent("hello there")
	sess.Say("hello there")
	ack := waitFor(t, sess, &tr, func(u client.Update) bool { return u.Kind == client.UpdateAck })
	if ack.Text != "hello there" || ack.Seq == 0 {
		t.Errorf("ack = %+v", ack)
	}
	// The turn is over when typing stops; the record of its model call
	// comes from the log straight after.
	until(t, sess, &tr, func() bool {
		_, ok := tr.Attempt()
		return ok && !tr.Live().Typing && strings.Contains(texts(&tr), "agent: Got it: hello there")
	})
	if len(tr.Pending()) != 0 {
		t.Errorf("pending = %q after the acknowledgement", tr.Pending())
	}
	if a, _ := tr.Attempt(); a.Route != "fake/fake-1" || a.Input == 0 {
		t.Errorf("attempt = %+v", a)
	}
	got := texts(&tr)
	for _, line := range []string{"user: before you arrived", "agent: Got it: before you arrived", "user: hello there", "agent: Got it: hello there"} {
		if strings.Count(got, line) != 1 {
			t.Errorf("%q appears %d times in:\n%s", line, strings.Count(got, line), got)
		}
	}
}

func TestSessionReconnectsAndCatchesUp(t *testing.T) {
	t.Parallel()
	srv := apitest.Start(t, apitest.Options{})
	c := overTCP(t, srv)
	id := create(t, c, "ada")

	var tr transcript.Transcript
	sess := c.Follow(t.Context(), id)
	defer sess.Close()
	waitFor(t, sess, &tr, isLive)
	sess.Say("first")
	waitFor(t, sess, &tr, turnEnded)

	srv.DropConnections()
	waitFor(t, sess, &tr, func(u client.Update) bool { return u.Kind == client.UpdateState && u.State != client.StateLive })
	// Said by someone else while this session was away.
	if _, err := overSocket(t, srv).Send(t.Context(), id, "second"); err != nil {
		t.Fatal(err)
	}
	synced := false
	waitFor(t, sess, &tr, func(u client.Update) bool {
		synced = synced || u.Kind == client.UpdateSynced
		return isLive(u)
	})
	if !synced {
		t.Error("the session came back without saying it had caught up")
	}
	until(t, sess, &tr, func() bool { return strings.Contains(texts(&tr), "agent: Got it: second") })

	sess.Say("third")
	until(t, sess, &tr, func() bool { return strings.Contains(texts(&tr), "agent: Got it: third") })
	got := texts(&tr)
	for _, line := range []string{"user: first", "agent: Got it: first", "user: second", "user: third"} {
		if strings.Count(got, line) != 1 {
			t.Errorf("%q appears %d times in:\n%s", line, strings.Count(got, line), got)
		}
	}
}

func TestSessionPagesBackToTheBeginning(t *testing.T) {
	t.Parallel()
	srv := apitest.Start(t, apitest.Options{Actor: quiet})
	c := overSocket(t, srv)
	id := create(t, c, "ada")
	const total = 250
	for i := 1; i <= total; i++ {
		if _, err := c.Send(t.Context(), id, fmt.Sprintf("message %d", i)); err != nil {
			t.Fatal(err)
		}
	}

	var tr transcript.Transcript
	sess := c.Follow(t.Context(), id)
	defer sess.Close()
	first := waitFor(t, sess, &tr, func(u client.Update) bool { return u.Kind == client.UpdateEvents })
	if len(first.Events) != 200 || first.Start || tr.Oldest() != 52 {
		t.Fatalf("first page: %d events, start=%t, oldest %d; want the newest 200", len(first.Events), first.Start, tr.Oldest())
	}
	waitFor(t, sess, &tr, isLive)

	sess.Older()
	older := waitFor(t, sess, &tr, func(u client.Update) bool { return u.Kind == client.UpdateOlder })
	if len(older.Events) != 51 || !older.Start || tr.Oldest() != 2 || len(tr.Items()) != total {
		t.Errorf("older page: %d events, start=%t, oldest %d, %d items", len(older.Events), older.Start, tr.Oldest(), len(tr.Items()))
	}
	sess.Older()
	if again := waitFor(t, sess, &tr, func(u client.Update) bool { return u.Kind == client.UpdateOlder }); len(again.Events) != 0 || !again.Start {
		t.Errorf("past the beginning: %+v", again)
	}
}

func TestSessionInterrupt(t *testing.T) {
	t.Parallel()
	opt := apitest.Options{}
	opt.LLM.PerChunk, opt.LLM.ReplyWords = 30*time.Millisecond, 400
	srv := apitest.Start(t, opt)
	c := overSocket(t, srv)
	id := create(t, c, "ada")

	var tr transcript.Transcript
	sess := c.Follow(t.Context(), id)
	defer sess.Close()
	waitFor(t, sess, &tr, isLive)
	sess.Say("tell me everything")
	until(t, sess, &tr, func() bool { return len(tr.Live().Text) > 20 })
	sess.Interrupt()
	waitFor(t, sess, &tr, turnEnded)

	items := tr.Items()
	last := items[len(items)-1]
	if last.Kind != transcript.Agent || !last.Interrupted || !strings.HasPrefix(last.Text, "Got it: tell me everything") {
		t.Fatalf("last item = %+v, want the cut-off reply", last)
	}
	// What the stream said was cut off is what the log kept.
	page, err := c.Tail(t.Context(), id, 10, core.KindSet(0).With(core.KindAgentMsg))
	if err != nil || len(page.Events) != 1 {
		t.Fatalf("agent messages in the log: %d, %v", len(page.Events), err)
	}
	var logged transcript.Transcript
	logged.Events(page.Events)
	if got := logged.Items()[0]; got.Text != last.Text || !got.Interrupted {
		t.Errorf("the log has %q (interrupted=%t); the stream gave %q", got.Text, got.Interrupted, last.Text)
	}
}

func TestSessionOpenedMidReply(t *testing.T) {
	t.Parallel()
	opt := apitest.Options{}
	opt.LLM.PerChunk, opt.LLM.ReplyWords = 30*time.Millisecond, 60
	srv := apitest.Start(t, opt)
	c := overSocket(t, srv)
	id := create(t, c, "ada")

	var early transcript.Transcript
	first := c.Follow(t.Context(), id)
	defer first.Close()
	waitFor(t, first, &early, isLive)
	first.Say("go on")
	until(t, first, &early, func() bool { return len(early.Live().Text) > 20 })

	// A second view opens while the reply is being written. It is given what
	// has been said so far, and ends up with the same reply.
	var late transcript.Transcript
	second := c.Follow(t.Context(), id)
	defer second.Close()
	waitFor(t, second, &late, isLive)
	until(t, second, &late, func() bool { return late.Live().Typing && strings.HasPrefix(late.Live().Text, "Got it: go on") })
	done := func(tr *transcript.Transcript) func() bool {
		return func() bool { return strings.Contains(texts(tr), "agent: Got it: go on") }
	}
	until(t, second, &late, done(&late))
	until(t, first, &early, done(&early))
	if a, b := texts(&early), texts(&late); a != b {
		t.Errorf("the two views differ:\n%s\n---\n%s", a, b)
	}
}

func TestSessionRefusalKeepsTheConnection(t *testing.T) {
	t.Parallel()
	srv := apitest.Start(t, apitest.Options{Actor: func(cfg *actor.Config) {
		quiet(cfg)
		cfg.MaxPendingBytes = 16
	}})
	c := overSocket(t, srv)
	id := create(t, c, "ada")

	var tr transcript.Transcript
	sess := c.Follow(t.Context(), id)
	defer sess.Close()
	waitFor(t, sess, &tr, isLive)
	for _, text := range []string{"0123456789", "one too many", "and another"} {
		tr.Sent(text)
		sess.Say(text)
	}
	var acked, refused []string
	waitFor(t, sess, &tr, func(u client.Update) bool {
		if u.Kind == client.UpdateAck {
			acked = append(acked, u.Text)
		}
		if u.Kind == client.UpdateRefused {
			refused = append(refused, u.Text)
			if !strings.Contains(u.Reason, "unanswered") {
				t.Errorf("refusal reason = %q", u.Reason)
			}
		}
		if u.Kind == client.UpdateState && u.State != client.StateLive {
			t.Errorf("a refused message cost the connection: %+v", u)
		}
		return len(acked)+len(refused) == 3
	})
	if strings.Join(acked, "|") != "0123456789" || strings.Join(refused, "|") != "one too many|and another" {
		t.Errorf("acked %q, refused %q", acked, refused)
	}
	if len(tr.Pending()) != 0 || texts(&tr) != "user: 0123456789\n" {
		t.Errorf("pending %q, transcript:\n%s", tr.Pending(), texts(&tr))
	}
}

func TestSessionForAContactThatDoesNotExist(t *testing.T) {
	t.Parallel()
	srv := apitest.Start(t, apitest.Options{})
	sess := overSocket(t, srv).Follow(t.Context(), 4242)
	defer sess.Close()
	gone := waitFor(t, sess, nil, func(u client.Update) bool { return u.Kind == client.UpdateState && u.State != client.StateConnecting })
	if gone.State != client.StateGone || !errors.Is(gone.Err, core.ErrContactNotFound) {
		t.Errorf("state = %+v, want gone", gone)
	}
	if _, ok := <-sess.Updates(); ok {
		t.Error("the session kept going for a contact that does not exist")
	}
}
