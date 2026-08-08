package otelcron_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/libtnb/cron"
	"github.com/libtnb/cron/contrib/otel"
)

func runOnce(t *testing.T, job cron.Job) []sdktrace.ReadOnlySpan {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	c := cron.MustNew(cron.WithLocation(time.UTC),
		cron.WithChain(otelcron.MustWrapper(otelcron.WithTracerProvider(tp))))
	id, _ := c.AddSchedule(cron.TriggeredSchedule(), job,
		cron.WithName("traced"), cron.WithKey("traced-job"))
	_ = c.Start()
	defer func() { _ = c.Stop(context.Background()) }()

	_ = c.TriggerAndWait(context.Background(), id)
	return rec.Ended()
}

func TestWrapper_SuccessSpan(t *testing.T) {
	spans := runOnce(t, cron.JobFunc(func(context.Context) error { return nil }))
	if len(spans) != 1 {
		t.Fatalf("spans = %d, want 1", len(spans))
	}
	s := spans[0]
	if s.Name() != "cron.job traced" {
		t.Fatalf("span name = %q", s.Name())
	}
	if s.Status().Code != codes.Ok {
		t.Fatalf("status = %v", s.Status())
	}
	var sawName, sawKey bool
	for _, a := range s.Attributes() {
		if string(a.Key) == "cron.entry.name" && a.Value.AsString() == "traced" {
			sawName = true
		}
		if string(a.Key) == "cron.entry.key" && a.Value.AsString() == "traced-job" {
			sawKey = true
		}
	}
	if !sawName || !sawKey {
		t.Fatalf("entry attributes missing: name=%v key=%v", sawName, sawKey)
	}
}

func TestWrapper_ErrorSpan(t *testing.T) {
	boom := errors.New("boom")
	spans := runOnce(t, cron.JobFunc(func(context.Context) error { return boom }))
	if len(spans) != 1 {
		t.Fatalf("spans = %d, want 1", len(spans))
	}
	s := spans[0]
	if s.Status().Code != codes.Error {
		t.Fatalf("status = %v, want error", s.Status())
	}
	if len(s.Events()) == 0 {
		t.Fatal("expected a recorded error event")
	}
}

func TestWrapper_NoEntryInfoFallsBack(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	// Run the wrapped job directly, outside any scheduler context.
	job := otelcron.MustWrapper(otelcron.WithTracerProvider(tp))(
		cron.JobFunc(func(context.Context) error { return nil }))
	if err := job.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	spans := rec.Ended()
	if len(spans) != 1 || spans[0].Name() != "cron.job" {
		t.Fatalf("spans = %v", spans)
	}
}

func TestWrapper_RejectsInvalidOptions(t *testing.T) {
	if _, err := otelcron.Wrapper(nil); !errors.Is(err, otelcron.ErrInvalidOption) {
		t.Fatalf("nil option error = %v", err)
	}
	var provider *sdktrace.TracerProvider
	if _, err := otelcron.Wrapper(otelcron.WithTracerProvider(provider)); !errors.Is(err, otelcron.ErrInvalidOption) {
		t.Fatalf("nil provider error = %v", err)
	}
}
