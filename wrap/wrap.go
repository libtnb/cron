// Package wrap supplies reusable cron.Wrapper decorators: panic recovery,
// timeouts, retry and overlap control. Install them with cron.WithChain or
// cron.WithEntryChain. A wrapper's state is created when it is applied at Add
// time, so one wrapper value installed globally still tracks each entry
// separately.
package wrap

import "log/slog"

// Option configures Recover.
type Option func(*config)

// config collects wrapper options.
type config struct {
	logger *slog.Logger
}

// WithLogger sets the logger Recover writes recovered panics to. The default
// is slog.Default(); a nil l keeps the default.
func WithLogger(l *slog.Logger) Option {
	return func(c *config) { c.logger = l }
}
