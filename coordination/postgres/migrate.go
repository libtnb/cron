package postgrescoord

import (
	"context"
	"database/sql"
	"fmt"
)

const (
	// DefaultClaimsTable stores fire claims.
	DefaultClaimsTable = "cron_claims"
	// DefaultLeaderTable stores leadership leases.
	DefaultLeaderTable = "cron_leader"
)

// Migrate creates the default coordination tables if they do not exist. Call
// it during deployment; constructors never run DDL.
func Migrate(ctx context.Context, db *sql.DB) error {
	return MigrateTables(ctx, db, DefaultClaimsTable, DefaultLeaderTable)
}

// MigrateTables is Migrate with custom plain or schema-qualified table names.
// It rejects a nil ctx or db and invalid identifiers, and bounds the DDL by a
// five-second statement timeout.
func MigrateTables(ctx context.Context, db *sql.DB, claimsTable, leaderTable string) error {
	if ctx == nil {
		return fmt.Errorf("postgrescoord: nil migration context")
	}
	if db == nil {
		return fmt.Errorf("postgrescoord: nil database")
	}
	if err := validateIdentifier(claimsTable); err != nil {
		return err
	}
	if err := validateIdentifier(leaderTable); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, statementTimeout)
	defer cancel()
	ddl := fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS %s (
	fire_key   text PRIMARY KEY,
	holder     text NOT NULL,
	claimed_at timestamptz NOT NULL DEFAULT now(),
	expires_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS %s (
	name       text PRIMARY KEY,
	holder     text NOT NULL,
	expires_at timestamptz NOT NULL
);`, claimsTable, leaderTable)
	if _, err := db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("postgrescoord: migrate: %w", err)
	}
	return nil
}
