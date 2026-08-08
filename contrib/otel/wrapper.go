// Package otelcron traces cron job runs with OpenTelemetry. Install the
// wrapper globally with cron.WithChain(otelcron.MustWrapper()) or per entry with
// cron.WithEntryChain.
package otelcron

import (
	"context"
	"errors"
	"fmt"
	"reflect"

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

// ErrInvalidOption reports an invalid wrapper option.
var ErrInvalidOption = errors.New("otelcron: invalid option")

// Option configures Wrapper.
type Option func(*config) error

// WithTracerProvider overrides the global otel.GetTracerProvider().
func WithTracerProvider(tp trace.TracerProvider) Option {
	return func(c *config) error {
		if nilLike(tp) {
			return fmt.Errorf("%w: tracer provider is nil", ErrInvalidOption)
		}
		c.tp = tp
		return nil
	}
}

// Wrapper returns a cron.Wrapper that runs every invocation inside a span.
// The span is named after the entry (via cron.EntryInfoFromContext), carries
// cron.* attributes, and records the job's error and status.
func Wrapper(opts ...Option) (cron.Wrapper, error) {
	cfg := config{tp: otel.GetTracerProvider()}
	for i, option := range opts {
		if option == nil {
			return nil, fmt.Errorf("%w: option %d is nil", ErrInvalidOption, i)
		}
		if err := option(&cfg); err != nil {
			return nil, err
		}
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
					attribute.String("cron.entry.key", info.Key),
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
	}, nil
}

// MustWrapper is Wrapper with panic-on-error semantics.
func MustWrapper(opts ...Option) cron.Wrapper {
	wrapper, err := Wrapper(opts...)
	if err != nil {
		panic(err)
	}
	return wrapper
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
