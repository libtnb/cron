# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Release notes for v0.5.4 and earlier live on [GitHub Releases](https://github.com/libtnb/cron/releases).

## [Unreleased]

## [v0.6.0] - 2026-09-02

### Changed

- `Remove`, `Pause` and `Resume` return `error` (`ErrEntryNotFound` for an unknown ID) instead of `bool`.
- The default missed-fire policy is `MissedRunOnce`, which is now the zero value of `MissedFirePolicy`; the constant order changed to `MissedRunOnce`, `MissedSkip`, `MissedRunAll`.
- The default `WithMissedTolerance` is one minute.
- `Schedule.Next` runs on per-fire planner goroutines and must be safe for concurrent use.
- `ConstantDelay` keeps sub-second periods and never fires for non-positive intervals.
- The parser rejects `@every` intervals shorter than 1ms and an empty `TZ=` / `CRON_TZ=` zone.
- A schedule that returns a non-future time exhausts its entry with an error log instead of spinning the loop.
- `QueueDepthEvent` is published per committed fire.

### Fixed

- Fire keys passed to `Claimer` are zone-independent (UTC), so replicas on hosts with different `TZ` settings agree on the same fire.
- A slow `Schedule.Next` no longer delays or drops other entries' fires.
- Missed-fire catch-up after long outages selects the most recent instants (bisection over `Next`) instead of a stale prefix.

### Other

- `cron.go` was split into `entry.go`, `registry.go`, `lifecycle.go`, `dispatch.go`, `trigger.go` and `missed.go`.

[Unreleased]: https://github.com/libtnb/cron/compare/v0.6.0...HEAD
[v0.6.0]: https://github.com/libtnb/cron/compare/v0.5.4...v0.6.0
