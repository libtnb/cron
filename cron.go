package cron

import (
	"context"
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
	key      string
	spec     string
	schedule Schedule
	wrapped  Job // global+entry chain applied
	timeout  time.Duration
	jitter   time.Duration
	missed   MissedFirePolicy
	claimer  Claimer

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
	byKey   map[string]EntryID // non-empty stable keys, unique within the scheduler
	nextID  atomic.Uint64

	viewMu sync.RWMutex // guards the views map structure; cell values are atomic
	views  viewMap      // snapshot map read by Entry/Entries

	events *eventBus

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
	jobsDone chan struct{}
	jobsOnce sync.Once
}

// New constructs a Cron. It does not start scheduling until Start is called.
func New(opts ...Option) (*Cron, error) {
	cfg := config{}
	for i, o := range opts {
		if o == nil {
			return nil, fmt.Errorf("%w: option %d is nil", ErrInvalidOption, i)
		}
		if err := o(&cfg); err != nil {
			return nil, err
		}
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
			popts = append(popts, WithOptionalSeconds())
		}
		cfg.parser = NewStandardParser(popts...)
	} else if cfg.locSet {
		cfg.logger.Warn("cron: WithLocation ignored; the parser from WithParser controls the timezone")
	}
	if cfg.missedTolerance <= 0 {
		cfg.missedTolerance = defaultMissedTolerance
	}

	c := &Cron{
		cfg:      cfg,
		h:        heap.New[*entry](),
		byEntry:  make(map[EntryID]*entry),
		byKey:    make(map[string]EntryID),
		views:    make(viewMap),
		wakeCh:   make(chan struct{}, 1),
		jobsDone: make(chan struct{}),
	}
	c.parseCache.Limit = defaultParseCacheLimit
	c.events = newEventBus(cfg.observers, cfg.recorder, cfg.logger, cfg.observerBuffer)
	return c, nil
}

