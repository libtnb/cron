package cron_test

import (
	"context"
	"errors"
	"testing"

	"github.com/libtnb/cron"
)

func TestMemoryLocker(t *testing.T) {
	l := cron.NewMemoryLocker()
	ctx := context.Background()

	release, err := l.Lock(ctx, "k@1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Lock(ctx, "k@1"); !errors.Is(err, cron.ErrLockHeld) {
		t.Fatalf("second acquire = %v, want ErrLockHeld", err)
	}
	if _, err := l.Lock(ctx, "k@2"); err != nil {
		t.Fatalf("distinct key must acquire: %v", err)
	}

	if err := release(ctx); err != nil {
		t.Fatal(err)
	}
	if err := release(ctx); err != nil { // idempotent
		t.Fatal(err)
	}
	if _, err := l.Lock(ctx, "k@1"); err != nil {
		t.Fatalf("released key must re-acquire in-process: %v", err)
	}
}

func TestMemoryElector(t *testing.T) {
	e := cron.NewMemoryElector()
	ctx := context.Background()

	if err := e.IsLeader(ctx); err != nil {
		t.Fatalf("starts as leader: %v", err)
	}
	e.SetLeader(false)
	if err := e.IsLeader(ctx); !errors.Is(err, cron.ErrNotLeader) {
		t.Fatalf("demoted = %v, want ErrNotLeader", err)
	}
	e.SetLeader(true)
	if err := e.IsLeader(ctx); err != nil {
		t.Fatalf("re-promoted: %v", err)
	}
}
