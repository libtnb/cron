package cron

import (
	"context"
	"errors"
	"testing"
)

func TestJobFunc_Run_PassesContextAndError(t *testing.T) {
	want := errors.New("boom")
	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "v")

	got := JobFunc(func(c context.Context) error {
		if c.Value(ctxKey{}) != "v" {
			t.Fatal("ctx not propagated")
		}
		return want
	}).Run(ctx)
	if !errors.Is(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestChain_OuterFirstOrder(t *testing.T) {
	var seen []string
	mk := func(label string) Wrapper {
		return func(j Job) Job {
			return JobFunc(func(ctx context.Context) error {
				seen = append(seen, "before:"+label)
				err := j.Run(ctx)
				seen = append(seen, "after:"+label)
				return err
			})
		}
	}
	wrapped := Chain(mk("A"), mk("B"))(JobFunc(func(ctx context.Context) error {
		seen = append(seen, "core")
		return nil
	}))
	if err := wrapped.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"before:A", "before:B", "core", "after:B", "after:A"}
	if len(seen) != len(want) {
		t.Fatalf("seen = %v", seen)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("seen[%d] = %q, want %q", i, seen[i], want[i])
		}
	}
}

func TestChain_Empty(t *testing.T) {
	core := JobFunc(func(ctx context.Context) error { return nil })
	if Chain()(core).Run(context.Background()) != nil {
		t.Fatal("empty chain should be a no-op")
	}
}
