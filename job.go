package cron

import "context"

// Job is the unit of work executed by the scheduler.
type Job interface {
	Run(ctx context.Context) error
}

// JobFunc adapts a function to Job.
type JobFunc func(ctx context.Context) error

func (f JobFunc) Run(ctx context.Context) error { return f(ctx) }

// Wrapper decorates a Job.
type Wrapper func(Job) Job

// Chain composes wrappers so the first wraps outermost.
func Chain(wrappers ...Wrapper) Wrapper {
	return func(j Job) Job {
		for i := len(wrappers) - 1; i >= 0; i-- {
			j = wrappers[i](j)
		}
		return j
	}
}
