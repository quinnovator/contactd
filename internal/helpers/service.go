package helpers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/quinnovator/contactd/internal/actor"
	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/obs"
)

// Watch couples a snapshot with updates registered before the triggering change.
// Close must be called even when Events is not consumed.
type (
	Watch struct {
		Task    *a2a.Task
		Message *a2a.Message // Alternative immediate response for backends without a task.
		Events  iter.Seq2[a2a.Event, error]
		Close   func()
	}
	// Backend is the durable operation seam shared by the HTTP handler and TCK SUT.
	Backend interface {
		Send(context.Context, *a2a.SendMessageRequest, bool) (*Watch, error)
		Subscribe(context.Context, a2a.TaskID) (*Watch, error)
		Snapshot(context.Context, string, *int, bool) (*a2a.Task, error)
		Cancel(context.Context, a2a.TaskID) (*a2a.Task, error)
		List(context.Context, *a2a.ListTasksRequest) (*a2a.ListTasksResponse, error)
		CreatePush(context.Context, *a2a.PushConfig) (*a2a.PushConfig, error)
		GetPush(context.Context, *a2a.GetTaskPushConfigRequest) (*a2a.PushConfig, error)
		ListPush(context.Context, *a2a.ListTaskPushConfigRequest) (*a2a.ListTaskPushConfigResponse, error)
		DeletePush(context.Context, *a2a.DeleteTaskPushConfigRequest) error
	}
)

// Service owns task transitions, schedule dispatch, and push delivery.
// Row locks are separate from per-task gates; the former never span contact or network calls.
type Service struct {
	deps   Deps
	cfg    Config
	log    *slog.Logger
	mu     sync.Mutex
	gates  map[string]chan struct{}
	create chan struct{}
	broker broker
	push   *pusher
	wake   chan struct{}
	kick   chan struct{}
}

// New builds the durable helper service; Run starts background work.
func New(deps Deps, cfg Config, log *slog.Logger) *Service {
	if cfg.MinEvery <= 0 {
		cfg.MinEvery = time.Minute
	}
	if cfg.MaxActive <= 0 {
		cfg.MaxActive = 16
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 4
	}
	return &Service{deps: deps, cfg: cfg, log: log, gates: make(map[string]chan struct{}), create: make(chan struct{}, 1), push: newPusher(cfg.PushAllow), wake: make(chan struct{}, 1), kick: make(chan struct{}, 1)}
}

func (s *Service) lock(ctx context.Context, id string) (func(), error) {
	s.mu.Lock()
	gate := s.gates[id]
	if gate == nil {
		gate = make(chan struct{}, 1)
		s.gates[id] = gate
	}
	s.mu.Unlock()
	select {
	case gate <- struct{}{}:
		return func() { <-gate }, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("wait for helper task: %w", ctx.Err())
	}
}

func (s *Service) notify() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func protocolError(err error) error {
	if errors.Is(err, core.ErrTaskNotFound) {
		return fmt.Errorf("%w: %w", a2a.ErrTaskNotFound, err)
	}
	return err
}

func taskInfo(t core.HelperTask) a2a.TaskInfo {
	return a2a.TaskInfo{TaskID: a2a.TaskID(t.ID), ContextID: t.ContextID}
}

func (s *Service) transition(ctx context.Context, t *core.HelperTask, state a2a.TaskState, text string) error {
	defer s.countActive(ctx)
	if a2a.TaskState(t.State).Terminal() {
		return fmt.Errorf("%w: task is terminal", a2a.ErrUnsupportedOperation)
	}
	if state.Terminal() {
		events, err := s.events(ctx, *t)
		if err != nil {
			return err
		}
		if len(events) > 0 {
			t.TerminalSeq = events[len(events)-1].Seq
		}
	}
	t.State = string(state)
	t.StatusTS = time.Now().UnixMilli()
	t.StatusMessage = ""
	if text != "" {
		raw, err := json.Marshal(a2a.NewMessageForTask(a2a.MessageRoleAgent, taskInfo(*t), a2a.NewTextPart(text)))
		if err != nil {
			return fmt.Errorf("encode status message: %w", err)
		}
		t.StatusMessage = string(raw)
	}
	if state != a2a.TaskStateWorking {
		t.NextDue = 0
	}
	if err := s.deps.Store.UpdateHelperTask(ctx, *t); err != nil {
		return fmt.Errorf("transition helper: %w", err)
	}
	s.notify()
	return nil
}

