// Package cron is a cron scheduler.
//
// Register jobs with Add or AddSchedule, then call Start. Stop cancels the
// loop and waits for in-flight jobs, capped by its context.
//
// Specs use five fields. WithSeconds adds an optional leading seconds field;
// WithSeconds(true) requires six.
//
// # Subpackages
//
//   - wrap: job wrappers (Recover, Timeout, SkipIfRunning, DelayIfRunning, Retry).
//   - workflow: DAG jobs with conditional dependencies.
//   - parserext: Quartz tokens (L, N#M, NL).
package cron
