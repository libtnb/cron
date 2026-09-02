package wrap

import (
	"context"
	"time"

	"github.com/libtnb/cron"
)

// Timeout bounds one invocation to d, cancelling the job context with
// cron.ErrJobTimeout as the cause (see context.Cause). It is the wrapper form
// of cron.WithTimeout. A non-positive d returns the job unchanged.
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
