package cron

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	mathrand "math/rand/v2"
	"runtime/debug"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/libtnb/cron/internal/heap"
	"github.com/libtnb/cron/internal/parsecache"
)

// defaultParseCacheLimit caps how many distinct specs Add memoizes.
const defaultParseCacheLimit = 1024

type entry struct {
	id       EntryID
	name     string
	spec     string
	schedule Schedule
	wrapped  Job // global+entry chain applied
	timeout  time.Duration
	jitter   time.Duration
	missed   MissedFirePolicy
	locker   Locker

	next   time.Time
	prev   time.Time
	paused bool
	gen    uint64 // bumped by Pause/Resume/Update; stales in-flight fire plans

	item *heap.Item[*entry] // nil iff not in the heap
	view *viewCell          // snapshot cell, stable for the entry's lifetime
}

// viewCell holds an entry's published snapshot. Fires swap the value with an
// atomic store; Add/Remove mutate the enclosing map under viewMu.
type viewCell struct {
	p atomic.Pointer[Entry]
}

type viewMap map[EntryID]*viewCell

// dueFire captures everything commit needs while still under c.mu; schedule
// and gen are snapshotted because Update can swap them concurrently.
type dueFire struct {
	e         *entry
	schedule  Schedule
	scheduled time.Time
	gen       uint64
}

type firePlan struct {
	e         *entry
	schedule  Schedule
	gen       uint64
	scheduled time.Time
	fireOne   time.Time   // zero if no fire (MissedSkip or exhausted catch-up)
	fireAll   []time.Time // MissedRunAll catch-up instants
	nextFire  time.Time
	lateness  time.Duration
	missed    bool
}

// fireOpts controls one dispatched invocation.
type fireOpts struct {
	advancePrev bool
	manual      bool         // Trigger: skip jitter
	result      chan<- error // if non-nil (cap >= 1), receives the outcome
}

// Cron is a job scheduler. Construct one with New, register jobs, then Start.
type Cron struct {
	cfg        config
	parseCache parsecache.Cache[Schedule]

	mu      sync.Mutex         // guards h, byEntry, entry mutation
	h       *heap.Heap[*entry] // scheduling heap
	byEntry map[EntryID]*entry // canonical entry table
	nextID  atomic.Uint64

	viewMu sync.RWMutex // guards the views map structure; cell values are atomic
	views  viewMap      // snapshot map read by Entry/Entries

	hooks *hookDispatcher

	running atomic.Bool
	wakeCh  chan struct{}

	startMu    sync.Mutex
	runCtx     context.Context
	runCancel  context.CancelCauseFunc
	loopCancel context.CancelFunc // stops the loop without cancelling jobs
	runDone    chan struct{}
	started    bool

	wg       sync.WaitGroup
	inflight atomic.Int64
}

// New constructs a Cron. It does not start scheduling until Start is called.
func New(opts ...Option) *Cron {
	cfg := config{}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.loc == nil {
		cfg.loc = time.Local
	}
	if cfg.logger == nil {
		cfg.logger = slog.Default()
	}
	if cfg.parser == nil {
		popts := []ParserOption{WithDefaultLocation(cfg.loc)}
		if cfg.secondsField {
			popts = append(popts, WithSeconds())
		}
		cfg.parser = NewStandardParser(popts...)
	} else if cfg.locSet {
		cfg.logger.Warn("cron: WithLocation ignored; the parser from WithParser controls the timezone")
	}
	if cfg.missedTolerance <= 0 {
		cfg.missedTolerance = defaultMissedTolerance
	}

	c := &Cron{
		cfg:     cfg,
		h:       heap.New[*entry](),
		byEntry: make(map[EntryID]*entry),
		views:   make(viewMap),
		wakeCh:  make(chan struct{}, 1),
	}
	c.parseCache.Limit = defaultParseCacheLimit
	c.hooks = newHookDispatcher(cfg.hooks, cfg.logger, cfg.recorder, cfg.hookBuffer)
	return c
}

