package cron_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/libtnb/cron"
	"github.com/libtnb/cron/wrap"
)

func TestOptions_AllSetters(t *testing.T) {
	loc, _ := time.LoadLocation("UTC")
	logger := slog.New(slog.DiscardHandler)
	c := cron.New(
		cron.WithLocation(loc),
		cron.WithParser(cron.NewStandardParser(cron.WithSeconds())),
		cron.WithLogger(logger),
		cron.WithChain(wrap.Recover(), wrap.Timeout(time.Second)),
		cron.WithJitter(time.Millisecond),
		cron.WithHooks(emptyHook{}),
		cron.WithHookBuffer(64),
		cron.WithMissedFire(cron.MissedRunOnce),
		cron.WithMissedTolerance(2*time.Second),
		cron.WithMaxConcurrent(4),
		cron.WithMaxEntries(8),
		cron.WithRetry(cron.RetryPolicy{MaxRetries: 1, Initial: time.Millisecond}),
		cron.WithRecorder(struct{}{}),
	)
	if c == nil {
		t.Fatal("New returned nil")
	}
	_, err := c.Add("@every 1m", cron.JobFunc(func(ctx context.Context) error { return nil }),
		cron.WithName("x"),
		cron.WithTimeout(time.Second),
		cron.WithEntryChain(wrap.SkipIfRunning()),
		cron.WithEntryRetry(cron.RetryPolicy{}),
	)
	if err != nil {
		t.Fatal(err)
	}
}

func TestDefaultParser_FollowsCronLocation(t *testing.T) {
	zone := time.FixedZone("cron-zone", 3*60*60)
	c := cron.New(cron.WithLocation(zone))
	id, err := c.Add("0 9 * * *", cron.JobFunc(func(ctx context.Context) error { return nil }))
	if err != nil {
		t.Fatal(err)
	}
	e, ok := c.Entry(id)
	if !ok {
		t.Fatal("entry not found")
	}
	type locationProvider interface{ Location() *time.Location }
	got := e.Schedule.(locationProvider).Location()
	if got.String() != "cron-zone" {
		t.Fatalf("parser location = %v, want cron-zone", got)
	}
}

func TestExplicitParser_LocationIsIndependentOfCronLocation(t *testing.T) {
	parserZone := time.FixedZone("parser-zone", -2*60*60)
	c := cron.New(
		cron.WithLocation(time.FixedZone("cron-zone", 3*60*60)),
		cron.WithParser(cron.NewStandardParser(cron.WithDefaultLocation(parserZone))),
	)
	id, err := c.Add("0 9 * * *", cron.JobFunc(func(ctx context.Context) error { return nil }))
	if err != nil {
		t.Fatal(err)
	}
	e, ok := c.Entry(id)
	if !ok {
		t.Fatal("entry not found")
	}
	type locationProvider interface{ Location() *time.Location }
	got := e.Schedule.(locationProvider).Location()
	if got.String() != "parser-zone" {
		t.Fatalf("parser location = %v, want parser-zone", got)
	}
}

func TestWithParser_LastWins(t *testing.T) {
	custom := parserFunc(func(string) (cron.Schedule, error) {
		return cron.ConstantDelay(time.Hour), nil
	})

	c := cron.New(
		cron.WithParser(cron.NewStandardParser(cron.WithSeconds())),
		cron.WithParser(custom),
	)
	id, err := c.Add("not a standard spec", cron.JobFunc(func(ctx context.Context) error { return nil }))
	if err != nil {
		t.Fatal(err)
	}
	e, ok := c.Entry(id)
	if !ok {
		t.Fatal("entry not found")
	}
	if _, ok := e.Schedule.(cron.ConstantDelay); !ok {
		t.Fatalf("schedule = %T, want custom parser schedule", e.Schedule)
	}

	c = cron.New(
		cron.WithParser(custom),
		cron.WithParser(cron.NewStandardParser(cron.WithSeconds())),
	)
	if _, err := c.Add("not a standard spec", cron.JobFunc(func(ctx context.Context) error { return nil })); err == nil {
		t.Fatal("later WithParser(standard) should restore standard parsing")
	}
}

type parserFunc func(string) (cron.Schedule, error)

func (f parserFunc) Parse(spec string) (cron.Schedule, error) { return f(spec) }

type emptyHook struct{}

func (emptyHook) OnSchedule(cron.EventSchedule)       {}
func (emptyHook) OnJobStart(cron.EventJobStart)       {}
func (emptyHook) OnJobComplete(cron.EventJobComplete) {}
func (emptyHook) OnMissedFire(cron.EventMissed)       {}

func TestWithSecondsField(t *testing.T) {
	c := cron.New(cron.WithSecondsField())
	if _, err := c.Add("0 30 9 * * *", cron.JobFunc(noop)); err != nil {
		t.Fatalf("6-field spec with WithSecondsField should parse: %v", err)
	}
}

func TestWithParserIgnoresLocation(t *testing.T) {
	c := cron.New(
		cron.WithParser(cron.NewStandardParser(cron.WithDefaultLocation(time.UTC))),
		cron.WithLocation(time.UTC),
	)
	if _, err := c.Add("* * * * *", cron.JobFunc(noop)); err != nil {
		t.Fatal(err)
	}
}
