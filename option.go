package cron

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"time"
)

// Option configures a Cron at construction. New applies options in order; an
// option that rejects its argument makes New fail with an error wrapping
// ErrInvalidOption.
type Option func(*config) error

// config is the scheduler configuration assembled by New.
type config struct {
	loc          *time.Location
	locSet       bool
	parser       Parser
	secondsField bool
	logger       *slog.Logger
	chain        []Wrapper
	jitter       time.Duration
	baseCtx      context.Context

	observers       []Observer
	observerBuffer  int
	missedPolicy    MissedFirePolicy
	missedTolerance time.Duration
	maxConcurrent   int
	maxEntries      int

	retry           RetryPolicy
	recorder        Recorder
	recoverDisabled bool
	claimer         Claimer
	elector         Elector
}

// WithLocation sets the time zone for specs without a TZ=/CRON_TZ= prefix.
// The default is time.Local; a nil loc is rejected. It is ignored, with a
// warning from New, when WithParser is set, because a custom parser resolves
// time zones itself.
func WithLocation(loc *time.Location) Option {
	return func(c *config) error {
		if loc == nil {
			return fmt.Errorf("%w: location is nil", ErrInvalidOption)
		}
		c.loc = loc
		c.locSet = true
		return nil
	}
}

// WithParser replaces the built-in parser used by Add and Update, for example
// with parserext.NewQuartzParser. The parser then owns time-zone and seconds
// handling, so WithLocation and WithSecondsField no longer apply. Results are
// memoized per spec, so p must be deterministic and safe for concurrent use.
// A nil p is rejected.
func WithParser(p Parser) Option {
	return func(c *config) error {
		if p == nil || isNilLike(p) {
			return fmt.Errorf("%w: parser is nil", ErrInvalidOption)
		}
		c.parser = p
		return nil
	}
}

// WithSecondsField makes the built-in parser accept an optional leading
// seconds field (six fields) alongside five-field specs, so seconds and
// WithLocation compose without WithParser. It has no effect when WithParser
// is set.
func WithSecondsField() Option {
	return func(c *config) error {
		c.secondsField = true
		return nil
	}
}

// WithLogger sets the logger for warnings and recovered panics. The default is
// slog.Default(); a nil l is rejected.
func WithLogger(l *slog.Logger) Option {
	return func(c *config) error {
		if l == nil {
			return fmt.Errorf("%w: logger is nil", ErrInvalidOption)
		}
		c.logger = l
		return nil
	}
}

// WithChain installs wrappers applied to every job, first outermost; repeated
// calls append. Entry wrappers (WithEntryChain) and the retry policy sit
// inside them. A nil wrapper is rejected.
func WithChain(wrappers ...Wrapper) Option {
	return func(c *config) error {
		for i, wrapper := range wrappers {
			if wrapper == nil {
				return fmt.Errorf("%w: wrapper %d is nil", ErrInvalidOption, i)
			}
		}
		c.chain = append(c.chain, wrappers...)
		return nil
	}
}

// WithJitter delays each automatic fire by a random duration in [0, max) so a
// fleet of schedulers does not fire in lockstep. The delay is waited on the
// run context, not the job timeout, and Trigger skips it. Zero (the default)
// disables jitter; a negative max is rejected. Override per entry with
// WithEntryJitter.
func WithJitter(max time.Duration) Option {
	return func(c *config) error {
		if max < 0 {
			return fmt.Errorf("%w: jitter must not be negative", ErrInvalidOption)
		}
		c.jitter = max
		return nil
	}
}

// WithObservers installs asynchronous event observers, notified in the order
// given through one bounded queue (see Observer). Repeated calls append. A nil
// observer is rejected.
func WithObservers(observers ...Observer) Option {
	return func(c *config) error {
		for i, observer := range observers {
			if isNilLike(observer) {
				return fmt.Errorf("%w: observer %d is nil", ErrInvalidOption, i)
			}
			c.observers = append(c.observers, observer)
		}
		return nil
	}
}

// WithObserverBuffer sets the observer queue capacity. Zero selects the
// default of 1024; a negative n is rejected. When the queue is full new events
// are dropped for observers and an ObserverDropEvent is recorded, while the
// Recorder still receives every event.
func WithObserverBuffer(n int) Option {
	return func(c *config) error {
		if n < 0 {
			return fmt.Errorf("%w: observer buffer must not be negative", ErrInvalidOption)
		}
		c.observerBuffer = n
		return nil
	}
}

// WithRecorder installs the inline event Recorder, typically a metrics
// adapter such as contrib/prometheus; see Recorder for the concurrency
// contract. A nil r is rejected.
func WithRecorder(r Recorder) Option {
	return func(c *config) error {
		if isNilLike(r) {
			return fmt.Errorf("%w: recorder is nil", ErrInvalidOption)
		}
		c.recorder = r
		return nil
	}
}

