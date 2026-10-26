package helpers

import (
	"context"
	"fmt"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

// CreatePush validates before storing a notification configuration.
func (s *Service) CreatePush(ctx context.Context, c *a2a.PushConfig) (*a2a.PushConfig, error) {
	if c == nil {
		return nil, a2a.ErrInvalidParams
	}
	if _, err := s.deps.Store.HelperTask(ctx, string(c.TaskID)); err != nil {
		return nil, protocolError(err)
	}
	row := pushRow(c)
	if err := s.push.validate(ctx, row); err != nil {
		return nil, err
	}
	if err := s.deps.Store.PutPushConfig(ctx, row); err != nil {
		return nil, fmt.Errorf("save push config: %w", err)
	}
	s.notify()
	return pushValue(row), nil
}

// GetPush returns one config or the protocol's task-not-found sentinel.
func (s *Service) GetPush(ctx context.Context, r *a2a.GetTaskPushConfigRequest) (*a2a.PushConfig, error) {
	if r == nil {
		return nil, a2a.ErrInvalidParams
	}
	if _, err := s.deps.Store.HelperTask(ctx, string(r.TaskID)); err != nil {
		return nil, protocolError(err)
	}
	c, err := s.deps.Store.PushConfig(ctx, string(r.TaskID), r.ID)
	if err != nil {
		return nil, protocolError(err)
	}
	return pushValue(c), nil
}

// ListPush returns every configuration in stable ID order.
func (s *Service) ListPush(ctx context.Context, r *a2a.ListTaskPushConfigRequest) (*a2a.ListTaskPushConfigResponse, error) {
	if r == nil {
		return nil, a2a.ErrInvalidParams
	}
	if _, err := s.deps.Store.HelperTask(ctx, string(r.TaskID)); err != nil {
		return nil, protocolError(err)
	}
	rows, err := s.deps.Store.PushConfigs(ctx, string(r.TaskID))
	if err != nil {
		return nil, fmt.Errorf("list push configs: %w", err)
	}
	out := &a2a.ListTaskPushConfigResponse{Configs: make([]*a2a.PushConfig, 0, len(rows))}
	for _, r := range rows {
		out.Configs = append(out.Configs, pushValue(r))
	}
	return out, nil
}

// DeletePush is idempotent for missing configs on an existing task.
func (s *Service) DeletePush(ctx context.Context, r *a2a.DeleteTaskPushConfigRequest) error {
	if r == nil {
		return a2a.ErrInvalidParams
	}
	if _, err := s.deps.Store.HelperTask(ctx, string(r.TaskID)); err != nil {
		return protocolError(err)
	}
	if err := s.deps.Store.DeletePushConfig(ctx, string(r.TaskID), r.ID); err != nil {
		return fmt.Errorf("delete push config: %w", err)
	}
	return nil
}
