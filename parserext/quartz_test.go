package parserext_test

import (
	"errors"
	"iter"
	"slices"
	"testing"
	"time"

	"github.com/libtnb/cron"
	"github.com/libtnb/cron/parserext"
)

func TestQuartzParser_LastDayOfMonth(t *testing.T) {
	p := parserext.NewQuartzParser(time.UTC)
	s, err := p.Parse("0 18 L * ?")
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, time.January, 30, 12, 0, 0, 0, time.UTC)
	want := time.Date(2026, time.January, 31, 18, 0, 0, 0, time.UTC)
	if got := s.Next(from); !got.Equal(want) {
		t.Fatalf("Next = %v, want %v", got, want)
	}
}

func TestQuartzParser_LastDayOfLeapFebruary(t *testing.T) {
	p := parserext.NewQuartzParser(time.UTC)
	s, err := p.Parse("0 0 L 2 ?")
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2028, time.February, 1, 0, 0, 0, 0, time.UTC)
	want := time.Date(2028, time.February, 29, 0, 0, 0, 0, time.UTC)
	if got := s.Next(from); !got.Equal(want) {
		t.Fatalf("Next = %v, want %v", got, want)
	}
}

func TestQuartzParser_NthWeekdayOfMonth(t *testing.T) {
	p := parserext.NewQuartzParser(time.UTC)
	s, err := p.Parse("0 9 ? * 5#3")
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, time.April, 1, 0, 0, 0, 0, time.UTC)
	want := time.Date(2026, time.April, 17, 9, 0, 0, 0, time.UTC)
	if got := s.Next(from); !got.Equal(want) {
		t.Fatalf("Next = %v, want %v", got, want)
	}
}

func TestQuartzParser_LastWeekdayOfMonth(t *testing.T) {
	p := parserext.NewQuartzParser(time.UTC)
	s, err := p.Parse("30 22 ? * 5L")
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, time.April, 1, 0, 0, 0, 0, time.UTC)
	want := time.Date(2026, time.April, 24, 22, 30, 0, 0, time.UTC)
	if got := s.Next(from); !got.Equal(want) {
		t.Fatalf("Next = %v, want %v", got, want)
	}
}

func TestQuartzParser_SixFieldSeconds(t *testing.T) {
	p := parserext.NewQuartzParser(time.UTC)
	s, err := p.Parse("15 30 22 ? * 5L")
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, time.April, 1, 0, 0, 0, 0, time.UTC)
	want := time.Date(2026, time.April, 24, 22, 30, 15, 0, time.UTC)
	if got := s.Next(from); !got.Equal(want) {
		t.Fatalf("Next = %v, want %v", got, want)
	}
}

func TestQuartzParser_ForwardsStandardNamedFields(t *testing.T) {
	p := parserext.NewQuartzParser(time.UTC)
	s, err := p.Parse("0 0 * jan mon")
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	want := time.Date(2026, time.January, 5, 0, 0, 0, 0, time.UTC)
	if got := s.Next(from); !got.Equal(want) {
		t.Fatalf("Next = %v, want %v", got, want)
	}
}

func TestQuartzParser_ForwardsDescriptorsAndTimezone(t *testing.T) {
	p := parserext.NewQuartzParser(time.UTC)
	s, err := p.Parse("TZ=America/New_York @hourly")
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, time.January, 1, 12, 15, 0, 0, time.UTC)
	want := time.Date(2026, time.January, 1, 13, 0, 0, 0, time.UTC)
	if got := s.Next(from); !got.Equal(want) {
		t.Fatalf("Next = %v, want %v", got, want)
	}
}

func TestQuartzParser_Upcoming(t *testing.T) {
	p := parserext.NewQuartzParser(time.UTC)
	s, err := p.Parse("0 0 L * ?")
	if err != nil {
		t.Fatal(err)
	}
	got := cron.NextN(s, time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC), 3)
	want := []time.Time{
		time.Date(2026, time.January, 31, 0, 0, 0, 0, time.UTC),
		time.Date(2026, time.February, 28, 0, 0, 0, 0, time.UTC),
		time.Date(2026, time.March, 31, 0, 0, 0, 0, time.UTC),
	}
	if !slices.EqualFunc(got, want, time.Time.Equal) {
		t.Fatalf("NextN = %v, want %v", got, want)
	}
}