// WithMissedFire sets the scheduler-wide missed-fire policy. The default is
// MissedRunOnce; an unknown policy value is rejected. Override per entry with
// WithEntryMissedFire.
func WithMissedFire(p MissedFirePolicy) Option {
	return func(c *config) error {
		if !p.valid() {
			return fmt.Errorf("%w: unknown missed-fire policy %d", ErrInvalidOption, p)
		}
		c.missedPolicy = p
		return nil
	}
}

// WithMissedTolerance sets how late a fire may run before the missed-fire
// policy applies. The default is one minute, the same threshold Quartz uses,
// so a brief scheduling stall runs the job late instead of invoking the
// policy. A non-positive d is rejected.
func WithMissedTolerance(d time.Duration) Option {
	return func(c *config) error {
		if d <= 0 {
			return fmt.Errorf("%w: missed tolerance must be positive", ErrInvalidOption)
		}
		c.missedTolerance = d
		return nil
	}
}

// WithMaxConcurrent caps in-flight jobs across all entries. Zero (the
// default) means unlimited; a negative n is rejected. Automatic fires above
// the cap publish RejectedFireEvent and are not retried; Trigger returns
// ErrConcurrencyLimit.
func WithMaxConcurrent(n int) Option {
	return func(c *config) error {
		if n < 0 {
			return fmt.Errorf("%w: max concurrent must not be negative", ErrInvalidOption)
		}
		c.maxConcurrent = n
		return nil
	}
}

// WithMaxEntries caps registered entries; Add returns ErrCapacityReached
// beyond it. Zero (the default) means unlimited; a negative n is rejected.
func WithMaxEntries(n int) Option {
	return func(c *config) error {
		if n < 0 {
			return fmt.Errorf("%w: max entries must not be negative", ErrInvalidOption)
		}
		c.maxEntries = n
		return nil
	}
}

// WithRetry sets the default RetryPolicy, applied innermost in every job's
// wrapper chain. A policy with negative delays or a jitter fraction outside
// [0, 1] is rejected. Override per entry with WithEntryRetry.
func WithRetry(p RetryPolicy) Option {
	return func(c *config) error {
		if err := p.validate(); err != nil {
			return fmt.Errorf("%w: retry: %v", ErrInvalidOption, err)
		}
		c.retry = p
		return nil
	}
}

// WithoutRecover disables the built-in job panic recovery. By default a
// panicking job is recovered into an ErrJobPanic-wrapped error and logged
// with its stack; with this option the panic propagates and crashes the
// process.
func WithoutRecover() Option {
	return func(c *config) error {
		c.recoverDisabled = true
		return nil
	}
}

// WithBaseContext sets the parent of the run context that jobs inherit.
// Cancelling it stops the loop and cancels in-flight jobs, like Stop but
// without waiting; still call Stop or Drain to wait for them. A nil ctx is
// rejected.
func WithBaseContext(ctx context.Context) Option {
	return func(c *config) error {
		if ctx == nil {
			return fmt.Errorf("%w: base context is nil", ErrInvalidOption)
		}
		c.baseCtx = ctx
		return nil
	}
}

// WithClaimer sets the Claimer consulted before every automatic fire. Entries
// then require WithKey (Add returns ErrClaimerRequiresKey) and may opt out or
// switch backends with WithEntryClaimer. Trigger bypasses claims. A nil
// claimer is rejected.
func WithClaimer(claimer Claimer) Option {
	return func(c *config) error {
		if claimer == nil || isNilLike(claimer) {
			return fmt.Errorf("%w: claimer is nil", ErrInvalidOption)
		}
		c.claimer = claimer
		return nil
	}
}

// WithElector gates automatic fires on leadership. A follower answer and a
// backend failure both skip the fire, with SkipNotLeader and SkipElectionError
// respectively. Trigger bypasses the elector. A nil e is rejected.
func WithElector(e Elector) Option {
	return func(c *config) error {
		if e == nil || isNilLike(e) {
			return fmt.Errorf("%w: elector is nil", ErrInvalidOption)
		}
		c.elector = e
		return nil
	}
}

// EntryOption configures one entry at Add or AddSchedule time. Options are
// applied in order; one that rejects its argument makes Add fail with an
// error wrapping ErrInvalidOption.
type EntryOption func(*entryConfig) error

