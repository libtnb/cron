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
	// DefaultClaimTTL is the time a fire claim remains reserved. It must cover
	// the largest expected jitter, clock skew, and delayed catch-up window.
	DefaultClaimTTL = 10 * time.Minute
)

// Claimer stores fire claims in Postgres. Expired claims for the same fire key
// are replaced atomically.
type Claimer struct {
	db          *sql.DB
	ttl         time.Duration
	table       string
	holder      string
	claimStmt   string
	cleanupStmt string
}

var _ cron.Claimer = (*Claimer)(nil)

type claimerConfig struct {
	ttl   time.Duration
	table string
}

// ClaimerOption configures a Claimer.
type ClaimerOption func(*claimerConfig) error

// WithClaimTTL overrides DefaultClaimTTL.
func WithClaimTTL(ttl time.Duration) ClaimerOption {
	return func(config *claimerConfig) error {
		if ttl <= 0 {
			return fmt.Errorf("postgrescoord: claim ttl must be positive")
		}
		config.ttl = ttl
		return nil
	}
}

// WithClaimsTable overrides DefaultClaimsTable.
func WithClaimsTable(table string) ClaimerOption {
	return func(config *claimerConfig) error {
		if err := validateIdentifier(table); err != nil {
			return err
		}
		config.table = table
		return nil
	}
}

// NewClaimer constructs a Postgres fire Claimer. The claims table must exist;
// see Migrate. The driver must support sql.Result.RowsAffected.
func NewClaimer(db *sql.DB, opts ...ClaimerOption) (*Claimer, error) {
	if db == nil {
		return nil, fmt.Errorf("postgrescoord: nil database")
	}
	config := claimerConfig{ttl: DefaultClaimTTL, table: DefaultClaimsTable}
	for _, opt := range opts {
		if opt == nil {
			return nil, fmt.Errorf("postgrescoord: nil claimer option")
		}
		if err := opt(&config); err != nil {
			return nil, err
		}
	}
	holder, err := newHolderID()
	if err != nil {
		return nil, err
	}
	c := &Claimer{db: db, ttl: config.ttl, table: config.table, holder: holder}
	c.claimStmt = fmt.Sprintf(`
INSERT INTO %s (fire_key, holder, expires_at)
VALUES ($1, $2, now() + ($3 * interval '1 second'))
ON CONFLICT (fire_key) DO UPDATE
	SET holder = EXCLUDED.holder, claimed_at = now(), expires_at = EXCLUDED.expires_at
	WHERE %s.expires_at < now()`, c.table, c.table)
	c.cleanupStmt = fmt.Sprintf(
		`DELETE FROM %s WHERE expires_at < now() - interval '1 minute'`,
		c.table,
	)
	return c, nil
}

// Claim reserves fireKey until its server-side TTL. false, nil means another
// scheduler instance already claimed it.
func (c *Claimer) Claim(ctx context.Context, fireKey string) (bool, error) {
	if ctx == nil {
		return false, fmt.Errorf("postgrescoord: nil claim context")
	}
	if strings.TrimSpace(fireKey) == "" {
		return false, fmt.Errorf("postgrescoord: empty fire key")
	}
	ctx, cancel := context.WithTimeout(ctx, statementTimeout)
	defer cancel()
	result, err := c.db.ExecContext(ctx, c.claimStmt, fireKey, c.holder, c.ttl.Seconds())
	if err != nil {
		return false, fmt.Errorf("postgrescoord: claim %q: %w", fireKey, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("postgrescoord: inspect claim %q: %w", fireKey, err)
	}
	return affected > 0, nil
}

// Cleanup deletes claims that have been expired for over one minute. Run it
// periodically when long-term claim history is not needed.
func (c *Claimer) Cleanup(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("postgrescoord: nil cleanup context")
	}
	ctx, cancel := context.WithTimeout(ctx, statementTimeout)
	defer cancel()
	if _, err := c.db.ExecContext(ctx, c.cleanupStmt); err != nil {
		return fmt.Errorf("postgrescoord: cleanup claims: %w", err)
	}
	return nil
}
