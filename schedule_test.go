package cron

import (
	"testing"
	"time"
)

func TestConstantDelay_Next(t *testing.T) {
	d := ConstantDelay(2 * time.Minute)
	from := t0(2026, 1, 1, 0, 0, 0).Add(123 * time.Millisecond)
	got := d.Next(from)
	if want := t0(2026, 1, 1, 0, 2, 0); !got.Equal(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestConstantDelay_SubSecondClampedToOneSecond(t *testing.T) {
	d := ConstantDelay(50 * time.Millisecond)
	from := t0(2026, 1, 1, 0, 0, 0)
	got := d.Next(from)
	if want := t0(2026, 1, 1, 0, 0, 1); !got.Equal(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestConstantDelay_String(t *testing.T) {
	d := ConstantDelay(90 * time.Second)
	if got := d.String(); got != "@every 1m30s" {
		t.Fatalf("String = %q", got)
	}
}

func TestTriggeredSchedule_NeverFires(t *testing.T) {
	s := TriggeredSchedule()
	if got := s.Next(time.Now()); !got.IsZero() {
		t.Fatalf("Next = %v, want zero", got)
	}
	if !IsTriggered(s) {
		t.Fatal("IsTriggered should report true")
	}
	other := ConstantDelay(time.Minute)
	if IsTriggered(other) {
		t.Fatal("IsTriggered should report false for ConstantDelay")
	}
}

func TestTriggeredSchedule_String(t *testing.T) {
	s := TriggeredSchedule()
	stringer, ok := s.(interface{ String() string })
	if !ok {
		t.Fatal("triggeredSchedule should implement Stringer")
	}
	if got := stringer.String(); got != "@triggered" {
		t.Fatalf("String = %q, want @triggered", got)
	}
}

func TestAlignedDelay(t *testing.T) {
	d := AlignedDelay(5 * time.Minute)

	// Epoch alignment: staggered observers converge on the same instant.
	a := d.Next(t0(2026, 1, 1, 12, 0, 3))
	b := d.Next(t0(2026, 1, 1, 12, 1, 17))
	want := t0(2026, 1, 1, 12, 5, 0)
	if !a.Equal(want) || !b.Equal(want) {
		t.Fatalf("staggered nexts = %v / %v, want both %v", a, b, want)
	}

	// Strictly after, even from an exact boundary.
	if got := d.Next(want); !got.Equal(t0(2026, 1, 1, 12, 10, 0)) {
		t.Fatalf("from boundary: got %v", got)
	}

	if got := AlignedDelay(0).Next(time.Now()); !got.IsZero() {
		t.Fatalf("non-positive interval: got %v", got)
	}
	if got := AlignedDelay(time.Minute).String(); got != "@aligned 1m0s" {
		t.Fatalf("String = %q", got)
	}
}

func TestOnceAt(t *testing.T) {
	at := t0(2026, 6, 1, 12, 0, 0)
	s := OnceAt(at)
	if got := s.Next(at.Add(-time.Hour)); !got.Equal(at) {
		t.Fatalf("before: got %v, want %v", got, at)
	}
	if got := s.Next(at); !got.IsZero() {
		t.Fatalf("at the instant: got %v, want zero (strictly after)", got)
	}
	if got := s.Next(at.Add(time.Hour)); !got.IsZero() {
		t.Fatalf("after: got %v, want zero", got)
	}
	str, ok := s.(interface{ String() string })
	if !ok || str.String() != "@at 2026-06-01T12:00:00Z" {
		t.Fatalf("String = %v", str)
	}
}

func TestUnion(t *testing.T) {
	t1 := t0(2026, 1, 1, 6, 0, 0)
	t2 := t0(2026, 1, 1, 9, 0, 0)
	u := Union(OnceAt(t2), nil, OnceAt(t1))

	from := t0(2026, 1, 1, 0, 0, 0)
	if got := u.Next(from); !got.Equal(t1) {
		t.Fatalf("first: got %v, want %v", got, t1)
	}
	if got := u.Next(t1); !got.Equal(t2) {
		t.Fatalf("second: got %v, want %v", got, t2)
	}
	if got := u.Next(t2); !got.IsZero() {
		t.Fatalf("exhausted: got %v", got)
	}
	if got := Union().Next(from); !got.IsZero() {
		t.Fatalf("empty union: got %v", got)
	}
}

func TestFilter(t *testing.T) {
	base := ConstantDelay(time.Minute)
	from := t0(2026, 1, 1, 0, 0, 30)

	odd := Filter(base, func(t time.Time) bool { return t.Minute()%2 == 1 })
	if got := odd.Next(from); got.Minute() != 1 {
		t.Fatalf("odd filter: got %v", got)
	}
	pass := Filter(base, nil)
	if got := pass.Next(from); !got.Equal(base.Next(from)) {
		t.Fatalf("nil keep: got %v", got)
	}
	if got := Filter(nil, nil).Next(from); !got.IsZero() {
		t.Fatalf("nil schedule: got %v", got)
	}
	// A finite schedule that never satisfies keep returns zero via exhaustion.
	never := Filter(OnceAt(from.Add(time.Hour)), func(time.Time) bool { return false })
	if got := never.Next(from); !got.IsZero() {
		t.Fatalf("exhausted: got %v", got)
	}
	// An infinite schedule that never satisfies keep gives up at the scan cap.
	capped := Filter(base, func(time.Time) bool { return false })
	if got := capped.Next(from); !got.IsZero() {
		t.Fatalf("cap: got %v", got)
	}
}
