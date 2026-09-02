package cron

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
)

// This file is the event bus. Every publish first calls the Recorder inline
// on the publishing goroutine, then offers the event to a bounded channel that
// one goroutine drains into the observers in order. A full channel drops the
// event, counted in ObserverDropEvent, rather than blocking the scheduler;
// panics in either sink are recovered and logged.

// Observer receives scheduler events asynchronously, in publication order, on
// one goroutine shared by all observers of a Cron. Observe therefore need not
// be safe for concurrent use, but it must not block for long: while one
// observer is slow the shared queue (WithObserverBuffer) fills and new events
// are dropped. Panics are recovered and logged. Stop and Drain wait for queued
// events to be delivered.
type Observer interface {
	// Observe handles one event.
	Observe(Event)
}

// ObserverFunc adapts a function to Observer.
type ObserverFunc func(Event)

// Observe calls f.
func (f ObserverFunc) Observe(event Event) { f(event) }

// Recorder receives every scheduler event inline on the publishing goroutine,
// before the observers, and never misses one. Record is called concurrently
// from the loop, planner and job goroutines, so it must be safe for concurrent
// use and fast: it sits on the dispatch path. A panic is recovered and logged.
// One Recorder can be installed with WithRecorder.
type Recorder interface {
	// Record handles one event synchronously.
	Record(Event)
}

// RecorderFunc adapts a function to Recorder.
type RecorderFunc func(Event)

// Record calls f.
func (f RecorderFunc) Record(event Event) { f(event) }

// eventBus fans events out to the inline Recorder and the async observers.
type eventBus struct {
	observers []Observer
	recorder  Recorder
	ch        chan Event
	log       *slog.Logger
	dropped   atomic.Int64

	startOnce sync.Once
	closeOnce sync.Once
	done      chan struct{}
}

// newEventBus builds a bus. Without observers no channel or goroutine is
// created and publish only feeds the recorder. A zero buffer selects 1024.
func newEventBus(observers []Observer, recorder Recorder, log *slog.Logger, buffer int) *eventBus {
	b := &eventBus{observers: observers, recorder: recorder, log: log}
	if len(observers) == 0 {
		return b
	}
	if buffer == 0 {
		buffer = 1024
	}
	b.ch = make(chan Event, buffer)
	b.done = make(chan struct{})
	return b
}

// publish records event inline, then queues it for the observers, dropping it
// when the queue is full. The delivery goroutine starts lazily on the first
// publish.
func (b *eventBus) publish(event Event) {
	if b == nil {
		return
	}
	b.record(event)
	if b.ch == nil {
		return
	}

	b.startOnce.Do(func() { go b.run() })
	defer func() { _ = recover() }() // close can race a final publisher
	select {
	case b.ch <- event:
	default:
		dropped := b.dropped.Add(1)
		b.record(ObserverDropEvent{Dropped: dropped})
		b.log.Warn("cron: observer queue full; event dropped",
			slog.Int64("dropped_total", dropped))
	}
}

// record hands event to the recorder, recovering a panic so a faulty metrics
// sink cannot take the scheduler down.
func (b *eventBus) record(event Event) {
	if b.recorder == nil {
		return
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			b.log.Error("cron: recorder panic recovered",
				slog.Any("event", event),
				slog.Any("panic", recovered))
		}
	}()
	b.recorder.Record(event)
}

// run delivers queued events to every observer, in order, until the channel
// is closed.
func (b *eventBus) run() {
	defer close(b.done)
	for event := range b.ch {
		for _, observer := range b.observers {
			b.notify(observer, event)
		}
	}
}

// notify delivers one event to one observer, recovering a panic so the other
// observers still receive it.
func (b *eventBus) notify(observer Observer, event Event) {
	defer func() {
		if recovered := recover(); recovered != nil {
			b.log.Error(
				"cron: observer panic recovered",
				slog.Any("observer", observer),
				slog.Any("event", event),
				slog.Any("panic", recovered),
			)
		}
	}()
	observer.Observe(event)
}

// close drains queued events, bounded by ctx. It is idempotent.
func (b *eventBus) close(ctx context.Context) error {
	if b == nil || b.ch == nil {
		return nil
	}
	b.startOnce.Do(func() { go b.run() })
	b.closeOnce.Do(func() { close(b.ch) })
	select {
	case <-b.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
