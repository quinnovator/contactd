package client

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/quinnovator/contactd/internal/core"
)

// ViewKinds are the events a view of a conversation needs: what was said,
// what tools did, what the runtime announced, and the record of each model
// call. Episode summaries and environment snapshots are bookkeeping.
func ViewKinds() core.KindSet {
	var set core.KindSet
	for _, k := range []core.EventKind{
		core.KindUserMsg, core.KindAgentMsg, core.KindToolCall, core.KindToolResult,
		core.KindAttempt, core.KindCheckpoint, core.KindNotice,
	} {
		set = set.With(k)
	}
	return set
}

// State is whether a Session has a live connection.
type State uint8

const (
	// StateConnecting means the session is opening its connection, for the
	// first time or again.
	StateConnecting State = iota + 1
	// StateLive means the session is connected and caught up with the log.
	StateLive
	// StateDown means the last attempt to connect failed; the session will
	// try again. Err says why.
	StateDown
	// StateGone means the session has given up: the contact does not exist.
	// Err says why.
	StateGone
)

// UpdateKind tags what an Update carries.
type UpdateKind uint8

const (
	// UpdateEvents carries events read from the log: the newest page when
	// the session opens, and whatever the stream did not carry after that
	// (the record of each model call, and anything missed while
	// disconnected). An event may repeat one already seen as a delta; its
	// sequence number says so.
	UpdateEvents UpdateKind = iota + 1
	// UpdateOlder answers Older: the page before everything read so far.
	// Err is set, and Events empty, when it could not be read.
	UpdateOlder
	// UpdateDelta carries one item of the contact's live stream.
	UpdateDelta
	// UpdateAck means a message sent with Say is durable; Seq is its
	// sequence number and Text the message.
	UpdateAck
	// UpdateRefused means a message sent with Say was not accepted. Text is
	// the message and Reason why.
	UpdateRefused
	// UpdateSynced follows a reconnect, once the log has been read up to
	// the present: a message sent before the connection dropped that has
	// neither been acknowledged nor turned up in the log did not arrive.
	UpdateSynced
	// UpdateState reports a change of connection state.
	UpdateState
)

// Update is one item of a Session's stream. Kind says which fields are set.
type Update struct {
	Kind   UpdateKind
	Events []core.Event
	// Start, with events, means the page reaches the beginning of the log.
	Start  bool
	Delta  core.Delta
	Seq    uint64
	Text   string
	Reason string
	State  State
	Err    error
}

// Session follows one contact. It keeps a live connection open, reconnects
// when it drops, and reads the log around the stream, so that its caller
// sees one ordered sequence of updates with nothing missing: events carry
// sequence numbers, and applying one twice is harmless.
//
// The protocol it relies on: a stream opens with a "ready" delta naming the
// last sequence number assigned before it began. Everything up to there is
// read from the log, and everything after it arrives on the stream.
type Session struct {
	c  *Client
	id core.ContactID

	updates chan Update
	cmds    chan sessionCmd
	cancel  context.CancelFunc
	done    chan struct{}

	// The session's goroutine owns everything below.
	first       bool
	logSeq      uint64   // the highest sequence number read from the log
	oldest      uint64   // the lowest one read; zero until the first page
	start       bool     // the beginning of the log has been read
	seen        uint64   // the highest sequence number seen on the stream
	outstanding []string // messages sent and neither acknowledged nor refused
}

type sessionCmd struct {
	op   sessionOp
	text string
}

type sessionOp uint8

const (
	opSay sessionOp = iota + 1
	opTyping
	opInterrupt
	opOlder
)

const (
	// pageSize is how many events one page of history holds.
	pageSize = 200
	// pingEvery is how often an idle connection is checked.
	pingEvery = 20 * time.Second
	// durableWait bounds the wait for an event the stream has announced to
	// appear in the log.
	durableWait = 2 * time.Second
)

// Follow starts a session for a contact. It runs until ctx is done or Close
// is called.
func (c *Client) Follow(ctx context.Context, id core.ContactID) *Session {
	ctx, cancel := context.WithCancel(ctx)
	s := &Session{
		c: c, id: id,
		updates: make(chan Update, 256),
		cmds:    make(chan sessionCmd, 64),
		cancel:  cancel,
		done:    make(chan struct{}),
		first:   true,
	}
	go s.run(ctx)
	return s
}