// Add parses spec and registers j. It returns a *ParseError for invalid specs
// or ErrCapacityReached when WithMaxEntries rejects the registration.
func (c *Cron) Add(spec string, j Job, opts ...EntryOption) (EntryID, error) {
	s, err := c.parse(spec)
	if err != nil {
		return 0, err
	}
	return c.add(spec, s, j, opts...)
}

// AddSchedule registers j against a programmatic Schedule.
func (c *Cron) AddSchedule(s Schedule, j Job, opts ...EntryOption) (EntryID, error) {
	return c.add("", s, j, opts...)
}

// Update re-parses spec and swaps id's schedule in place, keeping the job,
// entry options, ID, and Prev. The next fire is recomputed from now.
func (c *Cron) Update(id EntryID, spec string) error {
	s, err := c.parse(spec)
	if err != nil {
		return err
	}
	return c.updateSchedule(id, spec, s)
}

// UpdateSchedule is Update for a programmatic Schedule.
func (c *Cron) UpdateSchedule(id EntryID, s Schedule) error {
	if s == nil {
		return ErrNilSchedule
	}
	return c.updateSchedule(id, "", s)
}

// Remove deregisters id. In-flight invocations continue; future automatic
// fires and future Trigger calls for id are rejected.
func (c *Cron) Remove(id EntryID) bool {
	c.mu.Lock()
	e, ok := c.byEntry[id]
	if !ok {
		c.mu.Unlock()
		return false
	}
	if e.item != nil {
		c.h.Remove(e.item)
		e.item = nil
	}
	delete(c.byEntry, id)
	c.publishViewRemove(id)
	heapLen := c.h.Len()
	c.mu.Unlock()
	c.wake()
	recordQueueDepth(c.cfg.recorder, heapLen)
	return true
}

// Pause suspends automatic fires for id, keeping the entry and its Prev.
// Manual Trigger still works while paused. Returns false if id is unknown.
func (c *Cron) Pause(id EntryID) bool {
	c.mu.Lock()
	e, ok := c.byEntry[id]
	if !ok {
		c.mu.Unlock()
		return false
	}
	if !e.paused {
		e.paused = true
		e.gen++
		e.next = time.Time{}
		if e.item != nil {
			c.h.Remove(e.item)
			e.item = nil
		}
		view := entryView(e)
		e.view.p.Store(&view)
	}
	heapLen := c.h.Len()
	c.mu.Unlock()
	c.wake()
	recordQueueDepth(c.cfg.recorder, heapLen)
	return true
}

// Resume re-enables automatic fires for id, scheduling from now. Returns
// false if id is unknown.
func (c *Cron) Resume(id EntryID) bool {
	c.mu.Lock()
	e, ok := c.byEntry[id]
	if !ok {
		c.mu.Unlock()
		return false
	}
	if !e.paused {
		c.mu.Unlock()
		return true
	}
	gen := e.gen
	s := e.schedule
	c.mu.Unlock()

	// Schedule.Next stays outside c.mu; see fireDue.
	next := s.Next(time.Now())

	c.mu.Lock()
	cur, ok := c.byEntry[id]
	if !ok || cur != e {
		c.mu.Unlock()
		return false
	}
	if cur.gen != gen {
		// A racing Pause/Resume/Update won; its state stands.
		c.mu.Unlock()
		return true
	}
	cur.paused = false
	cur.gen++
	cur.next = next
	if !next.IsZero() {
		cur.item = c.h.Push(next.UnixNano(), cur)
	}
	view := entryView(cur)
	cur.view.p.Store(&view)
	name := cur.name
	heapLen := c.h.Len()
	c.mu.Unlock()

	c.wake()
	recordQueueDepth(c.cfg.recorder, heapLen)
	if !next.IsZero() {
		recordJobScheduled(c.cfg.recorder, name)
		c.hooks.emitSchedule(EventSchedule{
			EntryID: id, Name: name, Schedule: s, Next: next,
		})
	}
	return true
}

// Trigger fires id immediately, bypassing jitter. It returns
// ErrSchedulerNotRunning, ErrEntryNotFound, or ErrConcurrencyLimit when
// dispatch is rejected. Paused entries can still be triggered.
func (c *Cron) Trigger(id EntryID) error { return c.trigger(id, nil) }

