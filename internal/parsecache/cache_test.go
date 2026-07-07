package parsecache

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func TestCache_BuildsOnce(t *testing.T) {
	var c Cache[int]
	var calls atomic.Int32

	build := func() (int, error) {
		calls.Add(1)
		return 42, nil
	}

	for range 5 {
		got, err := c.Get("k", build)
		if err != nil || got != 42 {
			t.Fatalf("Get = %d, %v", got, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("build called %d times, want 1", calls.Load())
	}
	if c.Len() != 1 {
		t.Fatalf("Len = %d", c.Len())
	}
}

func TestCache_ConcurrentSingleflight(t *testing.T) {
	var c Cache[int]
	var calls atomic.Int32
	build := func() (int, error) {
		calls.Add(1)
		return 7, nil
	}

	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			if v, err := c.Get("k", build); err != nil || v != 7 {
				t.Errorf("Get = %d, %v", v, err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("build called %d times, want 1", calls.Load())
	}
}

func TestCache_PropagatesError(t *testing.T) {
	var c Cache[int]
	want := errors.New("boom")
	_, err := c.Get("bad", func() (int, error) { return 0, want })
	if !errors.Is(err, want) {
		t.Fatalf("err = %v", err)
	}
	_, err = c.Get("bad", func() (int, error) { return 0, errors.New("ignored") })
	if !errors.Is(err, want) {
		t.Fatalf("err = %v", err)
	}
}

func TestCache_Forget(t *testing.T) {
	var c Cache[int]
	var calls atomic.Int32
	build := func() (int, error) { calls.Add(1); return 1, nil }
	_, _ = c.Get("k", build)
	_, _ = c.Get("k", build)
	c.Forget("k")
	_, _ = c.Get("k", build)
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2", calls.Load())
	}
}

func TestCache_ForgetMissingIsNoop(t *testing.T) {
	var c Cache[int]
	c.Forget("missing")
	if c.Len() != 0 {
		t.Fatalf("Len = %d", c.Len())
	}
}

func TestCache_LimitStopsMemoising(t *testing.T) {
	c := Cache[int]{Limit: 1}
	var calls atomic.Int32
	build := func() (int, error) { calls.Add(1); return 9, nil }

	_, _ = c.Get("a", build) // stored
	_, _ = c.Get("b", build) // over limit: built, not stored
	_, _ = c.Get("b", build) // built again
	if got := calls.Load(); got != 3 {
		t.Fatalf("calls = %d, want 3", got)
	}
	if c.Len() != 1 {
		t.Fatalf("Len = %d, want 1", c.Len())
	}
	_, _ = c.Get("a", build) // cached hit
	if got := calls.Load(); got != 3 {
		t.Fatalf("calls after hit = %d, want 3", got)
	}

	c.Forget("a") // frees a slot
	_, _ = c.Get("b", build)
	_, _ = c.Get("b", build)
	if got := calls.Load(); got != 4 {
		t.Fatalf("calls after forget = %d, want 4 (b memoised)", got)
	}
}
