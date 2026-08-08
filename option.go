package cron

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"time"
)

// Option configures a Cron.
type Option func(*config) error

// EntryOption configures one entry.
type EntryOption func(*entryConfig) error

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

// WithLocation sets the default schedule timezone. Default is time.Local.
// Ignored when WithParser is set: a custom parser owns its timezone.
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

// WithParser installs a parser. It takes over timezone resolution, so
// WithLocation and WithSecondsField no longer apply.
func WithParser(p Parser) Option {
	return func(c *config) error {
		if p == nil || isNilLike(p) {
			return fmt.Errorf("%w: parser is nil", ErrInvalidOption)
		}
		c.parser = p
		return nil
	}
}

// WithSecondsField enables a leading seconds field in the built-in parser, so
// the common seconds + WithLocation case composes without WithParser.
func WithSecondsField() Option {
	return func(c *config) error {
		c.secondsField = true
		return nil
	}
}

// WithLogger sets the slog.Logger. Default slog.Default().
func WithLogger(l *slog.Logger) Option {
	return func(c *config) error {
		if l == nil {
			return fmt.Errorf("%w: logger is nil", ErrInvalidOption)
		}
		c.logger = l
		return nil
	}
}

// WithChain installs global wrappers. First wrapper is outermost.
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

// WithJitter adds a random delay in [0, max) to each firing.
func WithJitter(max time.Duration) Option {
	return func(c *config) error {
		if max < 0 {
			return fmt.Errorf("%w: jitter must not be negative", ErrInvalidOption)
		}
		c.jitter = max
		return nil
	}
}

// WithName labels an entry.
func WithName(name string) EntryOption {
	return func(e *entryConfig) error {
		e.name = name
		return nil
	}
}

// WithKey sets the stable, unique identity used for distributed fire claims.
// It is independent from the display-oriented Name.
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

// WithTimeout caps a Job's runtime with ErrJobTimeout as the cancel cause.
func WithTimeout(d time.Duration) EntryOption {
	return func(e *entryConfig) error {
		if d < 0 {
			return fmt.Errorf("%w: timeout must not be negative", ErrInvalidOption)
		}
		e.timeout = d
		return nil
	}
}

// WithEntryChain installs per-entry wrappers inside the global chain.
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

// WithObservers installs async event observers. Events are serialized through
// one bounded queue and delivered to observers in option order.
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

// WithObserverBuffer sets the async event queue capacity. Zero selects the
// default. A full queue drops new observer events while the Recorder still
// receives them.
func WithObserverBuffer(n int) Option {
	return func(c *config) error {
		if n < 0 {
			return fmt.Errorf("%w: observer buffer must not be negative", ErrInvalidOption)
		}
		c.observerBuffer = n
		return nil
	}
}

// WithMissedFire selects the missed-fire policy. Default MissedSkip.
func WithMissedFire(p MissedFirePolicy) Option {
	return func(c *config) error {
		if !p.valid() {
			return fmt.Errorf("%w: unknown missed-fire policy %d", ErrInvalidOption, p)
		}
		c.missedPolicy = p
		return nil
	}
}

// WithMissedTolerance sets the lateness threshold for "missed". Default 1s.
func WithMissedTolerance(d time.Duration) Option {
	return func(c *config) error {
		if d <= 0 {
			return fmt.Errorf("%w: missed tolerance must be positive", ErrInvalidOption)
		}
		c.missedTolerance = d
		return nil
	}
}

// WithMaxConcurrent caps in-flight jobs. Zero means unlimited.
func WithMaxConcurrent(n int) Option {
	return func(c *config) error {
		if n < 0 {
			return fmt.Errorf("%w: max concurrent must not be negative", ErrInvalidOption)
		}
		c.maxConcurrent = n
		return nil
	}
}

// WithMaxEntries caps registered entries. Zero means unlimited.
func WithMaxEntries(n int) Option {
	return func(c *config) error {
		if n < 0 {
			return fmt.Errorf("%w: max entries must not be negative", ErrInvalidOption)
		}
		c.maxEntries = n
		return nil
	}
}

// WithRetry sets the default RetryPolicy. Overridden by WithEntryRetry.
func WithRetry(p RetryPolicy) Option {
	return func(c *config) error {
		if err := p.validate(); err != nil {
			return fmt.Errorf("%w: retry: %v", ErrInvalidOption, err)
		}
		c.retry = p
		return nil
	}
}

// WithEntryRetry overrides the global retry for one entry. A zero policy
// disables retry for that entry.
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

// WithRecorder installs an inline event recorder. Record may be called
// concurrently and must be concurrency-safe and fast.
func WithRecorder(r Recorder) Option {
	return func(c *config) error {
		if isNilLike(r) {
			return fmt.Errorf("%w: recorder is nil", ErrInvalidOption)
		}
		c.recorder = r
		return nil
	}
}

// WithoutRecover disables the built-in job panic recovery. By default a
// panicking job is recovered into an ErrJobPanic-wrapped error; with this
// option the panic propagates and crashes the process.
func WithoutRecover() Option {
	return func(c *config) error {
		c.recoverDisabled = true
		return nil
	}
}

// WithBaseContext sets the root context jobs inherit from. Cancelling it stops
// firing and cancels in-flight jobs, like Stop but without waiting.
func WithBaseContext(ctx context.Context) Option {
	return func(c *config) error {
		if ctx == nil {
			return fmt.Errorf("%w: base context is nil", ErrInvalidOption)
		}
		c.baseCtx = ctx
		return nil
	}
}

// WithEntryMissedFire overrides the scheduler's missed-fire policy for one
// entry.
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
// disables jitter for the entry.
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
// now, so a missed-fire policy can catch up work missed while the process was
// down. It also seeds Entry.Prev.
func WithLastRun(t time.Time) EntryOption {
	return func(e *entryConfig) error {
		e.lastRun = t
		return nil
	}
}

// WithClaimer sets the distributed Claimer used for automatic fires. Manual
// Trigger bypasses coordination. Entries using it must configure WithKey.
func WithClaimer(claimer Claimer) Option {
	return func(c *config) error {
		if claimer == nil || isNilLike(claimer) {
			return fmt.Errorf("%w: claimer is nil", ErrInvalidOption)
		}
		c.claimer = claimer
		return nil
	}
}

// WithElector gates automatic fires on leadership. Follower state and backend
// failure both skip the fire but produce distinct SkipReason values. Manual
// Trigger bypasses coordination.
func WithElector(e Elector) Option {
	return func(c *config) error {
		if e == nil || isNilLike(e) {
			return fmt.Errorf("%w: elector is nil", ErrInvalidOption)
		}
		c.elector = e
		return nil
	}
}

// WithEntryClaimer overrides the scheduler's Claimer for one entry. An
// explicit nil disables distributed claims for the entry.
func WithEntryClaimer(claimer Claimer) EntryOption {
	return func(e *entryConfig) error {
		if isNilLike(claimer) {
			e.claimer = nil
		} else {
			e.claimer = claimer
		}
		e.claimerSet = true
		return nil
	}
}

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
