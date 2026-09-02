package cron

import "time"

// This file selects the instants a late entry fires. Schedule.Next is
// monotone in its argument, so instead of walking every missed firing the
// search bisects the offset from the last fire: about 60 Next calls locate
// the newest missed instant (MissedRunOnce) or the newest missedRunAllCap
// instants (MissedRunAll), however long the outage lasted.

const (
	missedRunAllCap        = 1000 // max fires dispatched by MissedRunAll
	defaultMissedTolerance = time.Minute
)

// MissedFirePolicy decides what happens when an entry fires later than
// WithMissedTolerance allows: after a scheduler stall, a suspended process, or
// a restart seeded with WithLastRun. A MissedFireEvent is published whatever
// the policy. Set it per scheduler with WithMissedFire or per entry with
// WithEntryMissedFire.
type MissedFirePolicy uint8

const (
	// MissedRunOnce runs the job once for the most recent missed firing (the
	// latest Schedule.Next result not after now), then resumes normally. It
	// is the default: late work still happens, but a backlog never turns into
	// a burst.
	MissedRunOnce MissedFirePolicy = iota

	// MissedSkip drops the missed firings and resumes from the next scheduled
	// time.
	MissedSkip

	// MissedRunAll runs the job once per missed firing, keeping only the
	// newest 1000 when the backlog is larger. The replays are dispatched
	// oldest first as separate concurrent invocations, each subject to
	// WithMaxConcurrent.
	MissedRunAll
)

// String returns "run-once", "skip", "run-all", or "unknown".
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

func (p MissedFirePolicy) valid() bool {
	return p <= MissedRunAll
}

// findMostRecentMissed returns the latest firing in [lastFire, now], or zero
// when lastFire is unset or in the future.
//
// Next is monotone in its argument, so the latest firing not after now is
// Next(c) for the largest c with Next(c) <= now. Bisecting c over
// [lastFire, now] finds it in about 60 Next calls however long the backlog
// is; a forward walk would need one call per missed firing.
func findMostRecentMissed(s Schedule, lastFire, now time.Time) time.Time {
	if lastFire.IsZero() || lastFire.After(now) {
		return time.Time{}
	}
	if first := s.Next(lastFire); first.IsZero() || first.After(now) {
		return lastFire
	}
	// Invariant: Next(lastFire+lo) <= now and Next(lastFire+hi) is past now
	// or exhausted. Offsets keep lastFire's location on the result.
	lo, hi := time.Duration(0), now.Sub(lastFire)
	for hi-lo > 1 {
		mid := lo + (hi-lo)/2
		if n := s.Next(lastFire.Add(mid)); n.IsZero() || n.After(now) {
			hi = mid
		} else {
			lo = mid
		}
	}
	return s.Next(lastFire.Add(lo))
}

// findAllMissed returns every firing in [lastFire, now] in increasing order,
// keeping only the newest missedRunAllCap when the backlog is larger. A
// backlog within the cap is collected by one forward walk; a larger one is
// located by bisecting for the latest instant that still has missedRunAllCap
// firings after it, so the kept window is the newest, not the oldest.
func findAllMissed(s Schedule, lastFire, now time.Time) []time.Time {
	if lastFire.IsZero() || lastFire.After(now) {
		return nil
	}
	all := append([]time.Time{lastFire}, firesAfter(s, lastFire, now, missedRunAllCap)...)
	if len(all) <= missedRunAllCap {
		return all
	}
	// Invariant: at least missedRunAllCap firings follow lastFire+lo, fewer
	// follow lastFire+hi. The loop ends with exactly missedRunAllCap after lo.
	lo, hi := time.Duration(0), now.Sub(lastFire)
	for hi-lo > 1 {
		mid := lo + (hi-lo)/2
		if countFiresAfter(s, lastFire.Add(mid), now, missedRunAllCap) >= missedRunAllCap {
			lo = mid
		} else {
			hi = mid
		}
	}
	return firesAfter(s, lastFire.Add(lo), now, missedRunAllCap)
}

// firesAfter walks Next from after until now, returning at most limit
// firings in (after, now]. A schedule that stops advancing ends the walk.
func firesAfter(s Schedule, after, now time.Time, limit int) []time.Time {
	var out []time.Time
	cur := after
	for len(out) < limit {
		n := s.Next(cur)
		exhausted := n.IsZero()
		beyondNow := n.After(now)
		stalled := !n.After(cur) // contract violation; do not loop forever
		if exhausted || beyondNow || stalled {
			break
		}
		out = append(out, n)
		cur = n
	}
	return out
}

// countFiresAfter counts firings in (after, now], stopping at limit.
func countFiresAfter(s Schedule, after, now time.Time, limit int) int {
	var count int
	cur := after
	for count < limit {
		n := s.Next(cur)
		exhausted := n.IsZero()
		beyondNow := n.After(now)
		stalled := !n.After(cur) // contract violation; do not loop forever
		if exhausted || beyondNow || stalled {
			break
		}
		count++
		cur = n
	}
	return count
}