// Send validates and commits a new brief or a continuation. No caller-supplied task ID creates a task.
func (s *Service) Send(ctx context.Context, req *a2a.SendMessageRequest, watch bool) (*Watch, error) {
	if req == nil {
		return nil, a2a.ErrInvalidParams
	}
	text, err := messageText(req.Message)
	if err != nil {
		return nil, err
	}
	var length *int
	var push *a2a.PushConfig
	if req.Config != nil {
		length = req.Config.HistoryLength
		push = req.Config.PushConfig
	}
	if length != nil && *length < 0 {
		return nil, fmt.Errorf("%w: historyLength must be nonnegative", a2a.ErrInvalidParams)
	}
	if push != nil {
		if err := s.push.validate(ctx, pushRow(push)); err != nil {
			return nil, err
		}
	}
	id := string(req.Message.TaskID)
	fresh := id == ""
	var p schedule
	if fresh {
		p, err = parseSchedule(req.Message.Metadata, text, time.Now(), s.cfg.MinEvery)
		if err != nil {
			return nil, err
		}
		id = string(a2a.NewTaskID())
	}
	unlock, err := s.lock(ctx, id)
	if err != nil {
		return nil, err
	}
	defer unlock()
	w := &Watch{Close: func() {}}
	if watch {
		sub, cancel := s.broker.subscribe(a2a.TaskID(id))
		w.Events = s.broker.stream(ctx, sub, cancel)
		w.Close = cancel
	}
	success := false
	defer func() {
		if !success {
			w.Close()
		}
	}()
	var t core.HelperTask
	if fresh {
		select {
		case s.create <- struct{}{}:
		case <-ctx.Done():
			return nil, fmt.Errorf("create helper: %w", ctx.Err())
		}
		t, err = s.createTask(ctx, id, req.Message.ContextID, p)
		<-s.create
	} else {
		t, err = s.deps.Store.HelperTask(ctx, id)
	}
	if err != nil {
		return nil, protocolError(err)
	}
	if !fresh && a2a.TaskState(t.State).Terminal() {
		return nil, a2a.ErrUnsupportedOperation
	}
	if req.Message.ContextID != "" && req.Message.ContextID != t.ContextID {
		return nil, fmt.Errorf("%w: contextId does not match the task", a2a.ErrInvalidParams)
	}
	if push != nil {
		row := pushRow(push)
		row.TaskID = id
		if err := s.deps.Store.PutPushConfig(ctx, row); err != nil {
			return nil, fmt.Errorf("save push config: %w", err)
		}
	}
	if !a2a.TaskState(t.State).Terminal() {
		duplicate := false
		if !fresh {
			events, err := s.deps.Events.EventsBefore(ctx, t.Contact, 0, 50, core.AllKinds)
			if err != nil {
				return nil, fmt.Errorf("check duplicate message: %w", err)
			}
			for _, ev := range events {
				if ev.Kind == core.KindUserMsg {
					var b core.MsgBody
					if err := json.Unmarshal(ev.Body, &b); err != nil {
						return nil, fmt.Errorf("decode message: %w", err)
					}
					if b.MessageID == req.Message.ID {
						duplicate = true
						break
					}
				}
			}
		}
		if !duplicate {
			rendered := text
			if fresh {
				rendered = renderBrief(t.Title, p.every.String(), p.until, text)
			}
			seq, sendErr := s.deps.Contacts.MessageFrom(ctx, t.Contact, rendered, text, req.Message.ID)
			if sendErr != nil {
				if fresh {
					saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
					err = s.transition(saveCtx, &t, a2a.TaskStateFailed, "could not start the helper")
					cancel()
					if err != nil {
						return nil, err
					}
				} else {
					return nil, fmt.Errorf("send helper message: %w", sendErr)
				}
			} else if fresh || t.State == string(a2a.TaskStateInputRequired) {
				if fresh {
					t.FirstSeq = seq
					t.Checks = 1
				}
				t.NextDue = time.Now().UnixMilli() + t.EveryMS
				if err := s.transition(ctx, &t, a2a.TaskStateWorking, ""); err != nil {
					return nil, err
				}
			}
		}
	}
	w.Task, err = s.snapshot(ctx, t, length, true)
	if err != nil {
		return nil, err
	}
	success = true
	s.notify()
	return w, nil
}

