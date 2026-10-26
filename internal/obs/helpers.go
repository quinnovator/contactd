package obs

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Helper and A2A collectors use bounded labels; task IDs and content are never labels.
var (
	HelperTasks  = promauto.NewGauge(prometheus.GaugeOpts{Name: "contactd_helper_tasks", Help: "Active helper tasks."})
	HelperChecks = promauto.NewCounterVec(prometheus.CounterOpts{Name: "contactd_helper_checks_total", Help: "Scheduled helper checks by dispatch outcome."}, []string{"outcome"})
	HelperTurns  = promauto.NewCounterVec(prometheus.CounterOpts{Name: "contactd_helper_turns_total", Help: "Helper turns by outcome."}, []string{"outcome"})
	A2ARequests  = promauto.NewCounterVec(prometheus.CounterOpts{Name: "contactd_a2a_requests_total", Help: "A2A operations by method and result."}, []string{"method", "result"})
	A2APush      = promauto.NewCounterVec(prometheus.CounterOpts{Name: "contactd_a2a_push_total", Help: "A2A callback attempts by kind and result."}, []string{"kind", "result"})
	A2AWebhook   = promauto.NewCounterVec(prometheus.CounterOpts{Name: "contactd_a2a_webhook_total", Help: "A2A callbacks by handling result."}, []string{"result"})
)
