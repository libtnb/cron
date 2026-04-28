package wrap_test

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/libtnb/cron"
	"github.com/libtnb/cron/wrap"
)

func TestRecover_TurnsPanicIntoError(t *testing.T) {
	w := wrap.Recover()
	got := w(cron.JobFunc(func(ctx context.Context) error {
		panic("boom")
	})).Run(context.Background())
	if got == nil || got.Error() == "" {
		t.Fatalf("got %v", got)
	}
}

func TestRecover_PassesThroughErrors(t *testing.T) {
	want := errors.New("normal error")
	got := wrap.Recover()(cron.JobFunc(func(ctx context.Context) error {
		return want
	})).Run(context.Background())
	if !errors.Is(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestTimeout_CancelsWithCause(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var seen error
		j := wrap.Timeout(100 * time.Millisecond)(cron.JobFunc(func(ctx context.Context) error {
			<-ctx.Done()
			seen = context.Cause(ctx)
			return ctx.Err()
		}))
		_ = j.Run(context.Background())
		if !errors.Is(seen, cron.ErrJobTimeout) {
			t.Fatalf("cause = %v", seen)
		}
	})
}

func TestTimeout_ZeroIsNoop(t *testing.T) {
	called := false
	j := wrap.Timeout(0)(cron.JobFunc(func(ctx context.Context) error {
		called = true
		if _, ok := ctx.Deadline(); ok {
			t.Fatal("ctx should have no deadline")
		}
		return nil
	}))
	_ = j.Run(context.Background())
	if !called {
		t.Fatal("not called")
	}
}

func TestSkipIfRunning_DropsConcurrentInvocations(t *testing.T) {
	release := make(chan struct{})
	var ran atomic.Int32
	w := wrap.SkipIfRunning()
	j := w(cron.JobFunc(func(ctx context.Context) error {
		ran.Add(1)
		<-release
		return nil
	}))

	var wg sync.WaitGroup
	results := make(chan error, 5)
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- j.Run(context.Background())
		}()
	}
	// Allow the first goroutine to enter and the rest to be skipped.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	close(results)

	skipped := 0
	for err := range results {
		if errors.Is(err, cron.ErrAlreadyRunning) {
			skipped++
		}
	}
	if skipped < 1 {
		t.Fatalf("expected ≥1 skipped, got %d", skipped)
	}
	if ran.Load() < 1 || ran.Load() > 5 {
		t.Fatalf("ran = %d", ran.Load())
	}
}

func TestDelayIfRunning_SerialisesInvocations(t *testing.T) {
	var inFlight atomic.Int32
	var maxInFlight atomic.Int32
	j := wrap.DelayIfRunning()(cron.JobFunc(func(ctx context.Context) error {
		v := inFlight.Add(1)
		for {
			cur := maxInFlight.Load()
			if v <= cur {
				break
			}
			if maxInFlight.CompareAndSwap(cur, v) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		inFlight.Add(-1)
		return nil
	}))

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() { defer wg.Done(); _ = j.Run(context.Background()) }()
	}
	wg.Wait()
	if got := maxInFlight.Load(); got != 1 {
		t.Fatalf("maxInFlight = %d, want 1", got)
	}
}

func TestDelayIfRunning_ReturnsWhenQueuedContextCancels(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	j := wrap.DelayIfRunning()(cron.JobFunc(func(ctx context.Context) error {
		select {
		case <-started:
		default:
			close(started)
		}
		<-release
		return nil
	}))

	firstDone := make(chan error, 1)
	go func() { firstDone <- j.Run(context.Background()) }()
	<-started

	ctx, cancel := context.WithCancel(context.Background())
	secondDone := make(chan error, 1)
	go func() { secondDone <- j.Run(ctx) }()
	cancel()

	select {
	case err := <-secondDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("queued Run err = %v, want context.Canceled", err)
		}
	case <-time.After(200 * time.Millisecond):
		close(release)
		<-firstDone
		t.Fatal("queued Run did not return after ctx cancellation")
	}

	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first Run err = %v", err)
	}
}

