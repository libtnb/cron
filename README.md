# cron

A modern, focused Go cron scheduler with no third-party dependencies.

## Install

```sh
go get github.com/libtnb/cron
```

## Quick Start

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
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	c := cron.New(
		cron.WithLogger(slog.Default()),
		cron.WithChain(wrap.Recover(), wrap.Timeout(30*time.Second)),
	)
	_, _ = c.Add("@every 5s", cron.JobFunc(func(ctx context.Context) error {
		fmt.Println("tick", time.Now())
		return nil
	}), cron.WithName("heartbeat"))

	if err := c.Start(); err != nil {
		panic(err)
	}
	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = c.Stop(shutdownCtx)
}
```

## Packages

| Path                               | Purpose                                                                             |
|------------------------------------|-------------------------------------------------------------------------------------|
| `github.com/libtnb/cron`           | Scheduler, parser, schedules, hooks, recorders, retry policy.                       |
| `github.com/libtnb/cron/wrap`      | Job wrappers: `Recover`, `Timeout`, `SkipIfRunning`, `DelayIfRunning`, `Retry`.     |
| `github.com/libtnb/cron/workflow`  | DAG jobs with `OnSuccess`, `OnFailure`, `OnSkipped`, and `OnComplete` dependencies. |
| `github.com/libtnb/cron/parserext` | Optional Quartz-style parser for `L`, `N#M`, and `NL` expressions.                  |

## Core API

The default parser accepts five fields:

```text
minute hour day-of-month month day-of-week
```

Descriptors such as `@hourly`, `@daily`, `@every 10s`, `TZ=...`, and
`CRON_TZ=...` are supported. Use `WithSeconds` for a leading seconds field.

`WithMissedFire` controls behaviour when a firing is later than
`WithMissedTolerance` (default 1s). `MissedSkip` (the default) drops the
overdue firing and waits for the next scheduled time. `MissedRunOnce`
runs the job once for the most recent missed firing, then resumes
normally — useful for "catch up after restart" semantics.

```go
c := cron.New(
	cron.WithLocation(time.UTC),
	cron.WithStandardParser(cron.WithSeconds()),
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

Schedules can also be registered directly:

```go
id, err := c.AddSchedule(cron.ConstantDelay(time.Hour), job)
```

Runtime control is explicit. `Trigger` returns `ErrEntryNotFound`,
`ErrSchedulerNotRunning`, or `ErrConcurrencyLimit` so the caller can
distinguish failure modes:

```go
if err := c.Start(); err != nil {
	panic(err)
}

if err := c.Trigger(id); err != nil {
	switch {
	case errors.Is(err, cron.ErrEntryNotFound):
	case errors.Is(err, cron.ErrSchedulerNotRunning):
	case errors.Is(err, cron.ErrConcurrencyLimit):
	}
}

count, err := c.TriggerByName("daily-digest") // err joins per-entry failures

c.Remove(id) // returns bool: false if id is unknown

_ = c.Stop(shutdownCtx)
```

`Remove` prevents future automatic fires and future manual triggers for that
entry. Jobs already dispatched continue running. `Stop` stops scheduling and
waits for in-flight jobs and hooks until its context is done.

Read APIs return copied views:

```go
if entry, ok := c.Entry(id); ok {
	fmt.Println(entry.Name, entry.Next)
}

for e := range c.Entries() {
	fmt.Println(e.Name, e.Prev, e.Next)
}
```

Schedule helpers are available without running a scheduler:

```go
next := cron.NextN(schedule, time.Now(), 10)
window := cron.Between(schedule, start, end)
matched := cron.Matches(schedule, t)
```

Hooks and recorders are split into small sub-interfaces
(`ScheduleHook`, `JobStartHook`, `JobCompleteHook`, `MissedHook` for
hooks; `JobScheduledRecorder`, `JobStartedRecorder`, ...
for recorders). The dispatcher type-asserts each subscriber and calls
only the methods it actually implements — no need for empty stubs:

```go
type metrics struct{}

// Implements JobCompleteHook only — the other 3 events are skipped.
func (*metrics) OnJobComplete(e cron.EventJobComplete) {
	// record duration, error, etc.
}

c := cron.New(cron.WithHooks(&metrics{}))
```

## Workflow

`workflow.Workflow` is a `cron.Job`, so a DAG can be scheduled like any other
job. Use `New` for config-driven graphs (returns `error` you can inspect with
`errors.Is` against `ErrDuplicateStep` / `ErrUnknownDep` / `ErrCycle`), or
`MustNew` for static graphs where a misconfiguration is a programmer error.

```go
w := workflow.MustNew(
	workflow.NewStep("download", downloadJob),
	workflow.NewStep("transform", transformJob,
		workflow.After("download", workflow.OnSuccess)),
	workflow.NewStep("notify_failure", notifyJob,
		workflow.After("transform", workflow.OnFailure)),
)
_, _ = c.Add("@hourly", w, cron.WithName("etl"))
```

## Quartz Parser

The optional Quartz parser forwards ordinary specs to the standard parser and
adds support for last day, nth weekday, and last weekday forms.

```go
c := cron.New(cron.WithParser(parserext.NewQuartzParser(time.UTC)))

_, _ = c.Add("0 0 18 L * ?", reportJob)    // last day of every month
_, _ = c.Add("0 0 9 ? * 5#3", standupJob)  // third Friday
_, _ = c.Add("0 30 22 ? * 5L", payrollJob) // last Friday
```

## Migrating from robfig/cron

| robfig/cron                        | libtnb/cron                                             |
|------------------------------------|---------------------------------------------------------|
| `cron.New(cron.WithSeconds())`     | `cron.New(cron.WithStandardParser(cron.WithSeconds()))` |
| `Job.Run()`                        | `Job.Run(context.Context) error`                        |
| `c.AddFunc(spec, func())`          | `c.Add(spec, cron.JobFunc(func(ctx) error { ... }))`    |
| `cron.WithLogger(custom)`          | `cron.WithLogger(*slog.Logger)`                         |
| `cron.Recover(logger)`             | `wrap.Recover(wrap.WithLogger(logger))`                 |
| `cron.SkipIfStillRunning(logger)`  | `wrap.SkipIfRunning()`                                  |
| `cron.DelayIfStillRunning(logger)` | `wrap.DelayIfRunning()`                                 |
| `c.Start()`                        | `c.Start() error`                                       |
| `c.Stop()`                         | `c.Stop(ctx) error`                                     |
| `c.Entries()`                      | `c.Entries()` as `iter.Seq[Entry]`                      |
| Panic-oriented registration        | Explicit `error` returns                                |

## Credits

- [robfig/cron](https://github.com/robfig/cron)