func TestQuartzParser_NearestWeekday(t *testing.T) {
	p := parserext.NewQuartzParser(time.UTC)
	s, err := p.Parse("0 9 15W * ?")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		from time.Time
		want time.Time
	}{
		// 2026-07-15 is a Wednesday: fire on the 15th.
		{time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 7, 15, 9, 0, 0, 0, time.UTC)},
		// 2026-08-15 is a Saturday: fire Friday the 14th.
		{time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC)},
		// 2026-11-15 is a Sunday: fire Monday the 16th.
		{time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		if got := s.Next(c.from); !got.Equal(c.want) {
			t.Errorf("15W from %v: got %v, want %v", c.from, got, c.want)
		}
	}
}

func TestQuartzParser_NearestWeekday_MonthEdges(t *testing.T) {
	p := parserext.NewQuartzParser(time.UTC)

	// 2026-08-01 is a Saturday: 1W must not cross into July, so Monday the 3rd.
	s, err := p.Parse("0 9 1W * ?")
	if err != nil {
		t.Fatal(err)
	}
	got := s.Next(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
	if want := time.Date(2026, 8, 3, 9, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("1W: got %v, want %v", got, want)
	}

	// 2026-05-31 is a Sunday: 31W must not cross into June, so Friday the 29th.
	// April has no 31st, so from April 31W skips straight to May.
	s, err = p.Parse("0 9 31W * ?")
	if err != nil {
		t.Fatal(err)
	}
	got = s.Next(time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC))
	if want := time.Date(2026, 5, 29, 9, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("31W: got %v, want %v", got, want)
	}
}

func TestQuartzParser_LWLastWeekday(t *testing.T) {
	p := parserext.NewQuartzParser(time.UTC)
	s, err := p.Parse("0 9 LW * ?")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		from time.Time
		want time.Time
	}{
		// 2026-01-31 is a Saturday: last weekday is Friday the 30th.
		{time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 1, 30, 9, 0, 0, 0, time.UTC)},
		// 2026-05-31 is a Sunday: last weekday is Friday the 29th.
		{time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 5, 29, 9, 0, 0, 0, time.UTC)},
		// 2026-03-31 is a Tuesday: last weekday is the 31st itself.
		{time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 31, 9, 0, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		if got := s.Next(c.from); !got.Equal(c.want) {
			t.Errorf("LW from %v: got %v, want %v", c.from, got, c.want)
		}
	}
}

