package rediscoord

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/libtnb/cron"
)

const (
	// DefaultLeaderKey is the Redis key holding the leadership lease.
	DefaultLeaderKey = "cron:leader"

	// DefaultLeaderTTL is the leadership lease. Failover can take one TTL.
	DefaultLeaderTTL = 30 * time.Second
)

// leaderScript atomically acquires a free lease or renews one owned by this
// process.
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

// Elector is a lease-based cron.Elector. IsLeader renews a lease already held
// by this process; no background goroutine or Close is required.
type Elector struct {
	client redis.UniversalClient
	key    string
	ttl    time.Duration
	id     string
}

var _ cron.Elector = (*Elector)(nil)

type electorConfig struct {
	key string
	ttl time.Duration
}

// ElectorOption configures an Elector.
type ElectorOption func(*electorConfig) error

// WithLeaderKey overrides DefaultLeaderKey.
func WithLeaderKey(key string) ElectorOption {
	return func(config *electorConfig) error {
		key = strings.TrimSpace(key)
		if key == "" {
			return fmt.Errorf("rediscoord: leader key is empty")
		}
		config.key = key
		return nil
	}
}

// WithLeaderTTL overrides DefaultLeaderTTL.
func WithLeaderTTL(ttl time.Duration) ElectorOption {
	return func(config *electorConfig) error {
		if ttl < time.Millisecond {
			return fmt.Errorf("rediscoord: leader ttl must be at least 1ms")
		}
		config.ttl = ttl
		return nil
	}
}

// NewElector constructs a lease-based Redis Elector with a random process
// identity.
func NewElector(client redis.UniversalClient, opts ...ElectorOption) (*Elector, error) {
	if client == nil || isNilLike(client) {
		return nil, fmt.Errorf("rediscoord: nil client")
	}
	config := electorConfig{key: DefaultLeaderKey, ttl: DefaultLeaderTTL}
	for _, opt := range opts {
		if opt == nil {
			return nil, fmt.Errorf("rediscoord: nil elector option")
		}
		if err := opt(&config); err != nil {
			return nil, err
		}
	}
	var identity [16]byte
	if _, err := crand.Read(identity[:]); err != nil {
		return nil, fmt.Errorf("rediscoord: create elector identity: %w", err)
	}
	return &Elector{
		client: client,
		key:    config.key,
		ttl:    config.ttl,
		id:     hex.EncodeToString(identity[:]),
	}, nil
}

// IsLeader returns false, nil while another process owns the lease.
func (e *Elector) IsLeader(ctx context.Context) (bool, error) {
	if ctx == nil {
		return false, fmt.Errorf("rediscoord: nil election context")
	}
	result, err := leaderScript.Run(
		ctx,
		e.client,
		[]string{e.key},
		e.id,
		e.ttl.Milliseconds(),
	).Int()
	if err != nil {
		return false, fmt.Errorf("rediscoord: leader check: %w", err)
	}
	return result == 1, nil
}
