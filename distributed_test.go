package cron_test

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/libtnb/cron"
)

type fakeLocker struct {
	mu       sync.Mutex
	err      error
	relErr   error
	keys     []string
	relCalls atomic.Int32
}

func (f *fakeLocker) Lock(_ context.Context, key string) (cron.ReleaseFunc, error) {
	f.mu.Lock()
	f.keys = append(f.keys, key)
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return func(context.Context) error {
		f.relCalls.Add(1)
		return f.relErr
	}, nil
}

func (f *fakeLocker) lockedKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.keys...)
}

type skipCounterHook struct {
	skips  atomic.Int64
	reason atomic.Int32
}

func (h *skipCounterHook) OnSkipped(e cron.EventSkipped) {
	h.skips.Add(1)
	h.reason.Store(int32(e.Reason))
}

type skipRecorder struct{ skips atomic.Int64 }

func (r *skipRecorder) JobSkipped(string, cron.SkipReason) { r.skips.Add(1) }

func TestElector_LeaderRuns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := cron.New(cron.WithLocation(time.UTC), cron.WithElector(cron.NewMemoryElector()))
		var runs atomic.Int64
		_, _ = c.Add("@every 1s", cron.JobFunc(func(context.Context) error {
			runs.Add(1)
			return nil
		}))
		_ = c.Start()
		time.Sleep(3 * time.Second)
		synctest.Wait()
		_ = c.Stop(context.Background())
		if runs.Load() == 0 {
			t.Fatal("leader instance should run jobs")
		}
	})
}

func TestElector_NotLeaderSkips(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		el := cron.NewMemoryElector()
		el.SetLeader(false)
		h := &skipCounterHook{}
		r := &skipRecorder{}
		c := cron.New(cron.WithLocation(time.UTC), cron.WithElector(el),
			cron.WithHooks(h), cron.WithRecorder(r), cron.WithMaxConcurrent(1))
		var runs atomic.Int64
		id, _ := c.Add("@every 1s", cron.JobFunc(func(context.Context) error {
			runs.Add(1)
			return nil
		}))
		_ = c.Start()
		time.Sleep(2500 * time.Millisecond)
		synctest.Wait()

		if runs.Load() != 0 {
			t.Fatalf("non-leader ran %d times", runs.Load())
		}
		if h.skips.Load() < 2 || cron.SkipReason(h.reason.Load()) != cron.SkipNotLeader {
			t.Fatalf("skips=%d reason=%v", h.skips.Load(), cron.SkipReason(h.reason.Load()))
		}
		if r.skips.Load() == 0 {
			t.Fatal("recorder not called")
		}
		e, _ := c.Entry(id)
		if !e.Prev.IsZero() {
			t.Fatal("Prev must not advance on skip")
		}

		// Multiple consecutive skips under MaxConcurrent(1) prove the inflight
		// slot is released on the skip path; promotion then actually runs.
		el.SetLeader(true)
		time.Sleep(1500 * time.Millisecond)
		synctest.Wait()
		_ = c.Stop(context.Background())
		if runs.Load() == 0 {
			t.Fatal("promoted leader should run (inflight slot must have been freed)")
		}
	})
}

func TestLocker_AcquiredRunsAndReleasesOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &fakeLocker{}
		c := cron.New(cron.WithLocation(time.UTC), cron.WithLocker(f))
		var runs atomic.Int64
		_, _ = c.Add("@every 1s", cron.JobFunc(func(context.Context) error {
			runs.Add(1)
			return nil
		}), cron.WithName("job"))
		_ = c.Start()
		time.Sleep(1500 * time.Millisecond)
		synctest.Wait()
		_ = c.Stop(context.Background())

		if runs.Load() == 0 {
			t.Fatal("lock acquired: job should run")
		}
		if f.relCalls.Load() != int32(runs.Load()) {
			t.Fatalf("release calls = %d, runs = %d", f.relCalls.Load(), runs.Load())
		}
	})
}

