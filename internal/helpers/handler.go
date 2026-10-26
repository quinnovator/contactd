package helpers

import (
	"context"
	"fmt"
	"iter"
	"log/slog"
	"net/http"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
)

// Handler maps durable backend operations to the SDK transport contract.
type Handler struct{ backend Backend }

// NewHandler wraps the backend in the SDK's version, capability and content checks.
func NewHandler(backend Backend, card *a2a.AgentCard, log *slog.Logger) (a2asrv.RequestHandler, error) {
	enforcer, err := a2asrv.NewAgentCardEnforcer(card)
	if err != nil {
		return nil, fmt.Errorf("create card enforcer: %w", err)
	}
	return &a2asrv.InterceptedHandler{Handler: &Handler{backend: backend}, Interceptors: []a2asrv.CallInterceptor{enforcer}, Logger: log}, nil
}

func (h *Handler) SendMessage(ctx context.Context, r *a2a.SendMessageRequest) (a2a.SendMessageResult, error) {
	immediate := r != nil && r.Config != nil && r.Config.ReturnImmediately
	w, err := h.backend.Send(ctx, r, !immediate)
	if err != nil {
		return nil, fmt.Errorf("send helper message: %w", err)
	}
	defer w.Close()
	if immediate || interrupted(w.Task.Status.State) {
		return w.Task, nil
	}
	for ev, err := range w.Events {
		if err != nil {
			return nil, fmt.Errorf("wait for helper: %w", err)
		}
		if e, ok := ev.(*a2a.TaskStatusUpdateEvent); ok && interrupted(e.Status.State) {
			var length *int
			if r.Config != nil {
				length = r.Config.HistoryLength
			}
			return backendResult(h.backend.Snapshot(ctx, string(w.Task.ID), length, true))
		}
	}
	return nil, fmt.Errorf("%w: task stream closed before interruption", a2a.ErrInternalError)
}

func (h *Handler) SendStreamingMessage(ctx context.Context, r *a2a.SendMessageRequest) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		w, err := h.backend.Send(ctx, r, true)
		if err != nil {
			yield(nil, err)
			return
		}
		yieldWatch(w, yield)
	}
}

func (h *Handler) SubscribeToTask(ctx context.Context, r *a2a.SubscribeToTaskRequest) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		if r == nil {
			yield(nil, a2a.ErrInvalidParams)
			return
		}
		w, err := h.backend.Subscribe(ctx, r.ID)
		if err != nil {
			yield(nil, err)
			return
		}
		yieldWatch(w, yield)
	}
}

func yieldWatch(w *Watch, yield func(a2a.Event, error) bool) {
	defer w.Close()
	if !yield(w.Task, nil) || interrupted(w.Task.Status.State) {
		return
	}
	for ev, err := range w.Events {
		if !yield(ev, err) || err != nil {
			return
		}
	}
}

func (h *Handler) GetTask(ctx context.Context, r *a2a.GetTaskRequest) (*a2a.Task, error) {
	if r == nil {
		return nil, a2a.ErrInvalidParams
	}
	return backendResult(h.backend.Snapshot(ctx, string(r.ID), r.HistoryLength, true))
}

func (h *Handler) ListTasks(ctx context.Context, r *a2a.ListTasksRequest) (*a2a.ListTasksResponse, error) {
	return backendResult(h.backend.List(ctx, r))
}

func (h *Handler) CancelTask(ctx context.Context, r *a2a.CancelTaskRequest) (*a2a.Task, error) {
	if r == nil {
		return nil, a2a.ErrInvalidParams
	}
	return backendResult(h.backend.Cancel(ctx, r.ID))
}

func (h *Handler) GetTaskPushConfig(ctx context.Context, r *a2a.GetTaskPushConfigRequest) (*a2a.PushConfig, error) {
	return backendResult(h.backend.GetPush(ctx, r))
}

func (h *Handler) ListTaskPushConfigs(ctx context.Context, r *a2a.ListTaskPushConfigRequest) (*a2a.ListTaskPushConfigResponse, error) {
	return backendResult(h.backend.ListPush(ctx, r))
}

func (h *Handler) CreateTaskPushConfig(ctx context.Context, r *a2a.PushConfig) (*a2a.PushConfig, error) {
	return backendResult(h.backend.CreatePush(ctx, r))
}

func (h *Handler) DeleteTaskPushConfig(ctx context.Context, r *a2a.DeleteTaskPushConfigRequest) error {
	return backendError(h.backend.DeletePush(ctx, r))
}

func (h *Handler) GetExtendedAgentCard(context.Context, *a2a.GetExtendedAgentCardRequest) (*a2a.AgentCard, error) {
	return nil, a2a.ErrExtendedCardNotConfigured
}

// HTTPHandler exposes discovery and both protocol bindings with auth and body limits.
func HTTPHandler(backend Backend, cfg Config, log *slog.Logger) (http.Handler, error) {
	card := Card(cfg)
	handler, err := NewHandler(backend, card, log)
	if err != nil {
		return nil, err
	}
	discovery, err := cardHandler(card)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.Handle("GET /.well-known/agent-card.json", discovery)
	mux.Handle("POST /a2a/helpers", authorize(a2asrv.NewJSONRPCHandler(handler), cfg))
	mux.Handle("/a2a/helpers/rest/", authorize(http.StripPrefix("/a2a/helpers/rest", a2asrv.NewRESTHandler(handler)), cfg))
	return mux, nil
}

// HTTPHandler builds the service's discovery and protocol routes.
func (s *Service) HTTPHandler() (http.Handler, error) { return HTTPHandler(s, s.cfg, s.log) }

func backendError(err error) error {
	if err != nil {
		return fmt.Errorf("helper backend: %w", err)
	}
	return nil
}
func backendResult[T any](v T, err error) (T, error) { return v, backendError(err) }
