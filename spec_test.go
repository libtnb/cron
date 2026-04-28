package cron

import (
	"testing"
	"time"
)

func TestSpecSchedule_NextEveryMinute(t *testing.T) {
	s := mustParseSpec(t, "* * * * *")
	got := s.Next(t0(2026, 1, 1, 0, 0, 30))
	if want := t0(2026, 1, 1, 0, 1, 0); !got.Equal(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestSpecSchedule_DOMandDOW_OrSemantics(t *testing.T) {
	s := mustParseSpec(t, "0 0 1 * 1")
	from := t0(2026, 1, 1, 0, 0, 0)
	got := s.Next(from)
	if want := t0(2026, 1, 5, 0, 0, 0); !got.Equal(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestSpecSchedule_DOMonly_OmitsDOW(t *testing.T) {
	s := mustParseSpec(t, "0 0 1 * *")
	got := s.Next(t0(2026, 1, 15, 0, 0, 0))
	if want := t0(2026, 2, 1, 0, 0, 0); !got.Equal(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestSpecSchedule_DOWonly_OmitsDOM(t *testing.T) {
	s := mustParseSpec(t, "0 0 * * 1")
	got := s.Next(t0(2026, 1, 6, 0, 0, 0))
	if want := t0(2026, 1, 12, 0, 0, 0); !got.Equal(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestSpecSchedule_NoFutureFiring_ReturnsZero(t *testing.T) {
	s := mustParseSpec(t, "0 0 30 2 *")
	got := s.Next(t0(2026, 1, 1, 0, 0, 0))
	if !got.IsZero() {
		t.Fatalf("expected zero time after 5y horizon, got %v", got)
	}
}

func TestSpecSchedule_LeapYearFeb29(t *testing.T) {
	s := mustParseSpec(t, "0 0 29 2 *")
	got := s.Next(t0(2026, 1, 1, 0, 0, 0))
	if want := t0(2028, 2, 29, 0, 0, 0); !got.Equal(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestSpecSchedule_Loc_RespectsTimezone(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("tzdata missing:", err)
	}
	p := NewStandardParser()
	s, err := p.Parse("TZ=America/New_York 0 0 * * *") // midnight NY
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	got := s.Next(from)
	want := time.Date(2026, 1, 2, 0, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestSpecSchedule_DST_SpringForwardSkipsImpossibleHour(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("tzdata missing:", err)
	}
	p := NewStandardParser()
	s, err := p.Parse("TZ=America/New_York 30 2 * * *")
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 3, 8, 1, 0, 0, 0, loc)
	got := s.Next(from)
	if got.IsZero() {
		t.Fatal("Next returned zero, scheduler would stall")
	}
	if !got.After(from) {
		t.Fatalf("Next not advanced: from=%v got=%v", from, got)
	}
}

func TestSpecSchedule_DST_FallBack_BothFiringsObservedAtScheduleLayer(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("tzdata missing:", err)
	}
	p := NewStandardParser()
	s, err := p.Parse("TZ=America/New_York 30 1 * * *")
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 11, 1, 0, 0, 0, 0, loc)
	first := s.Next(from)
	if first.IsZero() {
		t.Fatal("first Next returned zero")
	}
	second := s.Next(first)
	if second.IsZero() {
		t.Fatal("second Next returned zero")
	}
	if first.Equal(second) {
		t.Fatalf("identical absolute times: %v", first)
	}
	if first.Hour() != 1 || second.Hour() != 1 || first.Minute() != 30 || second.Minute() != 30 {
		t.Fatalf("wall-clock not 01:30: first=%v second=%v", first, second)
	}
	_, off1 := first.Zone()
	_, off2 := second.Zone()
	if off1 == off2 {
		t.Fatalf("expected different DST offsets, got %d and %d", off1, off2)
	}
}

func TestSpecSchedule_LogValue(t *testing.T) {
	s := mustParseSpec(t, "@hourly")
	_ = s.LogValue()
}
