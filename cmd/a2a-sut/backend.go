package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/quinnovator/contactd/internal/helpers"
)

type backend struct {
	mu      sync.Mutex
	tasks   map[a2a.TaskID]*a2a.Task
	configs map[a2a.TaskID]map[string]*a2a.PushConfig
	subs    map[a2a.TaskID]map[chan a2a.Event]bool
}

func newBackend() *backend {
	return &backend{tasks: make(map[a2a.TaskID]*a2a.Task), configs: make(map[a2a.TaskID]map[string]*a2a.PushConfig), subs: make(map[a2a.TaskID]map[chan a2a.Event]bool)}
}

// All stored values are immutable snapshots; replacing a task never mutates an
// object already handed to the HTTP encoder or to another subscriber.
func snapshot(t *a2a.Task, n *int, artifacts bool) *a2a.Task {
	c := *t
	c.History = slices.Clone(t.History)
	c.Artifacts = slices.Clone(t.Artifacts)
	if n != nil && *n >= 0 && *n < len(c.History) {
		c.History = c.History[len(c.History)-*n:]
	}
	if !artifacts {
		c.Artifacts = nil
	}
	return &c
}

func status(state a2a.TaskState) a2a.TaskStatus {
	now := time.Now().UTC().Truncate(time.Millisecond)
	return a2a.TaskStatus{State: state, Timestamp: &now}
}

func (b *backend) Send(ctx context.Context, r *a2a.SendMessageRequest, _ bool) (*helpers.Watch, error) {
	if r == nil || r.Message == nil || r.Message.ID == "" || len(r.Message.Parts) == 0 {
		return nil, a2a.ErrInvalidParams
	}
	m := r.Message
	if strings.HasPrefix(m.ID, "tck-message-response") {
		return &helpers.Watch{Message: a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("Direct message response")), Close: func() {}}, nil
	}
	b.mu.Lock()
	var t *a2a.Task
	if m.TaskID != "" {
		old := b.tasks[m.TaskID]
		if old == nil {
			b.mu.Unlock()
			return nil, a2a.ErrTaskNotFound
		}
		if m.ContextID != "" && m.ContextID != old.ContextID {
			b.mu.Unlock()
			return nil, a2a.ErrInvalidParams
		}
		if old.Status.State.Terminal() {
			b.mu.Unlock()
			return nil, a2a.ErrUnsupportedOperation
		}
		t = snapshot(old, nil, true)
	} else {
		contextID := m.ContextID
		if contextID == "" {
			contextID = a2a.NewContextID()
		}
		t = &a2a.Task{ID: a2a.NewTaskID(), ContextID: contextID}
	}
	t.History = append(t.History, m)
	t.Status = status(a2a.TaskStateWorking)
	initial := snapshot(t, nil, true)
	if strings.HasPrefix(m.ID, "test-resubscribe-message-id") {
		b.tasks[t.ID] = initial
		b.mu.Unlock()
		watch, err := b.Subscribe(ctx, t.ID)
		if err != nil {
			return nil, err
		}
		// The TCK HTTP client has a 30s streaming read timeout. This scenario
		// deliberately remains alive across disconnection and resubscription.
		go b.finishDelayed(context.WithoutCancel(ctx), t.ID)
		return watch, nil
	}
	events := []a2a.Event{&a2a.TaskStatusUpdateEvent{TaskID: t.ID, ContextID: t.ContextID, Status: t.Status}}
	if part := scriptPart(m.ID); part != nil {
		artifact := &a2a.Artifact{ID: a2a.ArtifactID("output-" + string(t.ID)), Parts: []*a2a.Part{part}}
		t.Artifacts = append(t.Artifacts, artifact)
		if strings.HasPrefix(m.ID, "tck-stream-artifact-chunked") {
			first, last := *artifact, *artifact
			first.Parts = []*a2a.Part{a2a.NewTextPart("chunk-1 ")}
			last.Parts = []*a2a.Part{a2a.NewTextPart("chunk-2")}
			events = append(events,
				&a2a.TaskArtifactUpdateEvent{TaskID: t.ID, ContextID: t.ContextID, Artifact: &first},
				&a2a.TaskArtifactUpdateEvent{TaskID: t.ID, ContextID: t.ContextID, Artifact: &last, Append: true, LastChunk: true})
		} else {
			events = append(events, &a2a.TaskArtifactUpdateEvent{TaskID: t.ID, ContextID: t.ContextID, Artifact: artifact, LastChunk: true})
		}
	}
	state := a2a.TaskStateCompleted
	if strings.HasPrefix(m.ID, "tck-input-required") {
		state = a2a.TaskStateInputRequired
	}
	if strings.HasPrefix(m.ID, "tck-reject-task") {
		state = a2a.TaskStateRejected
	}
	t.Status = status(state)
	t.Status.Message = a2a.NewMessageForTask(a2a.MessageRoleAgent, t, a2a.NewTextPart("Hello from TCK"))
	t.History = append(t.History, t.Status.Message)
	events = append(events, &a2a.TaskStatusUpdateEvent{TaskID: t.ID, ContextID: t.ContextID, Status: t.Status})
	b.tasks[t.ID] = t
	if r.Config != nil && r.Config.PushConfig != nil {
		c := *r.Config.PushConfig
		c.TaskID = t.ID
		b.putPush(&c)
	}
	configs := make([]*a2a.PushConfig, 0, len(b.configs[t.ID]))
	for _, c := range b.configs[t.ID] {
		configs = append(configs, c)
	}
	for _, ev := range events {
		b.publish(ev)
	}
	b.mu.Unlock()
	for _, c := range configs {
		if err := deliver(ctx, c, events[len(events)-1]); err != nil {
			return nil, err
		}
	}
	var length *int
	if r.Config != nil {
		length = r.Config.HistoryLength
	}
	initial = snapshot(initial, length, true)
	return &helpers.Watch{Task: initial, Close: func() {}, Events: func(yield func(a2a.Event, error) bool) {
		for _, ev := range events {
			if !yield(ev, nil) {
				return
			}
		}
	}}, nil
}

