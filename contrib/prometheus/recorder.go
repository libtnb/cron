// Package cronprom exposes cron scheduler events as Prometheus metrics.
package cronprom

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/libtnb/cron"
)

var (
	_ cron.Recorder        = (*Recorder)(nil)
	_ prometheus.Collector = (*Recorder)(nil)
)

// ErrInvalidOption reports an invalid Recorder option.
var ErrInvalidOption = errors.New("cronprom: invalid option")

type config struct {
	namespace       string
	registerer      prometheus.Registerer
	durationBuckets []float64
	latenessBuckets []float64
}

// Option configures New.
type Option func(*config) error

// WithNamespace sets the metric namespace. Default "cron".
func WithNamespace(ns string) Option {
	return func(c *config) error {
		c.namespace = ns
		return nil
	}
}

// WithRegisterer sets where collectors register. Default
// prometheus.DefaultRegisterer.
func WithRegisterer(r prometheus.Registerer) Option {
	return func(c *config) error {
		if nilLike(r) {
			return fmt.Errorf("%w: registerer is nil", ErrInvalidOption)
		}
		c.registerer = r
		return nil
	}
}

// WithDurationBuckets overrides the job duration histogram buckets.
func WithDurationBuckets(b []float64) Option {
	return func(c *config) error {
		if err := validateBuckets(b); err != nil {
			return fmt.Errorf("%w: duration buckets: %v", ErrInvalidOption, err)
		}
		c.durationBuckets = slices.Clone(b)
		return nil
	}
}

// WithLatenessBuckets overrides the missed-fire lateness histogram buckets.
func WithLatenessBuckets(b []float64) Option {
	return func(c *config) error {
		if err := validateBuckets(b); err != nil {
			return fmt.Errorf("%w: lateness buckets: %v", ErrInvalidOption, err)
		}
		c.latenessBuckets = slices.Clone(b)
		return nil
	}
}

// Recorder records scheduler activity as Prometheus metrics. Use New.
type Recorder struct {
	scheduled  *prometheus.CounterVec
	started    *prometheus.CounterVec
	completed  *prometheus.CounterVec
	duration   *prometheus.HistogramVec
	missed     *prometheus.CounterVec
	lateness   *prometheus.HistogramVec
	rejected   *prometheus.CounterVec
	canceled   *prometheus.CounterVec
	skipped    *prometheus.CounterVec
	depth      prometheus.Gauge
	dropped    prometheus.Counter
	collectors []prometheus.Collector
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
	for i, option := range opts {
		if option == nil {
			return nil, fmt.Errorf("%w: option %d is nil", ErrInvalidOption, i)
		}
		if err := option(&cfg); err != nil {
			return nil, err
		}
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
			Help: "Fires late enough to invoke the missed-fire policy.",
		}, []string{"name"}),
		lateness: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: cfg.namespace, Name: "job_lateness_seconds",
			Help: "How late missed fires were.", Buckets: cfg.latenessBuckets,
		}, []string{"name"}),
		rejected: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: cfg.namespace, Name: "jobs_rejected_total",
			Help: "Fires rejected before job execution, by reason.",
		}, []string{"name", "reason"}),
		canceled: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: cfg.namespace, Name: "jobs_canceled_total",
			Help: "Reserved fires canceled before job execution.",
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
			Namespace: cfg.namespace, Name: "observer_events_dropped_total",
			Help: "Observer events dropped because the queue was full.",
		}),
	}

	r.collectors = []prometheus.Collector{
		r.scheduled, r.started, r.completed, r.duration,
		r.missed, r.lateness, r.rejected, r.canceled,
		r.skipped, r.depth, r.dropped,
	}
	if err := cfg.registerer.Register(r); err != nil {
		return nil, err
	}
	return r, nil
}

// Describe implements prometheus.Collector.
func (r *Recorder) Describe(ch chan<- *prometheus.Desc) {
	for _, collector := range r.collectors {
		collector.Describe(ch)
	}
}

// Collect implements prometheus.Collector.
func (r *Recorder) Collect(ch chan<- prometheus.Metric) {
	for _, collector := range r.collectors {
		collector.Collect(ch)
	}
}

// Record implements cron.Recorder. Job names become label values, so callers
// should keep them low-cardinality.
func (r *Recorder) Record(raw cron.Event) {
	switch event := raw.(type) {
	case cron.ScheduleEvent:
		if !event.Next.IsZero() {
			r.scheduled.WithLabelValues(event.Entry.Name).Inc()
		}
	case cron.JobStartEvent:
		r.started.WithLabelValues(event.Entry.Name).Inc()
	case cron.JobCompleteEvent:
		status := "ok"
		if event.Err != nil {
			status = "error"
		}
		r.completed.WithLabelValues(event.Entry.Name, status).Inc()
		r.duration.WithLabelValues(event.Entry.Name).Observe(event.Duration.Seconds())
	case cron.MissedFireEvent:
		r.missed.WithLabelValues(event.Entry.Name).Inc()
		r.lateness.WithLabelValues(event.Entry.Name).Observe(event.Lateness.Seconds())
	case cron.RejectedFireEvent:
		r.rejected.WithLabelValues(event.Entry.Name, event.Reason.String()).Inc()
	case cron.CanceledFireEvent:
		r.canceled.WithLabelValues(event.Entry.Name).Inc()
	case cron.SkippedFireEvent:
		r.skipped.WithLabelValues(event.Entry.Name, event.Reason.String()).Inc()
	case cron.QueueDepthEvent:
		r.depth.Set(float64(event.Depth))
	case cron.ObserverDropEvent:
		r.dropped.Inc()
	}
}

func validateBuckets(buckets []float64) error {
	for i, bucket := range buckets {
		invalidBucket := math.IsNaN(bucket) || math.IsInf(bucket, 0) || bucket <= 0
		if invalidBucket {
			return fmt.Errorf("bucket %d must be finite and positive", i)
		}
		if i > 0 && bucket <= buckets[i-1] {
			return fmt.Errorf("bucket %d must be greater than the previous bucket", i)
		}
	}
	return nil
}

func nilLike(value any) bool {
	if value == nil {
		return true
	}
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}
