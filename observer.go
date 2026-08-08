package cron

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
)

// Observer receives scheduler events asynchronously and in publication order.
// One slow observer can fill the shared queue; new events are then dropped.
type Observer interface {
	Observe(Event)
}

// ObserverFunc adapts a function to Observer.
type ObserverFunc func(Event)

func (f ObserverFunc) Observe(event Event) { f(event) }

// Recorder receives scheduler events inline. Record may be called concurrently
// and must be concurrency-safe and fast. A panic is recovered and logged.
type Recorder interface {
	Record(Event)
}

// RecorderFunc adapts a function to Recorder.
type RecorderFunc func(Event)

func (f RecorderFunc) Record(event Event) { f(event) }

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

func (b *eventBus) run() {
	defer close(b.done)
	for event := range b.ch {
		for _, observer := range b.observers {
			b.notify(observer, event)
		}
	}
}

func (b *eventBus) notify(observer Observer, event Event) {
	defer func() {
		if recovered := recover(); recovered != nil {
			b.log.Error("cron: observer panic recovered",
				slog.Any("observer", observer),
				slog.Any("event", event),
				slog.Any("panic", recovered))
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
