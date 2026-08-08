package cron_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/libtnb/cron"
)

type fakeClaimer struct {
	mu     sync.Mutex
	reject bool
	err    error
	keys   []string
}

func (c *fakeClaimer) Claim(_ context.Context, key string) (bool, error) {
	c.mu.Lock()
	c.keys = append(c.keys, key)
	c.mu.Unlock()
	if c.err != nil {
		return false, c.err
	}
	return !c.reject, nil
}

func (c *fakeClaimer) claimedKeys() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.keys...)
}

type claimSet struct {
	mu   sync.Mutex
	seen map[string]struct{}
}

func (c *claimSet) Claim(_ context.Context, key string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.seen == nil {
		c.seen = make(map[string]struct{})
	}
	if _, exists := c.seen[key]; exists {
		return false, nil
	}
	c.seen[key] = struct{}{}
	return true, nil
}

type fakeElector struct {
	mu     sync.Mutex
	leader bool
	err    error
}

func (e *fakeElector) IsLeader(context.Context) (bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.leader, e.err
}

func (e *fakeElector) setLeader(leader bool) {
	e.mu.Lock()
	e.leader = leader
	e.mu.Unlock()
}

type skipHook struct {
	mu     sync.Mutex
	events []cron.SkippedFireEvent
}

func (h *skipHook) Observe(raw cron.Event) {
	event, ok := raw.(cron.SkippedFireEvent)
	if !ok {
		return
	}
	h.mu.Lock()
	h.events = append(h.events, event)
	h.mu.Unlock()
}

func (h *skipHook) last() (cron.SkippedFireEvent, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.events) == 0 {
		return cron.SkippedFireEvent{}, false
	}
	return h.events[len(h.events)-1], true
}

func TestElector_LeaderRuns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		elector := &fakeElector{leader: true}
		c := cron.MustNew(cron.WithLocation(time.UTC), cron.WithElector(elector))
		var runs atomic.Int64
		_, err := c.Add("@every 1s", cron.JobFunc(func(context.Context) error {
			runs.Add(1)
			return nil
		}))
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(1500 * time.Millisecond)
		synctest.Wait()
		if err := c.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
		if runs.Load() == 0 {
			t.Fatal("leader instance did not run the job")
		}
	})
}

func TestElector_FollowerThenPromotion(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		elector := &fakeElector{}
		hook := &skipHook{}
		c := cron.MustNew(
			cron.WithLocation(time.UTC),
			cron.WithElector(elector),
			cron.WithObservers(hook),
			cron.WithMaxConcurrent(1),
		)
		var runs atomic.Int64
		_, err := c.Add("@every 1s", cron.JobFunc(func(context.Context) error {
			runs.Add(1)
			return nil
		}))
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2500 * time.Millisecond)
		synctest.Wait()
		if runs.Load() != 0 {
			t.Fatalf("follower ran %d jobs", runs.Load())
		}
		if event, ok := hook.last(); !ok || event.Reason != cron.SkipNotLeader || event.Err != nil {
			t.Fatalf("follower skip = %#v, %v", event, ok)
		}

		// Repeated follower skips under a one-job limit also verify that the
		// coordination path releases its reserved in-flight slot.
		elector.setLeader(true)
		time.Sleep(1500 * time.Millisecond)
		synctest.Wait()
		if err := c.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
		if runs.Load() == 0 {
			t.Fatal("promoted leader did not run the job")
		}
	})
}

func TestElector_BackendErrorFailsClosed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		backendErr := errors.New("election backend down")
		elector := &fakeElector{err: backendErr}
		hook := &skipHook{}
		c := cron.MustNew(
			cron.WithLocation(time.UTC),
			cron.WithElector(elector),
			cron.WithObservers(hook),
		)
		var runs atomic.Int64
		_, err := c.Add("@every 1s", cron.JobFunc(func(context.Context) error {
			runs.Add(1)
			return nil
		}))
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(1500 * time.Millisecond)
		synctest.Wait()
		if err := c.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
		if runs.Load() != 0 {
			t.Fatal("election backend error must fail closed")
		}
		event, ok := hook.last()
		if !ok || event.Reason != cron.SkipElectionError || !errors.Is(event.Err, backendErr) {
			t.Fatalf("election error skip = %#v, %v", event, ok)
		}
	})
}

