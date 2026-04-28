package cron

import (
	"context"
	"iter"
	"log/slog"
	"maps"
	mathrand "math/rand/v2"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/libtnb/cron/internal/heap"
	"github.com/libtnb/cron/internal/parsecache"
)

type entry struct {
	id       EntryID
	name     string
	spec     string
	schedule Schedule
	wrapped  Job // global+entry chain applied
	timeout  time.Duration

	next time.Time
	prev time.Time

	item *heap.Item[*entry] // nil iff not in the heap
}

// viewCell lets Entry reads stay lock-free while writers swap snapshots.
type viewCell struct {
	p atomic.Pointer[Entry]
}

type viewMap map[EntryID]*viewCell

type dueFire struct {
	e         *entry
	scheduled time.Time
}

type firePlan struct {
	e         *entry
	scheduled time.Time
	fireOne   time.Time // zero if no fire (MissedSkip)
	nextFire  time.Time
	lateness  time.Duration
	missed    bool
}

// Cron is a job scheduler. Construct one with New, register jobs, then Start.
type Cron struct {
	cfg        config
	parseCache parsecache.Cache[Schedule]

	mu      sync.Mutex              // guards h, byEntry, view publishing
	h       *heap.Heap[*entry]      // scheduling heap
	byEntry map[EntryID]*entry      // canonical entry table
	views   atomic.Pointer[viewMap] // outer view map (rebuilt under mu)
	nextID  atomic.Uint64

	hooks *hookDispatcher

	running atomic.Bool
	wakeCh  chan struct{}

	startMu   sync.Mutex
	runCtx    context.Context
	runCancel context.CancelCauseFunc
	runDone   chan struct{}
	started   bool

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
	if cfg.parser == nil {
		pOpts := append([]ParserOption{WithDefaultLocation(cfg.loc)}, cfg.parserOpts...)
		cfg.parser = NewStandardParser(pOpts...)
	}
	if cfg.missedTolerance <= 0 {
		cfg.missedTolerance = defaultMissedTolerance
	}
	if cfg.logger == nil {
		cfg.logger = slog.Default()
	}

	c := &Cron{
		cfg:     cfg,
		h:       heap.New[*entry](),
		byEntry: make(map[EntryID]*entry),
		wakeCh:  make(chan struct{}, 1),
	}
	c.hooks = newHookDispatcher(cfg.hooks, cfg.logger, cfg.recorder, cfg.hookBuffer)
	empty := viewMap{}
	c.views.Store(&empty)
	return c
}

func (c *Cron) publishViewUpdateLocked(id EntryID, view *Entry) {
	(*c.views.Load())[id].p.Store(view)
}

func (c *Cron) publishViewAddLocked(id EntryID, view *Entry) {
	cell := &viewCell{}
	cell.p.Store(view)
	old := c.views.Load()
	nm := make(viewMap, len(*old)+1)
	maps.Copy(nm, *old)
	nm[id] = cell
	c.views.Store(&nm)
}

func (c *Cron) publishViewRemoveLocked(id EntryID) {
	old := c.views.Load()
	nm := make(viewMap, len(*old)-1)
	for k, vc := range *old {
		if k != id {
			nm[k] = vc
		}
	}
	c.views.Store(&nm)
}

// Add parses spec and registers j. It returns a *ParseError for invalid specs
// or ErrCapacityReached when WithMaxEntries rejects the registration.
func (c *Cron) Add(spec string, j Job, opts ...EntryOption) (EntryID, error) {
	if s, err, ok := c.parseCache.Lookup(spec); ok {
		if err != nil {
			return 0, err
		}
		return c.add(spec, s, j, opts...)
	}
	s, err := c.parseCache.Get(spec, func() (Schedule, error) {
		return c.cfg.parser.Parse(spec)
	})
	if err != nil {
		return 0, err
	}
	return c.add(spec, s, j, opts...)
}

// AddSchedule registers j against a programmatic Schedule.
func (c *Cron) AddSchedule(s Schedule, j Job, opts ...EntryOption) (EntryID, error) {
	return c.add("", s, j, opts...)
}

