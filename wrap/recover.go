package wrap

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"

	"github.com/libtnb/cron"
)

// Recover converts Job panics into errors and logs the stack.
func Recover(opts ...Option) cron.Wrapper {
	cfg := config{}
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
				cfg.logger.LogAttrs(ctx, slog.LevelError, "cron: panic recovered",
					slog.Any("panic", r),
					slog.String("stack", string(debug.Stack())),
				)
			}()
			return j.Run(ctx)
		})
	}
}