// TriggerAndWait fires id like Trigger and blocks until the invocation
// returns, yielding the job's error. ctx bounds only the wait; on ctx
// cancellation the job keeps running.
func (c *Cron) TriggerAndWait(ctx context.Context, id EntryID) error {
	result := make(chan error, 1)
	if err := c.trigger(id, result); err != nil {
		return err
	}
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Entry returns the current snapshot for id.
func (c *Cron) Entry(id EntryID) (Entry, bool) {
	c.viewMu.RLock()
	cell, ok := c.views[id]
	c.viewMu.RUnlock()
	if !ok {
		return Entry{}, false
	}
	return *cell.p.Load(), true
}

// Entries returns registered entry snapshots ordered by Next.
func (c *Cron) Entries() iter.Seq[Entry] {
	return func(yield func(Entry) bool) {
		c.viewMu.RLock()
		entries := make([]Entry, 0, len(c.views))
		for _, cell := range c.views {
			entries = append(entries, *cell.p.Load())
		}
		c.viewMu.RUnlock()
		if len(entries) == 0 {
			return
		}
		slices.SortFunc(entries, func(a, b Entry) int {
			return compareNext(a.Next, b.Next)
		})
		for _, e := range entries {
			if !yield(e) {
				return
			}
		}
	}
}

// Start launches the scheduler. It is idempotent while running and returns
// ErrSchedulerStopped after Stop has been called.
func (c *Cron) Start() error {
	c.startMu.Lock()
	defer c.startMu.Unlock()
	if c.started {
		if c.running.Load() {
			return nil
		}
		return ErrSchedulerStopped
	}
	c.started = true
	c.running.Store(true)
	base := c.cfg.baseCtx
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithCancelCause(base)
	c.runCtx = ctx
	c.runCancel = cancel
	loopCtx, loopCancel := context.WithCancel(ctx)
	c.loopCancel = loopCancel
	c.runDone = make(chan struct{})
	go c.loop(loopCtx)
	return nil
}

// Running reports whether the scheduler is running. It is observational; use
// Trigger's returned error for race-free dispatch decisions.
func (c *Cron) Running() bool { return c.running.Load() }

// Stop halts the scheduler, cancels in-flight jobs (ErrCronStopping as the
// cause), and waits for the loop, jobs and hook dispatcher to drain, capped
// by ctx. Returns ctx.Err() on timeout. Do not call it from inside a Job.
func (c *Cron) Stop(ctx context.Context) error {
	c.startMu.Lock()
	c.started = true
	if c.runDone == nil {
		c.startMu.Unlock()
		return c.hooks.close(ctx)
	}
	c.running.Store(false)
	c.runCancel(ErrCronStopping)
	done := c.runDone
	c.startMu.Unlock()
	return c.awaitShutdown(ctx, done)
}

// Drain is Stop without cancelling in-flight jobs: it stops scheduling new
// fires and waits for running jobs to finish naturally, capped by ctx.
func (c *Cron) Drain(ctx context.Context) error {
	c.startMu.Lock()
	c.started = true
	if c.runDone == nil {
		c.startMu.Unlock()
		return c.hooks.close(ctx)
	}
	if c.running.Swap(false) {
		c.loopCancel()
	}
	done := c.runDone
	c.startMu.Unlock()
	return c.awaitShutdown(ctx, done)
}

func (c *Cron) parse(spec string) (Schedule, error) {
	s, err := c.parseCache.Get(spec, func() (Schedule, error) {
		return c.cfg.parser.Parse(spec)
	})
	if err != nil {
		// Don't pin invalid, often caller-controlled specs in the cache.
		c.parseCache.Forget(spec)
		return nil, err
	}
	return s, nil
}

func (c *Cron) add(spec string, s Schedule, j Job, opts ...EntryOption) (EntryID, error) {
	if s == nil {
		return 0, ErrNilSchedule
	}
	if j == nil {
		return 0, ErrNilJob
	}
	ec := entryConfig{}
	for _, o := range opts {
		o(&ec)
	}

	wrappers := make([]Wrapper, 0, len(c.cfg.chain)+len(ec.chain)+1)
	wrappers = append(wrappers, c.cfg.chain...)
	wrappers = append(wrappers, ec.chain...)
	rp := c.cfg.retry
	if ec.retrySet {
		rp = ec.retry
	}
	if !rp.IsZero() {
		wrappers = append(wrappers, rp.Wrapper())
	}
	wrapped := Chain(wrappers...)(j)

	missed := c.cfg.missedPolicy
	if ec.missedSet {
		missed = ec.missed
	}
	jitter := c.cfg.jitter
	if ec.jitterSet {
		jitter = ec.jitter
	}
	locker := c.cfg.locker
	if ec.lockerSet {
		locker = ec.locker
	}
	anchor := ec.lastRun
	if anchor.IsZero() {
		anchor = time.Now()
	}
	next := s.Next(anchor)

	id := EntryID(c.nextID.Add(1))
	if locker != nil && ec.name == "" {
		// FireKey falls back to the process-local EntryID, which only matches
		// across identical binaries registering entries in identical order.
		c.cfg.logger.Warn("cron: distributed locker on unnamed entry; use WithName for cross-instance fire keys",
			slog.Uint64("id", uint64(id)))
	}
	e := &entry{
		id:       id,
		name:     ec.name,
		spec:     spec,
		schedule: s,
		wrapped:  wrapped,
		timeout:  ec.timeout,
		jitter:   jitter,
		missed:   missed,
		locker:   locker,
		next:     next,
		prev:     ec.lastRun,
	}

	c.mu.Lock()
	if c.cfg.maxEntries > 0 && len(c.byEntry) >= c.cfg.maxEntries {
		c.mu.Unlock()
		return 0, ErrCapacityReached
	}
	if !e.next.IsZero() {
		e.item = c.h.Push(e.next.UnixNano(), e)
	}
	c.byEntry[id] = e
	view := entryView(e)
	c.publishViewAdd(e, &view)
	heapLen := c.h.Len()
	c.mu.Unlock()

	recordJobScheduled(c.cfg.recorder, e.name)
	recordQueueDepth(c.cfg.recorder, heapLen)
	c.hooks.emitSchedule(EventSchedule{
		EntryID: id, Name: e.name, Schedule: s, Next: e.next,
	})
	c.wake()
	return id, nil
}

func (c *Cron) updateSchedule(id EntryID, spec string, s Schedule) error {
	next := s.Next(time.Now())

	c.mu.Lock()
	e, ok := c.byEntry[id]
	if !ok {
		c.mu.Unlock()
		return ErrEntryNotFound
	}
	e.spec, e.schedule = spec, s
	e.gen++
	if e.item != nil {
		c.h.Remove(e.item)
		e.item = nil
	}
	if e.paused {
		e.next = time.Time{}
	} else {
		e.next = next
		if !next.IsZero() {
			e.item = c.h.Push(next.UnixNano(), e)
		}
	}
	view := entryView(e)
	e.view.p.Store(&view)
	name := e.name
	emitNext := e.next
	heapLen := c.h.Len()
	c.mu.Unlock()

	c.wake()
	recordQueueDepth(c.cfg.recorder, heapLen)
	if !emitNext.IsZero() {
		recordJobScheduled(c.cfg.recorder, name)
		c.hooks.emitSchedule(EventSchedule{
			EntryID: id, Name: name, Schedule: s, Next: emitNext,
		})
	}
	return nil
}

func (c *Cron) trigger(id EntryID, result chan<- error) error {
	c.startMu.Lock()
	defer c.startMu.Unlock()
	if !c.running.Load() {
		return ErrSchedulerNotRunning
	}
	fireAt := time.Now()

	c.mu.Lock()
	e, ok := c.byEntry[id]
	if !ok {
		c.mu.Unlock()
		return ErrEntryNotFound
	}
	if !c.tryReserveInflight() {
		c.mu.Unlock()
		recordJobMissed(c.cfg.recorder, e.name, 0)
		c.hooks.emitMissed(EventMissed{
			EntryID: e.id, Name: e.name,
			ScheduledAt: fireAt, Lateness: 0,
			Policy: e.missed,
		})
		return ErrConcurrencyLimit
	}
	c.dispatch(c.runCtx, e, fireAt, fireOpts{manual: true, result: result})
	c.mu.Unlock()
	return nil
}

// publishViewAdd creates the entry's stable snapshot cell and inserts it. O(1).
func (c *Cron) publishViewAdd(e *entry, view *Entry) {
	cell := &viewCell{}
	cell.p.Store(view)
	e.view = cell
	c.viewMu.Lock()
	c.views[e.id] = cell
	c.viewMu.Unlock()
}

func (c *Cron) publishViewRemove(id EntryID) {
	c.viewMu.Lock()
	delete(c.views, id)
	c.viewMu.Unlock()
}

func (c *Cron) awaitShutdown(ctx context.Context, done <-chan struct{}) error {
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	wait := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(wait)
	}()
	select {
	case <-wait:
	case <-ctx.Done():
		return ctx.Err()
	}
	return c.hooks.close(ctx)
}