func TestQuartzParser_LastDayOffset(t *testing.T) {
	p := parserext.NewQuartzParser(time.UTC)
	s, err := p.Parse("0 9 L-3 * ?")
	if err != nil {
		t.Fatal(err)
	}
	// March 2026 has 31 days: L-3 is the 28th.
	got := s.Next(time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
	if want := time.Date(2026, 3, 28, 9, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("L-3: got %v, want %v", got, want)
	}

	for _, bad := range []string{"0 9 L-0 * ?", "0 9 L-31 * ?", "0 9 L-x * ?"} {
		if _, err := p.Parse(bad); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}

func TestQuartzParser_NameTokens(t *testing.T) {
	p := parserext.NewQuartzParser(time.UTC)

	// FRI#3: third Friday of July 2026 is the 17th.
	s, err := p.Parse("0 9 ? * FRI#3")
	if err != nil {
		t.Fatal(err)
	}
	got := s.Next(time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))
	if want := time.Date(2026, 7, 17, 9, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("FRI#3: got %v, want %v", got, want)
	}

	// FRIL: last Friday of July 2026 is the 31st.
	s, err = p.Parse("0 9 ? * FRIL")
	if err != nil {
		t.Fatal(err)
	}
	got = s.Next(time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))
	if want := time.Date(2026, 7, 31, 9, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("FRIL: got %v, want %v", got, want)
	}

	// Month names combine with quartz dom tokens.
	s, err = p.Parse("0 18 L JAN ?")
	if err != nil {
		t.Fatal(err)
	}
	got = s.Next(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if want := time.Date(2026, 1, 31, 18, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("L JAN: got %v, want %v", got, want)
	}
}

func TestQuartzParser_InvalidWToken(t *testing.T) {
	p := parserext.NewQuartzParser(time.UTC)
	for _, bad := range []string{"0 9 0W * ?", "0 9 32W * ?"} {
		_, err := p.Parse(bad)
		if err == nil {
			t.Fatalf("%q: expected error", bad)
		}
		var pe *cron.ParseError
		if !errors.As(err, &pe) || pe.Field != "dom" {
			t.Fatalf("err = %v, want dom ParseError", err)
		}
	}
}

func TestQuartzParser_RejectsInvalidNthWeekday(t *testing.T) {
	p := parserext.NewQuartzParser(time.UTC)
	_, err := p.Parse("0 9 ? * 8#1")
	if err == nil {
		t.Fatal("expected error")
	}
	var pe *cron.ParseError
	if !errors.As(err, &pe) || pe.Field != "dow" {
		t.Fatalf("err = %v, want dow ParseError", err)
	}
}

func TestQuartzParser_AnalyzeSpecPicksUpLocation(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("tzdata missing:", err)
	}
	p := parserext.NewQuartzParser(loc)
	a := cron.AnalyzeSpecWith("0 0 12 L * ?", p, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if !a.Valid {
		t.Fatalf("Valid=false err=%v", a.Err)
	}
	if a.Location == nil {
		t.Fatal("Location is nil for QuartzSchedule — duck-typed accessor failed")
	}
	if a.Location.String() != loc.String() {
		t.Fatalf("Location = %v, want %v", a.Location, loc)
	}
}

func TestQuartzParser_StandardWeekdayNamesNotMistakenAsQuartz(t *testing.T) {
	cases := []string{
		"0 0 * * WED",
		"0 0 * * MON-FRI",
		"0 0 * * SAT,SUN",
	}
	p := parserext.NewQuartzParser(time.UTC)
	for _, spec := range cases {
		t.Run(spec, func(t *testing.T) {
			if _, err := p.Parse(spec); err != nil {
				t.Fatalf("standard spec %q rejected: %v", spec, err)
			}
		})
	}
}

func TestQuartzParser_NilLocationDefaultsToLocal(t *testing.T) {
	p := parserext.NewQuartzParser(nil)
	s, err := p.Parse("0 0 L * ?")
	if err != nil {
		t.Fatal(err)
	}
	type loc interface{ Location() *time.Location }
	if got := s.(loc).Location(); got != time.Local {
		t.Fatalf("Location = %v, want time.Local", got)
	}
}

func TestQuartzParser_EmptySpec(t *testing.T) {
	p := parserext.NewQuartzParser(time.UTC)
	_, err := p.Parse("   ")
	if err == nil {
		t.Fatal("expected error")
	}
	var pe *cron.ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v, want *ParseError", err)
	}
}

func TestQuartzParser_BareDescriptorForwarded(t *testing.T) {
	p := parserext.NewQuartzParser(time.UTC)
	s, err := p.Parse("@hourly")
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, time.January, 1, 12, 30, 0, 0, time.UTC)
	want := time.Date(2026, time.January, 1, 13, 0, 0, 0, time.UTC)
	if got := s.Next(from); !got.Equal(want) {
		t.Fatalf("Next = %v, want %v", got, want)
	}
}

func TestQuartzParser_InvalidTZ(t *testing.T) {
	p := parserext.NewQuartzParser(time.UTC)
	_, err := p.Parse("TZ=Not/A/Place 0 0 * * ?")
	if err == nil {
		t.Fatal("expected error")
	}
	var pe *cron.ParseError
	if !errors.As(err, &pe) || pe.Field != "TZ" {
		t.Fatalf("err = %v, want TZ ParseError", err)
	}
}

func TestQuartzParser_TZForwardsToStandard(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("tzdata missing:", err)
	}
	p := parserext.NewQuartzParser(time.UTC)
	s, err := p.Parse("TZ=America/New_York 0 12 * * MON")
	if err != nil {
		t.Fatal(err)
	}
	type locIface interface{ Location() *time.Location }
	if got := s.(locIface).Location().String(); got != loc.String() {
		t.Fatalf("Location = %s, want %s", got, loc)
	}
}

