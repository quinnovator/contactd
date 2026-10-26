package requester

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2aclient/agentcard"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/helpers"
	"github.com/quinnovator/contactd/internal/turn"
)

// Config selects the remote agent and the externally reachable callback address.
type Config struct {
	AgentURL, Token, BaseURL string
	PerContact               int
}

// Requester implements turn.Helpers and the API's helper listing port.
type Requester struct {
	store    Store
	registry Registry
	cfg      Config
	log      *slog.Logger
	mu       sync.Mutex
	locks    map[uint64]*handleLock
	starts   sync.Mutex // serialize cap checks and row insertion, never network I/O
	cacheMu  sync.Mutex
	client   *a2aclient.Client
	expires  time.Time
	kick     chan struct{}
	inboxMu  sync.Mutex
	draining map[core.ContactID]bool
}

// New constructs a requester; discovery is lazy and cached for five minutes.
func New(store Store, registry Registry, cfg Config, log *slog.Logger) *Requester {
	if cfg.PerContact <= 0 {
		cfg.PerContact = 4
	}
	return &Requester{store: store, registry: registry, cfg: cfg, log: log, locks: make(map[uint64]*handleLock), kick: make(chan struct{}, 1), draining: make(map[core.ContactID]bool)}
}

// local reports whether the helper agent is this server, whose helper
// contacts the requester may name.
func (r *Requester) local() bool {
	return strings.TrimRight(r.cfg.AgentURL, "/") == strings.TrimRight(r.cfg.BaseURL, "/")
}

// handleLock serializes one helper; refs counts holders and waiters so the
// entry is dropped once nobody needs it.
type handleLock struct {
	sync.Mutex
	refs int
}

func (r *Requester) lock(handle uint64) func() {
	r.mu.Lock()
	m := r.locks[handle]
	if m == nil {
		m = &handleLock{}
		r.locks[handle] = m
	}
	m.refs++
	r.mu.Unlock()
	m.Lock()
	return func() {
		m.Unlock()
		r.mu.Lock()
		if m.refs--; m.refs == 0 {
			delete(r.locks, handle)
		}
		r.mu.Unlock()
	}
}

func (r *Requester) connect(ctx context.Context) (*a2aclient.Client, error) {
	r.cacheMu.Lock()
	defer r.cacheMu.Unlock()
	if r.client != nil && time.Now().Before(r.expires) {
		return r.client, nil
	}
	card, err := agentcard.DefaultResolver.Resolve(ctx, r.cfg.AgentURL)
	if err != nil {
		return nil, fmt.Errorf("discover helper agent: %w", err)
	}
	credentials := a2aclient.NewInMemoryCredentialsStore()
	credentials.Set("contactd", "bearer", a2aclient.AuthCredential(r.cfg.Token))
	c, err := a2aclient.NewFromCard(ctx, card, a2aclient.WithCallInterceptors(&a2aclient.AuthInterceptor{Service: credentials}))
	if err != nil {
		return nil, fmt.Errorf("connect helper agent: %w", err)
	}
	r.client = c
	r.expires = time.Now().Add(5 * time.Minute)
	return c, nil
}

func callContext(ctx context.Context) (context.Context, context.CancelFunc) {
	ctx = a2aclient.AttachSessionID(ctx, "contactd")
	ctx = a2aclient.AttachServiceParams(ctx, a2aclient.ServiceParams{"a2a-extensions": []string{helpers.ExtensionURI}})
	return context.WithTimeout(ctx, 15*time.Second)
}

