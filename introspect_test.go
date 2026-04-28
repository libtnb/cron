package cron

import (
	"testing"
	"time"
)

func TestNextN(t *testing.T) {
	s := mustParse(t, "@every 1m")
	from := t0(2026, 1, 1, 0, 0, 0)
	got := NextN(s, from, 3)
	want := []time.Time{
		t0(2026, 1, 1, 0, 1, 0),
		t0(2026, 1, 1, 0, 2, 0),
		t0(2026, 1, 1, 0, 3, 0),
	}
	if len(got) != 3 {
		t.Fatalf("len = %d", len(got))
	}
	for i := range want {
		if !got[i].Equal(want[i]) {
			t.Fatalf("[%d] got %v want %v", i, got[i], want[i])
		}
	}
}

func TestNextN_ZeroOrNegative(t *testing.T) {
	s := mustParse(t, "@hourly")
	if got := NextN(s, time.Now(), 0); got != nil {
		t.Fatalf("got %v", got)
	}
	if got := NextN(s, time.Now(), -1); got != nil {
		t.Fatalf("got %v", got)
	}
}

func TestNextN_TerminatesWhenScheduleExhausts(t *testing.T) {
	s := mustParse(t, "0 0 30 2 *")
	got := NextN(s, t0(2026, 1, 1, 0, 0, 0), 10)
	if len(got) != 0 {
		t.Fatalf("len = %d", len(got))
	}
}

func TestBetween(t *testing.T) {
	s := mustParse(t, "@hourly")
	start := t0(2026, 1, 1, 0, 30, 0)
	end := t0(2026, 1, 1, 5, 30, 0)
	got := Between(s, start, end)
	if len(got) != 5 {
		t.Fatalf("len = %d, want 5", len(got))
	}
}

func TestBetweenWithLimit(t *testing.T) {
	s := mustParse(t, "@hourly")
	start := t0(2026, 1, 1, 0, 30, 0)
	end := t0(2026, 1, 1, 23, 30, 0)
	got := BetweenWithLimit(s, start, end, 3)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
}

func TestBetween_StartAfterEnd(t *testing.T) {
	s := mustParse(t, "@hourly")
	got := Between(s, t0(2026, 1, 2, 0, 0, 0), t0(2026, 1, 1, 0, 0, 0))
	if got != nil {
		t.Fatalf("got %v", got)
	}
}

func TestCount(t *testing.T) {
	s := mustParse(t, "@hourly")
	start := t0(2026, 1, 1, 0, 30, 0)
	end := t0(2026, 1, 2, 0, 30, 0)
	if got := Count(s, start, end); got != 24 {
		t.Fatalf("Count = %d, want 24", got)
	}
	if got := CountWithLimit(s, start, end, 5); got != 5 {
		t.Fatalf("CountWithLimit = %d, want 5", got)
	}
}

func TestMatches(t *testing.T) {
	s := mustParse(t, "@hourly")
	if !Matches(s, t0(2026, 1, 1, 5, 0, 0)) {
		t.Fatal("hourly should match XX:00:00")
	}
	if Matches(s, t0(2026, 1, 1, 5, 30, 0)) {
		t.Fatal("hourly should not match XX:30:00")
	}
}

func TestUpcomingSeq_BreaksEarly(t *testing.T) {
	s := mustParse(t, "@hourly")
	count := 0
	for range UpcomingSeq(s, t0(2026, 1, 1, 0, 0, 0)) {
		count++
		if count == 3 {
			break
		}
	}
	if count != 3 {
		t.Fatalf("count = %d", count)
	}
}

func TestUpcomingSeq_UsesSchedulesUpcomingWhenAvailable(t *testing.T) {
	s := mustParse(t, "@hourly")
	if _, ok := s.(Upcoming); !ok {
		t.Fatal("SpecSchedule should implement Upcoming")
	}
	count := 0
	for range UpcomingSeq(s, t0(2026, 1, 1, 0, 0, 0)) {
		count++
		if count == 5 {
			break
		}
	}
	if count != 5 {
		t.Fatalf("count = %d", count)
	}
}
