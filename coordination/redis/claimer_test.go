package rediscoord_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/libtnb/cron"
	"github.com/libtnb/cron/coordination/redis"
)

func newRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return server, client
}

func TestClaimer_ClaimAndRetain(t *testing.T) {
	_, client := newRedis(t)
	claimer, err := rediscoord.NewClaimer(client)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	claimed, err := claimer.Claim(ctx, "job@100")
	if err != nil || !claimed {
		t.Fatalf("first claim = %v, %v", claimed, err)
	}
	claimed, err = claimer.Claim(ctx, "job@100")
	if err != nil || claimed {
		t.Fatalf("second claim = %v, %v; want false, nil", claimed, err)
	}
	claimed, err = claimer.Claim(ctx, "job@101")
	if err != nil || !claimed {
		t.Fatalf("distinct claim = %v, %v", claimed, err)
	}
}

func TestClaimer_TTLExpiry(t *testing.T) {
	server, client := newRedis(t)
	claimer, err := rediscoord.NewClaimer(
		client,
		rediscoord.WithClaimTTL(time.Minute),
		rediscoord.WithClaimPrefix("test:"),
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if claimed, err := claimer.Claim(ctx, "job@1"); err != nil || !claimed {
		t.Fatalf("first claim = %v, %v", claimed, err)
	}
	if claimed, err := claimer.Claim(ctx, "job@1"); err != nil || claimed {
		t.Fatalf("live claim = %v, %v", claimed, err)
	}
	server.FastForward(2 * time.Minute)
	if claimed, err := claimer.Claim(ctx, "job@1"); err != nil || !claimed {
		t.Fatalf("expired claim = %v, %v", claimed, err)
	}
}

func TestClaimer_BackendError(t *testing.T) {
	server, client := newRedis(t)
	claimer, err := rediscoord.NewClaimer(client)
	if err != nil {
		t.Fatal(err)
	}
	server.Close()
	if claimed, err := claimer.Claim(context.Background(), "job@1"); err == nil || claimed {
		t.Fatalf("outage claim = %v, %v", claimed, err)
	}
}

func TestClaimer_Validation(t *testing.T) {
	_, client := newRedis(t)
	if _, err := rediscoord.NewClaimer(nil); err == nil {
		t.Fatal("nil client accepted")
	}
	if _, err := rediscoord.NewClaimer(client, nil); err == nil {
		t.Fatal("nil option accepted")
	}
	if _, err := rediscoord.NewClaimer(client, rediscoord.WithClaimTTL(0)); err == nil {
		t.Fatal("zero ttl accepted")
	}
	claimer, err := rediscoord.NewClaimer(client)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := claimer.Claim(nil, "key"); err == nil {
		t.Fatal("nil context accepted")
	}
	if _, err := claimer.Claim(context.Background(), " "); err == nil {
		t.Fatal("empty key accepted")
	}
}

type skipCounter struct{ skips atomic.Int64 }

func (o *skipCounter) Observe(event cron.Event) {
	if _, ok := event.(cron.SkippedFireEvent); ok {
		o.skips.Add(1)
	}
}

func TestClaimer_EndToEnd(t *testing.T) {
	_, client := newRedis(t)
	shared, err := rediscoord.NewClaimer(client)
	if err != nil {
		t.Fatal(err)
	}
	var runs atomic.Int64
	job := cron.JobFunc(func(context.Context) error {
		runs.Add(1)
		return nil
	})
	hook := &skipCounter{}
	a := cron.MustNew(cron.WithLocation(time.UTC), cron.WithClaimer(shared), cron.WithObservers(hook))
	b := cron.MustNew(cron.WithLocation(time.UTC), cron.WithClaimer(shared), cron.WithObservers(hook))
	_, _ = a.AddSchedule(cron.AlignedDelay(time.Second), job, cron.WithKey("shared"))
	_, _ = b.AddSchedule(cron.AlignedDelay(time.Second), job, cron.WithKey("shared"))
	_ = a.Start()
	_ = b.Start()
	time.Sleep(2500 * time.Millisecond)
	_ = a.Stop(context.Background())
	_ = b.Stop(context.Background())
	if runs.Load() < 1 {
		t.Fatal("no fires happened")
	}
	if got, want := hook.skips.Load(), runs.Load(); got != want {
		t.Fatalf("skips = %d, runs = %d", got, want)
	}
}
