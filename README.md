# alerts

## What this is

`alerts` is a small daemon that runs checks following the monitoring-plugins convention (one line of output, exit code 0/1/2/3 for OK/WARNING/CRITICAL/UNKNOWN) on a schedule, decides when a result is worth an alert — confirmed state changes, recoveries, reminders — and delivers it through pluggable notifiers, Telegram first. No metrics, no history, no dashboard. It ships as one statically linked binary and one YAML config, and is deployed to Zezav's hosts by `repo/bolt`.

## Prerequisites

- Nix with flakes enabled
- direnv

## Quick start

```sh
git clone git@github.com:zezav-cz/alerts.git
cd alerts
direnv allow
just build
./bin/alerts --version
```

## Available tasks

| Recipe | What it does |
|---|---|
| `just` | List every recipe |
| `just fmt` | Repair formatting across the working tree (pre-commit hooks) |
| `just lint` | Verify without modifying anything (`nix flake check`) |
| `just test` | Run the test suite with the race detector |
| `just build` | Build a static binary for this machine into `bin/` |
| `just ci` | `lint` + `test` |
| `just release-build vX.Y.Z` | Build the release binaries and `SHA256SUMS` into `dist/` |

## Project structure

```
cmd/alerts/        the binary: subcommands daemon, validate, run, test-notify
internal/check/    check states, the Checker interface, the exec checker
internal/proc/     running external commands with a timeout and process-group kill
internal/alert/    the alert event and its message text
internal/config/   YAML loading and validation
internal/policy/   result -> alert state machine (pure)
internal/state/    the persisted state file
internal/notify/   notifiers (telegram, exec) and routing
internal/sdnotify/ systemd notify protocol
internal/daemon/   the core loop, delivery queues, heartbeat
scripts/           release build
doc/               architecture, development, operations, decisions, specs, plans
vendor/            the vendored YAML library (the sandboxed hooks build offline)
```
