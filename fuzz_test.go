package cron

import (
	"testing"
	"time"
)

func FuzzParser_StandardNeverPanics(f *testing.F) {
	seeds := []string{
		"* * * * *",
		"@every 1s",
		"@hourly",
		"0 0 * * *",
		"*/5 * * * *",
		"15,45 * * * *",
		"TZ=UTC 0 0 * * *",
		"@every 90s",
		"0-30/5 * * * *",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	p := NewStandardParser()
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	f.Fuzz(func(t *testing.T, spec string) {
		s, err := p.Parse(spec)
		if err != nil {
			return
		}
		cur := from
		for range 32 {
			next := s.Next(cur)
			if next.IsZero() {
				return
			}
			if !next.After(cur) {
				t.Fatalf("non-monotonic: spec=%q cur=%v next=%v", spec, cur, next)
			}
			cur = next
		}
	})
}
