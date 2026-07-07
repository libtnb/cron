package redislock_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/libtnb/cron"
	"github.com/libtnb/cron/lockers/redis"
)

func newRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return mr, client
}

func TestLocker_AcquireAndHeld(t *testing.T) {
	_, client := newRedis(t)
	l := redislock.NewLocker(client)
	ctx := context.Background()

	release, err := l.Lock(ctx, "job@100")
	if err != nil {
		t.Fatal(err)
	}
	if err := release(ctx); err != nil {
		t.Fatal(err)
	}

	// The claim is retained after release: a second acquire must fail.
	if _, err := l.Lock(ctx, "job@100"); !errors.Is(err, cron.ErrLockHeld) {
		t.Fatalf("second acquire = %v, want ErrLockHeld", err)
	}
	// A different fire key is independent.
	if _, err := l.Lock(ctx, "job@101"); err != nil {
		t.Fatalf("distinct key: %v", err)
	}
}

func TestLocker_TTLExpiryReopensKey(t *testing.T) {
	mr, client := newRedis(t)
	l := redislock.NewLocker(client, redislock.WithTTL(time.Minute), redislock.WithKeyPrefix("t:"))
	ctx := context.Background()

	if _, err := l.Lock(ctx, "job@1"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Lock(ctx, "job@1"); !errors.Is(err, cron.ErrLockHeld) {
		t.Fatalf("want held, got %v", err)
	}
	mr.FastForward(2 * time.Minute)
	if _, err := l.Lock(ctx, "job@1"); err != nil {
		t.Fatalf("after TTL expiry: %v", err)
	}
}

func TestLocker_BackendOutage(t *testing.T) {
	mr, client := newRedis(t)
	l := redislock.NewLocker(client)
	mr.Close()

	_, err := l.Lock(context.Background(), "job@1")
	if err == nil || errors.Is(err, cron.ErrLockHeld) {
		t.Fatalf("outage must surface as a backend error, got %v", err)
	}
}

type skipCounter struct{ skips atomic.Int64 }

func (h *skipCounter) OnSkipped(cron.EventSkipped) { h.skips.Add(1) }

func TestLocker_EndToEndTwoCrons(t *testing.T) {
	_, client := newRedis(t)
	shared := redislock.NewLocker(client)

	var runs atomic.Int64
	job := cron.JobFunc(func(context.Context) error {
		runs.Add(1)
		return nil
	})
	h := &skipCounter{}
	a := cron.New(cron.WithLocation(time.UTC), cron.WithLocker(shared), cron.WithHooks(h))
	b := cron.New(cron.WithLocation(time.UTC), cron.WithLocker(shared), cron.WithHooks(h))
	_, _ = a.Add("@every 1s", job, cron.WithName("shared"))
	_, _ = b.Add("@every 1s", job, cron.WithName("shared"))

	_ = a.Start()
	_ = b.Start()
	time.Sleep(2500 * time.Millisecond)
	_ = a.Stop(context.Background())
	_ = b.Stop(context.Background())

	// ConstantDelay's whole-second alignment gives both instances identical
	// fire instants, so every fire must be exactly one run plus one skip.
	if runs.Load() < 1 {
		t.Fatal("no fires happened")
	}
	if got, want := h.skips.Load(), runs.Load(); got != want {
		t.Fatalf("skips = %d, runs = %d; every fire must be one run + one skip", got, want)
	}
}
