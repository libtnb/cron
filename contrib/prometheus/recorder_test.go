package cronprom_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/libtnb/cron"
	"github.com/libtnb/cron/contrib/prometheus"
)

func newRecorder(t *testing.T) (*cronprom.Recorder, *prometheus.Registry) {
	t.Helper()
	reg := prometheus.NewRegistry()
	r, err := cronprom.New(cronprom.WithRegisterer(reg))
	if err != nil {
		t.Fatal(err)
	}
	return r, reg
}

func TestRecorder_DirectCalls(t *testing.T) {
	r, reg := newRecorder(t)
	entry := cron.EntryRef{Name: "a"}

	r.Record(cron.ScheduleEvent{Entry: entry, Next: time.Now()})
	r.Record(cron.JobStartEvent{Entry: entry})
	r.Record(cron.JobCompleteEvent{Entry: entry, Duration: 50 * time.Millisecond})
	r.Record(cron.JobCompleteEvent{Entry: entry, Duration: time.Second, Err: errors.New("boom")})
	r.Record(cron.MissedFireEvent{Entry: entry, Lateness: 3 * time.Second})
	r.Record(cron.RejectedFireEvent{Entry: entry, Reason: cron.RejectConcurrencyLimit})
	r.Record(cron.CanceledFireEvent{Entry: entry, Cause: context.Canceled})
	r.Record(cron.SkippedFireEvent{Entry: entry, Reason: cron.SkipAlreadyClaimed})
	r.Record(cron.QueueDepthEvent{Depth: 7})
	r.Record(cron.ObserverDropEvent{Dropped: 1})

	expected := `
# HELP cron_jobs_completed_total Job invocations completed, by status ok|error.
# TYPE cron_jobs_completed_total counter
cron_jobs_completed_total{name="a",status="error"} 1
cron_jobs_completed_total{name="a",status="ok"} 1
# HELP cron_jobs_skipped_total Fires suppressed by distributed coordination, by reason.
# TYPE cron_jobs_skipped_total counter
cron_jobs_skipped_total{name="a",reason="already-claimed"} 1
# HELP cron_jobs_rejected_total Fires rejected before job execution, by reason.
# TYPE cron_jobs_rejected_total counter
cron_jobs_rejected_total{name="a",reason="concurrency-limit"} 1
# HELP cron_jobs_canceled_total Reserved fires canceled before job execution.
# TYPE cron_jobs_canceled_total counter
cron_jobs_canceled_total{name="a"} 1
# HELP cron_queue_depth Entries currently scheduled in the timer heap.
# TYPE cron_queue_depth gauge
cron_queue_depth 7
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected),
		"cron_jobs_completed_total", "cron_jobs_skipped_total", "cron_jobs_rejected_total",
		"cron_jobs_canceled_total", "cron_queue_depth"); err != nil {
		t.Fatal(err)
	}
	if got, err := testutil.GatherAndCount(reg, "cron_job_duration_seconds"); err != nil || got != 1 {
		t.Fatalf("duration series = %d (%v), want 1", got, err)
	}
	if got, err := testutil.GatherAndCount(reg, "cron_job_lateness_seconds"); err != nil || got != 1 {
		t.Fatalf("lateness series = %d (%v), want 1", got, err)
	}
}

func TestRecorder_EndToEnd(t *testing.T) {
	reg := prometheus.NewRegistry()
	r, err := cronprom.New(cronprom.WithRegisterer(reg), cronprom.WithNamespace("app"))
	if err != nil {
		t.Fatal(err)
	}

	c := cron.MustNew(cron.WithLocation(time.UTC), cron.WithRecorder(r))
	id, _ := c.AddSchedule(cron.TriggeredSchedule(), cron.JobFunc(func(context.Context) error {
		return nil
	}), cron.WithName("e2e"))
	_ = c.Start()
	defer func() { _ = c.Stop(context.Background()) }()

	if err := c.TriggerAndWait(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	expected := `
# HELP app_jobs_completed_total Job invocations completed, by status ok|error.
# TYPE app_jobs_completed_total counter
app_jobs_completed_total{name="e2e",status="ok"} 1
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected), "app_jobs_completed_total"); err != nil {
		t.Fatal(err)
	}
}

func TestNew_DuplicateRegistration(t *testing.T) {
	reg := prometheus.NewRegistry()
	if _, err := cronprom.New(cronprom.WithRegisterer(reg)); err != nil {
		t.Fatal(err)
	}
	if _, err := cronprom.New(cronprom.WithRegisterer(reg)); err == nil {
		t.Fatal("second registration must fail")
	}
}

func TestNew_CustomBuckets(t *testing.T) {
	reg := prometheus.NewRegistry()
	r, err := cronprom.New(cronprom.WithRegisterer(reg),
		cronprom.WithDurationBuckets([]float64{1, 2}),
		cronprom.WithLatenessBuckets([]float64{10}))
	if err != nil {
		t.Fatal(err)
	}
	entry := cron.EntryRef{Name: "b"}
	r.Record(cron.JobCompleteEvent{Entry: entry, Duration: time.Second})
	r.Record(cron.MissedFireEvent{Entry: entry, Lateness: time.Minute})
}

func TestNew_RejectsInvalidOptions(t *testing.T) {
	if _, err := cronprom.New(nil); !errors.Is(err, cronprom.ErrInvalidOption) {
		t.Fatalf("nil option error = %v", err)
	}
	var registerer *prometheus.Registry
	if _, err := cronprom.New(cronprom.WithRegisterer(registerer)); !errors.Is(err, cronprom.ErrInvalidOption) {
		t.Fatalf("nil registerer error = %v", err)
	}
	if _, err := cronprom.New(cronprom.WithDurationBuckets([]float64{1, 1})); !errors.Is(err, cronprom.ErrInvalidOption) {
		t.Fatalf("invalid bucket error = %v", err)
	}
}

func TestNew_RegistrationIsAtomic(t *testing.T) {
	reg := prometheus.NewRegistry()
	conflict := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "cron", Name: "queue_depth", Help: "conflict",
	})
	if err := reg.Register(conflict); err != nil {
		t.Fatal(err)
	}
	if _, err := cronprom.New(cronprom.WithRegisterer(reg)); err == nil {
		t.Fatal("descriptor conflict was accepted")
	}
	started := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "cron", Name: "jobs_started_total", Help: "Job invocations started.",
	}, []string{"name"})
	if err := reg.Register(started); err != nil {
		t.Fatalf("failed constructor partially registered collectors: %v", err)
	}
}
