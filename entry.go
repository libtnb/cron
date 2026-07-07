package cron

import (
	"context"
	"log/slog"
	"strconv"
	"time"
)

// EntryID is an opaque, process-local identifier.
type EntryID uint64

func (id EntryID) String() string       { return strconv.FormatUint(uint64(id), 10) }
func (id EntryID) LogValue() slog.Value { return slog.StringValue(id.String()) }

// Entry is the public read-only view of a scheduled item. Safe to copy.
type Entry struct {
	ID       EntryID
	Name     string
	Spec     string // empty for AddSchedule entries
	Schedule Schedule
	Prev     time.Time // zero if never fired
	Next     time.Time // zero if exhausted, paused, or TriggeredSchedule
	Paused   bool
}

// Valid reports whether e refers to a registered entry. Zero is invalid.
func (e Entry) Valid() bool { return e.ID != 0 }

// EntryInfo identifies the running invocation inside a job's context.
type EntryInfo struct {
	ID          EntryID
	Name        string
	ScheduledAt time.Time
}

type entryInfoKey struct{}

// EntryInfoFromContext returns the identity of the entry whose job is running
// under ctx. The scheduler injects it for every dispatch, so wrappers and
// jobs can tell which entry — and which fire — they serve.
func EntryInfoFromContext(ctx context.Context) (EntryInfo, bool) {
	info, ok := ctx.Value(entryInfoKey{}).(EntryInfo)
	return info, ok
}

func (e Entry) LogValue() slog.Value {
	attrs := []slog.Attr{slog.String("id", e.ID.String())}
	if e.Name != "" {
		attrs = append(attrs, slog.String("name", e.Name))
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
