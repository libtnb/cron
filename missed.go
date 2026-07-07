package cron

import "time"

// MissedFirePolicy controls behaviour when a fire is later than
// WithMissedTolerance. OnMissedFire fires regardless of policy.
type MissedFirePolicy uint8

const (
	// MissedSkip ignores missed firings and resumes from the next
	// scheduled time. This is the default.
	MissedSkip MissedFirePolicy = iota

	// MissedRunOnce runs the job once for the most recent missed firing
	// (latest schedule.Next <= now), then resumes normally.
	MissedRunOnce

	// MissedRunAll runs the job once per missed firing (newest
	// missedRunAllCap kept), then resumes normally.
	MissedRunAll
)

const (
	missedScanCap          = 100000 // Schedule.Next calls per catch-up scan
	missedRunAllCap        = 1000   // max fires dispatched by MissedRunAll
	defaultMissedTolerance = time.Second
)

func (p MissedFirePolicy) String() string {
	switch p {
	case MissedSkip:
		return "skip"
	case MissedRunOnce:
		return "run-once"
	case MissedRunAll:
		return "run-all"
	default:
		return "unknown"
	}
}