// Updates is the session's stream. It is closed when the session ends.
func (s *Session) Updates() <-chan Update { return s.updates }

// Say sends a message. Its outcome arrives as UpdateAck or UpdateRefused.
func (s *Session) Say(text string) { s.send(sessionCmd{op: opSay, text: text}, true) }

// Typing tells the contact its human is still writing.
func (s *Session) Typing() { s.send(sessionCmd{op: opTyping}, false) }

// Interrupt stops the reply in flight.
func (s *Session) Interrupt() { s.send(sessionCmd{op: opInterrupt}, true) }

// Older asks for the page of history before everything read so far. It
// arrives as UpdateOlder.
func (s *Session) Older() { s.send(sessionCmd{op: opOlder}, true) }

// Close ends the session and waits for it to stop.
func (s *Session) Close() {
	s.cancel()
	<-s.done
}

// send queues a command. One that must not be lost waits for room; a typing
// indicator is dropped instead.
func (s *Session) send(c sessionCmd, wait bool) {
	if !wait {
		select {
		case s.cmds <- c:
		default:
		}
		return
	}
	select {
	case s.cmds <- c:
	case <-s.done:
	}
}

func (s *Session) emit(ctx context.Context, u Update) {
	select {
	case s.updates <- u:
	case <-ctx.Done():
	}
}

func (s *Session) run(ctx context.Context) {
	defer close(s.done)
	defer close(s.updates)
	for attempt := 0; ctx.Err() == nil; {
		s.emit(ctx, Update{Kind: UpdateState, State: StateConnecting})
		live, err := s.c.Live(ctx, s.id)
		if err == nil {
			var synced bool
			synced, err = s.serve(ctx, live)
			live.Close()
			if synced {
				attempt = 0
			}
		}
		if ctx.Err() != nil {
			return
		}
		if IsStatus(err, http.StatusNotFound) {
			s.emit(ctx, Update{Kind: UpdateState, State: StateGone, Err: core.ErrContactNotFound})
			return
		}
		s.emit(ctx, Update{Kind: UpdateState, State: StateDown, Err: err})
		s.idle(ctx, backoff(attempt))
		attempt++
	}
}

// idle waits out a reconnect delay, answering what cannot be done meanwhile.
func (s *Session) idle(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		select {
		case c := <-s.cmds:
			switch c.op {
			case opSay:
				s.emit(ctx, Update{Kind: UpdateRefused, Text: c.text, Reason: "not connected"})
			case opOlder:
				s.emit(ctx, Update{Kind: UpdateOlder, Err: ErrUnavailable})
			case opTyping, opInterrupt:
			}
		case <-timer.C:
			return
		case <-ctx.Done():
			return
		}
	}
}

// serve runs one connection until it ends. It reports whether the session
// got as far as being caught up, which is what resets the reconnect delay.
func (s *Session) serve(ctx context.Context, live *Live) (synced bool, err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	frames := make(chan Frame, 64)
	failed := make(chan error, 2)
	var readers sync.WaitGroup
	defer readers.Wait()
	defer cancel()
	readers.Go(func() {
		for {
			f, err := live.Recv(ctx)
			if err != nil {
				failed <- err
				return
			}
			select {
			case frames <- f:
			case <-ctx.Done():
				return
			}
		}
	})
	readers.Go(func() {
		tick := time.NewTicker(pingEvery)
		defer tick.Stop()
		for {
			select {
			case <-tick.C:
				if err := live.Ping(ctx); err != nil {
					failed <- err
					return
				}
			case <-ctx.Done():
				return
			}
		}
	})

	// The stream says where it begins before it says anything else.
	var ready core.Delta
	select {
	case f := <-frames:
		if f.Delta == nil || f.Delta.Type != core.DeltaReady {
			return false, errors.New("the server did not open the stream with a ready delta; is it an older contactd?")
		}
		ready = *f.Delta
	case err := <-failed:
		return false, err
	case <-time.After(10 * time.Second):
		return false, errors.New("the server opened a stream and then said nothing")
	case <-ctx.Done():
		return false, nil
	}
	s.seen = max(s.seen, ready.Seq)
	if err := s.readLog(ctx); err != nil {
		return false, err
	}
	if !s.first {
		s.outstanding = nil
		s.emit(ctx, Update{Kind: UpdateSynced})
	}
	s.first = false
	s.emit(ctx, Update{Kind: UpdateState, State: StateLive})

	for {
		select {
		case f := <-frames:
			if err := s.onFrame(ctx, f); err != nil {
				return true, err
			}
		case c := <-s.cmds:
			if err := s.onCmd(ctx, live, c); err != nil {
				return true, err
			}
		case err := <-failed:
			return true, err
		case <-ctx.Done():
			return true, nil
		}
	}
}

