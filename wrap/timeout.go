package wrap

import (
	"context"
	"time"

	"github.com/libtnb/cron"
)

// Timeout attaches a deadline with cron.ErrJobTimeout as the cancellation
// cause. Non-positive durations are a no-op.
func Timeout(d time.Duration) cron.Wrapper {
	return func(j cron.Job) cron.Job {
		if d <= 0 {
			return j
		}
		return cron.JobFunc(func(ctx context.Context) error {
			ctx, cancel := context.WithTimeoutCause(ctx, d, cron.ErrJobTimeout)
			defer cancel()
			return j.Run(ctx)
		})
	}
}
