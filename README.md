# cron

[![Go Reference](https://pkg.go.dev/badge/github.com/libtnb/cron.svg)](https://pkg.go.dev/github.com/libtnb/cron)
[![Test](https://github.com/libtnb/cron/actions/workflows/test.yml/badge.svg)](https://github.com/libtnb/cron/actions)

A context-aware Go scheduler with typed workflows, explicit lifecycle control,
missed-fire policies, and optional distributed coordination. Requires Go 1.27.

```sh
go get github.com/libtnb/cron
```

## Quick start

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

The built-in parser uses five fields and accepts descriptors such as `@hourly`,
`@every 10s`, and `CRON_TZ=Asia/Taipei`. `WithSecondsField` enables an optional
leading seconds field. Programmatic schedules include `OnceAt`, `ConstantDelay`,
`AlignedDelay`, `TriggeredSchedule`, `Union`, and `Filter`.

Entries have stable `EntryID` values and may be paused, resumed, updated,
triggered, inspected, or removed. `Stop` cancels running jobs; `Drain` lets them
finish. Both are bounded by the supplied context. Missed fires default to
`MissedSkip`; `MissedRunOnce` and `MissedRunAll` enable catch-up.

## Typed workflows

Go 1.27 generic methods keep workflow data flow type-safe:

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

wf := b.MustBuild() // Build freezes the graph.
```

The executor is bounded (32 concurrent steps by default). Dependencies may use
`OnSuccess`, `OnFailure`, `OnSkipped`, or `OnComplete`; `Execute` returns typed
outputs and per-step results.

## Distributed coordination

`Claimer.Claim` elects one replica for each keyed fire. `Elector.IsLeader`
restricts automatic fires to the current leader. `false, nil` is normal
contention/follower state; backend errors fail closed. Manual triggers bypass
coordination.

```go
c := cron.MustNew(cron.WithClaimer(claimer))
_, err := c.Add("0 * * * *", job, cron.WithKey("hourly-report"))
```

`WithKey` is the stable cross-replica identity; `WithName` is only a display
label. Redis and PostgreSQL implementations are separate modules:

```sh
go get github.com/libtnb/cron/coordination/redis
go get github.com/libtnb/cron/coordination/postgres
```

## Events and metrics

`WithObservers` delivers typed `Event` values asynchronously through a bounded,
lossy queue. `WithRecorder` receives the same events inline and is intended for
fast, concurrency-safe metrics recorders. Prometheus support lives in
`contrib/prometheus`; OpenTelemetry job tracing lives in `contrib/otel`.

## Packages

| Package | Purpose |
| --- | --- |
| `cron` | Scheduler, parser, lifecycle, events |
| `cron/workflow` | Typed bounded DAG executor |
| `cron/wrap` | Job wrappers |
| `cron/parserext` | Quartz day tokens |
| `cron/coordination/redis` | Redis claimer and elector |
| `cron/coordination/postgres` | PostgreSQL claimer and elector |
| `cron/contrib/prometheus` | Prometheus recorder |
| `cron/contrib/otel` | OpenTelemetry wrapper |

API details and examples are on [pkg.go.dev](https://pkg.go.dev/github.com/libtnb/cron).
