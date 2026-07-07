// Package otelcron traces cron job runs with OpenTelemetry. Install the
// wrapper globally with cron.WithChain(otelcron.Wrapper()) or per entry with
// cron.WithEntryChain.
package otelcron

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/libtnb/cron"
)

const scopeName = "github.com/libtnb/cron/contrib/otel"

type config struct {
	tp trace.TracerProvider
}

// Option configures Wrapper.
type Option func(*config)

// WithTracerProvider overrides the global otel.GetTracerProvider().
func WithTracerProvider(tp trace.TracerProvider) Option {
	return func(c *config) { c.tp = tp }
}

// Wrapper returns a cron.Wrapper that runs every invocation inside a span.
// The span is named after the entry (via cron.EntryInfoFromContext), carries
// cron.* attributes, and records the job's error and status.
func Wrapper(opts ...Option) cron.Wrapper {
	cfg := config{tp: otel.GetTracerProvider()}
	for _, o := range opts {
		o(&cfg)
	}
	tracer := cfg.tp.Tracer(scopeName)

	return func(j cron.Job) cron.Job {
		return cron.JobFunc(func(ctx context.Context) error {
			name := "cron.job"
			var attrs []attribute.KeyValue
			if info, ok := cron.EntryInfoFromContext(ctx); ok {
				if info.Name != "" {
					name = "cron.job " + info.Name
				}
				attrs = append(attrs,
					attribute.Int64("cron.entry.id", int64(info.ID)),
					attribute.String("cron.entry.name", info.Name),
					attribute.String("cron.scheduled_at", info.ScheduledAt.UTC().Format("2006-01-02T15:04:05Z07:00")),
				)
			}
			ctx, span := tracer.Start(ctx, name,
				trace.WithSpanKind(trace.SpanKindInternal),
				trace.WithAttributes(attrs...))
			defer span.End()

			err := j.Run(ctx)
			if err != nil {
				span.RecordError(err)
				span.SetStatus(codes.Error, err.Error())
			} else {
				span.SetStatus(codes.Ok, "")
			}
			return err
		})
	}
}
