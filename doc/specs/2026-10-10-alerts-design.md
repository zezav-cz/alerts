# alerts — v1 design

**Status:** Implemented in v1
**Date:** 2026-10-10

## Context

Zezav's two hosts — the server `z01` and the Raspberry Pi `rpi01` — need alerting and nothing else: no dashboards, no stored metrics, no history. `repo/bolt` currently ships Netdata for this, which carries a metrics store, a web dashboard, an upstream apt repository with a version pin across a dozen packages, and several hundred stock alarms tuned for generic servers that have to be silenced one by one. It also does not cover what actually went wrong on 2026-09-24, when nginx was removed by unattended-upgrades and every site was down for eight days without a failing unit to alarm on.

The ready-made checks come instead from `monitoring-plugins` (the Nagios plugin collection, packaged in Debian): small executables that print one line and exit 0/1/2/3 for OK/WARNING/CRITICAL/UNKNOWN. What is missing is the part around them — something that runs them on a schedule, decides when a result is worth an alert, and delivers it. `alerts` is that part.

## Goals

- Run checks that follow the plugin convention, on a per-check schedule.
- Turn results into alerts only when it matters: confirmed state changes, recoveries, periodic reminders.
- Deliver alerts through pluggable notifiers, routed by state and check. Telegram first; more channels later without changing the core.
- Notice its own failure: a dead or wedged daemon, and a notifier that cannot deliver.
- Be one statically linked binary with one YAML config, deployable by Puppet from a GitHub Release.

## Non-goals (v1)

- Metrics, history, graphs, any web interface.
- Silencing / maintenance windows.
- Reloading the config on SIGHUP — `systemctl restart` is enough because state survives on disk.
- Built-in Go checks. The `Checker` interface leaves room for them; v1 has only `exec`.
- Integration into `repo/bolt` (`monitoring::alerts`, removing Netdata, the infrastructure-specific check scripts). That is a separate, short design once a v1 release exists.

## Decisions taken

| Question | Decision |
|---|---|
| Where it lives | Its own repository, `github.com/zezav-cz/alerts`, cloned to `repo/alerts` |
| Language | Go, statically linked (`CGO_ENABLED=0`) |
| Own checks | Executables following the plugin convention; the infrastructure-specific ones live in `repo/bolt`, not here |
| Execution model | Long-running daemon with per-check intervals, state persisted to disk |
| Policy in v1 | State-change alerts, confirmation, recovery messages, reminders, routing |
| Notifiers | Built-in `telegram` plus a generic `exec` notifier |
| Config format | YAML, rendered by Puppet from Hiera with `to_yaml` |
| Distribution | Bare binaries for linux/amd64 and linux/arm64 on GitHub Releases, with `SHA256SUMS` |

## Architecture

```
cmd/alerts/          main and subcommands
internal/config/     YAML loading and validation
internal/check/      Checker interface, ExecChecker
internal/policy/     pure state machine: result -> alert events
internal/notify/     Notifier interface, telegram, exec, routing
internal/state/      persistent state (JSON file)
internal/daemon/     scheduling, delivery queues, heartbeat, watchdog
```

The only third-party dependency is a YAML library. Logging uses `log/slog` to stderr, which systemd forwards to the journal. The systemd notify protocol is a few lines written against `NOTIFY_SOCKET`, without a library.

### Data flow

```
scheduler ──run──► check (goroutine) ──result──► core loop ──alert──► notifier queues ──► notifiers
                                                  (policy,             (per notifier,
                                                   saves state)         FIFO, retry)
```

- The **core loop** is the only code that mutates state. It owns the schedule (next run time per check), launches a check in a goroutine when it is due, receives the result, runs it through the policy, persists the state, and enqueues any resulting alerts. A check is never run concurrently with itself.
- The **policy** is a pure function with no I/O and no clock of its own: `(previous check state, result, now, check settings) -> (new check state, alerts)`.
- Each **notifier** has a FIFO queue drained by its own worker, which retries the head of the queue until it is delivered. Queues are part of the persisted state.
- Time comes from an injectable clock, so tests drive the daemon without sleeping.

### Subcommands

All take `--config PATH` (default `/etc/alerts/config.yaml`). `alerts --version` prints the version embedded at build time.

| Command | Behaviour |
|---|---|
| `alerts daemon` | The service mode, run by systemd. `--debug` additionally logs every check result. |
| `alerts validate` | Static validation of the config only — no files read beyond the config, nothing executed. Used by Puppet as `validate_cmd`. Exit 0 if valid, 1 with the errors listed otherwise. |
| `alerts run <check>` | Runs one check once and prints its state, output and duration, plus what the policy would do given the current state file. Read-only: it never writes state or sends anything. |
| `alerts test-notify <notifier>` | Sends a test alert (kind `test`) through one notifier, directly, with no retries. Exit status reflects delivery. |

