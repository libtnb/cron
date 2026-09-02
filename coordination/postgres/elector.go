package postgrescoord

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/libtnb/cron"
)

const (
	// DefaultLeaderName scopes the default leadership lease row.
	DefaultLeaderName = "default"
	// DefaultLeaderTTL is the leadership lease. Failover can take one TTL.
	DefaultLeaderTTL = 30 * time.Second
)

// Elector is a lease-based cron.Elector. IsLeader renews a lease already held
// by this process; no background goroutine or Close is required.
type Elector struct {
	db         *sql.DB
	name       string
	ttl        time.Duration
	table      string
	holder     string
	leaderStmt string
}

var _ cron.Elector = (*Elector)(nil)

type electorConfig struct {
	name  string
	ttl   time.Duration
	table string
}

// ElectorOption configures an Elector.
type ElectorOption func(*electorConfig) error

// WithLeaderName scopes the lease row, letting several fleets share a table.
// The name is trimmed; an empty name is rejected.
func WithLeaderName(name string) ElectorOption {
	return func(config *electorConfig) error {
		name = strings.TrimSpace(name)
		if name == "" {
			return fmt.Errorf("postgrescoord: leader name is empty")
		}
		config.name = name
		return nil
	}
}

// WithLeaderTTL overrides DefaultLeaderTTL. A non-positive ttl is rejected.
func WithLeaderTTL(ttl time.Duration) ElectorOption {
	return func(config *electorConfig) error {
		if ttl <= 0 {
			return fmt.Errorf("postgrescoord: leader ttl must be positive")
		}
		config.ttl = ttl
		return nil
	}
}

// WithLeaderTable overrides DefaultLeaderTable. The name must be a plain or
// schema-qualified SQL identifier; anything else is rejected.
func WithLeaderTable(table string) ElectorOption {
	return func(config *electorConfig) error {
		if err := validateIdentifier(table); err != nil {
			return err
		}
		config.table = table
		return nil
	}
}

// NewElector constructs a lease-based Postgres Elector. The leader table must
// exist; see Migrate. It returns an error for a nil db, a nil option, an
// option that rejects its argument, or a failure of the system random source.
func NewElector(db *sql.DB, opts ...ElectorOption) (*Elector, error) {
	if db == nil {
		return nil, fmt.Errorf("postgrescoord: nil database")
	}
	config := electorConfig{
		name:  DefaultLeaderName,
		ttl:   DefaultLeaderTTL,
		table: DefaultLeaderTable,
	}
	for _, opt := range opts {
		if opt == nil {
			return nil, fmt.Errorf("postgrescoord: nil elector option")
		}
		if err := opt(&config); err != nil {
			return nil, err
		}
	}
	holder, err := newHolderID()
	if err != nil {
		return nil, err
	}
	e := &Elector{
		db:     db,
		name:   config.name,
		ttl:    config.ttl,
		table:  config.table,
		holder: holder,
	}
	e.leaderStmt = fmt.Sprintf(`
INSERT INTO %s (name, holder, expires_at)
VALUES ($1, $2, now() + ($3 * interval '1 second'))
ON CONFLICT (name) DO UPDATE
	SET holder = EXCLUDED.holder, expires_at = EXCLUDED.expires_at
	WHERE %s.holder = EXCLUDED.holder OR %s.expires_at < now()`, e.table, e.table, e.table)
	return e, nil
}

// IsLeader acquires the lease when it is free or expired, renews it when this
// process holds it, and returns false, nil while another process owns it.
// Each call is bounded by a five-second statement timeout. A nil ctx is
// rejected; database failures are returned wrapped, so the scheduler skips
// the fire with cron.SkipElectionError.
func (e *Elector) IsLeader(ctx context.Context) (bool, error) {
	if ctx == nil {
		return false, fmt.Errorf("postgrescoord: nil election context")
	}
	ctx, cancel := context.WithTimeout(ctx, statementTimeout)
	defer cancel()
	result, err := e.db.ExecContext(ctx, e.leaderStmt, e.name, e.holder, e.ttl.Seconds())
	if err != nil {
		return false, fmt.Errorf("postgrescoord: leader check: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("postgrescoord: inspect leader check: %w", err)
	}
	return affected > 0, nil
}
