// Package cron schedules context-aware jobs from cron expressions or
// programmatic [Schedule] implementations.
//
// # Lifecycle
//
// [New] builds a scheduler from options; [Cron.Add] and [Cron.AddSchedule]
// register jobs and return a stable [EntryID]; [Cron.Start] launches the
// loop. Entries can be added, removed, paused, resumed and updated before or
// after Start; [Cron.Trigger] needs a running scheduler. The loop goroutine
// only pops due entries: Schedule.Next, distributed coordination and jobs each
// run on their own goroutines, so one slow schedule or job never delays
// another entry.
//
// [Cron.Stop] cancels in-flight jobs (context cause [ErrCronStopping]) and
// waits for them; [Cron.Drain] stops scheduling and lets running jobs finish.
// Both are bounded by the supplied context. A stopped scheduler cannot be
// restarted.
//
// # Schedules
//
// The built-in parser accepts five-field specs (minute hour dom month dow),
// an optional leading seconds field (see [WithSecondsField]), descriptors
// such as "@hourly" and "@every 10s", and a "TZ=" or "CRON_TZ=" prefix.
// Programmatic schedules include [ConstantDelay], [AlignedDelay], [OnceAt],
// [TriggeredSchedule], [Union] and [Filter]. Quartz day tokens (L, W, #) live
// in the parserext subpackage.
//
// # Missed fires
//
// A fire that runs later than [WithMissedTolerance] (one minute by default)
// invokes the entry's [MissedFirePolicy]: [MissedRunOnce] (the default) runs
// the most recent missed instant once, [MissedRunAll] replays the backlog and
// [MissedSkip] drops it. Seed [WithLastRun] from persisted state to catch up
// across restarts.
//
// # Distributed coordination
//
// [WithClaimer] elects one replica per keyed fire and [WithElector] restricts
// automatic fires to the current leader. Fire keys combine [WithKey] with the
// UTC scheduled instant, so replicas in different time zones agree. Manual
// triggers bypass coordination. Redis and PostgreSQL backends are separate
// modules under coordination/.
//
// # Events
//
// [WithObservers] delivers [Event] values asynchronously through a bounded,
// lossy queue; [WithRecorder] receives the same events inline for metrics.
// Prometheus and OpenTelemetry integrations live under contrib/.
package cron
