// Package rediscoord provides Redis-backed cron.Claimer and cron.Elector
// implementations.
package rediscoord

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/libtnb/cron"
)

const (
	// DefaultClaimTTL is the time a fire claim remains reserved. It must cover
	// the largest expected jitter, clock skew, and delayed catch-up window.
	DefaultClaimTTL = 10 * time.Minute

	// DefaultClaimPrefix namespaces fire claims in Redis.
	DefaultClaimPrefix = "cron:claim:"
)

// Claimer stores fire claims with SET NX and retains them until their TTL.
type Claimer struct {
	client redis.UniversalClient
	ttl    time.Duration
	prefix string
}

var _ cron.Claimer = (*Claimer)(nil)

type claimerConfig struct {
	ttl    time.Duration
	prefix string
}

// ClaimerOption configures a Claimer.
type ClaimerOption func(*claimerConfig) error

// WithClaimTTL overrides DefaultClaimTTL.
func WithClaimTTL(ttl time.Duration) ClaimerOption {
	return func(config *claimerConfig) error {
		if ttl < time.Millisecond {
			return fmt.Errorf("rediscoord: claim ttl must be at least 1ms")
		}
		config.ttl = ttl
		return nil
	}
}

// WithClaimPrefix overrides DefaultClaimPrefix. An empty prefix is allowed.
func WithClaimPrefix(prefix string) ClaimerOption {
	return func(config *claimerConfig) error {
		config.prefix = prefix
		return nil
	}
}

// NewClaimer constructs a Redis fire Claimer.
func NewClaimer(client redis.UniversalClient, opts ...ClaimerOption) (*Claimer, error) {
	if client == nil || isNilLike(client) {
		return nil, fmt.Errorf("rediscoord: nil client")
	}
	config := claimerConfig{ttl: DefaultClaimTTL, prefix: DefaultClaimPrefix}
	for _, opt := range opts {
		if opt == nil {
			return nil, fmt.Errorf("rediscoord: nil claimer option")
		}
		if err := opt(&config); err != nil {
			return nil, err
		}
	}
	return &Claimer{client: client, ttl: config.ttl, prefix: config.prefix}, nil
}

// Claim reserves fireKey until its TTL. false, nil means it was already
// claimed by another scheduler instance.
func (c *Claimer) Claim(ctx context.Context, fireKey string) (bool, error) {
	if ctx == nil {
		return false, fmt.Errorf("rediscoord: nil claim context")
	}
	if strings.TrimSpace(fireKey) == "" {
		return false, fmt.Errorf("rediscoord: empty fire key")
	}
	claimed, err := c.client.SetNX(ctx, c.prefix+fireKey, "1", c.ttl).Result()
	if err != nil {
		return false, fmt.Errorf("rediscoord: claim %q: %w", fireKey, err)
	}
	return claimed, nil
}

func isNilLike(value any) bool {
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}