// entryConfig is the per-entry configuration assembled by Cron.add. The *Set
// flags distinguish "explicitly zero" from "inherit the scheduler default".
type entryConfig struct {
	name       string
	key        string
	timeout    time.Duration
	chain      []Wrapper
	retry      RetryPolicy
	retrySet   bool
	missed     MissedFirePolicy
	missedSet  bool
	jitter     time.Duration
	jitterSet  bool
	lastRun    time.Time
	claimer    Claimer
	claimerSet bool
}

// WithName labels an entry for Entry.Name, events, logs and TriggerByName.
// Names need not be unique; use WithKey for identity.
func WithName(name string) EntryOption {
	return func(e *entryConfig) error {
		e.name = name
		return nil
	}
}

// WithKey sets the stable identity used for distributed fire claims. Keys are
// trimmed, must be non-empty, must be unique within the scheduler (Add returns
// ErrDuplicateKey otherwise) and should be the same on every replica for the
// same logical job. Key is independent from the display-oriented Name.
func WithKey(key string) EntryOption {
	return func(e *entryConfig) error {
		key = strings.TrimSpace(key)
		if key == "" {
			return fmt.Errorf("%w: entry key is empty", ErrInvalidOption)
		}
		e.key = key
		return nil
	}
}

// WithTimeout caps one invocation's runtime; the job context is cancelled
// with ErrJobTimeout as its cause. The clock starts after jitter and
// coordination. Zero (the default) disables the timeout; a negative d is
// rejected.
func WithTimeout(d time.Duration) EntryOption {
	return func(e *entryConfig) error {
		if d < 0 {
			return fmt.Errorf("%w: timeout must not be negative", ErrInvalidOption)
		}
		e.timeout = d
		return nil
	}
}

// WithEntryChain installs wrappers for this entry, first outermost, inside
// the WithChain wrappers and outside the retry policy. A nil wrapper is
// rejected.
func WithEntryChain(wrappers ...Wrapper) EntryOption {
	return func(e *entryConfig) error {
		for i, wrapper := range wrappers {
			if wrapper == nil {
				return fmt.Errorf("%w: entry wrapper %d is nil", ErrInvalidOption, i)
			}
		}
		e.chain = append(e.chain, wrappers...)
		return nil
	}
}

// WithEntryRetry overrides the scheduler's RetryPolicy for one entry. A zero
// policy (MaxRetries == 0) disables retry for the entry even when WithRetry
// is set. An invalid policy is rejected.
func WithEntryRetry(p RetryPolicy) EntryOption {
	return func(e *entryConfig) error {
		if err := p.validate(); err != nil {
			return fmt.Errorf("%w: entry retry: %v", ErrInvalidOption, err)
		}
		e.retry = p
		e.retrySet = true
		return nil
	}
}

// WithEntryMissedFire overrides the scheduler's missed-fire policy for one
// entry. An unknown policy value is rejected.
func WithEntryMissedFire(p MissedFirePolicy) EntryOption {
	return func(e *entryConfig) error {
		if !p.valid() {
			return fmt.Errorf("%w: unknown entry missed-fire policy %d", ErrInvalidOption, p)
		}
		e.missed = p
		e.missedSet = true
		return nil
	}
}

// WithEntryJitter overrides the scheduler's jitter for one entry. Zero
// disables jitter for the entry; a negative max is rejected.
func WithEntryJitter(max time.Duration) EntryOption {
	return func(e *entryConfig) error {
		if max < 0 {
			return fmt.Errorf("%w: entry jitter must not be negative", ErrInvalidOption)
		}
		e.jitter = max
		e.jitterSet = true
		return nil
	}
}

// WithLastRun seeds the entry's schedule anchor, usually the persisted time of
// the last run before a restart. The first fire is computed from t instead of
// now, so an instant already in the past is popped immediately and, when it
// is later than WithMissedTolerance, handed to the missed-fire policy, which
// catches up work missed while the process was down. It also seeds
// Entry.Prev. A zero t means "anchor at now".
func WithLastRun(t time.Time) EntryOption {
	return func(e *entryConfig) error {
		e.lastRun = t
		return nil
	}
}

// WithEntryClaimer overrides the scheduler's Claimer for one entry. A nil
// claimer disables distributed claims for the entry, so it fires on every
// replica; a non-nil claimer requires WithKey.
func WithEntryClaimer(claimer Claimer) EntryOption {
	return func(e *entryConfig) error {
		e.claimer = claimer
		if isNilLike(claimer) {
			e.claimer = nil
		}
		e.claimerSet = true
		return nil
	}
}

// isNilLike reports whether value is nil or an interface holding a nil
// pointer, func, map, slice or channel, so a typed nil Job, Schedule or
// Observer is rejected at registration instead of panicking at fire time.
func isNilLike(value any) bool {
	if value == nil {
		return true
	}
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}
