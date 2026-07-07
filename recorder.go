package cron

import "time"

// Recorder sub-interfaces. WithRecorder subscribers may implement any subset.
//
// Unlike hooks, recorder methods are not serialized: they are called inline and
// concurrently from job goroutines, the scheduler loop, and Add/Remove/Trigger
// callers. Implementations must be safe for concurrent use and must not block —
// a slow recorder on the loop path delays scheduling.

type JobScheduledRecorder interface{ JobScheduled(name string) }
type JobStartedRecorder interface{ JobStarted(name string) }
type JobCompletedRecorder interface {
	JobCompleted(name string, dur time.Duration, err error)
}
type JobMissedRecorder interface {
	JobMissed(name string, lateness time.Duration)
}
type QueueDepthRecorder interface{ QueueDepth(n int) }
type HookDroppedRecorder interface{ HookDropped() }
type JobSkippedRecorder interface {
	JobSkipped(name string, reason SkipReason)
}

func recordJobScheduled(r any, name string) {
	if x, ok := r.(JobScheduledRecorder); ok {
		x.JobScheduled(name)
	}
}

func recordJobStarted(r any, name string) {
	if x, ok := r.(JobStartedRecorder); ok {
		x.JobStarted(name)
	}
}

func recordJobCompleted(r any, name string, dur time.Duration, err error) {
	if x, ok := r.(JobCompletedRecorder); ok {
		x.JobCompleted(name, dur, err)
	}
}

func recordJobMissed(r any, name string, lateness time.Duration) {
	if x, ok := r.(JobMissedRecorder); ok {
		x.JobMissed(name, lateness)
	}
}

func recordQueueDepth(r any, n int) {
	if x, ok := r.(QueueDepthRecorder); ok {
		x.QueueDepth(n)
	}
}

func recordJobSkipped(r any, name string, reason SkipReason) {
	if x, ok := r.(JobSkippedRecorder); ok {
		x.JobSkipped(name, reason)
	}
}