func (b *backend) finishDelayed(ctx context.Context, id a2a.TaskID) {
	timer := time.NewTimer(time.Minute)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	t := b.tasks[id]
	if t == nil || t.Status.State.Terminal() {
		return
	}
	t = snapshot(t, nil, true)
	t.Status = status(a2a.TaskStateCompleted)
	b.tasks[id] = t
	b.publish(&a2a.TaskStatusUpdateEvent{TaskID: id, ContextID: t.ContextID, Status: t.Status})
}

func scriptPart(id string) *a2a.Part {
	switch {
	case strings.HasPrefix(id, "tck-stream-002"):
		return nil
	case strings.Contains(id, "artifact-file-url"):
		p := a2a.NewFileURLPart("https://example.com/output.txt", "text/plain")
		p.Filename = "output.txt"
		return p
	case strings.Contains(id, "artifact-file"):
		p := a2a.NewRawPart([]byte("Generated file content"))
		p.Filename = "output.txt"
		p.MediaType = "text/plain"
		return p
	case strings.Contains(id, "artifact-data"):
		return a2a.NewDataPart(map[string]any{"key": "value", "count": 42})
	case strings.Contains(id, "stream-artifact-text"):
		return a2a.NewTextPart("Streamed text content")
	case strings.Contains(id, "stream-artifact-chunked"):
		return a2a.NewTextPart("chunk-1 chunk-2")
	case strings.Contains(id, "artifact-text"):
		return a2a.NewTextPart("Generated text content")
	case strings.Contains(id, "stream-ordering"):
		return a2a.NewTextPart("Ordered output")
	case strings.Contains(id, "stream-003"):
		return a2a.NewTextPart("Stream task lifecycle")
	case strings.Contains(id, "stream"):
		return a2a.NewTextPart("Stream hello from TCK")
	default:
		return nil
	}
}

func (b *backend) Snapshot(_ context.Context, id string, n *int, artifacts bool) (*a2a.Task, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if n != nil && *n < 0 {
		return nil, a2a.ErrInvalidParams
	}
	t := b.tasks[a2a.TaskID(id)]
	if t == nil {
		return nil, a2a.ErrTaskNotFound
	}
	return snapshot(t, n, artifacts), nil
}

func (b *backend) publish(ev a2a.Event) {
	id := ev.TaskInfo().TaskID
	for ch := range b.subs[id] {
		select {
		case ch <- ev:
		default:
			close(ch)
			delete(b.subs[id], ch)
		}
	}
	if e, ok := ev.(*a2a.TaskStatusUpdateEvent); ok && e.Status.State.Terminal() {
		for ch := range b.subs[id] {
			close(ch)
		}
		delete(b.subs, id)
	}
}

func (b *backend) Subscribe(ctx context.Context, id a2a.TaskID) (*helpers.Watch, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	t := b.tasks[id]
	if t == nil {
		return nil, a2a.ErrTaskNotFound
	}
	if t.Status.State.Terminal() {
		return nil, a2a.ErrUnsupportedOperation
	}
	ch := make(chan a2a.Event, 64)
	if b.subs[id] == nil {
		b.subs[id] = make(map[chan a2a.Event]bool)
	}
	b.subs[id][ch] = true
	closeWatch := func() { b.mu.Lock(); defer b.mu.Unlock(); delete(b.subs[id], ch) }
	events := iter.Seq2[a2a.Event, error](func(yield func(a2a.Event, error) bool) {
		for {
			select {
			case ev, ok := <-ch:
				if !ok || !yield(ev, nil) {
					return
				}
			case <-ctx.Done():
				return
			}
		}
	})
	return &helpers.Watch{Task: snapshot(t, nil, true), Events: events, Close: closeWatch}, nil
}