## Configuration

```yaml
host: z01                         # optional; defaults to the short hostname
state_file: /var/lib/alerts/state.json   # optional; this is the default

defaults:                         # every key optional; values shown are the defaults
  interval: 1m                    # time between runs while the state is settled
  retry_interval: 30s             # time between runs while a change is being confirmed
  timeout: 30s                    # per check run
  confirm: 3                      # consecutive differing results before the state changes
  repeat: 6h                      # reminder period while not OK; 0 disables

heartbeat:                        # optional
  url_file: /etc/alerts/heartbeat-url
  every: 5m
  fail_after: 15m                 # undelivered alert age that turns the ping into /fail

notifiers:
  - name: telegram
    type: telegram
    token_file: /etc/alerts/telegram-token
    chat_id: "-100123456"         # must be a string
  - name: ntfy-phone
    type: exec
    command: [/usr/local/libexec/alerts/notify-ntfy]
    env: { NTFY_TOPIC_FILE: /etc/alerts/ntfy-topic }
    timeout: 30s                  # optional, default 30s

routes:
  - notify: [telegram]            # no matcher: matches everything
  - states: [CRITICAL]
    notify: [ntfy-phone]

checks:
  - name: disk
    command: [/usr/lib/nagios/plugins/check_disk, -w, 15%, -c, 10%, -p, /]
  - name: cert-zezav
    type: exec                    # optional, exec is the only type in v1
    command: [/usr/lib/nagios/plugins/check_curl, -H, zezav.cz, -S, -C, "14,7"]
    interval: 1h                  # any key from `defaults` can be overridden per check
```

### Validation rules

Decoding is strict: an unknown key anywhere is an error. On top of that, `validate` rejects:

- Check and notifier names that are empty, duplicated, or not matching `^[a-z0-9][a-z0-9_-]*$`.
- A check with an empty `command`, or a `type` other than `exec`.
- A notifier with a `type` other than `telegram` or `exec`; a `telegram` notifier without `token_file` or `chat_id`; an `exec` notifier without `command`.
- A `chat_id` that is not a YAML string — an unquoted `-100123456` is rejected with a message to quote it.
- Durations that do not parse with Go's `time.ParseDuration`, or that are not positive (`repeat` may be `0`); `confirm` below 1.
- A config without any check — an empty or mistyped Hiera key must not install a config that silently monitors nothing.
- A route whose `notify` is empty or names an unknown notifier, whose `checks` names an unknown check, or whose `states` contains anything other than `WARNING`, `CRITICAL`, `UNKNOWN`.
- **Unrouted combinations:** every pair (check, state) for each check and each of `WARNING`, `CRITICAL`, `UNKNOWN` must match at least one route. This guarantees no alert can be routed nowhere.

`alerts daemon` runs the same validation and additionally fails at start-up if a `token_file` or `heartbeat.url_file` cannot be read, or if the state file cannot be written. Missing check commands are not fatal; they surface as UNKNOWN results, which alert.

## Checks

`ExecChecker` runs `command` directly (no shell) with a minimal environment (`PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin`, `LANG=C`), in its own process group.

| Outcome | State | Output |
|---|---|---|
| Exit 0 / 1 / 2 / 3 | OK / WARNING / CRITICAL / UNKNOWN | First non-blank line of stdout |
| Any other exit code | UNKNOWN | `exit status N: <first line>` |
| Command cannot be started | UNKNOWN | `cannot run <command>: <error>` |
| Timeout | UNKNOWN | `timed out after <timeout>`; the whole process group is killed |

The output line has any performance data (from the first `|` on) removed, is trimmed, and is cut to 500 characters. If stdout has no non-blank line, the first non-blank line of stderr is used instead; if neither has one, the output is `(no output)`.

## Policy

### Check state

Per check the state records: the confirmed state, when it was entered (`since`), the pending counter and the state of the latest differing result, the latest result's output, and when the last alert for the check was emitted.

### Evaluation of a result

1. If the result's state equals the confirmed state, the pending counter resets to 0.
2. Otherwise the counter increments. When it reaches `confirm`, the confirmed state becomes the result's state, `since` becomes now, the counter resets, and an alert is emitted: kind `recovery` if the new state is OK, kind `problem` otherwise (this covers OK→problem as well as WARNING↔CRITICAL↔UNKNOWN changes).
3. If the result agrees with the confirmed state (step 1), the confirmed state is not OK, `repeat` is not 0, and the last alert for the check is at least `repeat` old, a `reminder` alert is emitted. Reminders are therefore evaluated whenever a result arrives, so their precision is the check's interval, and none goes out while a recovery is being confirmed.

