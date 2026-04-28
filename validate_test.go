package cron

import (
	"errors"
	"testing"
	"time"
)

func TestValidateSpec_OK(t *testing.T) {
	if err := ValidateSpec("@hourly"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSpec("*/5 * * * *"); err != nil {
		t.Fatal(err)
	}
}

func TestValidateSpec_Error(t *testing.T) {
	err := ValidateSpec("@nope")
	if err == nil {
		t.Fatal("expected error")
	}
	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("got %T, want *ParseError", err)
	}
}

func TestValidateSpec_NilParser(t *testing.T) {
	err := ValidateSpecWith("@hourly", nil)
	if err == nil {
		t.Fatal("expected error for nil parser")
	}
}

func TestAnalyzeSpec_Descriptor(t *testing.T) {
	now := t0(2026, 1, 1, 12, 0, 0)
	a := AnalyzeSpec("@every 30s", now)
	if !a.Valid {
		t.Fatalf("Err = %v", a.Err)
	}
	if a.Descriptor != "@every" || a.Interval != 30*time.Second {
		t.Fatalf("Descriptor=%q Interval=%v", a.Descriptor, a.Interval)
	}
	if want := now.Add(30 * time.Second); !a.NextRun.Equal(want) {
		t.Fatalf("NextRun = %v, want %v", a.NextRun, want)
	}
}

func TestAnalyzeSpec_Standard(t *testing.T) {
	now := t0(2026, 1, 1, 12, 0, 0)
	a := AnalyzeSpec("0 0 * * *", now)
	if !a.Valid {
		t.Fatalf("err: %v", a.Err)
	}
	if a.Location == nil {
		t.Fatal("Location should be populated for SpecSchedule")
	}
	if a.NextRun.IsZero() {
		t.Fatal("NextRun should be set")
	}
}

func TestAnalyzeSpec_Triggered(t *testing.T) {
	ext := parserExtFunc(func(spec string) (Schedule, error) {
		if spec == "@triggered" {
			return TriggeredSchedule(), nil
		}
		return nil, nil
	})
	p := NewStandardParser(WithParserExt(ext))
	a := AnalyzeSpecWith("@triggered", p, t0(2026, 1, 1, 0, 0, 0))
	if !a.Valid || !a.IsTriggered {
		t.Fatalf("Valid=%v IsTriggered=%v", a.Valid, a.IsTriggered)
	}
	if !a.NextRun.IsZero() {
		t.Fatalf("Triggered schedules must not have NextRun: %v", a.NextRun)
	}
}

func TestAnalyzeSpec_Invalid(t *testing.T) {
	a := AnalyzeSpec("garbage * * * *", t0(2026, 1, 1, 0, 0, 0))
	if a.Valid || a.Err == nil {
		t.Fatalf("Valid=%v Err=%v", a.Valid, a.Err)
	}
}

func TestAnalyzeSpec_DescriptorPopulatedForAllAtTokens(t *testing.T) {
	cases := map[string]string{
		"@hourly":       "@hourly",
		"@daily":        "@daily",
		"@midnight":     "@midnight",
		"@weekly":       "@weekly",
		"@monthly":      "@monthly",
		"@yearly":       "@yearly",
		"@annually":     "@annually",
		"@every 5s":     "@every",
		"  @hourly\n":   "@hourly",
		"TZ=UTC @daily": "@daily",
	}
	for spec, want := range cases {
		t.Run(spec, func(t *testing.T) {
			a := AnalyzeSpec(spec, t0(2026, 1, 1, 0, 0, 0))
			if !a.Valid {
				t.Fatalf("Valid=false err=%v", a.Err)
			}
			if a.Descriptor != want {
				t.Fatalf("Descriptor=%q, want %q", a.Descriptor, want)
			}
		})
	}
}

func TestAnalyzeSpec_DescriptorEmptyFor5Field(t *testing.T) {
	a := AnalyzeSpec("*/5 * * * *", t0(2026, 1, 1, 0, 0, 0))
	if !a.Valid {
		t.Fatalf("Valid=false err=%v", a.Err)
	}
	if a.Descriptor != "" {
		t.Fatalf("Descriptor=%q, want empty", a.Descriptor)
	}
}
