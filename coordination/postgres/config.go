// Package postgrescoord provides Postgres-backed cron.Claimer and cron.Elector
// implementations through database/sql. It uses server time, so application
// clock skew does not affect claims or leadership leases.
package postgrescoord

import (
	crand "crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"time"
)

const statementTimeout = 5 * time.Second

var identifierPattern = regexp.MustCompile(
	`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)?$`,
)

func validateIdentifier(name string) error {
	if !identifierPattern.MatchString(name) {
		return fmt.Errorf("postgrescoord: invalid table name %q", name)
	}
	return nil
}

func newHolderID() (string, error) {
	var random [8]byte
	if _, err := crand.Read(random[:]); err != nil {
		return "", fmt.Errorf("postgrescoord: create holder identity: %w", err)
	}
	host, _ := os.Hostname()
	if host == "" {
		host = "cron"
	}
	return host + "-" + hex.EncodeToString(random[:]), nil
}
