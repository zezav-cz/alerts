# Operations

## Installing

`repo/bolt` installs `alerts` on each host; this is what it has to provide.

- The binary from the GitHub Release matching the host (`alerts_linux_amd64` or `alerts_linux_arm64`), verified against `SHA256SUMS`, at `/usr/local/bin/alerts`.
- A system user and group `alerts`. On the Raspberry Pi add it to the `video` group, so `vcgencmd` works for the throttling check. A check that ever needs root gets a narrow sudoers rule, not root for the daemon.
- `/etc/alerts/config.yaml`, mode 0644, written with `validate_cmd => '/usr/local/bin/alerts --config % validate'`.
- Secret files, mode 0640 `root:alerts`: `/etc/alerts/telegram-token` (the bot token alone) and `/etc/alerts/heartbeat-url` (the healthchecks.io ping URL alone).
- The unit below, and the service enabled and running; restart it when the binary or the config changes.

```ini
[Unit]
Description=alerts: check runner and alert delivery
Wants=network-online.target
After=network-online.target

[Service]
Type=notify
ExecStart=/usr/local/bin/alerts --config /etc/alerts/config.yaml daemon
Restart=always
RestartSec=10
WatchdogSec=60
User=alerts
Group=alerts
StateDirectory=alerts
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
NoNewPrivileges=yes

[Install]
WantedBy=multi-user.target
```

## Configuration reference

```yaml
host: z01                     # optional; default: the short hostname
state_file: /var/lib/alerts/state.json   # optional; this is the default

defaults:                     # all optional; these are the built-in values
  interval: 1m                # between runs while the state is settled
  retry_interval: 30s         # between runs while a change is being confirmed
  timeout: 30s                # per run; then the process group is killed, UNKNOWN
  confirm: 3                  # consecutive differing results before the state changes
  repeat: 6h                  # reminder period while not OK; 0 disables

heartbeat:                    # optional dead man's switch
  url_file: /etc/alerts/heartbeat-url
  every: 5m                   # default 5m
  fail_after: 15m             # default 15m; an alert undelivered this long turns the ping into /fail

notifiers:
  - name: telegram            # [a-z0-9][a-z0-9_-]*, unique
    type: telegram
    token_file: /etc/alerts/telegram-token
    chat_id: "-100123456"     # must be quoted
    timeout: 30s              # optional, default 30s
  - name: ntfy
    type: exec
    command: [/usr/local/libexec/alerts/notify-ntfy]
    env: {NTFY_TOPIC_FILE: /etc/alerts/ntfy-topic}   # optional

routes:                       # an alert goes to every matching route's notifiers
  - notify: [telegram]        # no matchers: matches everything
  - states: [CRITICAL]        # WARNING, CRITICAL, UNKNOWN
    checks: [disk]            # check names
    notify: [ntfy]

checks:
  - name: disk                # [a-z0-9][a-z0-9_-]*, unique
    command: [/usr/lib/nagios/plugins/check_disk, -w, 15%, -c, 10%, -p, /]
    interval: 5m              # any key from defaults can be overridden here
```

Durations are Go durations (`30s`, `5m`, `6h`). Every check must be routable in each of WARNING, CRITICAL and UNKNOWN, or the config is rejected. Commands run without a shell, with `PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin` and `LANG=C` only.

## Writing a check

Any executable works if it prints one line and exits 0 (OK), 1 (WARNING), 2 (CRITICAL) or 3 (UNKNOWN). Text after the first `|` is performance data and is dropped. Another exit code, a crash or a timeout is UNKNOWN, which alerts too. Try it as the daemon would run it:

```sh
sudo -u alerts env -i PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin LANG=C /path/to/check; echo $?
sudo -u alerts alerts run <check-name>
```

## Exec notifier contract

The command gets the alert as one JSON object on stdin, then stdin is closed. Exit 0 within `timeout` means delivered; anything else is retried with backoff (10 s doubling to 10 min) for up to 24 h. The first line of stderr is logged.

| Field | Meaning |
|---|---|
| `kind` | `problem`, `recovery`, `reminder` or `test` |
| `host` | `host` from the config |
| `check` | Check name (`test` for a test alert) |
| `state` | New state; `OK` for a recovery |
| `previous_state` | Confirmed state before the change; equal to `state` for a reminder |
| `output` | Output line of the result that triggered the alert |
| `since` | When the current state began (RFC 3339) |
| `time` | When the alert was emitted (RFC 3339) |
| `text` | Ready-made message, e.g. `🔴 z01 disk CRITICAL\nDISK CRITICAL - free space: / 4%` |

A notifier that only wants to send something sends `text`.

## Runbook

**Is it running?** `systemctl status alerts`; logs with `journalctl -u alerts`. Every alert is logged at info level (`msg=alert kind=problem check=disk ...`), every failed delivery at warning.

**See every check result.** Add `--debug` to `ExecStart` (a drop-in), or run one check by hand with `sudo -u alerts alerts --config /etc/alerts/config.yaml run <check>` — it prints the result and what the policy would do, and changes nothing.

**Does Telegram work?** `sudo -u alerts alerts --config /etc/alerts/config.yaml test-notify telegram`. It sends once, without retries, and exits non-zero with the error if delivery failed.

**healthchecks.io says the check is down.** The host, the daemon or its network is gone; `systemctl status alerts` on the host. If it reports a failure (`/fail`) instead, a notifier has been failing for over 15 minutes: the ping body names it, and `journalctl -u alerts | grep 'delivery failed'` shows why. Undelivered alerts stay queued and go out once the channel works again.

**`state file unusable` in the log.** The state was unreadable or from an unsupported version; it was moved to `/var/lib/alerts/state.json.corrupt` and the daemon started fresh. Existing problems alert again once confirmed. Inspect or delete the `.corrupt` file.

**Silencing a check for maintenance.** Not supported in v1: remove the check from Hiera and apply, or let it alert.

## Releasing a new version

Tag `vX.Y.Z` on `main` and push the tag; the release workflow does the rest. Then update the version and both checksums in `repo/bolt`'s Hiera and apply.
