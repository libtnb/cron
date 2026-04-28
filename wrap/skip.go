package wrap

import (
	"context"
	"sync/atomic"

	"github.com/libtnb/cron"
)

// SkipIfRunning drops overlapping invocations.
func SkipIfRunning() cron.Wrapper {
	return func(j cron.Job) cron.Job {
		var running atomic.Bool
		return cron.JobFunc(func(ctx context.Context) error {
			if !running.CompareAndSwap(false, true) {
				return cron.ErrAlreadyRunning
			}
			defer running.Store(false)
			return j.Run(ctx)
		})
	}
}