func (c *Cron) loop(ctx context.Context) {
	defer close(c.runDone)
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()

	for {
		timer.Reset(c.peekDelay())
		select {
		case <-ctx.Done():
			return
		case <-c.wakeCh:
		case <-timer.C:
			// Jobs parent off runCtx, not the loop ctx, so Drain can stop the
			// loop without cancelling them.
			c.fireDue(c.runCtx, time.Now())
		}
	}
}

func (c *Cron) peekDelay() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	it, ok := c.h.Peek()
	if !ok {
		return 24 * time.Hour
	}
	d := time.Until(time.Unix(0, it.Key))
	if d < 0 {
		return 0
	}
	return d
}

// fireDue keeps Schedule.Next outside c.mu; user schedules must not block
// Add/Remove/Trigger.
func (c *Cron) fireDue(ctx context.Context, now time.Time) {
	var due []dueFire
	nowNano := now.UnixNano()

	c.mu.Lock()
	for {
		it, ok := c.h.Peek()
		if !ok || it.Key > nowNano {
			break
		}
		c.h.Pop()
		e := it.Value
		e.item = nil
		due = append(due, dueFire{e: e, schedule: e.schedule, scheduled: e.next, gen: e.gen})
	}
	c.mu.Unlock()

	for _, d := range due {
		c.commitAndDispatch(ctx, c.makeFirePlan(d, now))
	}

	if len(due) > 0 {
		recordQueueDepth(c.cfg.recorder, c.heapLen())
	}
}