Confirmation applies in both directions, so a recovery needs `confirm` OK results just as a problem needs `confirm` non-OK ones; a check flapping on single results produces no alerts. While the counter is above 0 the check is scheduled at `retry_interval` instead of `interval`. With `confirm: 1` every change alerts immediately.

### Start-up, added and removed checks

- State is loaded from `state_file` on start, so a restart re-sends nothing and forgets nothing.
- A check absent from the state (first start, or newly added) starts with confirmed state OK and `since` = now. An existing problem therefore alerts after confirmation; a healthy check sends nothing.
- State of checks no longer in the config is dropped silently.
- On start-up the first run of each check is spread uniformly over the first 10 seconds.

### Routing

An alert goes to the union of the notifiers of every matching route. A route matches when each matcher it has matches; an absent matcher matches anything. The state used for matching is the alert's state for `problem` and `reminder`, and the state recovered **from** for `recovery`, so the "OK again" reaches the same notifiers as the problem did.

## Alerts and messages

An alert carries:

| Field | Meaning |
|---|---|
| `kind` | `problem`, `recovery`, `reminder` or `test` |
| `host` | `host` from the config |
| `check` | Check name |
| `state` | New state (for `recovery`: `OK`) |
| `previous_state` | Confirmed state before the change; for `reminder` equal to `state` |
| `output` | Output line of the result that triggered the alert |
| `since` | When the current confirmed state began (RFC 3339) |
| `time` | When the alert was emitted (RFC 3339) |
| `text` | Ready-made human-readable message, below |

`text` is plain text, first line a header, second line the output:

```
🔴 z01 disk CRITICAL
DISK CRITICAL - free space: / 4%

🟡 rpi01 throttle WARNING
WARNING - under-voltage detected

⚪ z01 cert-zezav UNKNOWN
cannot run /usr/lib/nagios/plugins/check_curl: no such file or directory

✅ z01 disk OK (was CRITICAL for 2h13m)
DISK OK - free space: / 31%

🔁 z01 disk still CRITICAL (for 6h)
DISK CRITICAL - free space: / 3%

🧪 z01 test alert from alerts v1.0.0 via telegram
```

Durations in the text are rounded to the minute and printed in hours and minutes with zero components omitted: `2h13m`, `6h`, `45m`; anything under a minute prints as `<1m`.

## Notifiers

```go
type Notifier interface {
    Name() string
    Send(ctx context.Context, a Alert) error
}
```

### `telegram`

Reads the token from `token_file` (trimmed) at start-up and calls `POST https://api.telegram.org/bot<token>/sendMessage` with `chat_id`, `text` and link previews disabled; no `parse_mode`, so the text needs no escaping. A 2xx response with `"ok": true` is success. HTTP 429 is a failure whose next attempt waits at least the `retry_after` Telegram returns. Every other failure, including 4xx such as a wrong chat id, is retried like any transient error, so a misconfiguration surfaces through the heartbeat (below) rather than by silently dropping alerts. The token never appears in logs; errors are logged with the URL redacted.

### `exec`

Runs `command` directly with the minimal environment plus the notifier's `env` map, writes the alert as one JSON object (the fields above) to its stdin, and closes stdin. Exit 0 within `timeout` is success; anything else is a failure, logged with the first line of stderr.

### Delivery

