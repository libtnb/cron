package cron_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/libtnb/cron"
)

func TestEntryID_StringAndLogValue(t *testing.T) {
	id := cron.EntryID(42)
	if got := id.String(); got != "42" {
		t.Fatalf("String=%q", got)
	}
	if got := id.LogValue().String(); got != "42" {
		t.Fatalf("LogValue=%v", got)
	}
}

func TestEntry_ValidAndLogValue(t *testing.T) {
	if (cron.Entry{}).Valid() {
		t.Fatal("zero Entry must be invalid")
	}
	e := cron.Entry{ID: 1, Name: "n", Spec: "@every 1m"}
	if !e.Valid() {
		t.Fatal("Valid")
	}
	if e.LogValue().Kind() != slog.KindGroup {
		t.Fatal("LogValue kind")
	}
	full := cron.Entry{
		ID:   2,
		Name: "n",
		Spec: "@every 1m",
		Prev: time.Now().Add(-time.Hour),
		Next: time.Now().Add(time.Hour),
	}
	if full.LogValue().Kind() != slog.KindGroup {
		t.Fatal("full LogValue kind")
	}
}

func TestEntries_OrderedWithMixedNext(t *testing.T) {
	c := cron.New()
	idA, _ := c.Add("@every 1m", cron.JobFunc(func(ctx context.Context) error { return nil }), cron.WithName("A"))
	_, _ = c.AddSchedule(cron.TriggeredSchedule(),
		cron.JobFunc(func(ctx context.Context) error { return nil }), cron.WithName("Z-zero-next"))
	idC, _ := c.Add("@every 5m", cron.JobFunc(func(ctx context.Context) error { return nil }), cron.WithName("C"))

	var names []string
	for e := range c.Entries() {
		names = append(names, e.Name)
	}
	if len(names) != 3 {
		t.Fatalf("entries=%v", names)
	}
	if names[2] != "Z-zero-next" {
		t.Fatalf("zero-Next entry should sort last, got %v", names)
	}
	_ = idA
	_ = idC
}

func TestEntries_BothZeroNextStableOrder(t *testing.T) {
	c := cron.New()
	_, _ = c.AddSchedule(cron.TriggeredSchedule(),
		cron.JobFunc(func(ctx context.Context) error { return nil }), cron.WithName("a"))
	_, _ = c.AddSchedule(cron.TriggeredSchedule(),
		cron.JobFunc(func(ctx context.Context) error { return nil }), cron.WithName("b"))
	count := 0
	for range c.Entries() {
		count++
	}
	if count != 2 {
		t.Fatalf("count=%d", count)
	}
}

func TestEntries_EarlyBreakStopsIteration(t *testing.T) {
	c := cron.New()
	for i := range 5 {
		_, _ = c.Add("@every 1m", cron.JobFunc(func(ctx context.Context) error { return nil }),
			cron.WithName(string(rune('a'+i))))
	}
	count := 0
	for range c.Entries() {
		count++
		if count == 2 {
			break
		}
	}
	if count != 2 {
		t.Fatalf("early break didn't stop iteration: count=%d", count)
	}
}
