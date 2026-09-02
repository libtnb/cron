# Contributing to cron

Thank you for your interest in contributing!

## Prerequisites

- Go 1.27 or later
- [golangci-lint](https://golangci-lint.run/) (CI runs the latest release)
- A PostgreSQL server, only for the `coordination/postgres` tests

## Quick Start

```bash
# Clone the repository
git clone https://github.com/libtnb/cron.git
cd cron

# Build
go build ./...

# Run unit tests with the race detector, as CI does
go test -race ./...

# Run linter
golangci-lint run ./...

# Formatting and vet checks that CI enforces
gofmt -l .
go vet ./...

# Short fuzz runs, as in CI
go test -run=^$ -fuzz=FuzzParser_StandardNeverPanics -fuzztime=30s .
go test -run=^$ -fuzz=FuzzSpecScheduleNext -fuzztime=30s .
```

### Submodules

`coordination/redis`, `coordination/postgres`, `contrib/prometheus` and `contrib/otel` are separate Go modules that pin a released `github.com/libtnb/cron` version. To test them against your working copy of the core package, create a temporary workspace as CI does (`go.work` is ignored by git):

```bash
go work init . coordination/redis coordination/postgres contrib/prometheus contrib/otel

(cd coordination/redis && go test -race ./...)   # starts an embedded miniredis
(cd contrib/prometheus && go test -race ./...)
(cd contrib/otel && go test -race ./...)

# The PostgreSQL tests need a reachable server
export CRON_PG_TEST_DSN='postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable'
(cd coordination/postgres && go test -race ./...)
```

## Development Workflow

1. Fork the repository
2. Create a feature branch: `git checkout -b feat/my-feature`
3. Make your changes
4. Add tests for new functionality
5. Run `go test -race ./...` and `golangci-lint run ./...`
6. Commit with a descriptive message
7. Push and open a Pull Request

## Code Guidelines

- Follow [Effective Go](https://go.dev/doc/effective_go)
- Add doc comments to all exported symbols
- Write table-driven tests that exercise behaviour, not configuration files
- Keep `gofmt` clean and `go vet` silent; CI fails on either
- Record user-visible API or default changes in `CHANGELOG.md` under `[Unreleased]`

## Reporting Issues

Use [GitHub Issues](https://github.com/libtnb/cron/issues). Include:

- Go version (`go version`)
- OS and architecture
- Steps to reproduce
- Expected vs actual behavior
