package redislock

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/libtnb/cron"
)

const (
	// DefaultLeaderKey is the Redis key holding the leadership lease.
	DefaultLeaderKey = "cron:leader"

	// DefaultLeaderTTL is the leadership lease. It must exceed the
	// scheduler's max jitter; failover takes up to one TTL.
	DefaultLeaderTTL = 30 * time.Second
)

// leaderScript atomically acquires a free lease or renews an owned one.
var leaderScript = redis.NewScript(`
local v = redis.call('GET', KEYS[1])
if v == false then
	redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2])
	return 1
end
if v == ARGV[1] then
	redis.call('PEXPIRE', KEYS[1], ARGV[2])
	return 1
end
return 0
`)

// Elector is a lease-based cron.Elector. The lease renews on every IsLeader
// call by the current holder — no background goroutine, no Close. Fires
// during a failover window (up to one TTL after leader death) are skipped
// fleet-wide.
type Elector struct {
	client redis.UniversalClient
	key    string
	ttl    time.Duration
	id     string
}

// ElectorOption configures an Elector.
type ElectorOption func(*Elector)

// WithLeaderKey overrides DefaultLeaderKey.
func WithLeaderKey(k string) ElectorOption {
	return func(e *Elector) { e.key = k }
}

// WithLeaderTTL overrides DefaultLeaderTTL.
func WithLeaderTTL(d time.Duration) ElectorOption {
	return func(e *Elector) { e.ttl = d }
}

// NewElector returns a lease-based cron.Elector with a random instance
// identity.
func NewElector(client redis.UniversalClient, opts ...ElectorOption) *Elector {
	var b [16]byte
	_, _ = crand.Read(b[:])
	e := &Elector{
		client: client,
		key:    DefaultLeaderKey,
		ttl:    DefaultLeaderTTL,
		id:     hex.EncodeToString(b[:]),
	}
	for _, o := range opts {
		o(e)
	}
	return e
}

func (e *Elector) IsLeader(ctx context.Context) error {
	n, err := leaderScript.Run(ctx, e.client, []string{e.key}, e.id, e.ttl.Milliseconds()).Int()
	if err != nil {
		return fmt.Errorf("redislock: leader check: %w", err)
	}
	if n != 1 {
		return cron.ErrNotLeader
	}
	return nil
}
