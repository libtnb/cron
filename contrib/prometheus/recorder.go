// Package cronprom exposes cron scheduler activity as Prometheus metrics.
// Recorder implements every cron recorder sub-interface; plug it in with
// cron.WithRecorder. Job names become label values, so keep them low-
// cardinality.
package cronprom

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/libtnb/cron"
)

// Compile-time interface conformance.
var (
	_ cron.JobScheduledRecorder = (*Recorder)(nil)
	_ cron.JobStartedRecorder   = (*Recorder)(nil)
	_ cron.JobCompletedRecorder = (*Recorder)(nil)
	_ cron.JobMissedRecorder    = (*Recorder)(nil)
	_ cron.JobSkippedRecorder   = (*Recorder)(nil)
	_ cron.QueueDepthRecorder   = (*Recorder)(nil)
	_ cron.HookDroppedRecorder  = (*Recorder)(nil)
)

type config struct {
	namespace       string
	registerer      prometheus.Registerer
	durationBuckets []float64
	latenessBuckets []float64
}

// Option configures New.
type Option func(*config)

// WithNamespace sets the metric namespace. Default "cron".
func WithNamespace(ns string) Option {
	return func(c *config) { c.namespace = ns }
}

// WithRegisterer sets where collectors register. Default
// prometheus.DefaultRegisterer.
func WithRegisterer(r prometheus.Registerer) Option {
	return func(c *config) { c.registerer = r }
}

// WithDurationBuckets overrides the job duration histogram buckets.
func WithDurationBuckets(b []float64) Option {
	return func(c *config) { c.durationBuckets = b }
}

// WithLatenessBuckets overrides the missed-fire lateness histogram buckets.
func WithLatenessBuckets(b []float64) Option {
	return func(c *config) { c.latenessBuckets = b }
}

// Recorder records scheduler activity as Prometheus metrics. Use New.
type Recorder struct {
	scheduled *prometheus.CounterVec
	started   *prometheus.CounterVec
	completed *prometheus.CounterVec
	duration  *prometheus.HistogramVec
	missed    *prometheus.CounterVec
	lateness  *prometheus.HistogramVec
	skipped   *prometheus.CounterVec
	depth     prometheus.Gauge
	dropped   prometheus.Counter
}

// New builds and registers a Recorder. It returns the registration error if
// any collector clashes with an existing one.
func New(opts ...Option) (*Recorder, error) {
	cfg := config{
		namespace:       "cron",
		registerer:      prometheus.DefaultRegisterer,
		durationBuckets: prometheus.DefBuckets,
		latenessBuckets: []float64{.1, .5, 1, 5, 15, 60, 300, 900, 3600},
	}
	for _, o := range opts {
		o(&cfg)
	}

	r := &Recorder{
		scheduled: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: cfg.namespace, Name: "jobs_scheduled_total",
			Help: "Next-fire schedulings, per entry name.",
		}, []string{"name"}),
		started: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: cfg.namespace, Name: "jobs_started_total",
			Help: "Job invocations started.",
		}, []string{"name"}),
		completed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: cfg.namespace, Name: "jobs_completed_total",
			Help: "Job invocations completed, by status ok|error.",
		}, []string{"name", "status"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: cfg.namespace, Name: "job_duration_seconds",
			Help: "Job run duration.", Buckets: cfg.durationBuckets,
		}, []string{"name"}),
		missed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: cfg.namespace, Name: "jobs_missed_total",
			Help: "Missed fires and concurrency rejections.",
		}, []string{"name"}),
		lateness: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: cfg.namespace, Name: "job_lateness_seconds",
			Help: "How late missed fires were.", Buckets: cfg.latenessBuckets,
		}, []string{"name"}),
		skipped: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: cfg.namespace, Name: "jobs_skipped_total",
			Help: "Fires suppressed by distributed coordination, by reason.",
		}, []string{"name", "reason"}),
		depth: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: cfg.namespace, Name: "queue_depth",
			Help: "Entries currently scheduled in the timer heap.",
		}),
		dropped: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: cfg.namespace, Name: "hook_events_dropped_total",
			Help: "Hook events dropped because the buffer was full.",
		}),
	}

	for _, c := range []prometheus.Collector{
		r.scheduled, r.started, r.completed, r.duration,
		r.missed, r.lateness, r.skipped, r.depth, r.dropped,
	} {
		if err := cfg.registerer.Register(c); err != nil {
			return nil, err
		}
	}
	return r, nil
}

func (r *Recorder) JobScheduled(name string) { r.scheduled.WithLabelValues(name).Inc() }
func (r *Recorder) JobStarted(name string)   { r.started.WithLabelValues(name).Inc() }

func (r *Recorder) JobCompleted(name string, dur time.Duration, err error) {
	status := "ok"
	if err != nil {
		status = "error"
	}
	r.completed.WithLabelValues(name, status).Inc()
	r.duration.WithLabelValues(name).Observe(dur.Seconds())
}

func (r *Recorder) JobMissed(name string, lateness time.Duration) {
	r.missed.WithLabelValues(name).Inc()
	r.lateness.WithLabelValues(name).Observe(lateness.Seconds())
}

func (r *Recorder) JobSkipped(name string, reason cron.SkipReason) {
	r.skipped.WithLabelValues(name, reason.String()).Inc()
}

func (r *Recorder) QueueDepth(n int) { r.depth.Set(float64(n)) }
func (r *Recorder) HookDropped()     { r.dropped.Inc() }