func (c *Cron) makeFirePlan(d dueFire, now time.Time) firePlan {
	p := firePlan{
		e:         d.e,
		schedule:  d.schedule,
		gen:       d.gen,
		scheduled: d.scheduled,
		lateness:  now.Sub(d.scheduled),
	}
	if p.lateness > c.cfg.missedTolerance {
		p.missed = true
		switch d.e.missed {
		case MissedRunOnce:
			p.fireOne = findMostRecentMissed(d.schedule, d.scheduled, now)
		case MissedRunAll:
			p.fireAll = findAllMissed(d.schedule, d.scheduled, now)
		}
	} else {
		p.fireOne = d.scheduled
	}
	p.nextFire = d.schedule.Next(now)
	return p
}

func (c *Cron) commitAndDispatch(ctx context.Context, p firePlan) {
	c.mu.Lock()
	cur, ok := c.byEntry[p.e.id]
	if !ok || cur != p.e || cur.gen != p.gen {
		// Removed, paused, resumed, or updated since the pop; the plan is stale.
		c.mu.Unlock()
		return
	}

	fires := p.fireAll
	if len(fires) == 0 && !p.fireOne.IsZero() {
		fires = []time.Time{p.fireOne}
	}
	var run []time.Time
	rejected := 0
	for _, ft := range fires {
		if c.tryReserveInflight() {
			run = append(run, ft)
		} else {
			rejected++
		}
	}

	cur.next = p.nextFire
	if !cur.next.IsZero() {
		cur.item = c.h.Push(cur.next.UnixNano(), cur)
	}
	view := entryView(cur)
	cur.view.p.Store(&view)

	for _, ft := range run {
		c.dispatch(ctx, cur, ft, fireOpts{advancePrev: true})
	}
	nextEmit := cur.next
	name := cur.name
	policy := cur.missed
	c.mu.Unlock()

	if p.missed {
		recordJobMissed(c.cfg.recorder, name, p.lateness)
		c.hooks.emitMissed(EventMissed{
			EntryID: p.e.id, Name: name,
			ScheduledAt: p.scheduled, Lateness: p.lateness,
			Policy: policy,
		})
	} else if !p.fireOne.IsZero() && rejected > 0 {
		// On-time fire rejected by the concurrency limit; late fires already
		// emitted one missed event above.
		lateness := time.Since(p.fireOne)
		recordJobMissed(c.cfg.recorder, name, lateness)
		c.hooks.emitMissed(EventMissed{
			EntryID: p.e.id, Name: name,
			ScheduledAt: p.fireOne, Lateness: lateness,
			Policy: policy,
		})
	}
	if !nextEmit.IsZero() {
		recordJobScheduled(c.cfg.recorder, name)
		c.hooks.emitSchedule(EventSchedule{
			EntryID: p.e.id, Name: name,
			Schedule: p.schedule, Next: nextEmit,
		})
	}
}

