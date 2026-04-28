// Package wrap supplies Job decorators.
package wrap

import (
	"log/slog"

	"github.com/libtnb/cron"
)

type Option func(*config)

type config struct {
	logger *slog.Logger
}

// WithLogger sets the slog.Logger used by Recover. Default slog.Default().
func WithLogger(l *slog.Logger) Option {
	return func(c *config) { c.logger = l }
}

func Chain(wrappers ...cron.Wrapper) cron.Wrapper { return cron.Chain(wrappers...) }
