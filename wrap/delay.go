package wrap

import (
	"context"

	"github.com/libtnb/cron"
)

// DelayIfRunning serializes invocations of one entry: an invocation that
// overlaps a running one waits for it to finish, then runs. A queued
// invocation returns ctx.Err() if its context is cancelled while waiting, and
// it holds its WithMaxConcurrent slot for the whole wait. Prefer SkipIfRunning
// when a late run is worthless.
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
			return j.Run(ctx)
		})
	}
}
