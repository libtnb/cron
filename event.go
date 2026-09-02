package cron

import "time"

// EntryRef identifies the entry an event concerns. It is a copy taken at
// publication; the entry may already have been removed when the event is
// observed.
type EntryRef struct {
	ID   EntryID
	Key  string // WithKey value; empty when unset
	Name string // WithName value; empty when unset
}

// Event is one scheduler notification, delivered to Observers and the
// Recorder. The set of concrete types is closed to this package, so consumers
// can switch on them exhaustively:
//
//	switch ev := ev.(type) {
//	case cron.JobCompleteEvent:
//		// ...
//	}
type Event interface {
	// cronEvent seals the interface to this package's event types.
	cronEvent()
}

// ScheduleEvent reports an entry's newly computed next fire: on Add, Update,
// Resume and after every committed fire. Next is zero only on Add, when the
// schedule has no future fire; later recomputations that exhaust an entry
// publish no ScheduleEvent.
type ScheduleEvent struct {
	Entry    EntryRef
	Schedule Schedule
	Next     time.Time
}

// JobStartEvent reports an invocation about to enter its wrapper chain, after
// jitter, coordination and concurrency admission. ScheduledAt is the fire
// instant; StartedAt is the wall-clock start.
type JobStartEvent struct {
	Entry       EntryRef
	ScheduledAt time.Time
	StartedAt   time.Time
}

// JobCompleteEvent reports the result of an invocation's wrapper chain. Err is
// the job's error, nil on success, including ErrJobPanic and retry aggregates.
type JobCompleteEvent struct {
	Entry       EntryRef
	ScheduledAt time.Time
	StartedAt   time.Time
	Duration    time.Duration
	Err         error
}

// MissedFireEvent reports a fire that ran later than WithMissedTolerance and
// therefore invoked the entry's missed-fire policy. It is published once per
// late pop, whatever the policy, including MissedSkip.
type MissedFireEvent struct {
	Entry       EntryRef
	ScheduledAt time.Time
	Lateness    time.Duration
	Policy      MissedFirePolicy
}

// RejectReason classifies a fire refused before job execution; it is carried
// by RejectedFireEvent.
type RejectReason uint8

const (
	// RejectUnknown is the zero value; the scheduler never publishes it.
	RejectUnknown RejectReason = iota
	// RejectConcurrencyLimit reports that WithMaxConcurrent had no free slot.
	RejectConcurrencyLimit
)

// String returns "concurrency-limit" or "unknown".
func (r RejectReason) String() string {
	switch r {
	case RejectConcurrencyLimit:
		return "concurrency-limit"
	default:
		return "unknown"
	}
}

// RejectedFireEvent reports a fire refused before job execution, for both
// automatic fires and Trigger calls (which also return ErrConcurrencyLimit).
// The instant is not retried.
type RejectedFireEvent struct {
	Entry       EntryRef
	ScheduledAt time.Time
	Reason      RejectReason
}

// CanceledFireEvent reports a fire that held a concurrency slot but was
// cancelled before its job started: the scheduler stopped while the fire was
// waiting out its jitter. Cause is the run context's cancellation cause,
// typically ErrCronStopping.
type CanceledFireEvent struct {
	Entry       EntryRef
	ScheduledAt time.Time
	Cause       error
}

// SkippedFireEvent reports a fire suppressed by distributed coordination.
// Err is non-nil only for SkipElectionError and SkipClaimError.
type SkippedFireEvent struct {
	Entry       EntryRef
	ScheduledAt time.Time
	Reason      SkipReason
	Err         error
}

// QueueDepthEvent reports the number of entries waiting in the scheduling
// heap, that is, entries with a non-zero Next. It is published after Add,
// Remove, Pause, Resume, Update and each committed fire.
type QueueDepthEvent struct {
	Depth int
}

// ObserverDropEvent reports that the observer queue was full and an event was
// dropped. Dropped is the cumulative count for this Cron. It reaches only the
// Recorder: observers cannot receive it because their queue is the one that
// overflowed.
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

// entryRef copies the immutable identity fields of e for an event.
func entryRef(e *entry) EntryRef {
	return EntryRef{ID: e.id, Key: e.key, Name: e.name}
}
