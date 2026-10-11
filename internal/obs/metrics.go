// Package obs owns logging setup and the Prometheus collectors. The
// collectors are the one set of package-level variables the codebase allows.
package obs

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metric names follow the design doc's Observability section verbatim.
var (
	// CacheBreakTier is the histogram of PromptPlan.FirstChanged: which tier
	// breaks the cached prefix, and how often. 5 means nothing changed.
	CacheBreakTier = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "cache_break_tier",
		Help:    "Predicted first prompt tier whose bytes changed versus the previous turn (0-4; 5 = unchanged).",
		Buckets: []float64{0, 1, 2, 3, 4, 5},
	})
	// ProviderCachedTokenRatio is the headline metric: the share of input
	// tokens the provider served from its prompt cache, per model call.
	ProviderCachedTokenRatio = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "provider_cached_token_ratio",
		Help:    "Share of input tokens served from the provider prompt cache, per model call.",
		Buckets: prometheus.LinearBuckets(0, 0.1, 11),
	})
	// PredictedCachedTokenRatio is what the planner expected for the same
	// call. A gap between the two is how cache bugs get caught.
	PredictedCachedTokenRatio = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "predicted_cached_token_ratio",
		Help:    "Share of input tokens the planner predicted would be cached, per turn.",
		Buckets: prometheus.LinearBuckets(0, 0.1, 11),
	})
	// Checkpoints counts tail checkpoints by outcome.
	Checkpoints = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "checkpoints_total",
		Help: "Tail checkpoints (folds of the oldest history into a summary) by outcome.",
	}, []string{"outcome"})
	// Delegations counts sub-agents started, by mode.
	Delegations = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "delegations_total",
		Help: "Sub-agents started, by mode (spawn or fork).",
	}, []string{"mode"})
	// CachePredictionMisses counts model calls where the provider reported
	// a clearly smaller cached share than the planner predicted.
	CachePredictionMisses = promauto.NewCounter(prometheus.CounterOpts{
		Name: "cache_prediction_misses_total",
		Help: "Model calls whose reported cached share fell well below the prediction (routes that report cache usage only).",
	})
	ProviderInputTokens = promauto.NewCounter(prometheus.CounterOpts{
		Name: "provider_input_tokens_total",
		Help: "Input tokens sent to the model provider.",
	})
	ProviderCachedTokens = promauto.NewCounter(prometheus.CounterOpts{
		Name: "provider_cached_tokens_total",
		Help: "Input tokens the provider served from its prompt cache.",
	})
	ProviderOutputTokens = promauto.NewCounter(prometheus.CounterOpts{
		Name: "provider_output_tokens_total",
		Help: "Output tokens produced by the model provider.",
	})

	// TurnOverheadMS is pure platform cost: debounce firing to the first
	// model request being sent.
	TurnOverheadMS = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "turn_overhead_ms",
		Help:    "Milliseconds from the debounce firing to the model request being sent.",
		Buckets: msBuckets(),
	})
	Turns = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "turns_total",
		Help: "Turns by outcome.",
	}, []string{"outcome"})

	RecallStage1MS = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "recall_stage1_ms",
		Help:    "Milliseconds spent in the bi-encoder centroid scan.",
		Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10},
	})
	RerankMS = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "rerank_ms",
		Help:    "Milliseconds spent in the cross-encoder rerank.",
		Buckets: msBuckets(),
	})
	RerankBatchSize = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "rerank_batch_size",
		Help:    "Pairs scored per rerank.",
		Buckets: []float64{1, 2, 4, 8, 12, 16, 20, 32},
	})
	EmbedQueueDepth = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "embed_queue_depth",
		Help: "Requests waiting in an inference micro-batcher.",
	}, []string{"model", "priority"})
	InferBatchSize = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "infer_batch_size",
		Help:    "Inputs per model call.",
		Buckets: []float64{1, 2, 4, 8, 16, 24, 32},
	}, []string{"model"})

	HydratedActors = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hydrated_actors",
		Help: "Contacts currently held in memory.",
	})
	HydrateMS = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "hydrate_ms",
		Help:    "Milliseconds to hydrate a cold contact.",
		Buckets: msBuckets(),
	})
	Hibernations = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hibernations_total",
		Help: "Actors retired, by reason.",
	}, []string{"reason"})

	WriterBatchSize = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "writer_batch_size",
		Help:    "Requests committed per transaction by the group-commit writer.",
		Buckets: prometheus.ExponentialBuckets(1, 2, 10),
	})
	CommitMS = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "commit_ms",
		Help:    "Milliseconds per group-commit transaction.",
		Buckets: msBuckets(),
	})

	EnvLeaseMS = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "env_lease_ms",
		Help:    "Milliseconds to obtain a REPL environment lease.",
		Buckets: msBuckets(),
	}, []string{"tier"})
	SpeculativeRestoreHits = promauto.NewCounter(prometheus.CounterOpts{
		Name: "speculative_restore_hits",
		Help: "Tool calls whose environment was already restoring or ready when the arguments finished.",
	})
)

// The labelled series below are touched once at start-up so they are exported
// at zero rather than appearing only after their first event; dashboards and
// alerts can then tell "nothing happened" from "not instrumented".
var _ = preregister()

func preregister() struct{} {
	for _, model := range []string{"embedder", "reranker"} {
		InferBatchSize.WithLabelValues(model)
		for _, priority := range []string{"critical", "background"} {
			EmbedQueueDepth.WithLabelValues(model, priority)
		}
	}
	for _, tier := range []string{"0", "1"} {
		EnvLeaseMS.WithLabelValues(tier)
	}
	for _, reason := range []string{"idle", "pressure", "shutdown", "write_failed"} {
		Hibernations.WithLabelValues(reason)
	}
	for _, outcome := range []string{"ok", "error", "interrupted", "replanned", "step_limit"} {
		Turns.WithLabelValues(outcome)
	}
	return struct{}{}
}

func msBuckets() []float64 {
	return []float64{0.25, 0.5, 1, 2.5, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000}
}

// SinceMS returns the milliseconds elapsed since t, for histogram observations.
func SinceMS(t time.Time) float64 {
	return float64(time.Since(t)) / float64(time.Millisecond)
}

// Handler serves the default registry in the Prometheus text format.
func Handler() http.Handler { return promhttp.Handler() }
