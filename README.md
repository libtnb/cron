# cron

[![Doc](https://pkg.go.dev/badge/github.com/libtnb/cron)](https://pkg.go.dev/github.com/libtnb/cron)
[![Go](https://img.shields.io/github/go-mod/go-version/libtnb/cron)](https://go.dev/)
[![Release](https://img.shields.io/github/release/libtnb/cron.svg)](https://github.com/libtnb/cron/releases)
[![Test](https://github.com/libtnb/cron/actions/workflows/test.yml/badge.svg)](https://github.com/libtnb/cron/actions)
[![Report Card](https://goreportcard.com/badge/github.com/libtnb/cron)](https://goreportcard.com/report/github.com/libtnb/cron)
[![Stars](https://img.shields.io/github/stars/libtnb/cron?style=flat)](https://github.com/libtnb/cron)
[![License](https://img.shields.io/github/license/libtnb/cron)](https://opensource.org/license/MIT)

A modern, focused Go cron scheduler with no third-party dependencies.

## Install

```sh
go get github.com/libtnb/cron
```

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

| Path                               | Purpose                                                                         |
|------------------------------------|---------------------------------------------------------------------------------|
| `github.com/libtnb/cron`           | Scheduler, parser, schedules, hooks, recorders, retry policy.                   |
| `github.com/libtnb/cron/wrap`      | Job wrappers: `Recover`, `Timeout`, `SkipIfRunning`, `DelayIfRunning`, `Retry`. |
| `github.com/libtnb/cron/workflow`  | DAG jobs with `OnSuccess`, `OnFailure`, `OnSkipped`, `OnComplete`.              |
| `github.com/libtnb/cron/parserext` | Quartz tokens (`L`, `N#M`, `NL`).                                               |

## Specs

Five fields:

```text
minute hour day-of-month month day-of-week
```

Plus descriptors `@hourly`, `@daily`, `@every 10s`, and the `TZ=` /
`CRON_TZ=` prefixes.

`WithSeconds()` adds an optional leading seconds field. Both 5- and 6-field
specs parse; a 5-field spec means `second=0`. Pass `WithSeconds(true)` to
require six.

## Missed fires

If a firing runs late by more than `WithMissedTolerance` (default `1s`):

- `MissedSkip` (default) drops it and waits for the next scheduled time.
- `MissedRunOnce` runs the job once at the most recent missed time, then resumes.

```go
c := cron.New(
	cron.WithLocation(time.UTC),
	cron.WithParser(cron.NewStandardParser(cron.WithSeconds())),
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

For a programmatic schedule:

```go
id, err := c.AddSchedule(cron.ConstantDelay(time.Hour), job)
```

## Triggering and removal

```go
if err := c.Trigger(id); err != nil {
	switch {
	case errors.Is(err, cron.ErrEntryNotFound):
	case errors.Is(err, cron.ErrSchedulerNotRunning):
	case errors.Is(err, cron.ErrConcurrencyLimit):
	}
}

count, err := c.TriggerByName("daily-digest") // err joins per-entry failures

c.Remove(id) // false if id is unknown

_ = c.Stop(shutdownCtx)
```

`Remove` blocks future fires and future `Trigger` calls. Jobs already dispatched
keep running. `Stop` halts the loop and waits for in-flight jobs and the hook
dispatcher, capped by the context.

## Reading entries

`Entry` and `Entries` return copies and never block.

```go
if entry, ok := c.Entry(id); ok {
	fmt.Println(entry.Name, entry.Next)
}

for e := range c.Entries() {
	fmt.Println(e.Name, e.Prev, e.Next)
}
```

## Schedule helpers

These work on any `Schedule` without a running scheduler:

```go
next := cron.NextN(schedule, time.Now(), 10)
window := cron.Between(schedule, start, end)
```

## Hooks and recorders

Hook and recorder interfaces are split per event. A subscriber implements only
the methods it cares about:

```go
type metrics struct{}

func (*metrics) OnJobComplete(e cron.EventJobComplete) {
	// record duration, error, etc.
}

c := cron.New(cron.WithHooks(&metrics{}))
```

The four hook interfaces are `ScheduleHook`, `JobStartHook`, `JobCompleteHook`,
`MissedHook`. Recorder interfaces follow the same pattern (`JobScheduledRecorder`,
`JobStartedRecorder`, ...).

## Workflow

`workflow.Workflow` is a `cron.Job`, so a DAG schedules like anything else.
`workflow.New` returns an error (`ErrDuplicateStep` / `ErrUnknownDep` /
`ErrCycle`); `workflow.MustNew` panics on misconfiguration.

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

## Quartz tokens

`parserext.NewQuartzParser` accepts standard specs plus `L`, `N#M`, and `NL`.

```go
c := cron.New(cron.WithParser(parserext.NewQuartzParser(time.UTC)))

_, _ = c.Add("0 0 18 L * ?", reportJob)    // last day of every month
_, _ = c.Add("0 0 9 ? * 5#3", standupJob)  // third Friday
_, _ = c.Add("0 30 22 ? * 5L", payrollJob) // last Friday
```

## Migrating from robfig/cron

| robfig/cron                        | libtnb/cron                                                             |
|------------------------------------|-------------------------------------------------------------------------|
| `cron.New(cron.WithSeconds())`     | `cron.New(cron.WithParser(cron.NewStandardParser(cron.WithSeconds())))` |
| `Job.Run()`                        | `Job.Run(context.Context) error`                                        |
| `c.AddFunc(spec, func())`          | `c.Add(spec, cron.JobFunc(func(ctx) error { ... }))`                    |
| `cron.WithLogger(custom)`          | `cron.WithLogger(*slog.Logger)`                                         |
| `cron.Recover(logger)`             | `wrap.Recover(wrap.WithLogger(logger))`                                 |
| `cron.SkipIfStillRunning(logger)`  | `wrap.SkipIfRunning()`                                                  |
| `cron.DelayIfStillRunning(logger)` | `wrap.DelayIfRunning()`                                                 |
| `c.Start()`                        | `c.Start() error`                                                       |
| `c.Stop()`                         | `c.Stop(ctx) error`                                                     |
| `c.Entries()`                      | `c.Entries()` as `iter.Seq[Entry]`                                      |

## Credits

- [robfig/cron](https://github.com/robfig/cron)