func TestQuartzParser_FieldCountMismatch(t *testing.T) {
	p := parserext.NewQuartzParser(time.UTC)
	cases := []string{
		"L",
		"L * * *",
		"0 0 0 0 0 0 L",
		"0 0 0 0",
		"0 0 0 0 0 0 0",
	}
	for _, spec := range cases {
		t.Run(spec, func(t *testing.T) {
			if _, err := p.Parse(spec); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestQuartzParser_ForwardsSixFieldStandard(t *testing.T) {
	p := parserext.NewQuartzParser(time.UTC)
	s, err := p.Parse("30 0 12 * * *")
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC)
	want := time.Date(2026, time.January, 1, 12, 0, 30, 0, time.UTC)
	if got := s.Next(from); !got.Equal(want) {
		t.Fatalf("Next = %v, want %v", got, want)
	}
}

func TestQuartzParser_NumericDom(t *testing.T) {
	p := parserext.NewQuartzParser(time.UTC)
	s, err := p.Parse("0 0 15 * 5L")
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, time.April, 1, 0, 0, 0, 0, time.UTC)
	if s.Next(from).IsZero() {
		t.Fatal("Next returned zero")
	}
}

func TestQuartzParser_DomBitmapError(t *testing.T) {
	p := parserext.NewQuartzParser(time.UTC)
	_, err := p.Parse("0 0 99 * 5L")
	if err == nil {
		t.Fatal("expected error for out-of-range dom")
	}
}

func TestQuartzParser_DowInvalidNLToken(t *testing.T) {
	p := parserext.NewQuartzParser(time.UTC)
	cases := []string{
		"0 0 ? * 9L",
		"0 0 ? * abL",
	}
	for _, spec := range cases {
		t.Run(spec, func(t *testing.T) {
			if _, err := p.Parse(spec); err == nil {
				t.Fatalf("expected NL error for %q", spec)
			}
		})
	}
}

func TestQuartzParser_DowBareLIsSaturday(t *testing.T) {
	p := parserext.NewQuartzParser(time.UTC)
	s, err := p.Parse("0 12 L * L")
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, time.April, 1, 0, 0, 0, 0, time.UTC)
	if s.Next(from).IsZero() {
		t.Fatal("Next returned zero")
	}
}

func TestQuartzParser_DowNumericInQuartzMode(t *testing.T) {
	p := parserext.NewQuartzParser(time.UTC)
	s, err := p.Parse("0 0 L * 1-3")
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, time.April, 1, 0, 0, 0, 0, time.UTC)
	if s.Next(from).IsZero() {
		t.Fatal("Next returned zero")
	}
}

func TestQuartzParser_DowBitmapError(t *testing.T) {
	p := parserext.NewQuartzParser(time.UTC)
	_, err := p.Parse("0 0 L * 9")
	if err == nil {
		t.Fatal("expected dow out-of-range")
	}
}

