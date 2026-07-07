package cron

import (
	"context"
	"strconv"
	"time"
)

// releaseTimeout caps the context used to release a fire lock.
const releaseTimeout = 5 * time.Second

// ReleaseFunc releases a lock acquired by a Locker. Implementations must be
// idempotent. The scheduler calls it with a short-deadline context detached
// from the job's context, so job cancellation never prevents release.
type ReleaseFunc func(ctx context.Context) error

// Locker coordinates fires across scheduler instances. Lock claims key (see
// FireKey); exactly one instance in the fleet succeeds per key. On failure it
// returns an error wrapping ErrLockHeld when another instance holds the
// claim, or any other error for backend failures — either way the fire is
// skipped on this instance (fail-closed) and EventSkipped is emitted.
//
// Implementations own TTL and acquisition-timeout policy; the ctx passed in
// is the scheduler's run context (cancelled on Stop), not a per-call
// deadline. Implementations should retain a claim until its TTL even after
// release: deleting the key on release re-opens the duplicate window that
// fire-scoped keys close (an instance with a larger jitter draw could still
// attempt the same fire). The TTL must exceed max jitter + clock skew + any
// catch-up horizon within which the same instant may be re-attempted.
type Locker interface {
	Lock(ctx context.Context, key string) (ReleaseFunc, error)
}

// Elector reports whether this instance should run scheduled jobs. nil means
// leader: run. Any error — including backend failures — means skip
// (fail-closed: during an outage nobody fires). Backends return an error
// wrapping ErrNotLeader when another instance holds leadership.
type Elector interface {
	IsLeader(ctx context.Context) error
}

// SkipReason classifies why distributed coordination suppressed a fire.
type SkipReason uint8

const (
	SkipNotLeader SkipReason = iota // Elector returned an error
	SkipLockHeld                    // another instance claimed this fire
	SkipLockError                   // Locker backend failure (fail-closed)
)

func (r SkipReason) String() string {
	switch r {
	case SkipNotLeader:
		return "not-leader"
	case SkipLockHeld:
		return "lock-held"
	case SkipLockError:
		return "lock-error"
	default:
		return "unknown"
	}
}

// FireKey is the coordination key for one fire of one entry: "<name>@<unix>",
// or "#<id>@<unix>" for unnamed entries. Including the scheduled time makes
// every fire — including each MissedRunOnce/MissedRunAll catch-up instant —
// its own claim, so deduplication depends on neither lock hold duration nor
// clock agreement between instances.
//
// The name is the cross-instance component: EntryID is process-local, so the
// "#<id>" fallback only matches across identical binaries registering entries
// in identical order. Under a Locker a name must uniquely identify a job;
// entries sharing a name share claims for coinciding instants.
func FireKey(name string, id EntryID, scheduledAt time.Time) string {
	if name == "" {
		name = "#" + strconv.FormatUint(uint64(id), 10)
	}
	return name + "@" + strconv.FormatInt(scheduledAt.Unix(), 10)
}
