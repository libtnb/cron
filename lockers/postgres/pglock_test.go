package pglock_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/libtnb/cron"
	"github.com/libtnb/cron/lockers/postgres"
)

// testDB connects via CRON_PG_TEST_DSN, skipping when unset so local runs
// stay green without a database. CI always sets it.
func testDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("CRON_PG_TEST_DSN")
	if dsn == "" {
		t.Skip("CRON_PG_TEST_DSN not set")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := pglock.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestMigrate_Idempotent(t *testing.T) {
	db := testDB(t)
	if err := pglock.Migrate(context.Background(), db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}

func TestLocker_AcquireHeldAndSteal(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	key := "job@" + t.Name()

	long := pglock.NewLocker(db, pglock.WithTTL(time.Hour))
	release, err := long.Lock(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := release(ctx); err != nil {
		t.Fatal(err)
	}
	// Claim retained after release: a second instance must be told it's held.
	if _, err := long.Lock(ctx, key); !errors.Is(err, cron.ErrLockHeld) {
		t.Fatalf("second acquire = %v, want ErrLockHeld", err)
	}

	// An expired claim is stolen atomically.
	short := pglock.NewLocker(db, pglock.WithTTL(time.Nanosecond))
	stealKey := "steal@" + t.Name()
	if _, err := short.Lock(ctx, stealKey); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond) // let the 1ns TTL lapse on the server
	if _, err := long.Lock(ctx, stealKey); err != nil {
		t.Fatalf("expired claim must be stealable: %v", err)
	}
}

func TestLocker_Cleanup(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	l := pglock.NewLocker(db, pglock.WithTTL(time.Nanosecond))
	if _, err := l.Lock(ctx, "gone@"+t.Name()); err != nil {
		t.Fatal(err)
	}
	// The row expired over a minute ago? Not yet — Cleanup only removes
	// claims expired >1 minute, so this mainly proves the statement runs.
	if err := l.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestElector_AcquireRenewBlockAndFailover(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	name := "lead-" + t.Name()

	a := pglock.NewElector(db, pglock.WithLeaderName(name), pglock.WithLeaderTTL(time.Hour))
	b := pglock.NewElector(db, pglock.WithLeaderName(name), pglock.WithLeaderTTL(time.Hour))

	if err := a.IsLeader(ctx); err != nil {
		t.Fatalf("first caller should win: %v", err)
	}
	if err := a.IsLeader(ctx); err != nil {
		t.Fatalf("holder should renew: %v", err)
	}
	if err := b.IsLeader(ctx); !errors.Is(err, cron.ErrNotLeader) {
		t.Fatalf("second instance = %v, want ErrNotLeader", err)
	}

	// Failover: a short-TTL leader lapses and is replaced.
	fname := "failover-" + t.Name()
	quick := pglock.NewElector(db, pglock.WithLeaderName(fname), pglock.WithLeaderTTL(time.Nanosecond))
	if err := quick.IsLeader(ctx); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	taker := pglock.NewElector(db, pglock.WithLeaderName(fname), pglock.WithLeaderTTL(time.Hour))
	if err := taker.IsLeader(ctx); err != nil {
		t.Fatalf("takeover after TTL: %v", err)
	}
}

func TestCustomTables(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	if err := pglock.MigrateTables(ctx, db, "myapp_locks", "myapp_leader"); err != nil {
		t.Fatal(err)
	}
	l := pglock.NewLocker(db, pglock.WithLocksTable("myapp_locks"))
	key := "custom@" + t.Name()
	if _, err := l.Lock(ctx, key); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Lock(ctx, key); !errors.Is(err, cron.ErrLockHeld) {
		t.Fatalf("second acquire = %v, want ErrLockHeld", err)
	}
	if err := l.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}

	e := pglock.NewElector(db,
		pglock.WithLeaderTable("myapp_leader"),
		pglock.WithLeaderName("custom-"+t.Name()))
	if err := e.IsLeader(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidTableNames(t *testing.T) {
	if err := pglock.MigrateTables(context.Background(), nil, "bad name; drop", "ok_table"); err == nil {
		t.Fatal("MigrateTables must reject invalid identifiers")
	}
	mustPanic := func(name string, f func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Fatalf("%s: expected panic", name)
			}
		}()
		f()
	}
	mustPanic("WithLocksTable", func() { pglock.NewLocker(nil, pglock.WithLocksTable(`x";--`)) })
	mustPanic("WithLeaderTable", func() { pglock.NewElector(nil, pglock.WithLeaderTable("1bad")) })
}

func TestLocker_BackendError(t *testing.T) {
	dsn := os.Getenv("CRON_PG_TEST_DSN")
	if dsn == "" {
		t.Skip("CRON_PG_TEST_DSN not set")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close() // closed pool: every call fails

	l := pglock.NewLocker(db)
	if _, err := l.Lock(context.Background(), "x@1"); err == nil || errors.Is(err, cron.ErrLockHeld) {
		t.Fatalf("outage must surface as a backend error, got %v", err)
	}
	e := pglock.NewElector(db)
	if err := e.IsLeader(context.Background()); err == nil || errors.Is(err, cron.ErrNotLeader) {
		t.Fatalf("outage must surface as a backend error, got %v", err)
	}
}