func (b *backend) Cancel(_ context.Context, id a2a.TaskID) (*a2a.Task, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	t := b.tasks[id]
	if t == nil {
		return nil, a2a.ErrTaskNotFound
	}
	if t.Status.State.Terminal() {
		return nil, a2a.ErrTaskNotCancelable
	}
	t = snapshot(t, nil, true)
	t.Status = status(a2a.TaskStateCanceled)
	b.tasks[id] = t
	b.publish(&a2a.TaskStatusUpdateEvent{TaskID: id, ContextID: t.ContextID, Status: t.Status})
	return snapshot(t, nil, true), nil
}

func (b *backend) List(_ context.Context, r *a2a.ListTasksRequest) (*a2a.ListTasksResponse, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if r == nil {
		r = &a2a.ListTasksRequest{}
	}
	size := r.PageSize
	if size == 0 {
		size = 50
	}
	if size < 1 || size > 100 {
		return nil, a2a.ErrInvalidParams
	}
	start := 0
	if r.PageToken != "" {
		n, err := strconv.Atoi(r.PageToken)
		if err != nil || n < 0 {
			return nil, a2a.ErrInvalidParams
		}
		start = n
	}
	tasks := make([]*a2a.Task, 0, len(b.tasks))
	for _, t := range b.tasks {
		if r.ContextID != "" && t.ContextID != r.ContextID || r.Status != "" && t.Status.State != r.Status || r.StatusTimestampAfter != nil && t.Status.Timestamp.Before(*r.StatusTimestampAfter) {
			continue
		}
		tasks = append(tasks, snapshot(t, r.HistoryLength, r.IncludeArtifacts))
	}
	slices.SortFunc(tasks, func(a, c *a2a.Task) int {
		if n := c.Status.Timestamp.Compare(*a.Status.Timestamp); n != 0 {
			return n
		}
		return strings.Compare(string(a.ID), string(c.ID))
	})
	out := &a2a.ListTasksResponse{Tasks: []*a2a.Task{}, PageSize: size, TotalSize: len(tasks)}
	if start < len(tasks) {
		end := min(start+size, len(tasks))
		out.Tasks = tasks[start:end]
		if end < len(tasks) {
			out.NextPageToken = strconv.Itoa(end)
		}
	}
	return out, nil
}

func (b *backend) putPush(c *a2a.PushConfig) *a2a.PushConfig {
	copyConfig := *c
	if copyConfig.ID == "" {
		copyConfig.ID = string(a2a.NewTaskID())
	}
	if b.configs[c.TaskID] == nil {
		b.configs[c.TaskID] = make(map[string]*a2a.PushConfig)
	}
	b.configs[c.TaskID][copyConfig.ID] = &copyConfig
	return &copyConfig
}

func (b *backend) CreatePush(_ context.Context, c *a2a.PushConfig) (*a2a.PushConfig, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if c == nil || c.URL == "" {
		return nil, a2a.ErrInvalidParams
	}
	if b.tasks[c.TaskID] == nil {
		return nil, a2a.ErrTaskNotFound
	}
	return b.putPush(c), nil
}

func (b *backend) GetPush(_ context.Context, r *a2a.GetTaskPushConfigRequest) (*a2a.PushConfig, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if r == nil {
		return nil, a2a.ErrInvalidParams
	}
	if b.tasks[r.TaskID] == nil {
		return nil, a2a.ErrTaskNotFound
	}
	c := b.configs[r.TaskID][r.ID]
	if c == nil {
		return nil, a2a.ErrTaskNotFound
	}
	return c, nil
}

func (b *backend) ListPush(_ context.Context, r *a2a.ListTaskPushConfigRequest) (*a2a.ListTaskPushConfigResponse, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if r == nil {
		return nil, a2a.ErrInvalidParams
	}
	if b.tasks[r.TaskID] == nil {
		return nil, a2a.ErrTaskNotFound
	}
	cs := make([]*a2a.PushConfig, 0, len(b.configs[r.TaskID]))
	for _, c := range b.configs[r.TaskID] {
		cs = append(cs, c)
	}
	return &a2a.ListTaskPushConfigResponse{Configs: cs}, nil
}

func (b *backend) DeletePush(_ context.Context, r *a2a.DeleteTaskPushConfigRequest) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if r == nil {
		return a2a.ErrInvalidParams
	}
	if b.tasks[r.TaskID] == nil {
		return a2a.ErrTaskNotFound
	}
	delete(b.configs[r.TaskID], r.ID)
	return nil
}

// The SUT only delivers to the TCK's loopback webhook receiver. Config CRUD also
// accepts example.com fixtures, but never sends traffic to them.
func deliver(ctx context.Context, c *a2a.PushConfig, ev a2a.Event) error {
	u, err := url.Parse(c.URL)
	if err != nil {
		return fmt.Errorf("parse SUT callback: %w", err)
	}
	if u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1" {
		return nil
	}
	raw, err := json.Marshal(a2a.StreamResponse{Event: ev})
	if err != nil {
		return fmt.Errorf("encode SUT callback: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("create SUT callback: %w", err)
	}
	req.Header.Set("Content-Type", "application/a2a+json")
	if c.Auth != nil {
		req.Header.Set("Authorization", c.Auth.Scheme+" "+c.Auth.Credentials)
	}
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("deliver SUT callback: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	return nil
}
