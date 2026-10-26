package helpers

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/quinnovator/contactd/internal/core"
)

// Snapshot derives protocol history and reports from the contact's durable log.
func (s *Service) Snapshot(ctx context.Context, id string, length *int, artifacts bool) (*a2a.Task, error) {
	unlock, err := s.lock(ctx, id)
	if err != nil {
		return nil, err
	}
	defer unlock()
	t, err := s.deps.Store.HelperTask(ctx, id)
	if err != nil {
		return nil, protocolError(err)
	}
	return s.snapshot(ctx, t, length, artifacts)
}

func status(t core.HelperTask) (a2a.TaskStatus, error) {
	ts := time.UnixMilli(t.StatusTS).UTC()
	st := a2a.TaskStatus{State: a2a.TaskState(t.State), Timestamp: &ts}
	if t.StatusMessage != "" {
		if err := json.Unmarshal([]byte(t.StatusMessage), &st.Message); err != nil {
			return st, fmt.Errorf("decode task status: %w", err)
		}
	}
	return st, nil
}

func (s *Service) events(ctx context.Context, t core.HelperTask) ([]core.Event, error) {
	if t.Contact == 0 {
		return nil, nil
	}
	var all []core.Event
	var after uint64
	kinds := core.KindSet(0).With(core.KindUserMsg).With(core.KindAgentMsg).With(core.KindNotice)
	for {
		rows, err := s.deps.Events.EventsAfterOfKinds(ctx, t.Contact, after, 200, kinds)
		if err != nil {
			return nil, fmt.Errorf("read helper events: %w", err)
		}
		all = append(all, rows...)
		if len(rows) < 200 {
			return all, nil
		}
		after = rows[len(rows)-1].Seq
	}
}

// logIndex is what snapshots need from a helper's log, kept current by
// reading only what is new: the position (last seq read, the trigger and
// check number there) and the newest history messages and reports. It is
// read and updated under the task's lock.
type (
	logIndex struct {
		after   uint64
		trigger string
		check   int
		history []indexed
		reports []indexed
	}
	indexed struct {
		seq uint64
		ts  int64
		msg *a2a.Message
		art *a2a.Artifact
	}
)

const (
	maxHistory = 100
	maxReports = 50
	// indexSlack keeps a few entries beyond the caps, for those a terminal
	// boundary hides.
	indexSlack = 8
	// maxIndexes bounds the cache; a task whose index was evicted rebuilds
	// it from its log on its next snapshot.
	maxIndexes = 64
)

func (s *Service) logIndex(ctx context.Context, t core.HelperTask) (*logIndex, error) {
	s.mu.Lock()
	ix := s.indexes[t.ID]
	if ix == nil {
		if len(s.indexes) >= maxIndexes {
			for id := range s.indexes {
				delete(s.indexes, id)
				break
			}
		}
		ix = &logIndex{trigger: "brief", check: 1}
		s.indexes[t.ID] = ix
	}
	s.mu.Unlock()
	if t.Contact == 0 {
		return ix, nil
	}
	kinds := core.KindSet(0).With(core.KindUserMsg).With(core.KindAgentMsg).With(core.KindNotice)
	for {
		rows, err := s.deps.Events.EventsAfterOfKinds(ctx, t.Contact, ix.after, 200, kinds)
		if err != nil {
			return nil, fmt.Errorf("read helper events: %w", err)
		}
		for _, ev := range rows {
			if err := ix.read(t, ev); err != nil {
				return nil, err
			}
		}
		if len(rows) < 200 {
			return ix, nil
		}
	}
}

func (ix *logIndex) read(t core.HelperTask, ev core.Event) error {
	ix.after = ev.Seq
	switch ev.Kind {
	case core.KindUserMsg:
		var b core.MsgBody
		if err := json.Unmarshal(ev.Body, &b); err != nil {
			return fmt.Errorf("decode helper message: %w", err)
		}
		ix.trigger = "message"
		if ev.Seq == t.FirstSeq {
			ix.trigger = "brief"
		}
		text := b.Raw
		if text == "" {
			text = b.Text
		}
		msg := &a2a.Message{ID: b.MessageID, TaskID: a2a.TaskID(t.ID), ContextID: t.ContextID, Role: a2a.MessageRoleUser, Parts: a2a.ContentParts{a2a.NewTextPart(text)}}
		ix.history = keep(append(ix.history, indexed{seq: ev.Seq, ts: ev.TS, msg: msg}), maxHistory+indexSlack)
	case core.KindNotice:
		var b core.NoticeBody
		if err := json.Unmarshal(ev.Body, &b); err != nil {
			return fmt.Errorf("decode helper notice: %w", err)
		}
		if b.Source == "helper-check" {
			ix.trigger = "check"
			ix.check++
		}
	case core.KindAgentMsg:
		var b core.MsgBody
		if err := json.Unmarshal(ev.Body, &b); err != nil {
			return fmt.Errorf("decode helper report: %w", err)
		}
		if !b.Interrupted {
			ix.reports = keep(append(ix.reports, indexed{seq: ev.Seq, ts: ev.TS, art: report(ev, b.Text, ix.trigger, ix.check)}), maxReports+indexSlack)
		}
	case core.KindToolCall, core.KindToolResult, core.KindSummary, core.KindEnvSnapshot, core.KindAttempt, core.KindCheckpoint:
	}
	return nil
}

func keep(xs []indexed, n int) []indexed {
	if len(xs) <= n {
		return xs
	}
	return append(xs[:0:0], xs[len(xs)-n:]...)
}

// last returns copies of the newest n entries the task's terminal boundary
// admits, oldest first.
func last[T any](xs []indexed, t core.HelperTask, n int, get func(indexed) *T) []*T {
	var out []*T
	for i := len(xs) - 1; i >= 0 && len(out) < n; i-- {
		if beforeEnd(t, xs[i].seq, xs[i].ts) {
			v := *get(xs[i])
			out = append(out, &v)
		}
	}
	slices.Reverse(out)
	return out
}

func (s *Service) snapshot(ctx context.Context, t core.HelperTask, length *int, artifacts bool) (*a2a.Task, error) {
	limit := maxHistory
	if length != nil {
		if *length < 0 {
			return nil, a2a.ErrInvalidParams
		}
		limit = min(*length, maxHistory)
	}
	st, err := status(t)
	if err != nil {
		return nil, err
	}
	task := &a2a.Task{ID: a2a.TaskID(t.ID), ContextID: t.ContextID, Status: st, Metadata: taskMetadata(t)}
	if limit == 0 && !artifacts {
		return task, nil
	}
	ix, err := s.logIndex(ctx, t)
	if err != nil {
		return nil, err
	}
	if limit > 0 {
		task.History = last(ix.history, t, limit, func(e indexed) *a2a.Message { return e.msg })
	}
	if artifacts {
		task.Artifacts = last(ix.reports, t, maxReports, func(e indexed) *a2a.Artifact { return e.art })
	}
	return task, nil
}

func report(ev core.Event, text, trigger string, check int) *a2a.Artifact {
	name := "report"
	if trigger == "message" {
		name = "reply"
	}
	return &a2a.Artifact{ID: a2a.ArtifactID(fmt.Sprintf("report-%d", ev.Seq)), Name: name, Parts: a2a.ContentParts{a2a.NewTextPart(text)}, Metadata: map[string]any{ExtensionURI: map[string]any{"trigger": trigger, "check": check, "seq": ev.Seq}}}
}
