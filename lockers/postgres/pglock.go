// Package pglock provides Postgres-backed cron.Locker and cron.Elector
// implementations on top of database/sql. It works with any Postgres driver,
// uses a lock table instead of session advisory locks (so it survives
// connection poolers like pgbouncer), and evaluates all expiry against the
// SERVER clock, making application clock skew irrelevant.
package pglock

import (
	"context"
	crand "crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"github.com/libtnb/cron"
)

const (
	// DefaultTTL is the claim lifetime. It must exceed max jitter + clock
	// skew + any catch-up horizon within which the same fire may be
	// re-attempted.
	DefaultTTL = 10 * time.Minute

	// DefaultLeaderName scopes the leadership lease row.
	DefaultLeaderName = "default"

	// DefaultLeaderTTL is the leadership lease. It must exceed the
	// scheduler's max jitter; failover takes up to one TTL.
	DefaultLeaderTTL = 30 * time.Second

	// stmtTimeout bounds every statement so a hung database fails fast.
	stmtTimeout = 5 * time.Second

	// cleanupEvery rate-limits the lazy expired-claim sweep.
	cleanupEvery = 5 * time.Minute
)

// Migrate creates the cron_locks and cron_leader tables if they do not exist.
// Call it once at deploy time; NewLocker/NewElector do not run DDL.
func Migrate(ctx context.Context, db *sql.DB) error {
	ctx, cancel := context.WithTimeout(ctx, stmtTimeout)
	defer cancel()
	const ddl = `
CREATE TABLE IF NOT EXISTS cron_locks (
	key         text PRIMARY KEY,
	holder      text NOT NULL,
	acquired_at timestamptz NOT NULL DEFAULT now(),
	expires_at  timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS cron_leader (
	name       text PRIMARY KEY,
	holder     text NOT NULL,
	expires_at timestamptz NOT NULL
);`
	if _, err := db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("pglock: migrate: %w", err)
	}
	return nil
}

// holderID returns a stable per-process identity like "host-1a2b3c4d".
func holderID() string {
	var b [4]byte
	_, _ = crand.Read(b[:])
	host, _ := os.Hostname()
	if host == "" {
		host = "cron"
	}
	return host + "-" + hex.EncodeToString(b[:])
}

// Locker claims fire keys in a Postgres lock table. Use NewLocker.
type Locker struct {
	db          *sql.DB
	ttl         time.Duration
	holder      string
	lastCleanup atomic.Int64 // unix seconds
}

// Option configures a Locker.
type Option func(*Locker)

// WithTTL sets the claim lifetime. See DefaultTTL for sizing guidance.
func WithTTL(d time.Duration) Option {
	return func(l *Locker) { l.ttl = d }
}

// NewLocker returns a cron.Locker backed by the cron_locks table. The tables
// must exist; see Migrate. The driver must support sql.Result.RowsAffected
// (pgx, lib/pq, and pq-compatible drivers all do).
func NewLocker(db *sql.DB, opts ...Option) *Locker {
	l := &Locker{db: db, ttl: DefaultTTL, holder: holderID()}
	for _, o := range opts {
		o(l)
	}
	return l
}

// Lock claims key until its server-side TTL expires. Expired claims are
// stolen atomically; a live claim means another instance owns this fire.
func (l *Locker) Lock(ctx context.Context, key string) (cron.ReleaseFunc, error) {
	l.maybeCleanup(ctx)

	cctx, cancel := context.WithTimeout(ctx, stmtTimeout)
	defer cancel()
	res, err := l.db.ExecContext(cctx, `
INSERT INTO cron_locks (key, holder, expires_at)
VALUES ($1, $2, now() + ($3 * interval '1 second'))
ON CONFLICT (key) DO UPDATE
	SET holder = EXCLUDED.holder, acquired_at = now(), expires_at = EXCLUDED.expires_at
	WHERE cron_locks.expires_at < now()`,
		key, l.holder, l.ttl.Seconds())
	if err != nil {
		return nil, fmt.Errorf("pglock: acquire %s: %w", key, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("pglock: acquire %s: %w", key, err)
	}
	if n == 0 {
		return nil, fmt.Errorf("%w: %s", cron.ErrLockHeld, key)
	}
	// The claim row is deliberately retained until TTL; deleting it on
	// release would let an instance with a larger jitter draw re-acquire the
	// same fire key. See cron.Locker's contract.
	return func(context.Context) error { return nil }, nil
}

// maybeCleanup sweeps long-expired claims at most once per cleanupEvery.
func (l *Locker) maybeCleanup(ctx context.Context) {
	now := time.Now().Unix()
	last := l.lastCleanup.Load()
	if now-last < int64(cleanupEvery.Seconds()) || !l.lastCleanup.CompareAndSwap(last, now) {
		return
	}
	_ = l.Cleanup(ctx) // best-effort; acquisition correctness relies on TTL
}

// Cleanup deletes claims expired for over a minute. It runs lazily during
// acquisition; operators can also call it directly.
func (l *Locker) Cleanup(ctx context.Context) error {
	cctx, cancel := context.WithTimeout(ctx, stmtTimeout)
	defer cancel()
	if _, err := l.db.ExecContext(cctx,
		`DELETE FROM cron_locks WHERE expires_at < now() - interval '1 minute'`); err != nil {
		return fmt.Errorf("pglock: cleanup: %w", err)
	}
	return nil
}

// Elector is a lease-based cron.Elector backed by the cron_leader table. The
// lease renews on every IsLeader call by the current holder — no background
// goroutine. Fires during a failover window (up to one TTL after leader
// death) are skipped fleet-wide.
type Elector struct {
	db     *sql.DB
	name   string
	ttl    time.Duration
	holder string
}

// ElectorOption configures an Elector.
type ElectorOption func(*Elector)

// WithLeaderName scopes the lease row, letting several fleets share tables.
func WithLeaderName(name string) ElectorOption {
	return func(e *Elector) { e.name = name }
}

// WithLeaderTTL overrides DefaultLeaderTTL.
func WithLeaderTTL(d time.Duration) ElectorOption {
	return func(e *Elector) { e.ttl = d }
}

// NewElector returns a lease-based cron.Elector with a random instance
// identity. The tables must exist; see Migrate.
func NewElector(db *sql.DB, opts ...ElectorOption) *Elector {
	e := &Elector{db: db, name: DefaultLeaderName, ttl: DefaultLeaderTTL, holder: holderID()}
	for _, o := range opts {
		o(e)
	}
	return e
}

func (e *Elector) IsLeader(ctx context.Context) error {
	cctx, cancel := context.WithTimeout(ctx, stmtTimeout)
	defer cancel()
	res, err := e.db.ExecContext(cctx, `
INSERT INTO cron_leader (name, holder, expires_at)
VALUES ($1, $2, now() + ($3 * interval '1 second'))
ON CONFLICT (name) DO UPDATE
	SET holder = EXCLUDED.holder, expires_at = EXCLUDED.expires_at
	WHERE cron_leader.holder = EXCLUDED.holder OR cron_leader.expires_at < now()`,
		e.name, e.holder, e.ttl.Seconds())
	if err != nil {
		return fmt.Errorf("pglock: leader check: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("pglock: leader check: %w", err)
	}
	if n == 0 {
		return cron.ErrNotLeader
	}
	return nil
}