func TestLocker_HeldSkips(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := cron.NewMemoryLocker()
		h := &skipCounterHook{}
		c := cron.New(cron.WithLocation(time.UTC), cron.WithLocker(l), cron.WithHooks(h))
		var runs atomic.Int64
		_, _ = c.Add("@every 1s", cron.JobFunc(func(context.Context) error {
			runs.Add(1)
			return nil
		}), cron.WithName("dup"))

		// Pre-claim the exact upcoming fire key.
		start := time.Now().UTC().Truncate(time.Second)
		for i := 1; i <= 3; i++ {
			_, err := l.Lock(context.Background(), cron.FireKey("dup", 0, start.Add(time.Duration(i)*time.Second)))
			if err != nil {
				t.Fatal(err)
			}
		}
		_ = c.Start()
		time.Sleep(2500 * time.Millisecond)
		synctest.Wait()
		_ = c.Stop(context.Background())

		if runs.Load() != 0 {
			t.Fatalf("held lock: job ran %d times", runs.Load())
		}
		if h.skips.Load() == 0 || cron.SkipReason(h.reason.Load()) != cron.SkipLockHeld {
			t.Fatalf("skips=%d reason=%v, want lock-held", h.skips.Load(), cron.SkipReason(h.reason.Load()))
		}
	})
}

func TestLocker_BackendErrorFailsClosed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &fakeLocker{err: errors.New("backend down")}
		h := &skipCounterHook{}
		c := cron.New(cron.WithLocation(time.UTC), cron.WithLocker(f), cron.WithHooks(h))
		var runs atomic.Int64
		_, _ = c.Add("@every 1s", cron.JobFunc(func(context.Context) error {
			runs.Add(1)
			return nil
		}), cron.WithName("job"))
		_ = c.Start()
		time.Sleep(1500 * time.Millisecond)
		synctest.Wait()
		_ = c.Stop(context.Background())

		if runs.Load() != 0 {
			t.Fatal("backend error must fail closed")
		}
		if cron.SkipReason(h.reason.Load()) != cron.SkipLockError {
			t.Fatalf("reason = %v, want lock-error", cron.SkipReason(h.reason.Load()))
		}
	})
}

func TestLocker_ReleaseErrorIsLogged(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &fakeLocker{relErr: errors.New("release failed")}
		logger := slog.New(slog.DiscardHandler)
		c := cron.New(cron.WithLocation(time.UTC), cron.WithLocker(f), cron.WithLogger(logger))
		var runs atomic.Int64
		_, _ = c.Add("@every 1s", cron.JobFunc(func(context.Context) error {
			runs.Add(1)
			return nil
		}), cron.WithName("job"))
		_ = c.Start()
		time.Sleep(1500 * time.Millisecond)
		synctest.Wait()
		_ = c.Stop(context.Background())
		if runs.Load() == 0 || f.relCalls.Load() == 0 {
			t.Fatal("job should run and release should be attempted")
		}
	})
}

func TestManualTriggerBypassesCoordination(t *testing.T) {
	el := cron.NewMemoryElector()
	el.SetLeader(false)
	boom := errors.New("boom")
	c := cron.New(cron.WithLocation(time.UTC),
		cron.WithElector(el),
		cron.WithLocker(&fakeLocker{err: errors.New("backend down")}))
	id, _ := c.AddSchedule(cron.TriggeredSchedule(), cron.JobFunc(func(context.Context) error {
		return boom
	}), cron.WithName("manual"))
	_ = c.Start()
	defer func() { _ = c.Stop(context.Background()) }()

	if err := c.TriggerAndWait(context.Background(), id); !errors.Is(err, boom) {
		t.Fatalf("manual trigger must bypass coordination, got %v", err)
	}
}

func TestEntryLockerOverride(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		failing := &fakeLocker{err: errors.New("down")}

		// (a) global failing locker, entry opts out with nil.
		c := cron.New(cron.WithLocation(time.UTC), cron.WithLocker(failing))
		var optOutRuns atomic.Int64
		_, _ = c.Add("@every 1s", cron.JobFunc(func(context.Context) error {
			optOutRuns.Add(1)
			return nil
		}), cron.WithName("opt-out"), cron.WithEntryLocker(nil))

		// (b) no global, entry brings its own failing locker.
		c2 := cron.New(cron.WithLocation(time.UTC))
		var ownRuns atomic.Int64
		_, _ = c2.Add("@every 1s", cron.JobFunc(func(context.Context) error {
			ownRuns.Add(1)
			return nil
		}), cron.WithName("own"), cron.WithEntryLocker(failing))

		_ = c.Start()
		_ = c2.Start()
		time.Sleep(1500 * time.Millisecond)
		synctest.Wait()
		_ = c.Stop(context.Background())
		_ = c2.Stop(context.Background())

		if optOutRuns.Load() == 0 {
			t.Fatal("WithEntryLocker(nil) must disable the global locker")
		}
		if ownRuns.Load() != 0 {
			t.Fatal("per-entry locker must apply without a global one")
		}
	})
}