func TestRetry_RetriesOnError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var attempts atomic.Int32
		boom := errors.New("boom")
		policy := cron.RetryPolicy{MaxRetries: 3, Initial: 100 * time.Millisecond, Multiplier: 2.0}
		j := wrap.Retry(policy)(cron.JobFunc(func(ctx context.Context) error {
			attempts.Add(1)
			if attempts.Load() < 3 {
				return boom
			}
			return nil
		}))
		err := j.Run(context.Background())
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if got := attempts.Load(); got != 3 {
			t.Fatalf("attempts = %d, want 3", got)
		}
	})
}

func TestRetry_ReturnsAggregatedErrors(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		boom := errors.New("boom")
		policy := cron.RetryPolicy{MaxRetries: 2, Initial: 10 * time.Millisecond}
		err := wrap.Retry(policy)(cron.JobFunc(func(ctx context.Context) error {
			return boom
		})).Run(context.Background())
		if err == nil {
			t.Fatal("expected error")
		}
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v, expected to wrap boom", err)
		}
	})
}

func TestRetry_AbortsOnContextCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var attempts atomic.Int32
		ctx, cancel := context.WithCancel(context.Background())
		policy := cron.RetryPolicy{MaxRetries: 10, Initial: 200 * time.Millisecond}
		errCh := make(chan error, 1)
		go func() {
			errCh <- wrap.Retry(policy)(cron.JobFunc(func(ctx context.Context) error {
				attempts.Add(1)
				return errors.New("boom")
			})).Run(ctx)
		}()
		time.Sleep(50 * time.Millisecond)
		cancel()
		err := <-errCh
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestRetry_CanceledContextSkipsAttempt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var attempts atomic.Int32
	err := wrap.Retry(cron.RetryPolicy{MaxRetries: 3, Initial: time.Millisecond})(cron.JobFunc(func(ctx context.Context) error {
		attempts.Add(1)
		return errors.New("should not run")
	})).Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if got := attempts.Load(); got != 0 {
		t.Fatalf("attempts = %d, want 0", got)
	}
}

func TestChain_ReExportEquivalent(t *testing.T) {
	var seen []string
	mk := func(s string) cron.Wrapper {
		return func(j cron.Job) cron.Job {
			return cron.JobFunc(func(ctx context.Context) error {
				seen = append(seen, s)
				return j.Run(ctx)
			})
		}
	}
	core := cron.JobFunc(func(ctx context.Context) error { seen = append(seen, "core"); return nil })
	_ = wrap.Chain(mk("a"), mk("b"))(core).Run(context.Background())
	if got := len(seen); got != 3 || seen[0] != "a" || seen[1] != "b" || seen[2] != "core" {
		t.Fatalf("seen = %v", seen)
	}
}

func TestRecover_UsesCustomLogger(t *testing.T) {
	logged := false
	h := slogHandlerFunc(func(_ context.Context, _ slog.Record) error {
		logged = true
		return nil
	})
	w := wrap.Recover(wrap.WithLogger(slog.New(h)))
	err := w(cron.JobFunc(func(ctx context.Context) error {
		panic("boom")
	})).Run(context.Background())
	if err == nil {
		t.Fatal("expected recovered error")
	}
	if !logged {
		t.Fatal("custom logger not invoked")
	}
}

type slogHandlerFunc func(context.Context, slog.Record) error

func (f slogHandlerFunc) Enabled(context.Context, slog.Level) bool { return true }
func (f slogHandlerFunc) Handle(ctx context.Context, r slog.Record) error {
	return f(ctx, r)
}
func (f slogHandlerFunc) WithAttrs([]slog.Attr) slog.Handler { return f }
func (f slogHandlerFunc) WithGroup(string) slog.Handler      { return f }
