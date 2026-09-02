package cron

import (
	"fmt"
	"iter"
	"log/slog"
	"slices"
	"time"
)

// Add parses spec with the configured parser and registers j. The first fire
// is computed from now, or from WithLastRun when set. Parsed specs are
// memoized, so repeated Add calls with the same expression share one Schedule.
//
// Returns a *ParseError for an invalid spec, ErrNilSchedule if the parser
// returned no schedule, ErrNilJob, an error wrapping ErrInvalidOption for a
// rejected entry option, ErrCapacityReached when WithMaxEntries is exhausted,
// ErrClaimerRequiresKey when a Claimer applies without WithKey, and an error
// wrapping ErrDuplicateKey when the key is already registered.
func (c *Cron) Add(spec string, j Job, opts ...EntryOption) (EntryID, error) {
	s, err := c.parse(spec)
	if err != nil {
		return 0, err
	}
	return c.add(spec, s, j, opts...)
}

// AddSchedule is Add for a programmatic Schedule; Entry.Spec stays empty for
// such entries. It returns the same errors as Add except *ParseError.
func (c *Cron) AddSchedule(s Schedule, j Job, opts ...EntryOption) (EntryID, error) {
	return c.add("", s, j, opts...)
}

// Update re-parses spec and swaps id's schedule in place, keeping the job,
// entry options, ID and Prev. The next fire is recomputed from now; a paused
// entry stays paused; a fire already being planned under the old schedule is
// discarded. Returns a *ParseError for an invalid spec, ErrNilSchedule, or
// ErrEntryNotFound.
func (c *Cron) Update(id EntryID, spec string) error {
	s, err := c.parse(spec)
	if err != nil {
		return err
	}
	return c.updateSchedule(id, spec, s)
}

// UpdateSchedule is Update for a programmatic Schedule. Returns ErrNilSchedule
// or ErrEntryNotFound.
func (c *Cron) UpdateSchedule(id EntryID, s Schedule) error {
	if isNilLike(s) {
		return ErrNilSchedule
	}
	return c.updateSchedule(id, "", s)
}

// Remove deregisters id. Invocations already running continue; no further
// automatic fires happen and Trigger rejects the id. Returns ErrEntryNotFound
// for an unknown or already removed id.
func (c *Cron) Remove(id EntryID) error {
	c.mu.Lock()
	e, ok := c.byEntry[id]
	if !ok {
		c.mu.Unlock()
		return ErrEntryNotFound
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
	return nil
}

// Pause suspends automatic fires for id, keeping the entry and its Prev.
// Trigger still works while paused. Pausing a paused entry is a no-op; an
// unknown id yields ErrEntryNotFound.
func (c *Cron) Pause(id EntryID) error {
	c.mu.Lock()
	e, ok := c.byEntry[id]
	if !ok {
		c.mu.Unlock()
		return ErrEntryNotFound
	}
	if e.paused {
		c.mu.Unlock()
		return nil
	}
	e.paused = true
	e.gen++
	e.next = time.Time{}
	if e.item != nil {
		c.h.Remove(e.item)
		e.item = nil
	}
	view := entryView(e)
	e.view.p.Store(&view)
	heapLen := c.h.Len()
	c.mu.Unlock()
	c.wake()
	c.events.publish(QueueDepthEvent{Depth: heapLen})
	return nil
}

// Resume re-enables automatic fires for id, computing the next fire from now
// rather than from the pause instant, so nothing is replayed. Resuming an
// entry that is not paused is a no-op; an unknown id yields ErrEntryNotFound.
func (c *Cron) Resume(id EntryID) error {
	c.mu.Lock()
	e, ok := c.byEntry[id]
	if !ok {
		c.mu.Unlock()
		return ErrEntryNotFound
	}
	if !e.paused {
		c.mu.Unlock()
		return nil
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
		return ErrEntryNotFound
	}
	if cur.gen != gen {
		// A racing Pause/Resume/Update won; its state stands.
		c.mu.Unlock()
		return nil
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
	return nil
}

// Entry returns the current snapshot for id. ok is false after Remove or for
// an id that was never registered. Reading a snapshot never blocks the
// scheduler.
func (c *Cron) Entry(id EntryID) (Entry, bool) {
	c.viewMu.RLock()
	cell, ok := c.views[id]
	c.viewMu.RUnlock()
	if !ok {
		return Entry{}, false
	}
	return *cell.p.Load(), true
}

// Entries yields a snapshot of every registered entry ordered by Next, with
// exhausted, paused and triggered-only entries (zero Next) last. The snapshot
// is taken when iteration starts; changes made during iteration are not
// reflected.
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

// add validates and registers one entry. The wrapper chain is assembled here,
// once, so stateful wrappers get one instance per entry, and Schedule.Next is
// evaluated before c.mu is taken.
func (c *Cron) add(spec string, s Schedule, j Job, opts ...EntryOption) (EntryID, error) {
	if isNilLike(s) {
		return 0, ErrNilSchedule
	}
	if isNilLike(j) {
		return 0, ErrNilJob
	}
	var ec entryConfig
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

// updateSchedule installs s on id and re-queues the entry from now. Bumping
// gen invalidates any fire plan computed under the previous schedule.
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
	e.next = time.Time{}
	if !e.paused {
		e.next = next
	}
	if !e.next.IsZero() {
		e.item = c.h.Push(e.next.UnixNano(), e)
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

// publishViewAdd creates the entry's stable snapshot cell and inserts it. O(1).
func (c *Cron) publishViewAdd(e *entry, view *Entry) {
	cell := &viewCell{}
	cell.p.Store(view)
	e.view = cell
	c.viewMu.Lock()
	c.views[e.id] = cell
	c.viewMu.Unlock()
}

// publishViewRemove drops the snapshot cell so Entry reports ok == false.
func (c *Cron) publishViewRemove(id EntryID) {
	c.viewMu.Lock()
	delete(c.views, id)
	c.viewMu.Unlock()
}