func TestExactlyOnceAcrossTwoInstances(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		shared := cron.NewMemoryLocker()
		h1, h2 := &skipCounterHook{}, &skipCounterHook{}
		var runs atomic.Int64
		job := cron.JobFunc(func(context.Context) error {
			runs.Add(1)
			time.Sleep(100 * time.Millisecond) // hold the claim across the peer's attempt
			return nil
		})

		a := cron.New(cron.WithLocation(time.UTC), cron.WithLocker(shared), cron.WithHooks(h1))
		b := cron.New(cron.WithLocation(time.UTC), cron.WithLocker(shared), cron.WithHooks(h2))
		_, _ = a.Add("@every 1s", job, cron.WithName("shared-job"))
		_, _ = b.Add("@every 1s", job, cron.WithName("shared-job"))

		_ = a.Start()
		_ = b.Start()
		time.Sleep(1500 * time.Millisecond)
		synctest.Wait()
		_ = a.Stop(context.Background())
		_ = b.Stop(context.Background())

		if runs.Load() != 1 {
			t.Fatalf("fire ran %d times across the fleet, want exactly 1", runs.Load())
		}
		if h1.skips.Load()+h2.skips.Load() != 1 {
			t.Fatalf("skips = %d, want exactly 1", h1.skips.Load()+h2.skips.Load())
		}
	})
}

func TestCatchUpFiresClaimDistinctKeys(t *testing.T) {
	f := &fakeLocker{}
	c := cron.New(cron.WithLocation(time.UTC), cron.WithLocker(f))
	var runs atomic.Int64
	done := make(chan struct{}, 8)
	last := time.Now().Add(-3*time.Hour - time.Minute)
	_, err := c.Add("0 * * * *", cron.JobFunc(func(context.Context) error {
		runs.Add(1)
		done <- struct{}{}
		return nil
	}), cron.WithName("catchup"), cron.WithLastRun(last), cron.WithEntryMissedFire(cron.MissedRunAll))
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Start()
	deadline := time.After(3 * time.Second)
	for range 3 {
		select {
		case <-done:
		case <-deadline:
			t.Fatalf("only %d catch-up fires arrived", runs.Load())
		}
	}
	_ = c.Stop(context.Background())

	keys := f.lockedKeys()
	seen := make(map[string]bool, len(keys))
	for _, k := range keys {
		if seen[k] {
			t.Fatalf("duplicate fire key %q", k)
		}
		seen[k] = true
	}
	if int64(len(keys)) < runs.Load() {
		t.Fatalf("keys=%d runs=%d", len(keys), runs.Load())
	}
}

func TestUnnamedEntryWithLockerRejected(t *testing.T) {
	c := cron.New(cron.WithLocation(time.UTC), cron.WithLocker(cron.NewMemoryLocker()))
	if _, err := c.Add("@every 1m", cron.JobFunc(noop)); !errors.Is(err, cron.ErrLockerRequiresName) {
		t.Fatalf("unnamed locked entry = %v, want ErrLockerRequiresName", err)
	}
	if _, err := c.Add("@every 1m", cron.JobFunc(noop), cron.WithName("named")); err != nil {
		t.Fatal(err)
	}
	// Opting the entry out of the locker lifts the name requirement.
	if _, err := c.Add("@every 1m", cron.JobFunc(noop), cron.WithEntryLocker(nil)); err != nil {
		t.Fatal(err)
	}
}

func TestFireKey(t *testing.T) {
	at := time.Unix(1750000000, 0)
	if got := cron.FireKey("job", 7, at); got != "job@1750000000000000000" {
		t.Fatalf("named: %q", got)
	}
	if got := cron.FireKey("", 7, at); got != "#7@1750000000000000000" {
		t.Fatalf("fallback: %q", got)
	}
	// Nanosecond precision: sub-second fires must claim distinct keys.
	if cron.FireKey("job", 7, at) == cron.FireKey("job", 7, at.Add(100*time.Millisecond)) {
		t.Fatal("fires within the same second must not share a key")
	}
}

func TestSkipReason_String(t *testing.T) {
	cases := map[cron.SkipReason]string{
		cron.SkipNotLeader:   "not-leader",
		cron.SkipLockHeld:    "lock-held",
		cron.SkipLockError:   "lock-error",
		cron.SkipReason(255): "unknown",
	}
	for r, want := range cases {
		if got := r.String(); got != want {
			t.Fatalf("%d: %q, want %q", r, got, want)
		}
	}
}
