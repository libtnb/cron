package cron

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestBackoff_JitterClampToZero(t *testing.T) {
	p := RetryPolicy{Initial: time.Microsecond, JitterFrac: 100}
	for range 200 {
		if p.backoff(0) == 0 {
			return
		}
	}
	t.Fatal("expected at least one zero backoff with extreme JitterFrac")
}

func TestWrapper_PreCancelledCtxReturnsImmediately(t *testing.T) {
	p := RetryPolicy{MaxRetries: 5, Initial: time.Microsecond}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	calls := 0
	job := JobFunc(func(context.Context) error {
		calls++
		return errors.New("should not run")
	})
	err := p.Wrapper()(job).Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v, want context.Canceled", err)
	}
	if calls != 0 {
		t.Fatalf("calls=%d, want 0 (loop must short-circuit before j.Run)", calls)
	}
}

type alwaysFailJob struct{}

func (alwaysFailJob) Run(context.Context) error { return errors.New("boom") }

func TestWrapper_BackoffZeroSkipsTimer(t *testing.T) {
	p := RetryPolicy{
		MaxRetries: 30,
		Initial:    time.Microsecond,
		JitterFrac: 100, // jit >> d so rand frequently lands on a zero/negative pre-clamp
	}
	_ = p.Wrapper()(alwaysFailJob{}).Run(context.Background())
}