func secret() string {
	var b [32]byte
	_, _ = rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// Start persists callback credentials before sending, so a racing push can find its owner.
func (r *Requester) Start(ctx context.Context, parent core.ContactID, req turn.HelperStart) (turn.HelperInfo, error) {
	if strings.TrimSpace(req.Brief) == "" || strings.TrimSpace(req.Title) == "" {
		return turn.HelperInfo{}, errors.New("a helper needs a title and brief")
	}
	if req.Every == 0 {
		req.Every = 15 * time.Minute
	}
	if req.For == 0 {
		req.For = 24 * time.Hour
	}
	row := core.RemoteHelper{Parent: parent, AgentURL: r.cfg.AgentURL, Title: req.Title, State: string(a2a.TaskStateSubmitted), EveryMS: req.Every.Milliseconds(), UntilTS: time.Now().Add(req.For).UnixMilli(), PushToken: secret(), PushSecret: secret(), CreatedAt: time.Now().UnixMilli()}
	r.starts.Lock()
	n, err := r.store.CountActiveRemoteHelpers(ctx, parent)
	if err == nil && n >= r.cfg.PerContact {
		err = fmt.Errorf("you already have %d active helpers", n)
	}
	if err == nil {
		row.Handle, err = r.store.InsertRemoteHelper(ctx, row)
	}
	r.starts.Unlock()
	if err != nil {
		return turn.HelperInfo{}, fmt.Errorf("start helper: %w", err)
	}
	request := &a2a.SendMessageRequest{Message: &a2a.Message{ID: secret(), Role: a2a.MessageRoleUser, Parts: a2a.ContentParts{a2a.NewTextPart(req.Brief)}}, Config: &a2a.SendMessageConfig{ReturnImmediately: true, PushConfig: &a2a.PushConfig{URL: strings.TrimRight(r.cfg.BaseURL, "/") + "/a2a/push", Token: row.PushToken, Auth: &a2a.PushAuthInfo{Scheme: "Bearer", Credentials: row.PushSecret}}}, Metadata: map[string]any{helpers.ExtensionURI: map[string]any{"title": req.Title, "every": req.Every.String(), "until": time.UnixMilli(row.UntilTS).UTC().Format(time.RFC3339Nano)}}}
	request.Message.Metadata = request.Metadata
	request.Metadata = nil
	got, err := r.send(ctx, row, request)
	if err != nil {
		// Retain a failed row to prevent handle reuse and preserve callback ownership.
		unlock := r.lock(row.Handle)
		defer unlock()
		saved, loadErr := r.store.RemoteHelper(ctx, row.Handle)
		switch {
		case loadErr != nil:
		case saved.TaskID != "" && !a2a.TaskState(saved.State).Terminal():
			// A push adopted the task while the reply was lost: it started.
			return info(saved), nil
		case saved.TaskID == "":
			abandon(&saved, err.Error())
			if e := r.store.UpdateRemoteHelper(ctx, saved); e != nil {
				r.log.WarnContext(ctx, "persist failed helper", "error", e)
			}
		}
	}
	return got, err
}

// abandon marks a helper whose start was never confirmed as failed and
// unwanted. If its task turns up after all, its pushes are acknowledged
// silently and the task is canceled (see the webhook).
func abandon(row *core.RemoteHelper, why string) {
	row.State = string(a2a.TaskStateFailed)
	row.StatusMessage = why
	row.CancelRequested = true
	row.NotifiedState = row.State // the requester was told, or never asked
	row.UpdatedAt = time.Now().UnixMilli()
}

// recoverStarts resolves starts a restart cut off before the agent
// answered. They would otherwise hold a per-contact slot for good.
func (r *Requester) recoverStarts(ctx context.Context) {
	rows, err := r.store.StrandedRemoteHelpers(ctx)
	if err != nil {
		if ctx.Err() == nil {
			r.log.ErrorContext(ctx, "load interrupted helper starts", "error", err)
		}
		return
	}
	for _, row := range rows {
		unlock := r.lock(row.Handle)
		saved, err := r.store.RemoteHelper(ctx, row.Handle)
		if err == nil && saved.TaskID == "" && saved.State == string(a2a.TaskStateSubmitted) {
			abandon(&saved, "The server restarted before the helper agent confirmed this helper. Start a new one.")
			err = r.store.UpdateRemoteHelper(ctx, saved)
		}
		unlock()
		if err != nil {
			r.log.ErrorContext(ctx, "recover interrupted helper start", "handle", row.Handle, "error", err)
		}
	}
}

// cancelUnwanted cancels a task that surfaced for an abandoned helper.
func (r *Requester) cancelUnwanted(ctx context.Context, row core.RemoteHelper) {
	go func() {
		call, cancel := callContext(context.WithoutCancel(ctx))
		defer cancel()
		c, err := r.connect(call)
		if err == nil {
			_, err = c.CancelTask(call, &a2a.CancelTaskRequest{ID: a2a.TaskID(row.TaskID)})
		}
		if err != nil && !errors.Is(err, a2a.ErrTaskNotCancelable) {
			r.log.WarnContext(call, "cancel unwanted helper task", "handle", row.Handle, "task_id", row.TaskID, "error", err)
		}
	}()
}

func (r *Requester) owned(ctx context.Context, parent core.ContactID, handle uint64) (core.RemoteHelper, error) {
	row, err := r.store.RemoteHelper(ctx, handle)
	if errors.Is(err, core.ErrTaskNotFound) || (err == nil && row.Parent != parent) {
		return row, fmt.Errorf("you have no helper %d", handle)
	}
	if err != nil {
		return row, fmt.Errorf("read helper: %w", err)
	}
	return row, nil
}

// Message sends an answer or amended brief to an existing helper.
func (r *Requester) Message(ctx context.Context, parent core.ContactID, handle uint64, text string) (turn.HelperInfo, error) {
	row, err := r.owned(ctx, parent, handle)
	if err != nil {
		return turn.HelperInfo{}, err
	}
	if a2a.TaskState(row.State).Terminal() {
		return info(row), fmt.Errorf("helper %d has already stopped", handle)
	}
	return r.send(ctx, row, &a2a.SendMessageRequest{Message: &a2a.Message{ID: secret(), TaskID: a2a.TaskID(row.TaskID), ContextID: row.ContextID, Role: a2a.MessageRoleUser, Parts: a2a.ContentParts{a2a.NewTextPart(text)}}, Config: &a2a.SendMessageConfig{ReturnImmediately: true}})
}

func (r *Requester) send(ctx context.Context, row core.RemoteHelper, request *a2a.SendMessageRequest) (turn.HelperInfo, error) {
	ctx, cancel := callContext(ctx)
	defer cancel()
	r.observe(ctx, row, "call", "SendMessage", "out", request, "Sending to helper")
	c, err := r.connect(ctx)
	var result a2a.SendMessageResult
	if err == nil {
		result, err = c.SendMessage(ctx, request)
	}
	if err != nil {
		r.observe(ctx, row, "error", "SendMessage", "in", nil, err.Error())
		return info(row), fmt.Errorf("send to helper: %w", err)
	}
	task, ok := result.(*a2a.Task)
	if !ok {
		return info(row), errors.New("the helper agent answered with a message instead of a task")
	}
	row, err = r.saveTask(ctx, row.Handle, task)
	if err != nil {
		return info(row), err
	}
	r.observe(ctx, row, "result", "SendMessage", "in", task, row.State)
	return info(row), nil
}

// Stop records cancel intent before the RPC so its push cannot wake the requester.
func (r *Requester) Stop(ctx context.Context, parent core.ContactID, handle uint64) (turn.HelperInfo, error) {
	row, err := r.owned(ctx, parent, handle)
	if err != nil {
		return turn.HelperInfo{}, err
	}
	unlock := r.lock(handle)
	row, err = r.store.RemoteHelper(ctx, handle)
	if err == nil {
		row.CancelRequested = true
		err = r.store.UpdateRemoteHelper(ctx, row)
	}
	unlock()
	if err != nil {
		return info(row), fmt.Errorf("record helper cancellation: %w", err)
	}
	ctx, cancel := callContext(ctx)
	defer cancel()
	request := &a2a.CancelTaskRequest{ID: a2a.TaskID(row.TaskID)}
	r.observe(ctx, row, "call", "CancelTask", "out", request, "Stopping helper")
	c, err := r.connect(ctx)
	var task *a2a.Task
	if err == nil {
		task, err = c.CancelTask(ctx, request)
	}
	if errors.Is(err, a2a.ErrTaskNotCancelable) {
		task, err = c.GetTask(ctx, &a2a.GetTaskRequest{ID: request.ID})
	}
	if err != nil {
		r.observe(ctx, row, "error", "CancelTask", "in", nil, err.Error())
		// Not canceled after all: a later stop the requester did not ask
		// for must still be told.
		unlock := r.lock(handle)
		if saved, e := r.store.RemoteHelper(ctx, handle); e == nil && !a2a.TaskState(saved.State).Terminal() {
			saved.CancelRequested = false
			if e := r.store.UpdateRemoteHelper(ctx, saved); e != nil {
				r.log.WarnContext(ctx, "clear helper cancellation", "handle", handle, "error", e)
			}
		}
		unlock()
		return info(row), fmt.Errorf("stop helper: %w", err)
	}
	row, err = r.saveTask(ctx, handle, task)
	r.observe(ctx, row, "result", "CancelTask", "in", task, row.State)
	return info(row), err
}

func (r *Requester) saveTask(ctx context.Context, handle uint64, task *a2a.Task) (core.RemoteHelper, error) {
	unlock := r.lock(handle)
	defer unlock()
	row, err := r.store.RemoteHelper(ctx, handle)
	if err != nil {
		return row, fmt.Errorf("load helper result: %w", err)
	}
	if row.TaskID != "" && row.TaskID != string(task.ID) {
		return row, errors.New("helper agent changed task ID")
	}
	applyTask(&row, task, r.local())
	if row.NotifiedState == string(a2a.TaskStateInputRequired) && row.State == string(a2a.TaskStateWorking) {
		// Answered: a later question must notify even if its push is the
		// first to arrive after this one.
		row.NotifiedState = row.State
	}
	if err := r.store.UpdateRemoteHelper(ctx, row); err != nil {
		return row, fmt.Errorf("save helper result: %w", err)
	}
	return row, nil
}
