package wrap

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"

	"github.com/libtnb/cron"
)

// Recover converts a panicking Job into an error ("cron: recovered panic:
// <value>") and logs the value and stack at error level. The scheduler already
// recovers job panics into cron.ErrJobPanic unless cron.WithoutRecover is set;
// use Recover to attach a custom logger, to recover inside an outer Retry so
// the panic is retried, or when running jobs outside the scheduler. The error
// does not wrap cron.ErrJobPanic.
func Recover(opts ...Option) cron.Wrapper {
	var cfg config
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.logger == nil {
		cfg.logger = slog.Default()
	}
	return func(j cron.Job) cron.Job {
		return cron.JobFunc(func(ctx context.Context) (err error) {
			defer func() {
				r := recover()
				if r == nil {
					return
				}
				err = fmt.Errorf("cron: recovered panic: %v", r)
				cfg.logger.LogAttrs(
					ctx,
					slog.LevelError,
					"cron: panic recovered",
					slog.Any("panic", r),
					slog.String("stack", string(debug.Stack())),
				)
			}()
			return j.Run(ctx)
		})
	}
}