- Each notifier has its own FIFO queue, persisted in the state file; order within a notifier is preserved, so a recovery never overtakes its problem.
- The worker retries the head of its queue with backoff starting at 10 s, doubling, capped at 10 min (or Telegram's `retry_after`, if longer, but never more than 1 h). A persisted retry time more than 1 h in the future means the wall clock stepped back since it was set — rpi01 has no RTC — and is treated as due.
- An alert still undelivered 24 h after it was enqueued is dropped and logged at error level.
- Each failure is logged at warning level; delivery after earlier failures is logged at info level.

## Self-monitoring

### Heartbeat

If `heartbeat` is configured, the core loop pings the URL read from `url_file` every `every` with an HTTP POST. If any notifier queue has had an alert undelivered for longer than `fail_after`, it posts to `<url>/fail` instead, with a short body naming the notifier and the age of its oldest alert. The format matches healthchecks.io's ping API; any service accepting a POST works for the plain ping. A failing heartbeat request is logged and otherwise ignored. Because the ping comes from the core loop, it covers a dead host, a dead or wedged daemon, and a notifier that cannot deliver.

### systemd

When `NOTIFY_SOCKET` is set the daemon sends `READY=1` once the config is loaded, the state is read and the schedule is built; `WATCHDOG=1` from the core loop every half of `WATCHDOG_USEC`; and `STOPPING=1` on shutdown. The intended unit uses `Type=notify` and `WatchdogSec=60`, so a wedged core loop gets the daemon restarted.

## State file

JSON, with a `version` field (1) for future migrations. Written after every change that matters (a confirmed-state change, an alert enqueued or delivered, a pending counter change) by writing a temporary file in the same directory, fsyncing it, and renaming it over the old one. Results that change nothing are not written, so a healthy system does not write to disk on every check — which matters on the Pi's SD card.

If the file is missing, the daemon starts empty. If it is unreadable or does not parse, the daemon logs an error, moves it aside as `state.json.corrupt`, and starts empty: the worst case is a repeated alert, never silence.

## Shutdown

On SIGTERM or SIGINT the daemon stops scheduling new runs, gives running checks and in-flight deliveries 10 s to finish, writes the state, sends `STOPPING=1`, and exits 0.

## Intended runtime environment

Documented here so the bolt design has something to follow; not part of this repository's code.

- System user `alerts`, not root; `SupplementaryGroups=video` on the Raspberry Pi so `vcgencmd` works. A check that ever needs root gets a narrow sudoers rule, not root for the daemon.
- Unit hardening: `ProtectSystem=strict`, `ProtectHome=yes`, `PrivateTmp=yes`, `NoNewPrivileges=yes`, `StateDirectory=alerts`.
- `/etc/alerts/config.yaml` 0644; secret files (`telegram-token`, `heartbeat-url`) 0640 `root:alerts`.

## Testing

| Unit | Approach |
|---|---|
| `policy` | Table tests over sequences of results and times: confirmation, escalation and de-escalation, recovery, reminders and their reset, flapping, `confirm: 1`, `repeat: 0`. |
| `config` | Valid and invalid documents: unknown keys, every validation rule above, unquoted `chat_id`, defaults and per-check overrides. |
| `check` | `ExecChecker` against scripts in `testdata/`: each exit code, an out-of-range code, a missing command, a timeout that also kills a grandchild process, performance data stripping, empty stdout falling back to stderr, truncation. |
| `notify` | `telegram` against `httptest.Server`: success, `"ok": false`, 429 with `retry_after`, 5xx, token redaction in errors. `exec` against a script that saves its stdin and environment; the JSON is compared field by field. Routing, including recovery routed by the previous state. |
| `state` | Round trip; missing file; corrupt file moved aside; a write interrupted before the rename leaves the old file intact. |
| `daemon` | Integration with the fake clock, `testdata/` scripts whose exit code a test can change, and a recording notifier: the exact sequence of alerts through a problem, a reminder, a recovery, a failing notifier that later succeeds, and a restart in the middle that neither repeats nor loses an alert. Heartbeat and `/fail` against `httptest.Server`. |

Everything runs under `go test -race ./...`.

## Repository and tooling

Following the Zezav conventions:

- `flake.nix` with the devshell (`go`, `golangci-lint`, `just`, `gh`) and git-hooks.nix hooks: `gofmt`, `govet`, `staticcheck`, `golangci-lint` (inline definition), `end-of-file-fixer`, `trim-trailing-whitespace`, `commitizen`; `flake.lock` committed; `.envrc` with `use flake`.
- `justfile` with `fmt`, `lint`, `test`, `build`, `ci`, and `release-build` calling `scripts/release-build`.
- `.editorconfig`, `.gitignore`, `.golangci.yml`.
- `README.md`; `doc/architecture.md`; `doc/development.md`; `doc/operations.md` holding the config reference, the alert JSON schema for `exec` notifiers, and the deployment notes above; `doc/decisions/001-separate-go-daemon-over-netdata.md`.
- Module path `github.com/zezav-cz/alerts`.

## Releases

1. A tag `vX.Y.Z` is pushed.
2. A GitHub Actions workflow runs `nix flake check`, then `nix develop -c just release-build`.
3. `scripts/release-build` builds `CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=vX.Y.Z"` for linux/amd64 and linux/arm64 as `alerts_linux_amd64` and `alerts_linux_arm64`, and writes `SHA256SUMS`.
4. The workflow creates the GitHub Release and uploads the three files.

`repo/bolt` then pins the version and the per-architecture checksum in Hiera. Creating the `zezav-cz/alerts` repository on GitHub is a separate step taken with Jan's confirmation.

## Success criteria for v1

- `just ci` passes, including the daemon integration scenario under `-race`.
- A tagged release produces both binaries and `SHA256SUMS`, and `alerts --version` reports the tag.
- With a config containing `check_disk`, `check_load`, `check_swap` and `check_curl`, `alerts validate` accepts it, `alerts run` reproduces the plugins' own output, and `alerts test-notify telegram` delivers a message to the real chat.
