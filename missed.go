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
)

const (
	missedRunOnceCap       = 100000
	defaultMissedTolerance = time.Second
)

func (p MissedFirePolicy) String() string {
	switch p {
	case MissedSkip:
		return "skip"
	case MissedRunOnce:
		return "run-once"
	default:
		return "unknown"
	}
}