func (s *Service) createTask(ctx context.Context, id, contextID string, p schedule) (core.HelperTask, error) {
	defer s.countActive(ctx)
	if contextID == "" {
		contextID = a2a.NewContextID()
	}
	now := time.Now().UnixMilli()
	t := core.HelperTask{ID: id, ContextID: contextID, Title: p.title, State: string(a2a.TaskStateSubmitted), StatusTS: now, EveryMS: p.every.Milliseconds(), UntilTS: p.until.UnixMilli(), MaxChecks: p.maxChecks, NextDue: now + p.every.Milliseconds(), PushedState: string(a2a.TaskStateSubmitted), CreatedAt: now}
	n, err := s.deps.Store.CountActiveHelperTasks(ctx)
	if err != nil {
		return t, fmt.Errorf("count helper tasks: %w", err)
	}
	if n >= s.cfg.MaxActive {
		t.State = string(a2a.TaskStateRejected)
		t.NextDue = 0
		raw, err := json.Marshal(a2a.NewMessageForTask(a2a.MessageRoleAgent, taskInfo(t), a2a.NewTextPart("The helper agent is at its active task limit.")))
		if err != nil {
			return t, fmt.Errorf("encode rejection: %w", err)
		}
		t.StatusMessage = string(raw)
	} else {
		t.Contact, err = s.deps.Creator.CreateContact(ctx, core.AgentConfig{Helper: true, System: helperSystem(t.Title), Provider: s.cfg.HelperProvider, Model: s.cfg.HelperModel, Effort: s.cfg.HelperEffort, Tools: s.cfg.HelperTools}, "", now)
		if err != nil {
			return t, fmt.Errorf("create helper contact: %w", err)
		}
	}
	if err := s.deps.Store.InsertHelperTask(ctx, t); err != nil {
		return t, fmt.Errorf("insert helper task: %w", err)
	}
	return t, nil
}

// Cancel commits a terminal state without interrupting a running contact turn.
func (s *Service) Cancel(ctx context.Context, id a2a.TaskID) (*a2a.Task, error) {
	unlock, err := s.lock(ctx, string(id))
	if err != nil {
		return nil, err
	}
	defer unlock()
	t, err := s.deps.Store.HelperTask(ctx, string(id))
	if err != nil {
		return nil, protocolError(err)
	}
	if a2a.TaskState(t.State).Terminal() {
		return nil, a2a.ErrTaskNotCancelable
	}
	if err := s.transition(ctx, &t, a2a.TaskStateCanceled, "Stopped by the requester."); err != nil {
		return nil, err
	}
	return s.snapshot(ctx, t, nil, true)
}

// Subscribe registers before reading the current snapshot, preventing a lost terminal event.
func (s *Service) Subscribe(ctx context.Context, id a2a.TaskID) (*Watch, error) {
	unlock, err := s.lock(ctx, string(id))
	if err != nil {
		return nil, err
	}
	defer unlock()
	t, err := s.deps.Store.HelperTask(ctx, string(id))
	if err != nil {
		return nil, protocolError(err)
	}
	if a2a.TaskState(t.State).Terminal() {
		return nil, a2a.ErrUnsupportedOperation
	}
	sub, cancel := s.broker.subscribe(id)
	task, err := s.snapshot(ctx, t, nil, true)
	if err != nil {
		cancel()
		return nil, err
	}
	return &Watch{Task: task, Events: s.broker.stream(ctx, sub, cancel), Close: cancel}, nil
}

