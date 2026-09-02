package cron

import (
	"context"
	"errors"
	mathrand "math/rand/v2"
	"time"
)

// RetryPolicy describes exponential backoff with optional jitter for jobs
// that return an error. MaxRetries == 0 disables retry; negative means retry
// until the context is cancelled. Fields are exported for config-driven
// assembly; use Retry for programmatic construction. Install a policy with
// WithRetry, WithEntryRetry, wrap.Retry or workflow.WithRetry.
type RetryPolicy struct {
	MaxRetries int           // retries after the initial attempt; 0 disables, negative is unlimited
	Initial    time.Duration // first retry delay; non-positive means one second
	MaxDelay   time.Duration // backoff cap; zero means uncapped
	Multiplier float64       // per-attempt growth factor; 1 or less keeps the delay constant
	JitterFrac float64       // uniform jitter fraction in [0, 1], e.g. 0.1 for ±10%
}

// RetryOption configures a RetryPolicy built by Retry.
type RetryOption func(*RetryPolicy)

// Retry builds a RetryPolicy. maxRetries is the number of retries after the
// initial attempt; negative retries until the context is cancelled. Values
// are not validated here: WithRetry and WithEntryRetry reject invalid ones.
func Retry(maxRetries int, opts ...RetryOption) RetryPolicy {
	p := RetryPolicy{MaxRetries: maxRetries}
	for _, o := range opts {
		o(&p)
	}
	return p
}

// RetryInitial sets the first retry delay. The default is one second.
func RetryInitial(d time.Duration) RetryOption {
	return func(p *RetryPolicy) { p.Initial = d }
}

// RetryMaxDelay caps the backoff delay. Zero, the default, is uncapped.
func RetryMaxDelay(d time.Duration) RetryOption {
	return func(p *RetryPolicy) { p.MaxDelay = d }
}

// RetryMultiplier sets the per-attempt growth factor. Values of 1 or less
// keep the delay constant.
func RetryMultiplier(m float64) RetryOption {
	return func(p *RetryPolicy) { p.Multiplier = m }
}

// RetryJitterFrac sets uniform jitter as a fraction of the delay, e.g. 0.1
// for ±10%. Valid values are in [0, 1].
func RetryJitterFrac(f float64) RetryOption {
	return func(p *RetryPolicy) { p.JitterFrac = f }
}

// IsZero reports whether the policy disables retry. It is keyed only on
// MaxRetries, so a half-filled policy (for example only Initial set) does not
// produce a useless wrapper.
func (p RetryPolicy) IsZero() bool { return p.MaxRetries == 0 }

// Wrapper returns a Wrapper that re-runs the job on error according to p. The
// returned error joins every attempt's error with errors.Join, keeping at
// most the first and the 15 most recent when retries are unlimited. Context
// cancellation aborts before the next attempt and appends context.Cause, so
// ErrJobTimeout and ErrCronStopping survive into the joined error and remain
// matchable with errors.Is. A zero policy runs the job exactly once.
func (p RetryPolicy) Wrapper() Wrapper {
	return func(j Job) Job {
		return JobFunc(func(ctx context.Context) error {
			// Bound retained errors so unlimited retry (MaxRetries < 0) can't grow
			// an unbounded slice; keep the first plus the most recent.
			const maxErrs = 16
			var errs []error
			addErr := func(e error) {
				if len(errs) < maxErrs {
					errs = append(errs, e)
					return
				}
				copy(errs[1:], errs[2:])
				errs[len(errs)-1] = e
			}
			for i := 0; ; i++ {
				if ctx.Err() != nil {
					addErr(context.Cause(ctx))
					return errors.Join(errs...)
				}
				err := j.Run(ctx)
				if err == nil {
					return nil
				}
				addErr(err)
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
					addErr(context.Cause(ctx))
					return errors.Join(errs...)
				}
			}
			return errors.Join(errs...)
		})
	}
}

// validate rejects negative delays and a jitter fraction outside [0, 1].
func (p RetryPolicy) validate() error {
	switch {
	case p.Initial < 0:
		return errors.New("initial delay must not be negative")
	case p.MaxDelay < 0:
		return errors.New("max delay must not be negative")
	case p.Multiplier < 0:
		return errors.New("multiplier must not be negative")
	case p.JitterFrac < 0 || p.JitterFrac > 1:
		return errors.New("jitter fraction must be between 0 and 1")
	default:
		return nil
	}
}

// backoff computes the delay before retry number attempt (0-based), applying
// the multiplier, the cap and the jitter.
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
	// The loop skips attempt 0, so cap it here too.
	if p.MaxDelay > 0 && d > p.MaxDelay {
		d = p.MaxDelay
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
