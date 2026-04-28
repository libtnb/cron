package cron_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/libtnb/cron"
)

func TestRetryPolicy_IsZero(t *testing.T) {
	if !(cron.RetryPolicy{}).IsZero() {
		t.Fatal("zero policy must be IsZero")
	}
	if (cron.RetryPolicy{MaxRetries: 1}).IsZero() {
		t.Fatal("MaxRetries=1 is non-zero")
	}
	if !(cron.RetryPolicy{Initial: time.Second, Multiplier: 2}).IsZero() {
		t.Fatal("MaxRetries=0 must mean IsZero regardless of other fields")
	}
}

func TestRetry_ConstructorEquivalentToStructLiteral(t *testing.T) {
	got := cron.Retry(3,
		cron.RetryInitial(10*time.Millisecond),
		cron.RetryMaxDelay(100*time.Millisecond),
		cron.RetryMultiplier(2),
		cron.RetryJitterFrac(0.1),
	)
	want := cron.RetryPolicy{
		MaxRetries: 3,
		Initial:    10 * time.Millisecond,
		MaxDelay:   100 * time.Millisecond,
		Multiplier: 2,
		JitterFrac: 0.1,
	}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestRetryPolicy_WrapperRetriesUntilSuccess(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var attempts atomic.Int32
		boom := errors.New("boom")
		p := cron.RetryPolicy{
			MaxRetries: 2,
			Initial:    10 * time.Millisecond,
			MaxDelay:   100 * time.Millisecond,
			Multiplier: 2.0,
			JitterFrac: 0.1,
		}
		j := p.Wrapper()(cron.JobFunc(func(ctx context.Context) error {
			if attempts.Add(1) < 3 {
				return boom
			}
			return nil
		}))
		if err := j.Run(context.Background()); err != nil {
			t.Fatalf("err=%v", err)
		}
		if got := attempts.Load(); got != 3 {
			t.Fatalf("attempts=%d", got)
		}
	})
}

func TestRetryPolicy_WrapperNegativeMaxIsForeverUntilCtx(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := cron.RetryPolicy{MaxRetries: -1, Initial: time.Millisecond}
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		err := p.Wrapper()(cron.JobFunc(func(ctx context.Context) error {
			return errors.New("always")
		})).Run(ctx)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestRetryPolicy_WrapperZeroMaxNoRetry(t *testing.T) {
	var attempts atomic.Int32
	boom := errors.New("boom")
	err := cron.RetryPolicy{}.Wrapper()(cron.JobFunc(func(ctx context.Context) error {
		attempts.Add(1)
		return boom
	})).Run(context.Background())
	if !errors.Is(err, boom) {
		t.Fatalf("err=%v", err)
	}
	if attempts.Load() != 1 {
		t.Fatalf("attempts=%d, want 1 (no retries on zero policy via Wrapper)", attempts.Load())
	}
}

func TestRetryPolicy_WrapperDefaultInitialOneSecond(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var attempts atomic.Int32
		policy := cron.RetryPolicy{MaxRetries: 1}
		err := policy.Wrapper()(cron.JobFunc(func(ctx context.Context) error {
			attempts.Add(1)
			return errors.New("boom")
		})).Run(context.Background())
		if err == nil {
			t.Fatal("expected error")
		}
		if attempts.Load() != 2 {
			t.Fatalf("attempts=%d, want 2", attempts.Load())
		}
	})
}

func TestRetryPolicy_WrapperMaxDelayCaps(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var attempts atomic.Int32
		policy := cron.RetryPolicy{
			MaxRetries: 3,
			Initial:    10 * time.Millisecond,
			MaxDelay:   20 * time.Millisecond,
			Multiplier: 10.0,
		}
		_ = policy.Wrapper()(cron.JobFunc(func(ctx context.Context) error {
			attempts.Add(1)
			return errors.New("boom")
		})).Run(context.Background())
		if attempts.Load() != 4 {
			t.Fatalf("attempts=%d, want 4", attempts.Load())
		}
	})
}

func TestRetryPolicy_WrapperJitterPositive(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var attempts atomic.Int32
		policy := cron.RetryPolicy{
			MaxRetries: 2,
			Initial:    100 * time.Millisecond,
			JitterFrac: 0.5,
		}
		_ = policy.Wrapper()(cron.JobFunc(func(ctx context.Context) error {
			attempts.Add(1)
			return errors.New("boom")
		})).Run(context.Background())
		if attempts.Load() != 3 {
			t.Fatalf("attempts=%d, want 3", attempts.Load())
		}
	})
}
