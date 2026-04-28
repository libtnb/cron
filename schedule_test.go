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
