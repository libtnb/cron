# cron

[![Doc](https://pkg.go.dev/badge/github.com/libtnb/cron)](https://pkg.go.dev/github.com/libtnb/cron)
[![Go](https://img.shields.io/github/go-mod/go-version/libtnb/cron)](https://go.dev/)
[![Release](https://img.shields.io/github/release/libtnb/cron.svg)](https://github.com/libtnb/cron/releases)
[![Test](https://github.com/libtnb/cron/actions/workflows/test.yml/badge.svg)](https://github.com/libtnb/cron/actions)
[![Report Card](https://goreportcard.com/badge/github.com/libtnb/cron)](https://goreportcard.com/report/github.com/libtnb/cron)
[![Stars](https://img.shields.io/github/stars/libtnb/cron?style=flat)](https://github.com/libtnb/cron)
[![License](https://img.shields.io/github/license/libtnb/cron)](https://opensource.org/license/MIT)

A modern, focused Go cron scheduler with no third-party dependencies.

## Features

- Standard 5-field cron expressions plus `@hourly` / `@daily` / `@every 10s`
  descriptors, a per-spec `TZ=` prefix, and POSIX `7` as Sunday.
- Optional seconds field via `WithSecondsField()` (or a custom parser).
- Quartz tokens (`L`, `L-n`, `LW`, `nW`, `N#M`, `NL`, names like `FRI#3`) via
  the `parserext` subpackage.
- Schedule combinators: `OnceAt`, `Union`, and `Filter` (calendar exclusions).
- DAG jobs with conditional dependencies, data flow between steps, and
  per-step timeout/retry via the `workflow` subpackage.
- Job wrappers in `wrap`: `Recover`, `Timeout`, `Retry`, `SkipIfRunning`,
  `DelayIfRunning`. Job panics are recovered by default (`ErrJobPanic`).
- Per-event hooks and recorders so you can plug in metrics and tracing.
- Missed-fire policies (`MissedSkip`, `MissedRunOnce`, `MissedRunAll`) with a
  configurable tolerance window, overridable per entry; `WithLastRun` seeds
  cross-restart catch-up.
- Lifecycle control: `Pause`/`Resume`, in-place `Update`, graceful `Drain` or
  cancelling `Stop`.
- Manual `Trigger`, `TriggerAndWait`, and `TriggerByName`, with concurrency
  and entry limits.
- DST-correct scheduling (differentially fuzzed against a wall-clock scan).
  Per-entry timeout, jitter, retry, missed-fire policy, name, and chain.

## Install

```sh
go get github.com/libtnb/cron
```

Requires Go 1.25+ (uses `iter.Seq`, `sync.WaitGroup.Go`, and `slog`).

## Quick start

```go
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os/signal"
	"syscall"
	"time"

	"github.com/libtnb/cron"
	"github.com/libtnb/cron/wrap"
)

func main() {
	// Cancel on SIGINT / SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Build a scheduler. Wrappers in WithChain apply to every entry.
	c := cron.New(
		cron.WithLogger(slog.Default()),
		cron.WithChain(wrap.Recover(), wrap.Timeout(30*time.Second)),
	)

	// Add a job. Add returns an EntryID and a parse error (if any).
	_, err := c.Add("@every 5s", cron.JobFunc(func(ctx context.Context) error {
		fmt.Println("tick", time.Now())
		return nil
	}), cron.WithName("heartbeat"))
	if err != nil {
		panic(err)
	}

	// Start the loop. Idempotent while running.
	if err := c.Start(); err != nil {
		panic(err)
	}

	<-ctx.Done()

	// Drain in-flight jobs and hooks. The deadline caps the wait.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = c.Stop(shutdownCtx)
}
```

## Subpackages

| Path                               | Purpose                                                                         |
|------------------------------------|---------------------------------------------------------------------------------|
| `github.com/libtnb/cron`           | Scheduler, parser, schedules, hooks, recorders, retry policy.                   |
| `github.com/libtnb/cron/wrap`      | Job wrappers: `Recover`, `Timeout`, `SkipIfRunning`, `DelayIfRunning`, `Retry`. |
| `github.com/libtnb/cron/workflow`  | DAG jobs with `OnSuccess`, `OnFailure`, `OnSkipped`, `OnComplete`.              |
| `github.com/libtnb/cron/parserext` | Quartz tokens (`L`, `N#M`, `NL`).                                               |

## Cron expressions

The default parser takes five fields:

```text
minute hour day-of-month month day-of-week
```

Names are accepted (`mon`, `MON`, `mon-fri`, `jan`). Step (`*/5`), range
(`1-5`), list (`15,45`), and combinations are supported. A spec may carry a
`TZ=Europe/Berlin` or `CRON_TZ=...` prefix to override the scheduler's
timezone for that entry.

The descriptors `@yearly`, `@monthly`, `@weekly`, `@daily`, `@midnight`,
`@hourly`, and `@every <duration>` are also accepted. `@every 90s` is the
canonical fixed-interval form; the interval has a 1s floor.

To use seconds, enable it on the built-in parser (keeps `WithLocation` working):

```go
cron.WithSecondsField()
```

Or install a custom parser, which then owns the timezone:

```go
// Optional seconds: 5- and 6-field specs both parse.
cron.WithParser(cron.NewStandardParser(cron.WithSeconds()))

// Strict: 6 fields required.
cron.WithParser(cron.NewStandardParser(cron.WithSeconds(true)))
```

## Building a scheduler

```go
c := cron.New(
	cron.WithLocation(time.UTC),
	cron.WithSecondsField(), // the spec below has a seconds field
	cron.WithMissedFire(cron.MissedRunOnce),
	cron.WithMaxConcurrent(32),
	cron.WithRetry(cron.Retry(3, cron.RetryInitial(time.Second))),
)

id, err := c.Add(
	"0 0 9 * * *",
	emailJob,
	cron.WithName("daily-digest"),
	cron.WithTimeout(time.Minute),
)
```

`AddSchedule` registers a programmatic `Schedule` instead of a string:

```go
id, err := c.AddSchedule(cron.ConstantDelay(time.Hour), job)
```

Built-in schedules compose:

```go
c.AddSchedule(cron.OnceAt(deployTime), job)          // fire exactly once
c.AddSchedule(cron.Union(weekdayNine, weekendTen), job)
c.AddSchedule(cron.Filter(daily, notHoliday), job)   // skip filtered firings
```

Entries can be paused, resumed, and updated in place — the ID and `Prev`
survive; `Update` re-parses the spec, `UpdateSchedule` swaps a programmatic
schedule:

```go
c.Pause(id)  // manual Trigger still works while paused
c.Resume(id) // reschedules from now
err = c.Update(id, "*/10 * * * *")
```

### Missed fires

When a firing runs more than `WithMissedTolerance` (default `1s`) late,
`WithMissedFire` decides what to do:

- `MissedSkip` (default) drops the missed firing and waits for the next
  scheduled time.
- `MissedRunOnce` runs the job once at the most recent missed time, then
  resumes the regular schedule.
- `MissedRunAll` runs the job once per missed firing (the newest 1000 are
  kept), then resumes.

The policy can be overridden per entry with `WithEntryMissedFire`, and jitter
with `WithEntryJitter`. By themselves the policies cover in-process stalls
(VM suspend, clock jumps, a backlog while the loop was blocked). To catch up
across restarts, persist the last run time and seed it back:

```go
c.Add("0 2 * * *", job,
	cron.WithLastRun(lastRunFromDB),             // first fire computes from here
	cron.WithEntryMissedFire(cron.MissedRunOnce)) // -> the 02:00 you missed runs once
```

## Triggering and removal

`Trigger` runs the job immediately. The returned error tells the caller why
dispatch was rejected:

```go
if err := c.Trigger(id); err != nil {
	switch {
	case errors.Is(err, cron.ErrEntryNotFound):
	case errors.Is(err, cron.ErrSchedulerNotRunning):
	case errors.Is(err, cron.ErrConcurrencyLimit):
	}
}

err = c.TriggerAndWait(ctx, id) // blocks and returns the job's error

count, err := c.TriggerByName("daily-digest") // err joins per-entry failures

c.Remove(id) // false if id is unknown
```

`Remove` blocks future automatic fires and future `Trigger` calls for that
entry. Jobs already dispatched keep running. Two shutdown modes:

- `Stop(ctx)` cancels in-flight jobs (`ErrCronStopping` as the cause) and
  waits for the loop, jobs, and hook dispatcher, capped by ctx.
- `Drain(ctx)` stops scheduling but lets in-flight jobs finish naturally.

Job panics are recovered into `ErrJobPanic`-wrapped errors by default, so one
bad job cannot crash the process; opt out with `WithoutRecover()`.

## Reading entries

`Entry` and `Entries` return copies and never block on the scheduler's
internal lock, so they are safe to call from a hot path (HTTP handler,
debug endpoint).

```go
if entry, ok := c.Entry(id); ok {
	fmt.Println(entry.Name, entry.Next)
}

for e := range c.Entries() {
	fmt.Println(e.Name, e.Prev, e.Next)
}
```

`NextN` and `Between` operate on a `Schedule` directly, without a running
scheduler:

```go
next := cron.NextN(schedule, time.Now(), 10)
window := cron.Between(schedule, start, end)
```

## Hooks and recorders

Hooks and recorders are split per event so a subscriber implements only the
methods it cares about:

- Hooks: `ScheduleHook`, `JobStartHook`, `JobCompleteHook`, `MissedHook`.
- Recorders: `JobScheduledRecorder`, `JobStartedRecorder`,
  `JobCompletedRecorder`, `JobMissedRecorder`, `QueueDepthRecorder`,
  `HookDroppedRecorder`.

```go
type metrics struct{}

// Implements JobCompleteHook only; the other 3 events are skipped automatically.
func (*metrics) OnJobComplete(e cron.EventJobComplete) {
	// record duration, error, etc.
}

c := cron.New(cron.WithHooks(&metrics{}))
```

Hooks are delivered on a buffered channel and dropped when the buffer is
full. The size is configurable via `WithHookBuffer` and the drop count is
exposed through `HookDroppedRecorder`.

Recorders, unlike hooks, are not serialized: their methods are called inline
and concurrently from job goroutines, the scheduler loop, and
Add/Remove/Trigger callers. Implementations must be concurrency-safe and
non-blocking.

## Workflow DAGs

`workflow.Workflow` is a `cron.Job`, so a DAG can be scheduled with `Add`
or `AddSchedule` like any other job. `workflow.New` validates the graph
and returns an error (`ErrDuplicateStep`, `ErrUnknownDep`, `ErrCycle`);
`workflow.MustNew` panics on misconfiguration and is convenient for
static graphs.

```go
w := workflow.MustNew(
	workflow.NewStep("download", downloadJob).
		WithTimeout(2*time.Minute).
		WithRetry(cron.Retry(3, cron.RetryInitial(time.Second))),
	workflow.NewStep("transform", transformJob,
		workflow.After("download", workflow.OnSuccess)),
	workflow.NewStep("notify_failure", notifyJob,
		workflow.After("transform", workflow.OnFailure)),
)
_, _ = c.Add("@hourly", w, cron.WithName("etl"))
```

Conditions: `OnSuccess`, `OnFailure`, `OnSkipped`, `OnComplete` (any
terminal state). A step is skipped when one of its dependencies didn't
match the requested condition.

`NewStepFunc` steps pass data through the DAG — each receives its
dependencies' outputs and `Execution.Steps` reports outputs and timings:

```go
extract := workflow.NewStepFunc("extract", func(ctx context.Context, _ workflow.Inputs) (any, error) {
	return fetchRows(ctx)
})
load := workflow.NewStepFunc("load", func(ctx context.Context, in workflow.Inputs) (any, error) {
	return nil, store(ctx, in["extract"].([]Row))
}, workflow.After("extract", workflow.OnSuccess))

w := workflow.MustNew(extract, load).WithOnComplete(func(e *workflow.Execution) {
	log.Printf("run %s took %v", e.ID, e.Duration)
})
```

## Quartz tokens

`parserext.NewQuartzParser` accepts standard 5/6-field specs and adds the
Quartz day tokens:

| Token  | Field | Meaning                                        |
|--------|-------|------------------------------------------------|
| `L`    | dom   | last day of the month                          |
| `L-3`  | dom   | 3 days before the last day                     |
| `LW`   | dom   | last weekday of the month                      |
| `15W`  | dom   | weekday nearest the 15th (never crosses month) |
| `5#3`  | dow   | third Friday (`FRI#3` also works)              |
| `5L`   | dow   | last Friday (`FRIL` also works)                |

```go
c := cron.New(cron.WithParser(parserext.NewQuartzParser(time.UTC)))

_, _ = c.Add("0 0 18 L * ?", reportJob)      // last day of every month
_, _ = c.Add("0 0 9 ? * FRI#3", standupJob)  // third Friday
_, _ = c.Add("0 30 22 ? * FRIL", payrollJob) // last Friday
_, _ = c.Add("0 0 9 15W * ?", invoiceJob)    // weekday nearest the 15th
```

`?` is accepted in the day-of-month and day-of-week fields per the Quartz
convention. Numeric day-of-week stays cron-style 0-6 Sunday-first (not
Quartz's 1-7); the name forms are unambiguous.

## Migrating from robfig/cron

| robfig/cron                        | libtnb/cron                                                             |
|------------------------------------|-------------------------------------------------------------------------|
| `cron.New(cron.WithSeconds())`     | `cron.New(cron.WithSecondsField())`                                     |
| `Job.Run()`                        | `Job.Run(context.Context) error`                                        |
| `c.AddFunc(spec, func())`          | `c.Add(spec, cron.JobFunc(func(ctx) error { ... }))`                    |
| `cron.WithLogger(custom)`          | `cron.WithLogger(*slog.Logger)`                                         |
| `cron.Recover(logger)`             | `wrap.Recover(wrap.WithLogger(logger))`                                 |
| `cron.SkipIfStillRunning(logger)`  | `wrap.SkipIfRunning()`                                                  |
| `cron.DelayIfStillRunning(logger)` | `wrap.DelayIfRunning()`                                                 |
| `c.Start()`                        | `c.Start() error`                                                       |
| `c.Stop()`                         | `c.Stop(ctx) error`                                                     |
| `c.Entries()`                      | `c.Entries() iter.Seq[Entry]`                                           |

## Credits

- [robfig/cron](https://github.com/robfig/cron)

## License

See [LICENSE](LICENSE).
