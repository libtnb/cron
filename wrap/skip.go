package wrap

import (
	"context"
	"sync/atomic"

	"github.com/libtnb/cron"
)

// SkipIfRunning drops an invocation that overlaps a running one, returning
// cron.ErrAlreadyRunning without calling the job. State is created when the
// wrapper is applied at Add time, so one value installed with cron.WithChain
// still tracks each entry separately.
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
