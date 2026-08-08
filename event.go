package cron

import "time"

// EntryRef identifies the entry associated with an event.
type EntryRef struct {
	ID   EntryID
	Key  string
	Name string
}

// Event is one scheduler event. Its concrete types are defined by this
// package, so consumers can use a type switch without accepting arbitrary
// implementations.
type Event interface {
	cronEvent()
}

// ScheduleEvent reports an entry's newly computed next fire. Next is zero
// when the schedule has no future fire.
type ScheduleEvent struct {
	Entry    EntryRef
	Schedule Schedule
	Next     time.Time
}

// JobStartEvent reports a job immediately before its wrapper chain runs.
type JobStartEvent struct {
	Entry       EntryRef
	ScheduledAt time.Time
	StartedAt   time.Time
}

// JobCompleteEvent reports the result of a job wrapper chain.
type JobCompleteEvent struct {
	Entry       EntryRef
	ScheduledAt time.Time
	StartedAt   time.Time
	Duration    time.Duration
	Err         error
}

// MissedFireEvent reports a fire that was late enough to invoke the entry's
// missed-fire policy.
type MissedFireEvent struct {
	Entry       EntryRef
	ScheduledAt time.Time
	Lateness    time.Duration
	Policy      MissedFirePolicy
}

// RejectReason classifies a fire rejected before job execution.
type RejectReason uint8

const (
	RejectUnknown RejectReason = iota
	RejectConcurrencyLimit
)

func (r RejectReason) String() string {
	switch r {
	case RejectConcurrencyLimit:
		return "concurrency-limit"
	default:
		return "unknown"
	}
}

// RejectedFireEvent reports a fire rejected before job execution.
type RejectedFireEvent struct {
	Entry       EntryRef
	ScheduledAt time.Time
	Reason      RejectReason
}

// CanceledFireEvent reports a reserved fire canceled before job execution,
// such as a scheduler shutdown during jitter.
type CanceledFireEvent struct {
	Entry       EntryRef
	ScheduledAt time.Time
	Cause       error
}

// SkippedFireEvent reports a fire suppressed by distributed coordination.
// Err is non-nil only when the coordination backend failed.
type SkippedFireEvent struct {
	Entry       EntryRef
	ScheduledAt time.Time
	Reason      SkipReason
	Err         error
}

// QueueDepthEvent reports the number of entries in the scheduling heap.
type QueueDepthEvent struct {
	Depth int
}

// ObserverDropEvent reports that the async observer queue dropped an event.
// Dropped is the cumulative count for this Cron.
type ObserverDropEvent struct {
	Dropped int64
}

func (ScheduleEvent) cronEvent()     {}
func (JobStartEvent) cronEvent()     {}
func (JobCompleteEvent) cronEvent()  {}
func (MissedFireEvent) cronEvent()   {}
func (RejectedFireEvent) cronEvent() {}
func (CanceledFireEvent) cronEvent() {}
func (SkippedFireEvent) cronEvent()  {}
func (QueueDepthEvent) cronEvent()   {}
func (ObserverDropEvent) cronEvent() {}

func entryRef(e *entry) EntryRef {
	return EntryRef{ID: e.id, Key: e.key, Name: e.name}
}
