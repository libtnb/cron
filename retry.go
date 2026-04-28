package cron

import (
	"context"
	"errors"
	mathrand "math/rand/v2"
	"time"
)

// RetryPolicy describes exponential backoff with optional jitter.
// MaxRetries == 0 disables retry; negative means unlimited until ctx
// cancellation. Fields are exported for config-driven assembly; use
// Retry(...) for programmatic construction.
type RetryPolicy struct {
	MaxRetries int
	Initial    time.Duration
	MaxDelay   time.Duration
	Multiplier float64
	JitterFrac float64
}

// IsZero is keyed only on MaxRetries so half-filled policies (e.g. only
// Initial set) don't produce a useless wrapper.
func (p RetryPolicy) IsZero() bool { return p.MaxRetries == 0 }

// Retry builds a RetryPolicy. maxRetries is the number of retries after the
// initial attempt; negative retries until ctx cancellation.
func Retry(maxRetries int, opts ...RetryOption) RetryPolicy {
	p := RetryPolicy{MaxRetries: maxRetries}
	for _, o := range opts {
		o(&p)
	}
	return p
}

// RetryOption configures a RetryPolicy built by Retry.
type RetryOption func(*RetryPolicy)

// RetryInitial sets the first retry delay (default 1s).
func RetryInitial(d time.Duration) RetryOption {
	return func(p *RetryPolicy) { p.Initial = d }
}

// RetryMaxDelay caps backoff (zero = uncapped).
func RetryMaxDelay(d time.Duration) RetryOption {
	return func(p *RetryPolicy) { p.MaxDelay = d }
}

// RetryMultiplier is the per-attempt growth factor (<=1 stays constant).
func RetryMultiplier(m float64) RetryOption {
	return func(p *RetryPolicy) { p.Multiplier = m }
}

// RetryJitterFrac is fractional uniform jitter (e.g. 0.1 = ±10%).
func RetryJitterFrac(f float64) RetryOption {
	return func(p *RetryPolicy) { p.JitterFrac = f }
}

func (p RetryPolicy) backoff(attempt int) time.Duration {
	d := p.Initial
	if d <= 0 {
		d = time.Second
	}
	mult := p.Multiplier
	for range attempt {
		if mult > 1 {
			d = time.Duration(float64(d) * mult)
		}
		if p.MaxDelay > 0 && d > p.MaxDelay {
			d = p.MaxDelay
			break
		}
	}
	if p.JitterFrac > 0 {
		jit := time.Duration(float64(d) * p.JitterFrac)
		if jit > 0 {
			d += mathrand.N(2*jit) - jit
		}
		if d < 0 {
			d = 0
		}
	}
	return d
}

// Wrapper returns a Wrapper that retries on error per p. Attempt errors
// are joined via errors.Join; ctx cancellation aborts.
func (p RetryPolicy) Wrapper() Wrapper {
	return func(j Job) Job {
		return JobFunc(func(ctx context.Context) error {
			var errs []error
			for i := 0; ; i++ {
				if err := ctx.Err(); err != nil {
					errs = append(errs, err)
					return errors.Join(errs...)
				}
				err := j.Run(ctx)
				if err == nil {
					return nil
				}
				errs = append(errs, err)
				if p.MaxRetries >= 0 && i >= p.MaxRetries {
					break
				}
				d := p.backoff(i)
				if d <= 0 {
					continue
				}
				timer := time.NewTimer(d)
				select {
				case <-timer.C:
				case <-ctx.Done():
					timer.Stop()
					errs = append(errs, ctx.Err())
					return errors.Join(errs...)
				}
			}
			return errors.Join(errs...)
		})
	}
}