func (c *Cron) dispatch(parent context.Context, e *entry, scheduledAt time.Time, opts fireOpts) {
	c.wg.Go(func() {
		defer c.inflight.Add(-1)

		// Jitter waits on the run ctx, not the job-timeout ctx, so it never eats
		// the timeout budget; manual Trigger fires immediately.
		// Manual triggers carry opts.result and skip jitter, so this abort path
		// never owes a result.
		if !opts.manual && !c.applyJitter(parent, e.jitter) {
			// Reachable only when Stop cancels mid-jitter; record it so the
			// reserved fire isn't dropped silently.
			lateness := time.Since(scheduledAt)
			recordJobMissed(c.cfg.recorder, e.name, lateness)
			c.hooks.emitMissed(EventMissed{
				EntryID: e.id, Name: e.name,
				ScheduledAt: scheduledAt, Lateness: lateness,
				Policy: e.missed,
			})
			return
		}

		// Distributed coordination runs after jitter (which spreads the fleet's
		// Lock calls) and before the timeout ctx. Manual triggers bypass it:
		// Trigger means "run it HERE now", and manual fires are the only ones
		// carrying opts.result, so these skip paths never owe a result.
		if !opts.manual {
			if el := c.cfg.elector; el != nil {
				if err := el.IsLeader(parent); err != nil {
					c.skipFire(e, scheduledAt, SkipNotLeader, err)
					return
				}
			}
			if e.locker != nil {
				release, err := e.locker.Lock(parent, FireKey(e.name, e.id, scheduledAt))
				if err != nil {
					reason := SkipLockError
					if errors.Is(err, ErrLockHeld) {
						reason = SkipLockHeld
					}
					c.skipFire(e, scheduledAt, reason, err)
					return
				}
				// Release after the job with a ctx detached from cancellation,
				// so Stop or a job timeout cannot prevent it; TTL is the net.
				defer func() {
					rctx, cancel := context.WithTimeout(context.WithoutCancel(parent), releaseTimeout)
					defer cancel()
					if rerr := release(rctx); rerr != nil {
						c.cfg.logger.Error("cron: lock release failed",
							slog.String("name", e.name), slog.Any("error", rerr))
					}
				}()
			}
		}

		// Build the timeout ctx after jitter so it covers only runtime. The
		// e.timeout > 0 guard matters: WithTimeoutCause(parent, 0) is born expired.
		jobCtx := parent
		if e.timeout > 0 {
			var cancel context.CancelFunc
			jobCtx, cancel = context.WithTimeoutCause(parent, e.timeout, ErrJobTimeout)
			defer cancel()
		}

		fireAt := time.Now()
		recordJobStarted(c.cfg.recorder, e.name)
		c.hooks.emitJobStart(EventJobStart{
			EntryID: e.id, Name: e.name,
			ScheduledAt: scheduledAt, FireAt: fireAt,
		})
		err := c.runJob(jobCtx, e)
		dur := time.Since(fireAt)
		recordJobCompleted(c.cfg.recorder, e.name, dur, err)
		c.hooks.emitJobComplete(EventJobComplete{
			EntryID: e.id, Name: e.name,
			ScheduledAt: scheduledAt, FireAt: fireAt,
			Duration: dur,
			Err:      err,
		})
		if opts.advancePrev {
			c.advancePrev(e.id, scheduledAt)
		}
		if opts.result != nil {
			opts.result <- err
		}
	})
}