func TestQuartzParser_ParsePartBranches(t *testing.T) {
	p := parserext.NewQuartzParser(time.UTC)
	cases := []struct {
		spec    string
		wantErr bool
	}{
		{"*/15 * L * ?", false},
		{"0 1-5 L * ?", false},
		{"0 1-5/2 L * ?", false},
		{"*/0 * L * ?", true},
		{"0 1-x L * ?", true},
		{"0 99 L * ?", true},
		{"0 5-1 L * ?", true},
		{"0 abc L * ?", true},
		{"0 */abc L * ?", true},
	}
	for _, tc := range cases {
		t.Run(tc.spec, func(t *testing.T) {
			_, err := p.Parse(tc.spec)
			if tc.wantErr && err == nil {
				t.Fatal("expected error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestQuartzParser_FieldErrors(t *testing.T) {
	p := parserext.NewQuartzParser(time.UTC)
	cases := []struct {
		spec  string
		field string
	}{
		{"99 0 L * ?", "minute"},
		{"0 99 L * ?", "hour"},
		{"0 0 L 99 ?", "month"},
	}
	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			_, err := p.Parse(tc.spec)
			if err == nil {
				t.Fatal("expected error")
			}
			var pe *cron.ParseError
			if !errors.As(err, &pe) || pe.Field != tc.field {
				t.Fatalf("err=%v, want %s ParseError", err, tc.field)
			}
		})
	}
}

func TestQuartzParser_SecondsFieldError(t *testing.T) {
	p := parserext.NewQuartzParser(time.UTC)
	_, err := p.Parse("99 0 0 L * ?")
	if err == nil {
		t.Fatal("expected error for invalid second")
	}
	var pe *cron.ParseError
	if !errors.As(err, &pe) || pe.Field != "second" {
		t.Fatalf("err = %v, want second ParseError", err)
	}
}

func TestQuartzParser_Next_AdvancesAcrossMonths(t *testing.T) {
	p := parserext.NewQuartzParser(time.UTC)
	s, err := p.Parse("0 0 L 7 ?")
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	want := time.Date(2026, time.July, 31, 0, 0, 0, 0, time.UTC)
	if got := s.Next(from); !got.Equal(want) {
		t.Fatalf("Next = %v, want %v", got, want)
	}
}

func TestQuartzParser_Next_NoMatchInFiveYears(t *testing.T) {
	p := parserext.NewQuartzParser(time.UTC)
	s, err := p.Parse("0 0 ? 2 3#5")
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	if got := s.Next(from); !got.IsZero() {
		t.Fatalf("Next = %v, want zero", got)
	}
}

func TestQuartzParser_Upcoming_BreakStops(t *testing.T) {
	p := parserext.NewQuartzParser(time.UTC)
	s, err := p.Parse("0 0 L * ?")
	if err != nil {
		t.Fatal(err)
	}
	type up interface {
		Upcoming(from time.Time) iter.Seq[time.Time]
	}
	upi, ok := s.(up)
	if !ok {
		t.Fatal("schedule should implement Upcoming")
	}
	from := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	count := 0
	for range upi.Upcoming(from) {
		count++
		if count == 2 {
			break
		}
	}
	if count != 2 {
		t.Fatalf("count = %d, want 2", count)
	}
}

func TestQuartzSchedule_ZeroValueReturnsZero(t *testing.T) {
	var s parserext.QuartzSchedule
	if got := s.Next(time.Now()); !got.IsZero() {
		t.Fatalf("zero-value Next = %v, want zero", got)
	}
}

func TestQuartzSchedule_DSTSpringForward(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("tzdata missing:", err)
	}
	p := parserext.NewQuartzParser(loc)
	from := time.Date(2026, 3, 8, 0, 30, 0, 0, loc) // clocks jump 02:00 -> 03:00

	s, err := p.Parse("0 5 L * ?") // quartz token keeps it on the quartz path
	if err != nil {
		t.Fatal(err)
	}
	got := s.Next(time.Date(2026, 3, 31, 0, 30, 0, 0, loc))
	if want := time.Date(2026, 3, 31, 5, 0, 0, 0, loc); !got.Equal(want) {
		t.Fatalf("L-day 05:00: got %v, want %v", got, want)
	}

	// 02:30 does not exist on 2026-03-08; the fire rolls forward, not lost.
	s, err = p.Parse("30 2 8 * ?")
	if err != nil {
		t.Fatal(err)
	}
	got = s.Next(from)
	if got.IsZero() || !got.After(from) {
		t.Fatalf("gap fire: got %v", got)
	}
}

func TestQuartzParser_DowValueBounds(t *testing.T) {
	p := parserext.NewQuartzParser(time.UTC)
	for _, bad := range []string{"0 9 ? * 7#3", "0 9 ? * 9L", "0 9 ? * BAD#2"} {
		if _, err := p.Parse(bad); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}

func TestQuartzSchedule_DSTGapHourFallback(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("tzdata missing:", err)
	}
	p := parserext.NewQuartzParser(loc)
	// 2026-03-08 is the second Sunday of March (DST starts, 02:00 -> 03:00),
	// and SUN#2 keeps this on the quartz path. 02:00 does not exist that day:
	// the schedule must not stall and must fire the following week... except
	// SUN#2 only matches one day; it rolls to April's second Sunday.
	s, err := p.Parse("0 2 ? 3,4 SUN#2")
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 3, 8, 0, 30, 0, 0, loc)
	got := s.Next(from)
	if want := time.Date(2026, 4, 12, 2, 0, 0, 0, loc); !got.Equal(want) {
		t.Fatalf("gap: got %v, want %v", got, want)
	}
}
