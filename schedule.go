package cron

import (
	"iter"
	"time"
)

// Schedule yields successive firing times. Next must return the first firing
// strictly after now, or zero when exhausted.
type Schedule interface {
	Next(now time.Time) time.Time
}

// Upcoming is an optional lazy iteration capability.
type Upcoming interface {
	Upcoming(from time.Time) iter.Seq[time.Time]
}

// Parser turns a textual spec into a Schedule. Cron caches parser results.
type Parser interface {
	Parse(spec string) (Schedule, error)
}

// ConstantDelay is a fixed-interval Schedule.
type ConstantDelay time.Duration

func (d ConstantDelay) Next(now time.Time) time.Time {
	delay := max(time.Duration(d), time.Second)
	delay -= time.Duration(now.Nanosecond()) * time.Nanosecond
	return now.Add(delay)
}

func (d ConstantDelay) String() string {
	return "@every " + time.Duration(d).String()
}

type triggeredSchedule struct{}

// TriggeredSchedule never fires automatically. Combine with Trigger.
func TriggeredSchedule() Schedule { return triggeredSchedule{} }

func (triggeredSchedule) Next(time.Time) time.Time { return time.Time{} }
func (triggeredSchedule) String() string           { return "@triggered" }

// IsTriggered reports whether s came from TriggeredSchedule.
func IsTriggered(s Schedule) bool {
	_, ok := s.(triggeredSchedule)
	return ok
}
