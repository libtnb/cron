package cron

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParser_Standard5Field(t *testing.T) {
	p := utcParser
	cases := []struct {
		spec string
		from time.Time
		want time.Time
	}{
		{"* * * * *", t0(2026, 1, 1, 0, 0, 0), t0(2026, 1, 1, 0, 1, 0)},
		{"*/5 * * * *", t0(2026, 1, 1, 0, 0, 0), t0(2026, 1, 1, 0, 5, 0)},
		{"0 * * * *", t0(2026, 1, 1, 0, 30, 0), t0(2026, 1, 1, 1, 0, 0)},
		{"0 0 * * *", t0(2026, 1, 1, 12, 0, 0), t0(2026, 1, 2, 0, 0, 0)},
		{"0 0 1 * *", t0(2026, 1, 15, 0, 0, 0), t0(2026, 2, 1, 0, 0, 0)},
		{"0 0 1 1 *", t0(2026, 6, 1, 0, 0, 0), t0(2027, 1, 1, 0, 0, 0)},
		{"0 0 * * 1", t0(2026, 1, 1, 0, 0, 0), t0(2026, 1, 5, 0, 0, 0)}, // first Mon Jan 2026 = 5th
		{"30 14 * * 1-5", t0(2026, 1, 1, 0, 0, 0), t0(2026, 1, 1, 14, 30, 0)},
		{"0 0 * jan mon-fri", t0(2026, 1, 1, 0, 0, 0), t0(2026, 1, 2, 0, 0, 0)},
		{"15,45 * * * *", t0(2026, 1, 1, 0, 0, 0), t0(2026, 1, 1, 0, 15, 0)},
		{"15,45 * * * *", t0(2026, 1, 1, 0, 20, 0), t0(2026, 1, 1, 0, 45, 0)},
	}
	for _, c := range cases {
		t.Run(c.spec, func(t *testing.T) {
			s, err := p.Parse(c.spec)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			got := s.Next(c.from)
			if !got.Equal(c.want) {
				t.Fatalf("Next(%v) = %v, want %v", c.from, got, c.want)
			}
		})
	}
}

