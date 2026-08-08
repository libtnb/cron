package cron

import (
	"context"
	"slices"
)

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
		for _, wrapper := range slices.Backward(wrappers) {
			j = wrapper(j)
		}
		return j
	}
}
