package cron

import (
	"context"
	"time"
)

// Claimer elects one scheduler instance per fire across replicas. The
// scheduler calls Claim on the fire goroutine, after jitter and any Elector
// check, with a key unique to the entry key and the UTC scheduled instant
// ("report@2026-01-02T15:04:05Z"), so replicas in different time zones agree.
//
// Implementations must be safe for concurrent use and should keep a claim
// reserved long enough that a delayed replica cannot run the same fire later;
// they own their timeout and retention policy. A false, nil result is the
// normal "another instance won" outcome and produces SkipAlreadyClaimed; a
// non-nil error fails closed, skipping the fire with SkipClaimError. Manual
// triggers never claim.
type Claimer interface {
	// Claim reserves fireKey for this instance. It returns true when the
	// caller may run the fire, false when another instance already holds it,
	// and a non-nil error only for backend failures.
	Claim(ctx context.Context, fireKey string) (bool, error)
}

// Elector gates automatic fires on leadership. IsLeader is called on every
// fire goroutine, so it must be safe for concurrent use and cheap enough to
// run once per fire; lease-renewing implementations fit well. A false, nil
// result is the normal follower state and produces SkipNotLeader; a non-nil
// error fails closed with SkipElectionError. Manual triggers bypass the
// elector.
type Elector interface {
	// IsLeader reports whether this instance currently leads. A non-nil error
	// means the answer is unknown, not that the instance is a follower.
	IsLeader(ctx context.Context) (bool, error)
}

// SkipReason classifies why distributed coordination suppressed a fire; it is
// carried by SkippedFireEvent.
type SkipReason uint8

const (
	// SkipUnknown is the zero value; the scheduler never publishes it.
	SkipUnknown SkipReason = iota
	// SkipNotLeader reports that the Elector answered false, nil.
	SkipNotLeader
	// SkipElectionError reports that the Elector returned an error.
	SkipElectionError
	// SkipAlreadyClaimed reports that the Claimer answered false, nil.
	SkipAlreadyClaimed
	// SkipClaimError reports that the Claimer returned an error.
	SkipClaimError
)

// String returns a kebab-case label suitable for metric labels, or "unknown".
func (r SkipReason) String() string {
	switch r {
	case SkipNotLeader:
		return "not-leader"
	case SkipElectionError:
		return "election-error"
	case SkipAlreadyClaimed:
		return "already-claimed"
	case SkipClaimError:
		return "claim-error"
	default:
		return "unknown"
	}
}

// fireKey names one fire of one entry independently of the process time
// zone: scheduledAt carries the host's Local zone, and replicas on hosts with
// different TZ settings must still agree on the claim.
func fireKey(entryKey string, scheduledAt time.Time) string {
	return entryKey + "@" + scheduledAt.UTC().Format(time.RFC3339Nano)
}
