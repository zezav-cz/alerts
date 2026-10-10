# 001: A separate Go daemon over Netdata for alerting

**Status:** Accepted
**Date:** 2026-10-10

## Context

Zezav's hosts — the server `z01` and the Raspberry Pi `rpi01` — need alerting to Telegram and nothing else. `repo/bolt` ships Netdata for it, which brings a metrics store, a dashboard, an upstream apt repository whose dozen packages have to be pinned together, and several hundred stock alarms tuned for generic servers that have to be silenced one by one. Its stock alarms also miss what actually went wrong on 2026-09-24: nginx was removed by unattended-upgrades and every site was down for eight days with no failing unit to alarm on.

The ready-made checks we want already exist in `monitoring-plugins` (Debian package): small executables printing one line and exiting 0–3. Monit was considered as the runner; it has no ready-made rules either, and its alerting and state handling are less flexible than routing alerts to several channels needs.

## Decision

Write `alerts`, a small Go daemon, in its own repository:

- checks are commands following the monitoring-plugins convention; Zezav-specific checks are scripts kept in `repo/bolt`;
- a long-running daemon with per-check intervals and its state persisted to disk, not a timer-started one-shot;
- a pure policy layer with confirmation, recoveries, reminders and routing;
- notifiers are built in (`telegram`) or any command (`exec`, alert JSON on stdin);
- a YAML config rendered by Puppet from Hiera;
- distributed as statically linked binaries on GitHub Releases, which Puppet downloads with a pinned checksum.

## Consequences

- One small binary per host, no runtime dependencies, nothing listening on the network, no metrics written anywhere.
- We own the code: the policy and delivery logic must be tested well, which the pure policy and the clock-driven daemon tests are designed for.
- The checks are only as good as the plugins and scripts configured; there is no long tail of stock alarms. That is intended — every alert that exists was chosen.
- Netdata comes out of `repo/bolt` once `alerts` is deployed, taking its apt repository and pins with it.
- The repository needs a GitHub remote and a release workflow, which also gives it CI.
