package cron

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func BenchmarkSpecSchedule_Next(b *testing.B) {
	cases := []struct {
		name string
		spec string
		from time.Time
	}{
		{"daily_midnight", "0 0 * * *", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"every_minute", "* * * * *", time.Date(2026, 1, 1, 0, 0, 30, 0, time.UTC)},
		{"every_5min", "*/5 * * * *", time.Date(2026, 1, 1, 0, 7, 0, 0, time.UTC)},
		{"yearly_jan1", "0 0 1 1 *", time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)},
		{"weekly_mon", "0 0 * * 1", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"sparse_15_45", "15,45 14 * * *", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		s := mustParseSpecB(b, c.spec)
		b.Run(c.name, func(b *testing.B) {
			for b.Loop() {
				_ = s.Next(c.from)
			}
		})
	}
}

func BenchmarkParser_Parse_Standard(b *testing.B) {
	p := NewStandardParser(WithDefaultLocation(time.UTC))
	for b.Loop() {
		_, _ = p.Parse("*/5 0 * * *")
	}
}

func BenchmarkParser_Parse_Descriptor(b *testing.B) {
	p := NewStandardParser(WithDefaultLocation(time.UTC))
	for b.Loop() {
		_, _ = p.Parse("@hourly")
	}
}

func BenchmarkAdd_Throughput(b *testing.B) {
	c := New(WithLocation(time.UTC))
	for b.Loop() {
		_, _ = c.Add("@every 1m", noopJob{})
	}
}

func BenchmarkAdd_AtSize(b *testing.B) {
	for _, n := range []int{10, 100, 1000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			c := New(WithLocation(time.UTC))
			for range n {
				_, _ = c.Add("@every 1m", noopJob{})
			}
			b.ReportAllocs()
			for b.Loop() {
				id, _ := c.Add("@every 1m", noopJob{})
				c.Remove(id)
			}
		})
	}
}

func BenchmarkSnapshotLookup(b *testing.B) {
	c := New(WithLocation(time.UTC))
	id, err := c.Add("@every 1m", noopJob{})
	if err != nil {
		b.Fatal(err)
	}
	for b.Loop() {
		if _, ok := c.Entry(id); !ok {
			b.Fatal("missing")
		}
	}
}

type noopJob struct{}

func (noopJob) Run(_ context.Context) error { return nil }

func mustParseSpecB(b *testing.B, spec string) *SpecSchedule {
	b.Helper()
	p := NewStandardParser(WithDefaultLocation(time.UTC))
	got, err := p.Parse(spec)
	if err != nil {
		b.Fatalf("parse: %v", err)
	}
	s, ok := got.(*SpecSchedule)
	if !ok {
		b.Fatalf("got %T", got)
	}
	return s
}
