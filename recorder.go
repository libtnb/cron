package cron

import "time"

// Recorder sub-interfaces. WithRecorder subscribers may implement any subset.
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

// NoopRecorder implements all recorder sub-interfaces with empty methods.
type NoopRecorder struct{}

func (NoopRecorder) JobScheduled(string)                       {}
func (NoopRecorder) JobStarted(string)                         {}
func (NoopRecorder) JobCompleted(string, time.Duration, error) {}
func (NoopRecorder) JobMissed(string, time.Duration)           {}
func (NoopRecorder) QueueDepth(int)                            {}
func (NoopRecorder) HookDropped()                              {}

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