// MustNew constructs a Cron and panics if an option is invalid.
func MustNew(opts ...Option) *Cron {
	c, err := New(opts...)
	if err != nil {
		panic(err)
	}
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
	if isNilLike(s) {
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
	if e.key != "" {
		delete(c.byKey, e.key)
	}
	c.publishViewRemove(id)
	heapLen := c.h.Len()
	c.mu.Unlock()
	c.wake()
	c.events.publish(QueueDepthEvent{Depth: heapLen})
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
	c.events.publish(QueueDepthEvent{Depth: heapLen})
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
	c.events.publish(QueueDepthEvent{Depth: heapLen})
	if !next.IsZero() {
		c.events.publish(ScheduleEvent{
			Entry: EntryRef{ID: id, Key: cur.key, Name: name}, Schedule: s, Next: next,
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
	if ctx == nil {
		return ErrNilContext
	}
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
// cause), and waits for the loop, jobs, and observer queue to drain, capped
// by ctx. Returns ctx.Err() on timeout. Do not call it from inside a Job.
func (c *Cron) Stop(ctx context.Context) error {
	if ctx == nil {
		return ErrNilContext
	}
	c.startMu.Lock()
	c.started = true
	if c.runDone == nil {
		c.startMu.Unlock()
		return c.events.close(ctx)
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
	if ctx == nil {
		return ErrNilContext
	}
	c.startMu.Lock()
	c.started = true
	if c.runDone == nil {
		c.startMu.Unlock()
		return c.events.close(ctx)
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
	if isNilLike(s) {
		c.parseCache.Forget(spec)
		return nil, ErrNilSchedule
	}
	return s, nil
}

func (c *Cron) add(spec string, s Schedule, j Job, opts ...EntryOption) (EntryID, error) {
	if isNilLike(s) {
		return 0, ErrNilSchedule
	}
	if isNilLike(j) {
		return 0, ErrNilJob
	}
	ec := entryConfig{}
	for i, o := range opts {
		if o == nil {
			return 0, fmt.Errorf("%w: entry option %d is nil", ErrInvalidOption, i)
		}
		if err := o(&ec); err != nil {
			return 0, err
		}
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
	if isNilLike(wrapped) {
		return 0, ErrNilJob
	}

	missed := c.cfg.missedPolicy
	if ec.missedSet {
		missed = ec.missed
	}
	jitter := c.cfg.jitter
	if ec.jitterSet {
		jitter = ec.jitter
	}
	claimer := c.cfg.claimer
	if ec.claimerSet {
		claimer = ec.claimer
	}
	if claimer != nil {
		if ec.key == "" {
			return 0, ErrClaimerRequiresKey
		}
		if _, ok := s.(ConstantDelay); ok {
			// Not rejected: identical replicas registering together do share
			// keys. But any stagger desynchronizes the phases for good.
			c.cfg.logger.Warn(
				"cron: ConstantDelay is process-local; use AlignedDelay or a cron expression with a Claimer",
				slog.String("key", ec.key),
			)
		}
	}
	anchor := ec.lastRun
	if anchor.IsZero() {
		anchor = time.Now()
	}
	next := s.Next(anchor)

	id := EntryID(c.nextID.Add(1))
	e := &entry{
		id:       id,
		name:     ec.name,
		key:      ec.key,
		spec:     spec,
		schedule: s,
		wrapped:  wrapped,
		timeout:  ec.timeout,
		jitter:   jitter,
		missed:   missed,
		claimer:  claimer,
		next:     next,
		prev:     ec.lastRun,
	}

	c.mu.Lock()
	if c.cfg.maxEntries > 0 && len(c.byEntry) >= c.cfg.maxEntries {
		c.mu.Unlock()
		return 0, ErrCapacityReached
	}
	if e.key != "" {
		if _, exists := c.byKey[e.key]; exists {
			c.mu.Unlock()
			return 0, fmt.Errorf("%w: %q", ErrDuplicateKey, e.key)
		}
		c.byKey[e.key] = id
	}
	if !e.next.IsZero() {
		e.item = c.h.Push(e.next.UnixNano(), e)
	}
	c.byEntry[id] = e
	view := entryView(e)
	c.publishViewAdd(e, &view)
	heapLen := c.h.Len()
	c.mu.Unlock()

	c.events.publish(ScheduleEvent{
		Entry: entryRef(e), Schedule: s, Next: e.next,
	})
	c.events.publish(QueueDepthEvent{Depth: heapLen})
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
	c.events.publish(QueueDepthEvent{Depth: heapLen})
	if !emitNext.IsZero() {
		c.events.publish(ScheduleEvent{
			Entry: EntryRef{ID: id, Key: e.key, Name: name}, Schedule: s, Next: emitNext,
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
		c.events.publish(RejectedFireEvent{
			Entry: entryRef(e), ScheduledAt: fireAt,
			Reason: RejectConcurrencyLimit,
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
	c.jobsOnce.Do(func() {
		go func() {
			c.wg.Wait()
			close(c.jobsDone)
		}()
	})
	select {
	case <-c.jobsDone:
	case <-ctx.Done():
		return ctx.Err()
	}
	return c.events.close(ctx)
}

func (c *Cron) loop(ctx context.Context) {
	defer close(c.runDone)
	defer c.running.Store(false)
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
		c.events.publish(QueueDepthEvent{Depth: c.heapLen()})
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
	stalePlan := !ok || cur != p.e || cur.gen != p.gen
	if stalePlan {
		// Removed, paused, resumed, or updated since the pop; the plan is stale.
		c.mu.Unlock()
		return
	}

	fires := p.fireAll
	if len(fires) == 0 && !p.fireOne.IsZero() {
		fires = []time.Time{p.fireOne}
	}
	var run []time.Time
	var rejected []time.Time
	for _, ft := range fires {
		if c.tryReserveInflight() {
			run = append(run, ft)
		} else {
			rejected = append(rejected, ft)
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
		c.events.publish(MissedFireEvent{
			Entry: EntryRef{
				ID:   p.e.id,
				Key:  p.e.key,
				Name: name,
			},
			ScheduledAt: p.scheduled, Lateness: p.lateness,
			Policy: policy,
		})
	}
	for _, scheduledAt := range rejected {
		c.events.publish(RejectedFireEvent{
			Entry: EntryRef{
				ID:   p.e.id,
				Key:  p.e.key,
				Name: name,
			},
			ScheduledAt: scheduledAt, Reason: RejectConcurrencyLimit,
		})
	}
	if !nextEmit.IsZero() {
		c.events.publish(ScheduleEvent{
			Entry: EntryRef{
				ID:   p.e.id,
				Key:  p.e.key,
				Name: name,
			},
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
			c.events.publish(CanceledFireEvent{
				Entry: entryRef(e), ScheduledAt: scheduledAt,
				Cause: context.Cause(parent),
			})
			return
		}

		// Distributed coordination runs after jitter (which spreads the fleet's
		// backend calls) and before the timeout ctx. Manual triggers bypass it:
		// Trigger means "run it HERE now", and manual fires are the only ones
		// carrying opts.result, so these skip paths never owe a result.
		if !opts.manual {
			if el := c.cfg.elector; el != nil {
				leader, err := el.IsLeader(parent)
				if err != nil {
					c.skipFire(e, scheduledAt, SkipElectionError, err)
					return
				}
				if !leader {
					c.skipFire(e, scheduledAt, SkipNotLeader, nil)
					return
				}
			}
			if e.claimer != nil {
				claimed, err := e.claimer.Claim(parent, fireKey(e.key, scheduledAt))
				if err != nil {
					c.skipFire(e, scheduledAt, SkipClaimError, err)
					return
				}
				if !claimed {
					c.skipFire(e, scheduledAt, SkipAlreadyClaimed, nil)
					return
				}
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
		jobCtx = context.WithValue(jobCtx, entryInfoKey{}, EntryInfo{
			ID: e.id, Name: e.name, Key: e.key, ScheduledAt: scheduledAt,
		})

		fireAt := time.Now()
		c.events.publish(JobStartEvent{
			Entry: entryRef(e), ScheduledAt: scheduledAt, StartedAt: fireAt,
		})
		err := c.runJob(jobCtx, e)
		dur := time.Since(fireAt)
		c.events.publish(JobCompleteEvent{
			Entry:       entryRef(e),
			ScheduledAt: scheduledAt, StartedAt: fireAt,
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
	c.events.publish(SkippedFireEvent{
		Entry:       entryRef(e),
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
		Key:      e.key,
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
