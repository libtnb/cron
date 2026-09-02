package cron

import (
	"errors"
	"fmt"
)

// Sentinel errors. They may be returned wrapped; match them with errors.Is.
var (
	// ErrCapacityReached is returned by Add and AddSchedule when the
	// WithMaxEntries limit is reached.
	ErrCapacityReached = errors.New("cron: capacity reached")
	// ErrAlreadyRunning is returned by wrap.SkipIfRunning when an invocation
	// overlaps one still in progress.
	ErrAlreadyRunning = errors.New("cron: job already running")
	// ErrJobTimeout is the context cancellation cause when WithTimeout or
	// wrap.Timeout expires; read it with context.Cause.
	ErrJobTimeout = errors.New("cron: job timeout")
	// ErrCronStopping is the context cancellation cause for jobs cancelled by
	// Stop; read it with context.Cause.
	ErrCronStopping = errors.New("cron: scheduler stopping")
	// ErrEntryNotFound is returned by Remove, Pause, Resume, Update,
	// UpdateSchedule, Trigger and TriggerAndWait for an unknown or removed id.
	ErrEntryNotFound = errors.New("cron: entry not found")
	// ErrSchedulerNotRunning is returned by Trigger, TriggerAndWait and
	// TriggerByName before Start or after Stop and Drain.
	ErrSchedulerNotRunning = errors.New("cron: scheduler not running")
	// ErrConcurrencyLimit is returned by Trigger when WithMaxConcurrent has no
	// free slot. Automatic fires publish RejectedFireEvent instead.
	ErrConcurrencyLimit = errors.New("cron: max concurrent reached")
	// ErrSchedulerStopped is returned by Start after Stop or Drain.
	ErrSchedulerStopped = errors.New("cron: scheduler stopped")
	// ErrNilJob is returned by Add and AddSchedule for a nil Job, including a
	// typed nil, or when the wrapper chain produced one.
	ErrNilJob = errors.New("cron: nil job")
	// ErrNilSchedule is returned when a Parser produces no Schedule or when
	// AddSchedule or UpdateSchedule receives a nil one.
	ErrNilSchedule = errors.New("cron: nil schedule")
	// ErrJobPanic wraps the recovered panic value in a job's result unless
	// WithoutRecover is set.
	ErrJobPanic = errors.New("cron: job panicked")
	// ErrClaimerRequiresKey is returned by Add and AddSchedule when a Claimer
	// applies to an entry registered without WithKey.
	ErrClaimerRequiresKey = errors.New("cron: distributed claimer requires WithKey")
	// ErrDuplicateKey is returned, wrapped with the key, when WithKey repeats
	// a key already registered in this scheduler.
	ErrDuplicateKey = errors.New("cron: duplicate entry key")
	// ErrNilContext is returned by Stop, Drain and TriggerAndWait for a nil
	// context.
	ErrNilContext = errors.New("cron: nil context")
	// ErrInvalidOption is wrapped by New, Add and AddSchedule when an Option,
	// an EntryOption, or its argument is invalid.
	ErrInvalidOption = errors.New("cron: invalid option")
)

// ParseError describes why a cron specification was rejected. It is returned
// as *ParseError by StandardParser.Parse, the parserext parsers, and through
// them by Add, Update, ValidateSpec and AnalyzeSpec; match it with errors.As.
type ParseError struct {
	Spec   string // the specification being parsed
	Field  string // "second", "minute", "hour", "dom", "month", "dow", "@every", "TZ" or "CRON_TZ"; "" if not applicable
	Pos    int    // 0-based byte offset; -1 if unknown
	Reason string // human-readable cause
	Err    error  // underlying error, if any (for example from time.LoadLocation)
}

// Error formats the spec together with whichever of Field and Pos are known,
// for example: cron: parse "61 * * * *": field "minute": 61 above maximum 59.
func (e *ParseError) Error() string {
	switch {
	case e.Field != "" && e.Pos >= 0:
		return fmt.Sprintf("cron: parse %q: field %q at offset %d: %s", e.Spec, e.Field, e.Pos, e.Reason)
	case e.Field != "":
		return fmt.Sprintf("cron: parse %q: field %q: %s", e.Spec, e.Field, e.Reason)
	case e.Pos >= 0:
		return fmt.Sprintf("cron: parse %q: offset %d: %s", e.Spec, e.Pos, e.Reason)
	default:
		return fmt.Sprintf("cron: parse %q: %s", e.Spec, e.Reason)
	}
}

// Unwrap exposes the underlying error, such as a time.LoadLocation failure,
// to errors.Is and errors.As.
func (e *ParseError) Unwrap() error { return e.Err }
