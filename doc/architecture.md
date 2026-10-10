# Architecture

`alerts` turns check results into delivered alerts. It has three layers, each behind a small interface, and one goroutine that owns all state.

```
checks ──► result ──► policy ──► alert ──► routing ──► notifier queues ──► notifiers
```

## Layers

**Checks** (`internal/check`) produce a `Result`: a `State` (OK, WARNING, CRITICAL, UNKNOWN — numbered like plugin exit codes), one line of output and a duration. `Checker` is the interface; `Exec` is the only implementation and runs any command that follows the monitoring-plugins convention, so the monitoring-plugins collection and Zezav's own scripts look identical. Built-in Go checks can be added later behind the same interface. `internal/proc` runs the command: no shell, a minimal environment, its own process group, and a timeout that kills the whole group.

**Policy** (`internal/policy`) is a pure function, `Evaluate(previous state, result, now, settings) -> (new state, alert or nil)`. A check's confirmed state changes only after `confirm` consecutive results that differ from it, in both directions, so a flapping check is quiet and a recovery is as trustworthy as a problem. A confirmed change into a non-OK state is a `problem` alert, back to OK a `recovery`; while a problem persists and results agree with it, a `reminder` goes out every `repeat`. Because nothing in the policy touches the clock or the disk, all of that is tested with plain tables.

**Notifiers** (`internal/notify`) deliver an `Alert`. `Telegram` posts the alert's ready-made text to the Bot API; `Exec` hands the alert as JSON on stdin to any command, which is how a new channel is added without a rebuild. `Route` picks the notifiers for an alert: the union of every matching route. A recovery is routed by the state it recovered from, so it reaches whoever got the problem, and a change between two problem states is routed by both, so a CRITICAL-only channel hears when CRITICAL drops to WARNING.

## The core loop

`internal/daemon` is the only stateful part. One goroutine runs `Run`, and only it reads or writes the state:

- a ticker (1 s) starts every check whose next run is due, each in its own goroutine, and never the same check twice at once;
- a finished check comes back on the `results` channel; the loop runs it through the policy, persists if something that matters changed, enqueues any alert, and schedules the next run — at `retry_interval` while a change is being confirmed, at `interval` otherwise;
- each notifier has a FIFO queue; the loop sends its head in a goroutine, and the outcome comes back on the `outcomes` channel. Success pops the head; failure schedules a retry with backoff (10 s doubling to 10 min, or Telegram's `retry_after`). Order within a notifier is preserved, so a recovery never overtakes its problem. Alerts older than 24 h are dropped with an error;
- the heartbeat pings an external dead man's switch every `every`, or `<url>/fail` when some notifier has had an alert undelivered for longer than `fail_after` — so a dead host, a wedged daemon and a broken channel are all reported from outside;
- under systemd, `READY=1` after start-up, `WATCHDOG=1` from the loop, `STOPPING=1` on shutdown, so a wedged loop is restarted.

Tests drive the loop's handlers directly with a fake clock instead of running `Run`, which is why `tick`, `handleResult` and `handleOutcome` are separate methods.

## State

`internal/state` keeps each check's policy state and every queue in one JSON file, written atomically (temporary file, fsync, rename) and only when something other than the output text changed, which spares the Raspberry Pi's SD card. A file that cannot be read is moved aside to `state.json.corrupt` and the daemon starts empty: the worst case is a repeated alert, never silence. Checks and notifiers no longer in the config are pruned at start-up.

## Configuration

`internal/config` decodes YAML strictly (unknown keys are errors), fills in defaults, and validates everything it can without touching the system, reporting all problems at once. It refuses any configuration in which some check's WARNING, CRITICAL or UNKNOWN alert would match no route. Puppet renders the file from Hiera and runs `alerts validate` on it before installing it; the daemon additionally checks at start-up that the token and heartbeat files it names are readable.
