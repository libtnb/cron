package cron

import (
	"context"
	"time"
)

// Claimer lets one scheduler instance claim a fire. false, nil means another
// instance already claimed it; a non-nil error is a backend failure. Claims
// must remain reserved long enough that a delayed replica cannot run the same
// fire later. Implementations own their timeout and retention policy.
type Claimer interface {
	Claim(ctx context.Context, fireKey string) (bool, error)
}

// Elector reports whether this instance currently leads. false, nil is the
// normal follower state; a non-nil error is a backend failure.
type Elector interface {
	IsLeader(ctx context.Context) (bool, error)
}

// SkipReason classifies why distributed coordination suppressed a fire.
type SkipReason uint8

const (
	SkipUnknown SkipReason = iota
	SkipNotLeader
	SkipElectionError
	SkipAlreadyClaimed
	SkipClaimError
)

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

func fireKey(entryKey string, scheduledAt time.Time) string {
	return entryKey + "@" + scheduledAt.Format(time.RFC3339Nano)
}