func (c *Cron) add(spec string, s Schedule, j Job, opts ...EntryOption) (EntryID, error) {
	ec := entryConfig{}
	for _, o := range opts {
		o(&ec)
	}

	wrappers := append(append([]Wrapper(nil), c.cfg.chain...), ec.chain...)
	rp := c.cfg.retry
	if ec.retrySet {
		rp = ec.retry
	}
	if !rp.IsZero() {
		wrappers = append(wrappers, rp.Wrapper())
	}
	wrapped := Chain(wrappers...)(j)

	if c.cfg.maxEntries > 0 {
		c.mu.Lock()
		full := len(c.byEntry) >= c.cfg.maxEntries
		c.mu.Unlock()
		if full {
			return 0, ErrCapacityReached
		}
	}
	next := s.Next(time.Now())

	id := EntryID(c.nextID.Add(1))
	e := &entry{
		id:       id,
		name:     ec.name,
		spec:     spec,
		schedule: s,
		wrapped:  wrapped,
		timeout:  ec.timeout,
		next:     next,
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
	c.publishViewAddLocked(id, &view)
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
	c.publishViewRemoveLocked(id)
	heapLen := c.h.Len()
	c.mu.Unlock()
	c.wake()
	recordQueueDepth(c.cfg.recorder, heapLen)
	return true
}

// Trigger fires id immediately. It returns ErrSchedulerNotRunning,
// ErrEntryNotFound, or ErrConcurrencyLimit when dispatch is rejected.
func (c *Cron) Trigger(id EntryID) error {
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
			Policy: c.cfg.missedPolicy,
		})
		return ErrConcurrencyLimit
	}
	c.dispatch(c.runCtx, e, fireAt, false)
	c.mu.Unlock()
	return nil
}

// Entry returns the current snapshot for id.
func (c *Cron) Entry(id EntryID) (Entry, bool) {
	m := c.views.Load()
	cell, ok := (*m)[id]
	if !ok {
		return Entry{}, false
	}
	return *cell.p.Load(), true
}

// Entries returns registered entry snapshots ordered by Next.
func (c *Cron) Entries() iter.Seq[Entry] {
	return func(yield func(Entry) bool) {
		m := c.views.Load()
		if len(*m) == 0 {
			return
		}
		entries := make([]Entry, 0, len(*m))
		for _, cell := range *m {
			entries = append(entries, *cell.p.Load())
		}
		slices.SortFunc(entries, compareEntriesByNext)
		for _, e := range entries {
			if !yield(e) {
				return
			}
		}
	}
}

func compareEntriesByNext(a, b Entry) int {
	switch {
	case a.Next.IsZero() && b.Next.IsZero():
		return 0
	case a.Next.IsZero():
		return 1
	case b.Next.IsZero():
		return -1
	default:
		return a.Next.Compare(b.Next)
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
	ctx, cancel := context.WithCancelCause(context.Background())
	c.runCtx = ctx
	c.runCancel = cancel
	c.runDone = make(chan struct{})
	go c.loop(ctx)
	return nil
}

// Running reports whether the scheduler is running. It is observational; use
// Trigger's returned error for race-free dispatch decisions.
func (c *Cron) Running() bool { return c.running.Load() }

// Stop halts the scheduler and waits for the loop, in-flight jobs and
// hook dispatcher to drain, capped by ctx. Returns ctx.Err() on
// timeout. Do not call it from inside a Job.
func (c *Cron) Stop(ctx context.Context) error {
	c.startMu.Lock()
	c.started = true
	if c.runDone == nil {
		c.startMu.Unlock()
		return c.hooks.close(ctx)
	}
	if c.running.Swap(false) {
		c.runCancel(ErrCronStopping)
	}
	done := c.runDone
	c.startMu.Unlock()

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
			c.fireDue(ctx, time.Now())
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
	if ctx.Err() != nil {
		return
	}

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
		due = append(due, dueFire{e: e, scheduled: e.next})
	}
	c.mu.Unlock()

	for i, d := range due {
		if ctx.Err() != nil {
			c.rePushDue(due[i:])
			return
		}
		p, ok := c.makeFirePlan(ctx, d, now)
		if !ok {
			c.rePushDue(due[i:])
			return
		}
		c.commitAndDispatch(ctx, p)
	}

	if len(due) > 0 {
		recordQueueDepth(c.cfg.recorder, c.heapLen())
	}
}

func (c *Cron) makeFirePlan(ctx context.Context, d dueFire, now time.Time) (firePlan, bool) {
	p := firePlan{
		e:         d.e,
		scheduled: d.scheduled,
		lateness:  now.Sub(d.scheduled),
	}
	if p.lateness > c.cfg.missedTolerance {
		p.missed = true
		switch c.cfg.missedPolicy {
		case MissedSkip:
		case MissedRunOnce:
			fireOne, ok := c.findMostRecentMissed(ctx, d.e.schedule, d.scheduled, now)
			if !ok {
				return firePlan{}, false
			}
			p.fireOne = fireOne
		}
	} else {
		p.fireOne = d.scheduled
	}
	if ctx.Err() != nil {
		return firePlan{}, false
	}
	p.nextFire = d.e.schedule.Next(now)
	return p, true
}