// skipFire records a fire suppressed by distributed coordination.
func (c *Cron) skipFire(e *entry, scheduledAt time.Time, reason SkipReason, err error) {
	recordJobSkipped(c.cfg.recorder, e.name, reason)
	c.hooks.emitSkipped(EventSkipped{
		EntryID: e.id, Name: e.name,
		ScheduledAt: scheduledAt, Reason: reason, Err: err,
	})
}

// runJob executes the wrapped job, converting panics into ErrJobPanic unless
// WithoutRecover was set.
func (c *Cron) runJob(ctx context.Context, e *entry) (err error) {
	if !c.cfg.recoverDisabled {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("%w: %v", ErrJobPanic, r)
				c.cfg.logger.Error("cron: job panic recovered",
					slog.String("name", e.name),
					slog.Any("panic", r),
					slog.String("stack", string(debug.Stack())))
			}
		}()
	}
	return e.wrapped.Run(ctx)
}

func (c *Cron) advancePrev(id EntryID, fireAt time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cur, ok := c.byEntry[id]
	if !ok {
		return
	}
	if !fireAt.After(cur.prev) {
		return
	}
	cur.prev = fireAt
	view := entryView(cur)
	cur.view.p.Store(&view)
}

func (c *Cron) tryReserveInflight() bool {
	if c.cfg.maxConcurrent <= 0 {
		c.inflight.Add(1)
		return true
	}
	limit := int64(c.cfg.maxConcurrent)
	for {
		cur := c.inflight.Load()
		if cur >= limit {
			return false
		}
		if c.inflight.CompareAndSwap(cur, cur+1) {
			return true
		}
	}
}

func (c *Cron) heapLen() int {
	c.mu.Lock()
	n := c.h.Len()
	c.mu.Unlock()
	return n
}

func (c *Cron) wake() {
	select {
	case c.wakeCh <- struct{}{}:
	default:
	}
}

func (c *Cron) applyJitter(ctx context.Context, max time.Duration) bool {
	if max <= 0 {
		return true
	}
	d := mathrand.N(max)
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func findMostRecentMissed(s Schedule, lastFire, now time.Time) time.Time {
	if lastFire.IsZero() || lastFire.After(now) {
		return time.Time{}
	}
	last := lastFire
	cursor := lastFire
	for range missedScanCap {
		next := s.Next(cursor)
		if next.IsZero() || next.After(now) {
			return last
		}
		last = next
		cursor = next
	}
	return last
}

// findAllMissed returns every missed instant in [lastFire, now], keeping the
// newest missedRunAllCap when the backlog is larger.
func findAllMissed(s Schedule, lastFire, now time.Time) []time.Time {
	if lastFire.IsZero() || lastFire.After(now) {
		return nil
	}
	all := []time.Time{lastFire}
	cursor := lastFire
	for range missedScanCap {
		next := s.Next(cursor)
		if next.IsZero() || next.After(now) {
			break
		}
		all = append(all, next)
		cursor = next
	}
	if len(all) > missedRunAllCap {
		all = all[len(all)-missedRunAllCap:]
	}
	return all
}

func entryView(e *entry) Entry {
	return Entry{
		ID:       e.id,
		Name:     e.name,
		Spec:     e.spec,
		Schedule: e.schedule,
		Prev:     e.prev,
		Next:     e.next,
		Paused:   e.paused,
	}
}

// compareNext orders entries by Next, with zero times (exhausted or triggered)
// sorted last.
func compareNext(a, b time.Time) int {
	switch {
	case a.IsZero() && b.IsZero():
		return 0
	case a.IsZero():
		return 1
	case b.IsZero():
		return -1
	default:
		return a.Compare(b)
	}
}