func TestClaimer_ClaimedFireRuns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		claimer := &fakeClaimer{}
		c := cron.MustNew(cron.WithLocation(time.UTC), cron.WithClaimer(claimer))
		var runs atomic.Int64
		var info cron.EntryInfo
		id, err := c.AddSchedule(
			cron.AlignedDelay(time.Second),
			cron.JobFunc(func(ctx context.Context) error {
				runs.Add(1)
				info, _ = cron.EntryInfoFromContext(ctx)
				return nil
			}),
			cron.WithName("display name"),
			cron.WithKey("billing.reconcile"),
		)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(1500 * time.Millisecond)
		synctest.Wait()
		if err := c.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
		if runs.Load() == 0 {
			t.Fatal("claimed fire did not run")
		}
		entry, ok := c.Entry(id)
		if !ok || entry.Key != "billing.reconcile" || info.Key != entry.Key {
			t.Fatalf("entry key = %q, context key = %q", entry.Key, info.Key)
		}
		keys := claimer.claimedKeys()
		if len(keys) == 0 || !strings.HasPrefix(keys[0], "billing.reconcile@") {
			t.Fatalf("claim keys = %v", keys)
		}
	})
}

func TestClaimer_AlreadyClaimedSkips(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hook := &skipHook{}
		c := cron.MustNew(
			cron.WithLocation(time.UTC),
			cron.WithClaimer(&fakeClaimer{reject: true}),
			cron.WithObservers(hook),
		)
		var runs atomic.Int64
		_, err := c.AddSchedule(
			cron.AlignedDelay(time.Second),
			cron.JobFunc(func(context.Context) error {
				runs.Add(1)
				return nil
			}),
			cron.WithKey("shared"),
		)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(1500 * time.Millisecond)
		synctest.Wait()
		if err := c.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
		if runs.Load() != 0 {
			t.Fatal("an already-claimed fire ran")
		}
		event, ok := hook.last()
		if !ok || event.Reason != cron.SkipAlreadyClaimed || event.Err != nil {
			t.Fatalf("claim skip = %#v, %v", event, ok)
		}
	})
}

func TestClaimer_BackendErrorFailsClosed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		backendErr := errors.New("claim backend down")
		hook := &skipHook{}
		c := cron.MustNew(
			cron.WithLocation(time.UTC),
			cron.WithClaimer(&fakeClaimer{err: backendErr}),
			cron.WithObservers(hook),
		)
		var runs atomic.Int64
		_, err := c.AddSchedule(
			cron.AlignedDelay(time.Second),
			cron.JobFunc(func(context.Context) error {
				runs.Add(1)
				return nil
			}),
			cron.WithKey("shared"),
		)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(1500 * time.Millisecond)
		synctest.Wait()
		if err := c.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
		if runs.Load() != 0 {
			t.Fatal("claim backend error must fail closed")
		}
		event, ok := hook.last()
		if !ok || event.Reason != cron.SkipClaimError || !errors.Is(event.Err, backendErr) {
			t.Fatalf("claim error skip = %#v, %v", event, ok)
		}
	})
}

func TestManualTriggerBypassesCoordination(t *testing.T) {
	jobErr := errors.New("job failed")
	c := cron.MustNew(
		cron.WithLocation(time.UTC),
		cron.WithElector(&fakeElector{}),
		cron.WithClaimer(&fakeClaimer{err: errors.New("backend down")}),
	)
	id, err := c.AddSchedule(
		cron.TriggeredSchedule(),
		cron.JobFunc(func(context.Context) error { return jobErr }),
		cron.WithKey("manual"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Stop(context.Background()) }()
	if err := c.TriggerAndWait(context.Background(), id); !errors.Is(err, jobErr) {
		t.Fatalf("manual trigger error = %v, want job error", err)
	}
}

func TestEntryClaimerOverride(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		failing := &fakeClaimer{err: errors.New("down")}
		global := cron.MustNew(cron.WithLocation(time.UTC), cron.WithClaimer(failing))
		var optedOutRuns atomic.Int64
		_, err := global.AddSchedule(
			cron.AlignedDelay(time.Second),
			cron.JobFunc(func(context.Context) error {
				optedOutRuns.Add(1)
				return nil
			}),
			cron.WithEntryClaimer(nil),
		)
		if err != nil {
			t.Fatal(err)
		}

		perEntry := cron.MustNew(cron.WithLocation(time.UTC))
		var ownRuns atomic.Int64
		_, err = perEntry.AddSchedule(
			cron.AlignedDelay(time.Second),
			cron.JobFunc(func(context.Context) error {
				ownRuns.Add(1)
				return nil
			}),
			cron.WithKey("own"),
			cron.WithEntryClaimer(failing),
		)
		if err != nil {
			t.Fatal(err)
		}

		if err := global.Start(); err != nil {
			t.Fatal(err)
		}
		if err := perEntry.Start(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(1500 * time.Millisecond)
		synctest.Wait()
		_ = global.Stop(context.Background())
		_ = perEntry.Stop(context.Background())
		if optedOutRuns.Load() == 0 {
			t.Fatal("entry could not opt out of the global claimer")
		}
		if ownRuns.Load() != 0 {
			t.Fatal("per-entry claimer did not apply")
		}
	})
}

