package wrap

import (
	"context"

	"github.com/libtnb/cron"
)

// DelayIfRunning serialises invocations; queued callers observe ctx
// cancellation.
func DelayIfRunning() cron.Wrapper {
	return func(j cron.Job) cron.Job {
		sem := make(chan struct{}, 1)
		sem <- struct{}{}
		return cron.JobFunc(func(ctx context.Context) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-sem:
			}
			defer func() { sem <- struct{}{} }()

			if err := ctx.Err(); err != nil {
				return err
			}
			return j.Run(ctx)
		})
	}
}