// List applies protocol filtering and opaque cursor pagination.
func (s *Service) List(ctx context.Context, req *a2a.ListTasksRequest) (*a2a.ListTasksResponse, error) {
	if req == nil {
		return nil, a2a.ErrInvalidParams
	}
	size := req.PageSize
	if size == 0 {
		size = 50
	}
	if size < 1 || size > 100 {
		return nil, fmt.Errorf("%w: pageSize must be 1 to 100", a2a.ErrInvalidParams)
	}
	f := core.HelperTaskFilter{ContextID: req.ContextID, State: string(req.Status), Limit: size + 1}
	if req.StatusTimestampAfter != nil {
		f.After = req.StatusTimestampAfter.UnixMilli()
	}
	if req.PageToken != "" {
		raw, err := base64.RawURLEncoding.DecodeString(req.PageToken)
		if err != nil {
			return nil, a2a.ErrInvalidParams
		}
		f.Cursor = string(raw)
		stamp, id, ok := strings.Cut(f.Cursor, ":")
		if _, err := strconv.ParseInt(stamp, 10, 64); err != nil || !ok || id == "" {
			return nil, a2a.ErrInvalidParams
		}
	}
	rows, err := s.deps.Store.ListHelperTasks(ctx, f)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	total, err := s.deps.Store.CountHelperTasks(ctx, f)
	if err != nil {
		return nil, fmt.Errorf("count tasks: %w", err)
	}
	out := &a2a.ListTasksResponse{Tasks: make([]*a2a.Task, 0, min(size, len(rows))), PageSize: size, TotalSize: total}
	if len(rows) > size {
		rows = rows[:size]
		last := rows[len(rows)-1]
		out.NextPageToken = base64.RawURLEncoding.EncodeToString(fmt.Appendf(nil, "%d:%s", last.CreatedAt, last.ID))
	}
	length := req.HistoryLength
	if length == nil {
		n := 0
		length = &n
	}
	for _, r := range rows {
		task, err := s.Snapshot(ctx, r.ID, length, req.IncludeArtifacts)
		if err != nil {
			return nil, err
		}
		out.Tasks = append(out.Tasks, task)
	}
	return out, nil
}

// HelperTurnEnded returns immediately; per-task gates preserve committed row transitions.
func (s *Service) HelperTurnEnded(ctx context.Context, contact core.ContactID, o actor.HelperOutcome) {
	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if err := s.outcome(ctx, contact, o); err != nil && !errors.Is(err, context.Canceled) {
			s.log.ErrorContext(ctx, "record helper outcome", "contact_id", contact, "error", err)
		}
	}()
}

func (s *Service) outcome(ctx context.Context, contact core.ContactID, o actor.HelperOutcome) error {
	t, err := s.deps.Store.HelperTaskByContact(ctx, contact)
	if err != nil {
		return fmt.Errorf("find helper task: %w", err)
	}
	unlock, err := s.lock(ctx, t.ID)
	if err != nil {
		return err
	}
	defer unlock()
	t, err = s.deps.Store.HelperTask(ctx, t.ID)
	if err != nil {
		return fmt.Errorf("load helper task: %w", err)
	}
	if a2a.TaskState(t.State).Terminal() {
		s.notify()
		return nil
	}
	defer func() {
		if t.LastOutcome != "" {
			obs.HelperTurns.WithLabelValues(t.LastOutcome).Inc()
		}
	}()
	t.LastCheckAt = time.Now().UnixMilli()
	t.Failures = 0
	switch o.Kind {
	case actor.OutcomeReplied:
		t.LastOutcome = "reported"
	case actor.OutcomeQuiet:
		t.LastOutcome = "quiet"
	case actor.OutcomeAsked:
		t.LastOutcome = "asked"
		return s.transition(ctx, &t, a2a.TaskStateInputRequired, o.Text)
	case actor.OutcomeFinished:
		t.LastOutcome = "finished"
		return s.transition(ctx, &t, a2a.TaskStateCompleted, o.Text)
	case actor.OutcomeFailed:
		old, err := s.deps.Store.HelperTask(ctx, t.ID)
		if err != nil {
			return fmt.Errorf("load helper failures: %w", err)
		}
		t.Failures = old.Failures + 1
		t.LastOutcome = "failed"
		if t.Failures >= 3 {
			return s.transition(ctx, &t, a2a.TaskStateFailed, "Stopped after 3 failed checks: "+o.Text)
		}
	}
	if err := s.deps.Store.UpdateHelperTask(ctx, t); err != nil {
		return fmt.Errorf("save helper outcome: %w", err)
	}
	s.notify()
	return nil
}