func TestClaimer_ExactlyOnceAcrossInstances(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		shared := &claimSet{}
		var runs atomic.Int64
		job := cron.JobFunc(func(context.Context) error {
			runs.Add(1)
			return nil
		})
		a := cron.MustNew(cron.WithLocation(time.UTC), cron.WithClaimer(shared))
		b := cron.MustNew(cron.WithLocation(time.UTC), cron.WithClaimer(shared))
		_, err := a.AddSchedule(cron.AlignedDelay(time.Second), job,
			cron.WithName("replica a"), cron.WithKey("shared-job"))
		if err != nil {
			t.Fatal(err)
		}
		_, err = b.AddSchedule(cron.AlignedDelay(time.Second), job,
			cron.WithName("replica b"), cron.WithKey("shared-job"))
		if err != nil {
			t.Fatal(err)
		}
		_ = a.Start()
		_ = b.Start()
		time.Sleep(1500 * time.Millisecond)
		synctest.Wait()
		_ = a.Stop(context.Background())
		_ = b.Stop(context.Background())
		if runs.Load() != 1 {
			t.Fatalf("fire ran %d times, want exactly one", runs.Load())
		}
	})
}

func TestClaimer_CatchUpFiresUseDistinctKeys(t *testing.T) {
	claimer := &fakeClaimer{}
	c := cron.MustNew(cron.WithLocation(time.UTC), cron.WithClaimer(claimer))
	done := make(chan struct{}, 8)
	last := time.Now().Add(-3*time.Hour - time.Minute)
	_, err := c.Add(
		"0 * * * *",
		cron.JobFunc(func(context.Context) error {
			done <- struct{}{}
			return nil
		}),
		cron.WithKey("catchup"),
		cron.WithLastRun(last),
		cron.WithEntryMissedFire(cron.MissedRunAll),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(3 * time.Second)
	for range 3 {
		select {
		case <-done:
		case <-deadline:
			t.Fatal("catch-up fires did not arrive")
		}
	}
	if err := c.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	keys := claimer.claimedKeys()
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if _, exists := seen[key]; exists {
			t.Fatalf("duplicate fire key %q", key)
		}
		seen[key] = struct{}{}
	}
}

func TestEntryKeyValidation(t *testing.T) {
	claimer := &fakeClaimer{}
	c := cron.MustNew(cron.WithLocation(time.UTC), cron.WithClaimer(claimer))
	if _, err := c.Add("@hourly", cron.JobFunc(noop), cron.WithName("name-only")); !errors.Is(err, cron.ErrClaimerRequiresKey) {
		t.Fatalf("missing key error = %v", err)
	}
	if _, err := c.Add("@hourly", cron.JobFunc(noop), cron.WithKey("   ")); !errors.Is(err, cron.ErrInvalidOption) {
		t.Fatalf("blank key error = %v", err)
	}

	first, err := c.Add("@hourly", cron.JobFunc(noop), cron.WithName("same"), cron.WithKey("unique"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Add("@daily", cron.JobFunc(noop), cron.WithName("same"), cron.WithKey("unique")); !errors.Is(err, cron.ErrDuplicateKey) {
		t.Fatalf("duplicate key error = %v", err)
	}
	if !c.Remove(first) {
		t.Fatal("Remove failed")
	}
	if _, err := c.Add("@daily", cron.JobFunc(noop), cron.WithName("same"), cron.WithKey("unique")); err != nil {
		t.Fatalf("reusing a removed key: %v", err)
	}
	if _, err := c.Add("@daily", cron.JobFunc(noop), cron.WithEntryClaimer(nil)); err != nil {
		t.Fatalf("claimer opt-out still required a key: %v", err)
	}
}

func TestSkipReason_String(t *testing.T) {
	tests := []struct {
		reason cron.SkipReason
		want   string
	}{
		{cron.SkipUnknown, "unknown"},
		{cron.SkipNotLeader, "not-leader"},
		{cron.SkipElectionError, "election-error"},
		{cron.SkipAlreadyClaimed, "already-claimed"},
		{cron.SkipClaimError, "claim-error"},
		{cron.SkipReason(255), "unknown"},
	}
	for _, test := range tests {
		if got := test.reason.String(); got != test.want {
			t.Errorf("SkipReason(%d).String() = %q, want %q", test.reason, got, test.want)
		}
	}
}
