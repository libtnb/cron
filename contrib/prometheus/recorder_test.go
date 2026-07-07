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

	r.JobScheduled("a")
	r.JobStarted("a")
	r.JobCompleted("a", 50*time.Millisecond, nil)
	r.JobCompleted("a", time.Second, errors.New("boom"))
	r.JobMissed("a", 3*time.Second)
	r.JobSkipped("a", cron.SkipLockHeld)
	r.QueueDepth(7)
	r.HookDropped()

	expected := `
# HELP cron_jobs_completed_total Job invocations completed, by status ok|error.
# TYPE cron_jobs_completed_total counter
cron_jobs_completed_total{name="a",status="error"} 1
cron_jobs_completed_total{name="a",status="ok"} 1
# HELP cron_jobs_skipped_total Fires suppressed by distributed coordination, by reason.
# TYPE cron_jobs_skipped_total counter
cron_jobs_skipped_total{name="a",reason="lock-held"} 1
# HELP cron_queue_depth Entries currently scheduled in the timer heap.
# TYPE cron_queue_depth gauge
cron_queue_depth 7
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected),
		"cron_jobs_completed_total", "cron_jobs_skipped_total", "cron_queue_depth"); err != nil {
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

	c := cron.New(cron.WithLocation(time.UTC), cron.WithRecorder(r))
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
	r.JobCompleted("b", time.Second, nil)
	r.JobMissed("b", time.Minute)
}