func TestParser_WithSeconds(t *testing.T) {
	p := NewStandardParser(WithSeconds(), WithDefaultLocation(time.UTC))
	s, err := p.Parse("*/5 * * * * *")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := s.Next(t0(2026, 1, 1, 0, 0, 0))
	if want := t0(2026, 1, 1, 0, 0, 5); !got.Equal(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParser_Descriptors(t *testing.T) {
	p := utcParser
	from := t0(2026, 6, 15, 12, 30, 0)
	cases := []struct {
		spec string
		want time.Time
	}{
		{"@yearly", t0(2027, 1, 1, 0, 0, 0)},
		{"@annually", t0(2027, 1, 1, 0, 0, 0)},
		{"@monthly", t0(2026, 7, 1, 0, 0, 0)},
		{"@daily", t0(2026, 6, 16, 0, 0, 0)},
		{"@midnight", t0(2026, 6, 16, 0, 0, 0)},
		{"@hourly", t0(2026, 6, 15, 13, 0, 0)},
	}
	for _, c := range cases {
		t.Run(c.spec, func(t *testing.T) {
			s, err := p.Parse(c.spec)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			got := s.Next(from)
			if !got.Equal(c.want) {
				t.Fatalf("Next = %v, want %v", got, c.want)
			}
		})
	}
}

func TestParser_DescriptorWeeklyIsSunday(t *testing.T) {
	p := utcParser
	s, err := p.Parse("@weekly")
	if err != nil {
		t.Fatal(err)
	}
	from := t0(2026, 1, 1, 0, 0, 0) // Thursday
	got := s.Next(from)
	if want := t0(2026, 1, 4, 0, 0, 0); !got.Equal(want) {
		t.Fatalf("Next = %v, want %v (Sunday)", got, want)
	}
}

func TestParser_Every(t *testing.T) {
	p := utcParser
	s, err := p.Parse("@every 90s")
	if err != nil {
		t.Fatal(err)
	}
	got := s.Next(t0(2026, 1, 1, 0, 0, 0))
	if want := t0(2026, 1, 1, 0, 1, 30); !got.Equal(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParser_TZPrefix(t *testing.T) {
	p := utcParser
	for _, prefix := range []string{"TZ=", "CRON_TZ="} {
		s, err := p.Parse(prefix + "America/New_York 0 0 * * *")
		if err != nil {
			t.Fatalf("%s: %v", prefix, err)
		}
		spec, ok := s.(*SpecSchedule)
		if !ok {
			t.Fatalf("%s: not SpecSchedule, got %T", prefix, s)
		}
		if spec.Location().String() != "America/New_York" {
			t.Fatalf("%s: Loc = %s", prefix, spec.Location())
		}
	}
}

func TestParser_NamedFields(t *testing.T) {
	p := utcParser
	cases := []string{
		"0 0 * jan *",
		"0 0 * JAN *",
		"0 0 * jan-mar *",
		"0 0 * * mon",
		"0 0 * * MON-FRI",
		"0 0 * * sun,sat",
	}
	for _, spec := range cases {
		t.Run(spec, func(t *testing.T) {
			if _, err := p.Parse(spec); err != nil {
				t.Fatalf("Parse: %v", err)
			}
		})
	}
}

func TestParser_QuestionMark(t *testing.T) {
	p := utcParser
	s, err := p.Parse("0 0 ? * MON")
	if err != nil {
		t.Fatal(err)
	}
	from := t0(2026, 1, 1, 0, 0, 0)
	got := s.Next(from)
	if want := t0(2026, 1, 5, 0, 0, 0); !got.Equal(want) { // first Mon
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParser_StepWithRange(t *testing.T) {
	p := utcParser
	s, err := p.Parse("0-30/10 * * * *")
	if err != nil {
		t.Fatal(err)
	}
	from := t0(2026, 1, 1, 0, 0, 0)
	expected := []time.Time{
		t0(2026, 1, 1, 0, 10, 0),
		t0(2026, 1, 1, 0, 20, 0),
		t0(2026, 1, 1, 0, 30, 0),
		t0(2026, 1, 1, 1, 0, 0),
	}
	cur := from
	for _, w := range expected {
		cur = s.Next(cur)
		if !cur.Equal(w) {
			t.Fatalf("got %v, want %v", cur, w)
		}
	}
}

func TestParser_ParseErrors(t *testing.T) {
	p := utcParser
	cases := []struct {
		spec string
		want string // substring expected in error message
	}{
		{"", "empty spec"},
		{"* * * *", "expected 5 fields"},
		{"60 * * * *", "above maximum"},
		{"* * 0 * *", "below minimum"}, // dom min=1
		{"5-3 * * * *", "beyond end"},
		{"* * * * * *", "expected 5 fields"},
		{"@bogus", "unrecognized descriptor"},
		{"@every nope", "invalid duration"},
		{"@every -5s", "must be > 0"},
		{"TZ=Atlantis/Bermuda 0 0 * * *", "unknown time zone"},
		{"a/b/c * * * *", "too many slashes"},
		{"1-2-3 * * * *", "too many dashes"},
		{"foo * * * *", "not a valid number or name"},
	}
	for _, c := range cases {
		t.Run(c.spec, func(t *testing.T) {
			_, err := p.Parse(c.spec)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			var pe *ParseError
			if !errors.As(err, &pe) {
				t.Fatalf("expected *ParseError, got %T (%v)", err, err)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q lacks substring %q", err, c.want)
			}
		})
	}
}

func TestParser_Ext_TakesPrecedence(t *testing.T) {
	called := false
	want := ConstantDelay(time.Hour)
	ext := parserExtFunc(func(spec string) (Schedule, error) {
		called = true
		if spec == "EXT" {
			return want, nil
		}
		return nil, nil
	})
	p := NewStandardParser(WithParserExt(ext), WithDefaultLocation(time.UTC))

	s, err := p.Parse("EXT")
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("ext not invoked")
	}
	if s != want {
		t.Fatalf("got %v, want %v", s, want)
	}

	if _, err := p.Parse("@hourly"); err != nil {
		t.Fatalf("fallthrough failed: %v", err)
	}
}

type parserExtFunc func(spec string) (Schedule, error)

func (f parserExtFunc) Parse(spec string) (Schedule, error) { return f(spec) }

func TestParser_Ext_ErrorPropagated(t *testing.T) {
	want := errors.New("ext failed")
	ext := parserExtFunc(func(spec string) (Schedule, error) {
		return nil, want
	})
	p := NewStandardParser(WithParserExt(ext), WithDefaultLocation(time.UTC))
	if _, err := p.Parse("anything"); !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

func TestParser_TZWithEmptyRemainderRejected(t *testing.T) {
	p := NewStandardParser()
	_, err := p.Parse("TZ=UTC ")
	if err == nil {
		t.Fatal("expected error for TZ-only spec")
	}
	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v, want *ParseError", err)
	}
}

func TestParser_NSlashStep(t *testing.T) {
	p := NewStandardParser()
	s, err := p.Parse("5/10 * * * *")
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	want := time.Date(2026, time.January, 1, 0, 5, 0, 0, time.UTC)
	if got := s.Next(from); !got.Equal(want) {
		t.Fatalf("Next = %v, want %v", got, want)
	}
}

func TestParser_InvalidStep(t *testing.T) {
	p := NewStandardParser()
	cases := []string{
		"*/0 * * * *",
		"*/abc * * * *",
		"*/1/2 * * * *",
	}
	for _, spec := range cases {
		t.Run(spec, func(t *testing.T) {
			if _, err := p.Parse(spec); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestParser_InvalidRange(t *testing.T) {
	p := NewStandardParser()
	cases := []string{
		"abc * * * *",
		"1-x * * * *",
		"1-2-3 * * * *",
	}
	for _, spec := range cases {
		t.Run(spec, func(t *testing.T) {
			if _, err := p.Parse(spec); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

// t0 builds a UTC time for tests.
func t0(year int, month time.Month, day, hour, minute, second int) time.Time {
	return time.Date(year, month, day, hour, minute, second, 0, time.UTC)
}