func (s *Session) onFrame(ctx context.Context, f Frame) error {
	switch {
	case f.Delta != nil:
		d := *f.Delta
		s.seen = max(s.seen, d.Seq)
		s.emit(ctx, Update{Kind: UpdateDelta, Delta: d})
		if d.Type == core.DeltaTyping && !d.Typing {
			// A turn just ended. The stream does not carry the record of its
			// model calls; the log does.
			return s.readLog(ctx)
		}
	case f.Refused != "":
		s.emit(ctx, Update{Kind: UpdateRefused, Text: s.popOutstanding(), Reason: f.Refused})
	default:
		s.emit(ctx, Update{Kind: UpdateAck, Seq: f.Ack, Text: s.popOutstanding()})
	}
	return nil
}

func (s *Session) popOutstanding() string {
	if len(s.outstanding) == 0 {
		return ""
	}
	text := s.outstanding[0]
	s.outstanding = s.outstanding[1:]
	return text
}

func (s *Session) onCmd(ctx context.Context, live *Live, c sessionCmd) error {
	switch c.op {
	case opSay:
		if err := live.Say(ctx, c.text); err != nil {
			s.emit(ctx, Update{Kind: UpdateRefused, Text: c.text, Reason: "not connected"})
			return err
		}
		s.outstanding = append(s.outstanding, c.text)
	case opTyping:
		return live.Typing(ctx)
	case opInterrupt:
		return live.Interrupt(ctx)
	case opOlder:
		s.older(ctx)
	}
	return nil
}

// readLog brings the caller up to date with the log: the newest page the
// first time, and after that everything since the last read.
func (s *Session) readLog(ctx context.Context) error {
	s.waitDurable(ctx, s.seen)
	if s.first && s.oldest == 0 {
		page, err := s.c.Tail(ctx, s.id, pageSize, ViewKinds())
		if err != nil {
			return err
		}
		s.start = !page.More
		if n := len(page.Events); n > 0 {
			s.oldest, s.logSeq = page.Events[0].Seq, page.Events[n-1].Seq
		}
		s.emit(ctx, Update{Kind: UpdateEvents, Events: page.Events, Start: s.start})
		return nil
	}
	for {
		page, err := s.c.After(ctx, s.id, s.logSeq, pageSize, ViewKinds())
		if err != nil {
			return err
		}
		if n := len(page.Events); n > 0 {
			s.logSeq = page.Events[n-1].Seq
			if s.oldest == 0 {
				s.oldest = page.Events[0].Seq
			}
			s.emit(ctx, Update{Kind: UpdateEvents, Events: page.Events})
		}
		if !page.More {
			return nil
		}
	}
}

// waitDurable waits, briefly, until the event with this sequence number can
// be read from the log. The stream runs ahead of the group commit by design,
// so an event a delta has just announced may be a moment from readable.
func (s *Session) waitDurable(ctx context.Context, seq uint64) {
	if seq == 0 || seq <= s.logSeq {
		return
	}
	deadline := time.Now().Add(durableWait)
	for wait := 2 * time.Millisecond; ; wait = min(wait*2, 100*time.Millisecond) {
		page, err := s.c.After(ctx, s.id, seq-1, 1, core.AllKinds)
		if err != nil || len(page.Events) > 0 || time.Now().After(deadline) {
			// On an error the read that follows reports it. Past the deadline
			// the write most likely failed, and the event will never appear.
			return
		}
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return
		}
	}
}

func (s *Session) older(ctx context.Context) {
	if s.start || s.oldest <= 1 {
		s.start = true
		s.emit(ctx, Update{Kind: UpdateOlder, Start: true})
		return
	}
	page, err := s.c.Before(ctx, s.id, s.oldest, pageSize, ViewKinds())
	if err != nil {
		s.emit(ctx, Update{Kind: UpdateOlder, Err: err})
		return
	}
	s.start = !page.More
	if len(page.Events) > 0 {
		s.oldest = page.Events[0].Seq
	}
	s.emit(ctx, Update{Kind: UpdateOlder, Events: page.Events, Start: s.start})
}
