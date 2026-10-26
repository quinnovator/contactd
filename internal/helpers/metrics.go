package helpers

import (
	"context"
	"sync"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/quinnovator/contactd/internal/obs"
)

type requestMetrics struct {
	a2asrv.PassthroughCallInterceptor
}

type metricCallKey struct{}

func (requestMetrics) Before(ctx context.Context, _ *a2asrv.CallContext, _ *a2asrv.Request) (context.Context, any, error) {
	return context.WithValue(ctx, metricCallKey{}, &sync.Once{}), nil, nil
}

func (requestMetrics) After(ctx context.Context, call *a2asrv.CallContext, resp *a2asrv.Response) error {
	result := "ok"
	if resp.Err != nil {
		result = a2a.ErrorReason(resp.Err)
	}
	// Streaming invokes After per event; count the call's first response once.
	if once, ok := ctx.Value(metricCallKey{}).(*sync.Once); ok {
		once.Do(func() { obs.A2ARequests.WithLabelValues(call.Method(), result).Inc() })
	}
	return nil
}

func (s *Service) countActive(ctx context.Context) {
	if n, err := s.deps.Store.CountActiveHelperTasks(ctx); err == nil {
		obs.HelperTasks.Set(float64(n))
	}
}
