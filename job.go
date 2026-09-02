package cron

import (
	"context"
	"slices"
)

// Job is the unit of work the scheduler runs. Run receives a context that is
// cancelled by Stop (cause ErrCronStopping), by WithTimeout (cause
// ErrJobTimeout) or by the WithBaseContext parent, and that carries EntryInfo
// (see EntryInfoFromContext). Implementations must be safe for concurrent
// use: overlapping fires of one entry run concurrently unless a wrapper such
// as wrap.SkipIfRunning prevents it.
type Job interface {
	// Run performs the work. The returned error is reported through
	// JobCompleteEvent and, for TriggerAndWait, to the caller; a panic is
	// recovered into ErrJobPanic unless WithoutRecover is set.
	Run(ctx context.Context) error
}

// JobFunc adapts a plain function to Job.
type JobFunc func(ctx context.Context) error

// Run calls f.
func (f JobFunc) Run(ctx context.Context) error { return f(ctx) }

// Wrapper decorates a Job, for example with a timeout, retries or tracing.
// Wrappers are applied once per entry at Add time, so a wrapper that keeps
// state (such as wrap.SkipIfRunning) gets one instance per entry.
type Wrapper func(Job) Job

// Chain composes wrappers so the first one is outermost: Chain(a, b)(j) runs
// a around b around j. Chain with no wrappers returns j unchanged. Cron builds
// each entry's chain as WithChain, then WithEntryChain, then the retry policy,
// from the outside in.
func Chain(wrappers ...Wrapper) Wrapper {
	return func(j Job) Job {
		for _, wrapper := range slices.Backward(wrappers) {
			j = wrapper(j)
		}
		return j
	}
}
