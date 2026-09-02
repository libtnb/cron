# cron

[![Go Version](https://img.shields.io/github/go-mod/go-version/libtnb/cron)](https://go.dev/) [![License](https://img.shields.io/github/license/libtnb/cron)](./LICENSE) [![Build Status](https://img.shields.io/github/actions/workflow/status/libtnb/cron/test.yml?branch=main)](https://github.com/libtnb/cron/actions) [![Go Report Card](https://goreportcard.com/badge/github.com/libtnb/cron)](https://goreportcard.com/report/github.com/libtnb/cron) [![Go Reference](https://pkg.go.dev/badge/github.com/libtnb/cron.svg)](https://pkg.go.dev/github.com/libtnb/cron)

A context-aware job scheduler for Go with explicit lifecycle control, missed-fire policies, typed workflows and optional distributed coordination. Requires Go 1.27.

```go
c := cron.MustNew(cron.WithLocation(time.UTC))

_, err := c.Add("0 9 * * MON-FRI", cron.JobFunc(func(ctx context.Context) error {
	return sendDigest(ctx)
}), cron.WithName("digest"))
if err != nil {
	log.Fatal(err)
}

if err := c.Start(); err != nil {
	log.Fatal(err)
}
defer func() {
	if err := c.Stop(context.Background()); err != nil {
		log.Printf("stop cron: %v", err)
	}
}()
```

## 🚀 Getting Started

```bash
go get github.com/libtnb/cron
```

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os/signal"
	"syscall"
	"time"

	"github.com/libtnb/cron"
)

func main() {
	c := cron.MustNew(cron.WithLocation(time.UTC))

	_, err := c.Add("@every 5s", cron.JobFunc(func(ctx context.Context) error {
		fmt.Println("tick", time.Now().Format(time.RFC3339))
		return nil
	}), cron.WithName("heartbeat"))
	if err != nil {
		log.Fatal(err)
	}
	if err := c.Start(); err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Stop(shutdownCtx); err != nil {
		log.Printf("stop: %v", err)
	}
}
```

Runnable programs for seconds fields, Quartz tokens, structured logging and workflows live in [`_examples`](_examples); executable `Example` functions are on [pkg.go.dev](https://pkg.go.dev/github.com/libtnb/cron#pkg-examples).

## ✨ Features

### Scheduler lifecycle

- `New` / `MustNew` build a scheduler from options; nothing fires until `Start`.
- `Stop(ctx)` cancels in-flight jobs (context cause `ErrCronStopping`) and waits for the loop, the jobs and the observer queue; `Drain(ctx)` stops scheduling and lets running jobs finish. Both are bounded by `ctx` and return `ctx.Err()` on timeout.
- A stopped scheduler cannot be restarted: `Start` returns `ErrSchedulerStopped`.
- `WithBaseContext` ties the run context to a parent; `WithMaxConcurrent` caps in-flight jobs (excess automatic fires are rejected, not queued); `WithMaxEntries` caps registrations.
- The loop goroutine never runs user code: `Schedule.Next`, coordination calls and jobs run on their own goroutines, so a slow schedule or job delays only its own entry.

### Entries and options

`Add` (cron spec) and `AddSchedule` (programmatic `Schedule`) return a stable `EntryID`. Entries can be inspected (`Entry`, `Entries`), paused, resumed, updated (`Update`, `UpdateSchedule`), removed and triggered (`Trigger`, `TriggerAndWait`, `TriggerByName`). `Remove`, `Pause` and `Resume` return `ErrEntryNotFound` for unknown IDs.

```go
id, err := c.Add("*/15 * * * *", job,
	cron.WithName("sync"),
	cron.WithTimeout(time.Minute),             // ErrJobTimeout as the cancel cause
	cron.WithEntryRetry(cron.Retry(3)),        // exponential backoff, 1s initial delay
	cron.WithEntryChain(wrap.SkipIfRunning()), // drop overlapping runs
)
```

Scheduler-wide defaults (`WithChain`, `WithRetry`, `WithJitter`, `WithMissedFire`, `WithClaimer`) can be overridden per entry (`WithEntryChain`, `WithEntryRetry`, `WithEntryJitter`, `WithEntryMissedFire`, `WithEntryClaimer`). Jobs read their identity with `EntryInfoFromContext(ctx)`. Panics are recovered into `ErrJobPanic` unless `WithoutRecover` is set.

### Schedules and parser

The built-in parser accepts five fields (`minute hour dom month dow`), names (`JAN`, `MON-FRI`), ranges and steps (`1-5`, `*/10`), descriptors (`@hourly`, `@daily`, `@weekly`, `@monthly`, `@yearly`, `@every 90s`) and a `TZ=` / `CRON_TZ=` prefix. `WithSecondsField` adds an optional leading seconds field. `NewStandardParser` exposes the same grammar standalone with `WithOptionalSeconds`, `WithRequiredSeconds`, `WithDefaultLocation` and `WithParserExt`. Day-of-month and day-of-week follow the classic rule: when either is `*`, both must match; otherwise either may.

Programmatic schedules: `ConstantDelay` (process-anchored interval), `AlignedDelay` (epoch-aligned, identical across replicas), `OnceAt`, `TriggeredSchedule` (manual only), `Union` and `Filter`. Any type implementing `Schedule` works; `Next` must return a time strictly after its argument and be safe for concurrent use.

Quartz day tokens (`L`, `L-n`, `LW`, `nW`, `N#M`, `NL`) live in `parserext`; numeric day-of-week stays cron-style `0-6`, Sunday first:

```go
c := cron.MustNew(cron.WithParser(parserext.NewQuartzParser(time.UTC)))
_, err := c.Add("0 0 22 ? * 5L", job) // 22:00 on the last Friday of each month
```

Introspection: `ValidateSpec`, `AnalyzeSpec` (descriptor, interval, location, next run), `NextN` and `Between`.

```go
s, _ := cron.NewStandardParser(cron.WithDefaultLocation(time.UTC)).Parse("0 9 * * MON-FRI")
for _, t := range cron.NextN(s, time.Now(), 3) {
	fmt.Println(t)
}
```

### Missed fires

A fire that runs later than `WithMissedTolerance` (default: one minute) invokes the entry's missed-fire policy and publishes a `MissedFireEvent`:

| Policy | Behaviour |
| --- | --- |
| `MissedRunOnce` (default) | runs the most recent missed instant once |
| `MissedRunAll` | replays every missed instant, newest 1000 at most |
| `MissedSkip` | drops the backlog and resumes from the next instant |

Catch-up bisects over `Schedule.Next`: `MissedRunOnce` costs about 60 `Next` calls however long the outage, and `MissedRunAll` walks only the newest window. Seed `WithLastRun` from persisted state to catch up across restarts:

```go
_, err := c.Add("0 * * * *", job,
	cron.WithLastRun(lastRunFromDB),
	cron.WithEntryMissedFire(cron.MissedRunAll),
)
```

### Distributed coordination

```go
c := cron.MustNew(cron.WithClaimer(claimer), cron.WithElector(elector))
_, err := c.Add("0 * * * *", job, cron.WithKey("hourly-report"))
```

- `Claimer.Claim` elects one replica per keyed fire. The key combines `WithKey` with the UTC scheduled instant, so replicas in different time zones agree.
- `Elector.IsLeader` restricts automatic fires to the current leader.
- `false, nil` is the normal contention or follower state; backend errors fail closed. Every suppressed fire publishes a `SkippedFireEvent` with a `SkipReason`.
- `WithKey` is the stable cross-replica identity and is required when a claimer applies; `WithName` is only a display label. Manual triggers bypass coordination.
- `ConstantDelay` is process-local; use `AlignedDelay` or a cron expression with a claimer.

Redis and PostgreSQL implementations are separate modules:

```bash
go get github.com/libtnb/cron/coordination/redis
go get github.com/libtnb/cron/coordination/postgres
```

### Events, observers and recorder

Every scheduler activity publishes a typed `Event`: `ScheduleEvent`, `JobStartEvent`, `JobCompleteEvent`, `MissedFireEvent`, `RejectedFireEvent`, `CanceledFireEvent`, `SkippedFireEvent`, `QueueDepthEvent` (after every heap change, including each committed fire) and `ObserverDropEvent`.

- `WithObservers` delivers events asynchronously, in order, through one bounded queue (`WithObserverBuffer`, default 1024). A full queue drops events instead of blocking the scheduler.
- `WithRecorder` receives every event inline on the publishing goroutine; it must be concurrency-safe and fast. Panics in observers and recorders are recovered and logged.

```go
c := cron.MustNew(cron.WithObservers(cron.ObserverFunc(func(ev cron.Event) {
	if done, ok := ev.(cron.JobCompleteEvent); ok && done.Err != nil {
		log.Printf("%s failed after %s: %v", done.Entry.Name, done.Duration, done.Err)
	}
})))
```

### Workflows

`workflow` builds typed DAGs that run as a single `cron.Job`. Go 1.27 generic methods keep the data flow between steps type-safe:

```go
b := workflow.New(workflow.WithMaxParallelism(8))

download := b.Step[[]byte]("download", func(ctx context.Context, _ workflow.Inputs) ([]byte, error) {
	return fetch(ctx)
})

b.Step[int]("store", func(ctx context.Context, in workflow.Inputs) (int, error) {
	data, ok := in.Get(download)
	if !ok {
		return 0, errors.New("download output unavailable")
	}
	return save(ctx, data)
}, workflow.After(download, workflow.OnSuccess))

wf, err := b.Build() // validates names, dependencies and cycles; freezes the builder
```

Dependencies use `OnSuccess`, `OnFailure`, `OnSkipped` or `OnComplete`; a step whose conditions are not met is skipped. Steps accept `WithTimeout` and `WithRetry`. `Execute` returns an `Execution` with per-step results, typed outputs (`Get`) and a joined error; at most 32 steps run concurrently by default.

### Wrappers

`wrap` supplies `cron.Wrapper` decorators: `Recover`, `Timeout`, `Retry`, `SkipIfRunning` and `DelayIfRunning`. Install them globally with `WithChain` or per entry with `WithEntryChain`; `Chain` composes wrappers with the first outermost. Retry policies (`Retry(n, RetryInitial(...), RetryMaxDelay(...), RetryMultiplier(...), RetryJitterFrac(...))`) join every attempt's error and stop on context cancellation.

### Contrib

| Module | Purpose |
| --- | --- |
| `github.com/libtnb/cron/contrib/prometheus` | `cron.Recorder` exposing job counters, duration and lateness histograms, queue depth and dropped events |
| `github.com/libtnb/cron/contrib/otel` | `cron.Wrapper` tracing each invocation as an OpenTelemetry span |

### Packages

| Package | Purpose |
| --- | --- |
| `cron` | Scheduler, parser, lifecycle, events |
| `cron/workflow` | Typed bounded DAG executor |
| `cron/wrap` | Job wrappers |
| `cron/parserext` | Quartz day tokens |
| `cron/coordination/redis` | Redis claimer and elector (separate module) |
| `cron/coordination/postgres` | PostgreSQL claimer and elector (separate module) |
| `cron/contrib/prometheus` | Prometheus recorder (separate module) |
| `cron/contrib/otel` | OpenTelemetry wrapper (separate module) |

## 🤝 Contributing

Please read the [contributing guide](CONTRIBUTING.md) before submitting a PR.

## 📄 License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.
