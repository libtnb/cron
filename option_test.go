package cron_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/libtnb/cron"
	"github.com/libtnb/cron/wrap"
)

func TestOptions_AllSetters(t *testing.T) {
	loc, _ := time.LoadLocation("UTC")
	logger := slog.New(slog.DiscardHandler)
	c := cron.MustNew(
		cron.WithLocation(loc),
		cron.WithParser(cron.NewStandardParser(cron.WithOptionalSeconds())),
		cron.WithLogger(logger),
		cron.WithChain(wrap.Recover(), wrap.Timeout(time.Second)),
		cron.WithJitter(time.Millisecond),
		cron.WithObservers(emptyObserver{}),
		cron.WithObserverBuffer(64),
		cron.WithMissedFire(cron.MissedRunOnce),
		cron.WithMissedTolerance(2*time.Second),
		cron.WithMaxConcurrent(4),
		cron.WithMaxEntries(8),
		cron.WithRetry(cron.RetryPolicy{MaxRetries: 1, Initial: time.Millisecond}),
		cron.WithRecorder(cron.RecorderFunc(func(cron.Event) {})),
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
	c := cron.MustNew(cron.WithLocation(zone))
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
	c := cron.MustNew(
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

	c := cron.MustNew(
		cron.WithParser(cron.NewStandardParser(cron.WithOptionalSeconds())),
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

	c = cron.MustNew(
		cron.WithParser(custom),
		cron.WithParser(cron.NewStandardParser(cron.WithOptionalSeconds())),
	)
	if _, err := c.Add("not a standard spec", cron.JobFunc(func(ctx context.Context) error { return nil })); err == nil {
		t.Fatal("later WithParser(standard) should restore standard parsing")
	}
}

type parserFunc func(string) (cron.Schedule, error)

func (f parserFunc) Parse(spec string) (cron.Schedule, error) { return f(spec) }

type emptyObserver struct{}

func (emptyObserver) Observe(cron.Event) {}

func TestWithSecondsField(t *testing.T) {
	c := cron.MustNew(cron.WithSecondsField())
	if _, err := c.Add("0 30 9 * * *", cron.JobFunc(noop)); err != nil {
		t.Fatalf("6-field spec with WithSecondsField should parse: %v", err)
	}
}

func TestWithParserIgnoresLocation(t *testing.T) {
	c := cron.MustNew(
		cron.WithParser(cron.NewStandardParser(cron.WithDefaultLocation(time.UTC))),
		cron.WithLocation(time.UTC),
	)
	if _, err := c.Add("* * * * *", cron.JobFunc(noop)); err != nil {
		t.Fatal(err)
	}
}

func TestNew_RejectsInvalidOptions(t *testing.T) {
	var typedNilParser parserFunc
	var typedNilClaimer *fakeClaimer
	var typedNilElector *fakeElector
	tests := []struct {
		name string
		opt  cron.Option
	}{
		{name: "nil option", opt: nil},
		{name: "nil location", opt: cron.WithLocation(nil)},
		{name: "nil parser", opt: cron.WithParser(nil)},
		{name: "typed nil parser", opt: cron.WithParser(typedNilParser)},
		{name: "nil logger", opt: cron.WithLogger(nil)},
		{name: "nil wrapper", opt: cron.WithChain(nil)},
		{name: "negative jitter", opt: cron.WithJitter(-time.Second)},
		{name: "negative observer buffer", opt: cron.WithObserverBuffer(-1)},
		{name: "unknown missed policy", opt: cron.WithMissedFire(cron.MissedFirePolicy(99))},
		{name: "zero missed tolerance", opt: cron.WithMissedTolerance(0)},
		{name: "negative max concurrent", opt: cron.WithMaxConcurrent(-1)},
		{name: "negative max entries", opt: cron.WithMaxEntries(-1)},
		{name: "invalid retry", opt: cron.WithRetry(cron.RetryPolicy{JitterFrac: 2})},
		{name: "negative retry initial", opt: cron.WithRetry(cron.RetryPolicy{Initial: -time.Second})},
		{name: "negative retry max delay", opt: cron.WithRetry(cron.RetryPolicy{MaxDelay: -time.Second})},
		{name: "negative retry multiplier", opt: cron.WithRetry(cron.RetryPolicy{Multiplier: -1})},
		{name: "negative retry jitter", opt: cron.WithRetry(cron.RetryPolicy{JitterFrac: -1})},
		{name: "nil context", opt: cron.WithBaseContext(nil)},
		{name: "nil claimer", opt: cron.WithClaimer(nil)},
		{name: "typed nil claimer", opt: cron.WithClaimer(typedNilClaimer)},
		{name: "nil elector", opt: cron.WithElector(nil)},
		{name: "typed nil elector", opt: cron.WithElector(typedNilElector)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := cron.New(test.opt); err == nil {
				t.Fatal("New accepted invalid option")
			}
		})
	}
	if _, err := cron.New(); err != nil {
		t.Fatalf("New() = %v", err)
	}
}

func TestOptions_NormalizeOptionalTypedNils(t *testing.T) {
	var claimer *fakeClaimer
	c := cron.MustNew()
	if _, err := c.Add("@hourly", cron.JobFunc(noop), cron.WithEntryClaimer(claimer)); err != nil {
		t.Fatal(err)
	}

	var parser parserFunc
	p := cron.NewStandardParser(cron.WithParserExt(parser), cron.WithDefaultLocation(time.UTC))
	if _, err := p.Parse("@hourly"); err != nil {
		t.Fatalf("typed-nil parser extension was not normalized: %v", err)
	}
}

func TestEntryOptions_RejectInvalidValues(t *testing.T) {
	tests := []struct {
		name string
		opt  cron.EntryOption
	}{
		{name: "nil option", opt: nil},
		{name: "negative timeout", opt: cron.WithTimeout(-time.Second)},
		{name: "nil wrapper", opt: cron.WithEntryChain(nil)},
		{name: "invalid retry", opt: cron.WithEntryRetry(cron.RetryPolicy{Initial: -time.Second})},
		{name: "unknown missed policy", opt: cron.WithEntryMissedFire(cron.MissedFirePolicy(99))},
		{name: "negative jitter", opt: cron.WithEntryJitter(-time.Second)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c := cron.MustNew()
			_, err := c.Add("@hourly", cron.JobFunc(noop), test.opt)
			if !errors.Is(err, cron.ErrInvalidOption) {
				t.Fatalf("Add error = %v", err)
			}
		})
	}
}

func TestMustNew_PanicsOnInvalidOption(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("MustNew did not panic")
		}
	}()
	cron.MustNew(cron.WithMaxEntries(-1))
}
