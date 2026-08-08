package postgrescoord_test

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/libtnb/cron/coordination/postgres"
)

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
	if err := postgrescoord.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestMigrate_Idempotent(t *testing.T) {
	db := testDB(t)
	if err := postgrescoord.Migrate(context.Background(), db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}

func TestClaimer_ClaimRetainAndReplaceExpired(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	long, err := postgrescoord.NewClaimer(db, postgrescoord.WithClaimTTL(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	key := "job@" + t.Name()
	if claimed, err := long.Claim(ctx, key); err != nil || !claimed {
		t.Fatalf("first claim = %v, %v", claimed, err)
	}
	if claimed, err := long.Claim(ctx, key); err != nil || claimed {
		t.Fatalf("live claim = %v, %v; want false, nil", claimed, err)
	}

	short, err := postgrescoord.NewClaimer(db, postgrescoord.WithClaimTTL(time.Nanosecond))
	if err != nil {
		t.Fatal(err)
	}
	stealKey := "steal@" + t.Name()
	if claimed, err := short.Claim(ctx, stealKey); err != nil || !claimed {
		t.Fatalf("short claim = %v, %v", claimed, err)
	}
	time.Sleep(50 * time.Millisecond)
	if claimed, err := long.Claim(ctx, stealKey); err != nil || !claimed {
		t.Fatalf("expired claim = %v, %v", claimed, err)
	}
}

func TestClaimer_Cleanup(t *testing.T) {
	claimer, err := postgrescoord.NewClaimer(testDB(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := claimer.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestElector_AcquireRenewFollowAndFailover(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	name := "leader-" + t.Name()
	a, err := postgrescoord.NewElector(
		db,
		postgrescoord.WithLeaderName(name),
		postgrescoord.WithLeaderTTL(time.Hour),
	)
	if err != nil {
		t.Fatal(err)
	}
	b, err := postgrescoord.NewElector(
		db,
		postgrescoord.WithLeaderName(name),
		postgrescoord.WithLeaderTTL(time.Hour),
	)
	if err != nil {
		t.Fatal(err)
	}
	if leader, err := a.IsLeader(ctx); err != nil || !leader {
		t.Fatalf("first check = %v, %v", leader, err)
	}
	if leader, err := a.IsLeader(ctx); err != nil || !leader {
		t.Fatalf("renewal = %v, %v", leader, err)
	}
	if leader, err := b.IsLeader(ctx); err != nil || leader {
		t.Fatalf("follower check = %v, %v", leader, err)
	}

	failoverName := "failover-" + t.Name()
	quick, err := postgrescoord.NewElector(
		db,
		postgrescoord.WithLeaderName(failoverName),
		postgrescoord.WithLeaderTTL(time.Nanosecond),
	)
	if err != nil {
		t.Fatal(err)
	}
	if leader, err := quick.IsLeader(ctx); err != nil || !leader {
		t.Fatalf("quick leader = %v, %v", leader, err)
	}
	time.Sleep(50 * time.Millisecond)
	taker, err := postgrescoord.NewElector(db, postgrescoord.WithLeaderName(failoverName))
	if err != nil {
		t.Fatal(err)
	}
	if leader, err := taker.IsLeader(ctx); err != nil || !leader {
		t.Fatalf("takeover = %v, %v", leader, err)
	}
}

func TestCustomTables(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if err := postgrescoord.MigrateTables(ctx, db, "myapp_claims", "myapp_leader"); err != nil {
		t.Fatal(err)
	}
	claimer, err := postgrescoord.NewClaimer(db, postgrescoord.WithClaimsTable("myapp_claims"))
	if err != nil {
		t.Fatal(err)
	}
	key := "custom@" + t.Name()
	if claimed, err := claimer.Claim(ctx, key); err != nil || !claimed {
		t.Fatalf("first claim = %v, %v", claimed, err)
	}
	if claimed, err := claimer.Claim(ctx, key); err != nil || claimed {
		t.Fatalf("second claim = %v, %v", claimed, err)
	}
	if err := claimer.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}

	elector, err := postgrescoord.NewElector(
		db,
		postgrescoord.WithLeaderTable("myapp_leader"),
		postgrescoord.WithLeaderName("custom-"+t.Name()),
	)
	if err != nil {
		t.Fatal(err)
	}
	if leader, err := elector.IsLeader(ctx); err != nil || !leader {
		t.Fatalf("leader check = %v, %v", leader, err)
	}
}

func TestValidation(t *testing.T) {
	if err := postgrescoord.MigrateTables(context.Background(), nil, "claims", "leader"); err == nil {
		t.Fatal("nil migration database accepted")
	}
	if err := postgrescoord.MigrateTables(context.Background(), nil, "bad name; drop", "leader"); err == nil {
		t.Fatal("invalid migration table accepted")
	}
	if _, err := postgrescoord.NewClaimer(nil); err == nil {
		t.Fatal("nil claimer database accepted")
	}
	if _, err := postgrescoord.NewElector(nil); err == nil {
		t.Fatal("nil elector database accepted")
	}

	db := new(sql.DB)
	if _, err := postgrescoord.NewClaimer(db, nil); err == nil {
		t.Fatal("nil claimer option accepted")
	}
	if _, err := postgrescoord.NewClaimer(db, postgrescoord.WithClaimTTL(0)); err == nil {
		t.Fatal("zero claim ttl accepted")
	}
	if _, err := postgrescoord.NewClaimer(db, postgrescoord.WithClaimsTable(`x";--`)); err == nil {
		t.Fatal("invalid claims table accepted")
	}
	if _, err := postgrescoord.NewElector(db, nil); err == nil {
		t.Fatal("nil elector option accepted")
	}
	if _, err := postgrescoord.NewElector(db, postgrescoord.WithLeaderName(" ")); err == nil {
		t.Fatal("blank leader name accepted")
	}
	if _, err := postgrescoord.NewElector(db, postgrescoord.WithLeaderTTL(0)); err == nil {
		t.Fatal("zero leader ttl accepted")
	}
	if _, err := postgrescoord.NewElector(db, postgrescoord.WithLeaderTable("1bad")); err == nil {
		t.Fatal("invalid leader table accepted")
	}
}

func TestBackendErrors(t *testing.T) {
	dsn := os.Getenv("CRON_PG_TEST_DSN")
	if dsn == "" {
		t.Skip("CRON_PG_TEST_DSN not set")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	claimer, err := postgrescoord.NewClaimer(db)
	if err != nil {
		t.Fatal(err)
	}
	elector, err := postgrescoord.NewElector(db)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if claimed, err := claimer.Claim(context.Background(), "x@1"); err == nil || claimed {
		t.Fatalf("outage claim = %v, %v", claimed, err)
	}
	if leader, err := elector.IsLeader(context.Background()); err == nil || leader {
		t.Fatalf("outage election = %v, %v", leader, err)
	}
}
