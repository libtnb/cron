package cron

import (
	"log/slog"
	"time"
)

// Option configures a Cron.
type Option func(*config)

// EntryOption configures one entry.
type EntryOption func(*entryConfig)

type config struct {
	loc          *time.Location
	locSet       bool
	parser       Parser
	secondsField bool
	logger       *slog.Logger
	chain        []Wrapper
	jitter       time.Duration

	hooks           []any
	hookBuffer      int
	missedPolicy    MissedFirePolicy
	missedTolerance time.Duration
	maxConcurrent   int
	maxEntries      int

	retry    RetryPolicy
	recorder any
}

type entryConfig struct {
	name     string
	timeout  time.Duration
	chain    []Wrapper
	retry    RetryPolicy
	retrySet bool
}

// WithLocation sets the default schedule timezone. Default is time.Local.
// Ignored when WithParser is set: a custom parser owns its timezone.
func WithLocation(loc *time.Location) Option {
	return func(c *config) { c.loc = loc; c.locSet = true }
}

// WithParser installs a parser. It takes over timezone resolution, so
// WithLocation and WithSecondsField no longer apply.
func WithParser(p Parser) Option {
	return func(c *config) { c.parser = p }
}

// WithSecondsField enables a leading seconds field in the built-in parser, so
// the common seconds + WithLocation case composes without WithParser.
func WithSecondsField() Option {
	return func(c *config) { c.secondsField = true }
}

// WithLogger sets the slog.Logger. Default slog.Default().
func WithLogger(l *slog.Logger) Option {
	return func(c *config) { c.logger = l }
}

// WithChain installs global wrappers. First wrapper is outermost.
func WithChain(wrappers ...Wrapper) Option {
	return func(c *config) { c.chain = append(c.chain, wrappers...) }
}

// WithJitter adds a random delay in [0, max) to each firing.
func WithJitter(max time.Duration) Option {
	return func(c *config) { c.jitter = max }
}

// WithName labels an entry.
func WithName(name string) EntryOption {
	return func(e *entryConfig) { e.name = name }
}

// WithTimeout caps a Job's runtime with ErrJobTimeout as the cancel cause.
func WithTimeout(d time.Duration) EntryOption {
	return func(e *entryConfig) { e.timeout = d }
}

// WithEntryChain installs per-entry wrappers inside the global chain.
func WithEntryChain(wrappers ...Wrapper) EntryOption {
	return func(e *entryConfig) { e.chain = append(e.chain, wrappers...) }
}

// WithHooks installs async hook subscribers. Values may implement any subset
// of ScheduleHook, JobStartHook, JobCompleteHook, and MissedHook.
func WithHooks(hooks ...any) Option {
	return func(c *config) { c.hooks = append(c.hooks, hooks...) }
}

// WithHookBuffer sets the hook event buffer size. Full buffers drop new events.
func WithHookBuffer(n int) Option {
	return func(c *config) { c.hookBuffer = n }
}

// WithMissedFire selects the missed-fire policy. Default MissedSkip.
func WithMissedFire(p MissedFirePolicy) Option {
	return func(c *config) { c.missedPolicy = p }
}

// WithMissedTolerance sets the lateness threshold for "missed". Default 1s.
func WithMissedTolerance(d time.Duration) Option {
	return func(c *config) { c.missedTolerance = d }
}

// WithMaxConcurrent caps in-flight jobs. Zero means unlimited.
func WithMaxConcurrent(n int) Option {
	return func(c *config) { c.maxConcurrent = n }
}

// WithMaxEntries caps registered entries. Zero means unlimited.
func WithMaxEntries(n int) Option {
	return func(c *config) { c.maxEntries = n }
}

// WithRetry sets the default RetryPolicy. Overridden by WithEntryRetry.
func WithRetry(p RetryPolicy) Option {
	return func(c *config) { c.retry = p }
}

// WithEntryRetry overrides the global retry for one entry. A zero policy
// disables retry for that entry.
func WithEntryRetry(p RetryPolicy) EntryOption {
	return func(e *entryConfig) {
		e.retry = p
		e.retrySet = true
	}
}

// WithRecorder installs a metrics subscriber. Values may implement any subset
// of the recorder sub-interfaces.
func WithRecorder(r any) Option {
	return func(c *config) { c.recorder = r }
}
