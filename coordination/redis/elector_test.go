package rediscoord_test

import (
	"context"
	"testing"
	"time"

	"github.com/libtnb/cron/coordination/redis"
)

func TestElector_AcquireRenewAndFollow(t *testing.T) {
	_, client := newRedis(t)
	a, err := rediscoord.NewElector(client, rediscoord.WithLeaderTTL(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	b, err := rediscoord.NewElector(client, rediscoord.WithLeaderTTL(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if leader, err := a.IsLeader(ctx); err != nil || !leader {
		t.Fatalf("first check = %v, %v", leader, err)
	}
	if leader, err := a.IsLeader(ctx); err != nil || !leader {
		t.Fatalf("renewal = %v, %v", leader, err)
	}
	if leader, err := b.IsLeader(ctx); err != nil || leader {
		t.Fatalf("follower check = %v, %v; want false, nil", leader, err)
	}
}

func TestElector_FailoverAfterTTL(t *testing.T) {
	server, client := newRedis(t)
	a, err := rediscoord.NewElector(
		client,
		rediscoord.WithLeaderKey("leader:test"),
		rediscoord.WithLeaderTTL(30*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	b, err := rediscoord.NewElector(
		client,
		rediscoord.WithLeaderKey("leader:test"),
		rediscoord.WithLeaderTTL(30*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if leader, err := a.IsLeader(ctx); err != nil || !leader {
		t.Fatalf("a first check = %v, %v", leader, err)
	}
	server.FastForward(time.Minute)
	if leader, err := b.IsLeader(ctx); err != nil || !leader {
		t.Fatalf("b takeover = %v, %v", leader, err)
	}
	if leader, err := a.IsLeader(ctx); err != nil || leader {
		t.Fatalf("a after takeover = %v, %v", leader, err)
	}
}

func TestElector_BackendError(t *testing.T) {
	server, client := newRedis(t)
	elector, err := rediscoord.NewElector(client)
	if err != nil {
		t.Fatal(err)
	}
	server.Close()
	if leader, err := elector.IsLeader(context.Background()); err == nil || leader {
		t.Fatalf("outage check = %v, %v", leader, err)
	}
}

func TestElector_Validation(t *testing.T) {
	_, client := newRedis(t)
	if _, err := rediscoord.NewElector(nil); err == nil {
		t.Fatal("nil client accepted")
	}
	if _, err := rediscoord.NewElector(client, nil); err == nil {
		t.Fatal("nil option accepted")
	}
	if _, err := rediscoord.NewElector(client, rediscoord.WithLeaderKey(" ")); err == nil {
		t.Fatal("blank leader key accepted")
	}
	if _, err := rediscoord.NewElector(client, rediscoord.WithLeaderTTL(0)); err == nil {
		t.Fatal("zero ttl accepted")
	}
	elector, err := rediscoord.NewElector(client)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := elector.IsLeader(nil); err == nil {
		t.Fatal("nil context accepted")
	}
}
