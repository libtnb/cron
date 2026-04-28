// Package cron provides a modern, focused Go cron scheduler.
//
// Jobs implement Job and are registered with Add or AddSchedule. The
// scheduler is explicit: call Start to run it and Stop to cancel and drain
// in-flight work. Standard specs use five fields by default; use
// WithStandardParser(WithSeconds()) for a leading seconds field.
//
// # Subpackages
//
//   - wrap - Job decorators such as Recover, Timeout, SkipIfRunning, and Retry.
//   - workflow - DAG orchestration with conditional dependencies.
//   - parserext - Optional Quartz-style parser with L, N#M, NL support.
package cron
