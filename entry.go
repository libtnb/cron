package cron

import (
	"context"
	"log/slog"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/libtnb/cron/internal/heap"
)

// EntryID identifies one registration for the lifetime of a Cron. IDs are
// process-local, allocated sequentially from 1 and never reused, so a stale
// ID yields ErrEntryNotFound rather than another entry. Zero is never a valid
// ID; see Entry.Valid.
type EntryID uint64

// String formats the ID as a decimal number.
func (id EntryID) String() string { return strconv.FormatUint(uint64(id), 10) }

// LogValue renders the ID as a string attribute so log output does not depend
// on integer formatting.
func (id EntryID) LogValue() slog.Value { return slog.StringValue(id.String()) }

// Entry is a point-in-time snapshot of a registered entry, as returned by
// Cron.Entry and Cron.Entries. It is a plain value: copying it or holding it
// across scheduler changes is safe, but it does not update.
type Entry struct {
	ID       EntryID
	Name     string // display label from WithName; not unique
	Key      string // stable identity for distributed fire claims (WithKey)
	Spec     string // source expression; empty for AddSchedule entries
	Schedule Schedule
	Prev     time.Time // last automatic fire or the WithLastRun seed; zero if never fired
	Next     time.Time // zero if exhausted, paused, or TriggeredSchedule
	Paused   bool
}

// Valid reports whether e describes a registered entry. The zero Entry, which
// Cron.Entry returns with ok == false, is not valid.
func (e Entry) Valid() bool { return e.ID != 0 }

// LogValue renders the snapshot as a slog group, omitting zero fields.
func (e Entry) LogValue() slog.Value {
	attrs := []slog.Attr{slog.String("id", e.ID.String())}
	if e.Name != "" {
		attrs = append(attrs, slog.String("name", e.Name))
	}
	if e.Key != "" {
		attrs = append(attrs, slog.String("key", e.Key))
	}
	if e.Spec != "" {
		attrs = append(attrs, slog.String("spec", e.Spec))
	}
	if !e.Prev.IsZero() {
		attrs = append(attrs, slog.Time("prev", e.Prev))
	}
	if !e.Next.IsZero() {
		attrs = append(attrs, slog.Time("next", e.Next))
	}
	if e.Paused {
		attrs = append(attrs, slog.Bool("paused", true))
	}
	return slog.GroupValue(attrs...)
}

// EntryInfo identifies the invocation a job is serving; retrieve it with
// EntryInfoFromContext. ScheduledAt is the fire instant the job was dispatched
// for, or the wall-clock time for manual triggers.
type EntryInfo struct {
	ID          EntryID
	Name        string
	Key         string
	ScheduledAt time.Time
}

// entryInfoKey is the context key under which dispatch stores EntryInfo.
type entryInfoKey struct{}

// EntryInfoFromContext returns the identity of the entry whose job is running
// under ctx. The scheduler injects it for every dispatch, including manual
// triggers, so wrappers and jobs can tell which entry and which fire they
// serve. ok is false when ctx did not come from the scheduler, for example
// when a Job is run directly in tests.
func EntryInfoFromContext(ctx context.Context) (EntryInfo, bool) {
	info, ok := ctx.Value(entryInfoKey{}).(EntryInfo)
	return info, ok
}

// entry is the scheduler's canonical record of one registration. Fields up to
// claimer are immutable after add; the rest are guarded by Cron.mu.
type entry struct {
	id       EntryID
	name     string
	key      string
	spec     string
	schedule Schedule
	wrapped  Job // global+entry chain applied
	timeout  time.Duration
	jitter   time.Duration
	missed   MissedFirePolicy
	claimer  Claimer

	next   time.Time
	prev   time.Time
	paused bool
	gen    uint64 // bumped by Pause/Resume/Update; stales in-flight fire plans

	item *heap.Item[*entry] // nil iff not in the heap
	view *viewCell          // snapshot cell, stable for the entry's lifetime
}

// viewCell holds an entry's published snapshot. Fires swap the value with an
// atomic store; Add/Remove mutate the enclosing map under viewMu.
type viewCell struct {
	p atomic.Pointer[Entry]
}

// viewMap indexes snapshot cells by entry ID. Only the map structure needs
// viewMu; the cells themselves are read atomically.
type viewMap map[EntryID]*viewCell

// entryView copies the mutable state of e into a snapshot. Callers hold
// Cron.mu.
func entryView(e *entry) Entry {
	return Entry{
		ID:       e.id,
		Name:     e.name,
		Key:      e.key,
		Spec:     e.spec,
		Schedule: e.schedule,
		Prev:     e.prev,
		Next:     e.next,
		Paused:   e.paused,
	}
}

// compareNext orders entries by Next, with zero times (exhausted or triggered)
// sorted last.
func compareNext(a, b time.Time) int {
	switch {
	case a.IsZero() && b.IsZero():
		return 0
	case a.IsZero():
		return 1
	case b.IsZero():
		return -1
	default:
		return a.Compare(b)
	}
}