func (c *Cron) rePushDue(due []dueFire) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, d := range due {
		cur, ok := c.byEntry[d.e.id]
		if !ok || cur != d.e {
			continue
		}
		if !cur.next.IsZero() && cur.item == nil {
			cur.item = c.h.Push(cur.next.UnixNano(), cur)
		}
	}
}

func (c *Cron) commitAndDispatch(ctx context.Context, p firePlan) {
	c.mu.Lock()
	cur, ok := c.byEntry[p.e.id]
	if !ok || cur != p.e {
		c.mu.Unlock()
		return
	}
	if ctx.Err() != nil {
		c.mu.Unlock()
		return
	}

	reserved := false
	if !p.fireOne.IsZero() {
		reserved = c.tryReserveInflight()
	}

	cur.next = p.nextFire
	if !cur.next.IsZero() {
		cur.item = c.h.Push(cur.next.UnixNano(), cur)
	}
	view := entryView(cur)
	c.publishViewUpdateLocked(p.e.id, &view)

	if reserved {
		c.dispatch(ctx, cur, p.fireOne, true)
	}
	nextEmit := cur.next
	name := cur.name
	c.mu.Unlock()

	if p.missed {
		recordJobMissed(c.cfg.recorder, name, p.lateness)
		c.hooks.emitMissed(EventMissed{
			EntryID: p.e.id, Name: name,
			ScheduledAt: p.scheduled, Lateness: p.lateness,
			Policy: c.cfg.missedPolicy,
		})
	}
	if !p.fireOne.IsZero() && !reserved {
		lateness := time.Since(p.fireOne)
		recordJobMissed(c.cfg.recorder, name, lateness)
		c.hooks.emitMissed(EventMissed{
			EntryID: p.e.id, Name: name,
			ScheduledAt: p.fireOne, Lateness: lateness,
			Policy: c.cfg.missedPolicy,
		})
	}
	if !nextEmit.IsZero() {
		recordJobScheduled(c.cfg.recorder, name)
		c.hooks.emitSchedule(EventSchedule{
			EntryID: p.e.id, Name: name,
			Schedule: p.e.schedule, Next: nextEmit,
		})
	}
}

func (c *Cron) findMostRecentMissed(ctx context.Context, s Schedule, lastFire, now time.Time) (time.Time, bool) {
	if lastFire.IsZero() || lastFire.After(now) {
		return time.Time{}, true
	}
	last := lastFire
	cursor := lastFire
	for range missedRunOnceCap {
		if ctx.Err() != nil {
			return time.Time{}, false
		}
		next := s.Next(cursor)
		if next.IsZero() || next.After(now) {
			return last, true
		}
		last = next
		cursor = next
	}
	return last, true
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

func (c *Cron) dispatch(parent context.Context, e *entry, scheduledAt time.Time, advancePrev bool) {
	jobCtx := parent
	var cancel context.CancelFunc
	if e.timeout > 0 {
		jobCtx, cancel = context.WithTimeoutCause(jobCtx, e.timeout, ErrJobTimeout)
	}

	c.wg.Go(func() {
		defer c.inflight.Add(-1)
		if cancel != nil {
			defer cancel()
		}
		if !c.applyJitter(jobCtx) {
			return
		}
		fireAt := time.Now()
		recordJobStarted(c.cfg.recorder, e.name)
		c.hooks.emitJobStart(EventJobStart{
			EntryID: e.id, Name: e.name,
			ScheduledAt: scheduledAt, FireAt: fireAt,
		})
		start := fireAt
		err := e.wrapped.Run(jobCtx)
		dur := time.Since(start)
		recordJobCompleted(c.cfg.recorder, e.name, dur, err)
		c.hooks.emitJobComplete(EventJobComplete{
			EntryID: e.id, Name: e.name,
			ScheduledAt: scheduledAt, FireAt: fireAt,
			Duration: dur,
			Err:      err,
		})
		if advancePrev {
			c.advancePrev(e.id, scheduledAt)
		}
	})
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
	c.publishViewUpdateLocked(id, &view)
}

func (c *Cron) heapLen() int {
	c.mu.Lock()
	n := c.h.Len()
	c.mu.Unlock()
	return n
}

func entryView(e *entry) Entry {
	return Entry{
		ID:       e.id,
		Name:     e.name,
		Spec:     e.spec,
		Schedule: e.schedule,
		Prev:     e.prev,
		Next:     e.next,
	}
}

func (c *Cron) wake() {
	select {
	case c.wakeCh <- struct{}{}:
	default:
	}
}

func (c *Cron) applyJitter(ctx context.Context) bool {
	if c.cfg.jitter <= 0 {
		return true
	}
	d := mathrand.N(c.cfg.jitter)
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
