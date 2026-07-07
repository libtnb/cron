package redislock_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/libtnb/cron"
	"github.com/libtnb/cron/lockers/redis"
)

func TestElector_AcquireRenewAndBlock(t *testing.T) {
	_, client := newRedis(t)
	ctx := context.Background()

	a := redislock.NewElector(client, redislock.WithLeaderTTL(time.Minute))
	b := redislock.NewElector(client, redislock.WithLeaderTTL(time.Minute))

	if err := a.IsLeader(ctx); err != nil {
		t.Fatalf("first caller should win: %v", err)
	}
	if err := a.IsLeader(ctx); err != nil {
		t.Fatalf("holder should renew: %v", err)
	}
	if err := b.IsLeader(ctx); !errors.Is(err, cron.ErrNotLeader) {
		t.Fatalf("second instance = %v, want ErrNotLeader", err)
	}
}

func TestElector_FailoverAfterTTL(t *testing.T) {
	mr, client := newRedis(t)
	ctx := context.Background()

	a := redislock.NewElector(client, redislock.WithLeaderKey("lead:x"), redislock.WithLeaderTTL(30*time.Second))
	b := redislock.NewElector(client, redislock.WithLeaderKey("lead:x"), redislock.WithLeaderTTL(30*time.Second))

	if err := a.IsLeader(ctx); err != nil {
		t.Fatal(err)
	}
	mr.FastForward(time.Minute) // a's lease expires
	if err := b.IsLeader(ctx); err != nil {
		t.Fatalf("b should take over after TTL: %v", err)
	}
	if err := a.IsLeader(ctx); !errors.Is(err, cron.ErrNotLeader) {
		t.Fatalf("a must observe the new leader, got %v", err)
	}
}

func TestElector_BackendOutage(t *testing.T) {
	mr, client := newRedis(t)
	e := redislock.NewElector(client)
	mr.Close()

	err := e.IsLeader(context.Background())
	if err == nil || errors.Is(err, cron.ErrNotLeader) {
		t.Fatalf("outage must surface as a backend error, got %v", err)
	}
}
