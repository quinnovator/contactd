package helpers

import (
	"context"
	"encoding/json"
	"fmt"
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

func (s *Service) snapshot(ctx context.Context, t core.HelperTask, length *int, artifacts bool) (*a2a.Task, error) {
	limit := 100
	if length != nil {
		if *length < 0 {
			return nil, a2a.ErrInvalidParams
		}
		limit = min(*length, 100)
	}
	st, err := status(t)
	if err != nil {
		return nil, err
	}
	task := &a2a.Task{ID: a2a.TaskID(t.ID), ContextID: t.ContextID, Status: st, Metadata: taskMetadata(t)}
	if limit == 0 && !artifacts {
		return task, nil
	}
	events, err := s.events(ctx, t)
	if err != nil {
		return nil, err
	}
	trigger := "brief"
	check := 1
	for _, ev := range events {
		if st.State.Terminal() && ev.Seq > t.TerminalSeq {
			continue
		}
		switch ev.Kind {
		case core.KindUserMsg:
			var b core.MsgBody
			if err := json.Unmarshal(ev.Body, &b); err != nil {
				return nil, fmt.Errorf("decode helper message: %w", err)
			}
			trigger = "message"
			if ev.Seq == t.FirstSeq {
				trigger = "brief"
			}
			text := b.Raw
			if text == "" {
				text = b.Text
			}
			if limit > 0 {
				task.History = append(task.History, &a2a.Message{ID: b.MessageID, TaskID: task.ID, ContextID: t.ContextID, Role: a2a.MessageRoleUser, Parts: a2a.ContentParts{a2a.NewTextPart(text)}})
			}
		case core.KindNotice:
			var b core.NoticeBody
			if err := json.Unmarshal(ev.Body, &b); err != nil {
				return nil, fmt.Errorf("decode helper notice: %w", err)
			}
			if b.Source == "helper-check" {
				trigger = "check"
				check++
			}
		case core.KindAgentMsg:
			var b core.MsgBody
			if err := json.Unmarshal(ev.Body, &b); err != nil {
				return nil, fmt.Errorf("decode helper report: %w", err)
			}
			if artifacts && !b.Interrupted {
				task.Artifacts = append(task.Artifacts, report(ev, b.Text, trigger, check))
			}
		case core.KindToolCall, core.KindToolResult, core.KindSummary, core.KindEnvSnapshot, core.KindAttempt, core.KindCheckpoint:
		}
	}
	if len(task.History) > limit {
		task.History = task.History[len(task.History)-limit:]
	}
	if len(task.Artifacts) > 50 {
		task.Artifacts = task.Artifacts[len(task.Artifacts)-50:]
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
