package cron

import (
	"testing"
	"time"
)

var utcParser = NewStandardParser(WithDefaultLocation(time.UTC))

func mustParse(t *testing.T, spec string) Schedule {
	t.Helper()
	s, err := utcParser.Parse(spec)
	if err != nil {
		t.Fatalf("Parse(%q): %v", spec, err)
	}
	return s
}

func mustParseSpec(t *testing.T, spec string) *SpecSchedule {
	t.Helper()
	s := mustParse(t, spec)
	got, ok := s.(*SpecSchedule)
	if !ok {
		t.Fatalf("Parse(%q): expected *SpecSchedule, got %T", spec, s)
	}
	return got
}
