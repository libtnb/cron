// Package redislock provides Redis-backed cron.Locker and cron.Elector
// implementations built on redsync.
package redislock

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-redsync/redsync/v4"
	"github.com/go-redsync/redsync/v4/redis/goredis/v9"
	"github.com/redis/go-redis/v9"

	"github.com/libtnb/cron"
)

const (
	// DefaultTTL is the claim lifetime. It must exceed max jitter + clock
	// skew + any catch-up horizon within which the same fire may be
	// re-attempted.
	DefaultTTL = 10 * time.Minute

	// DefaultKeyPrefix namespaces fire keys in Redis.
	DefaultKeyPrefix = "cron:lock:"
)

// Locker claims fire keys in Redis. Use NewLocker.
type Locker struct {
	rs     *redsync.Redsync
	ttl    time.Duration
	prefix string
	extra  []redsync.Option
}

// Option configures a Locker.
type Option func(*Locker)

// WithTTL sets the claim lifetime. See DefaultTTL for sizing guidance.
func WithTTL(d time.Duration) Option {
	return func(l *Locker) { l.ttl = d }
}

// WithKeyPrefix overrides DefaultKeyPrefix.
func WithKeyPrefix(p string) Option {
	return func(l *Locker) { l.prefix = p }
}

// WithMutexOptions appends raw redsync options to every acquisition
// (advanced; applied after the TTL and single-try defaults).
func WithMutexOptions(opts ...redsync.Option) Option {
	return func(l *Locker) { l.extra = append(l.extra, opts...) }
}

// NewLocker returns a cron.Locker that claims each fire key in Redis.
func NewLocker(client redis.UniversalClient, opts ...Option) *Locker {
	l := &Locker{
		rs:     redsync.New(goredis.NewPool(client)),
		ttl:    DefaultTTL,
		prefix: DefaultKeyPrefix,
	}
	for _, o := range opts {
		o(l)
	}
	return l
}

// Lock claims key until its TTL expires. It never retries: a held claim
// means another instance owns this fire.
func (l *Locker) Lock(ctx context.Context, key string) (cron.ReleaseFunc, error) {
	mopts := append([]redsync.Option{
		redsync.WithExpiry(l.ttl),
		redsync.WithTries(1),
	}, l.extra...)
	mu := l.rs.NewMutex(l.prefix+key, mopts...)
	if err := mu.TryLockContext(ctx); err != nil {
		var taken *redsync.ErrTaken
		if errors.As(err, &taken) || errors.Is(err, redsync.ErrFailed) {
			return nil, fmt.Errorf("%w: %s", cron.ErrLockHeld, key)
		}
		return nil, fmt.Errorf("redislock: acquire %s: %w", key, err)
	}
	// The claim is deliberately retained until TTL. Unlocking here would let
	// an instance with a larger jitter draw re-acquire the same fire key and
	// run the job again — the exact duplicate window fire-scoped keys close.
	return func(context.Context) error { return nil }, nil
}
