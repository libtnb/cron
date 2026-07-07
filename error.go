package cron

import (
	"errors"
	"fmt"
)

var (
	ErrCapacityReached     = errors.New("cron: capacity reached")
	ErrAlreadyRunning      = errors.New("cron: job already running")
	ErrJobTimeout          = errors.New("cron: job timeout")
	ErrCronStopping        = errors.New("cron: scheduler stopping")
	ErrEntryNotFound       = errors.New("cron: entry not found")
	ErrSchedulerNotRunning = errors.New("cron: scheduler not running")
	ErrConcurrencyLimit    = errors.New("cron: max concurrent reached")
	ErrSchedulerStopped    = errors.New("cron: scheduler stopped")
	ErrNilJob              = errors.New("cron: nil job")
	ErrNilSchedule         = errors.New("cron: nil schedule")
	ErrJobPanic            = errors.New("cron: job panicked")
)

// ParseError describes a failure parsing a cron specification.
type ParseError struct {
	Spec   string
	Field  string // e.g. "minute"; "" if not applicable
	Pos    int    // 0-based byte offset; -1 if unknown
	Reason string
	Err    error
}

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

func (e *ParseError) Unwrap() error { return e.Err }
