package cron

import (
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
	Next     time.Time // zero if exhausted or TriggeredSchedule
}

// Valid reports whether e refers to a registered entry. Zero is invalid.
func (e Entry) Valid() bool { return e.ID != 0 }

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
	return slog.GroupValue(attrs...)
}
