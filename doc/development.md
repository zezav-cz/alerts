# Development

## Setup

`direnv allow` once; the devshell then provides Go, golangci-lint, just and gh, and installs the git hooks (pre-commit, commit-msg with commitizen for Conventional Commits). Without direnv, prefix commands with `nix develop -c`.

## The gates

- `just test` runs `go test -race ./...`. The race detector needs a C toolchain, which is why the devshell is `mkShell` and not `mkShellNoCC`.
- `just lint` runs `nix flake check`, which runs every hook (gofmt, govet, staticcheck, golangci-lint, shellcheck, check-yaml, nixfmt, whitespace) in a sandbox over a copy of the tree. Nix only sees files git knows about: `git add` new files before `just lint`, or the check silently ignores them.
- `just ci` is both, and runs before every commit.

## Dependencies are vendored

The sandbox has no network, so Go modules cannot be downloaded there. The one third-party module (`go.yaml.in/yaml/v3`) lives in `vendor/`, which the hooks exclude. After changing `go.mod`:

```sh
go mod tidy
go mod vendor
git add go.mod go.sum vendor
```

Keep dependencies to the minimum; each one is code that ships in a monitoring daemon on every host.

## Tests

- Test helper scripts live in each package's `testdata/` and must be executable (`chmod +x`); git records the bit. They use `#!/bin/sh` and only POSIX tools, because they run with the minimal `PATH` that `internal/proc` gives every command.
- `internal/policy` is pure: test it with tables of results and times, never with sleeps.
- `internal/daemon` tests drive the core loop by hand: the harness in `daemon_test.go` owns the clock and consumes the result and outcome channels itself, so a whole scenario — problem, reminder, failed delivery, restart — runs deterministically in milliseconds. Use it for any new daemon behaviour.
- `internal/notify` tests talk to `httptest` servers; nothing in the suite reaches the internet.

## Trying it by hand

```sh
just build
cat > /tmp/alerts.yaml <<'EOF'
host: dev
state_file: /tmp/alerts-state.json
notifiers:
  - {name: log, type: exec, command: [/usr/bin/tee, /tmp/alerts-sent.json]}
routes:
  - notify: [log]
checks:
  - {name: true, command: [/usr/bin/true]}
EOF
./bin/alerts --config /tmp/alerts.yaml validate
./bin/alerts --config /tmp/alerts.yaml run true
./bin/alerts --config /tmp/alerts.yaml test-notify log
./bin/alerts --config /tmp/alerts.yaml --debug daemon
```

## Releasing

Push a tag `vX.Y.Z` on `main`. The release workflow re-runs both gates, builds with `just release-build`, and creates the GitHub Release with `alerts_linux_amd64`, `alerts_linux_arm64` and `SHA256SUMS`. Then bump the version and checksums in `repo/bolt`'s Hiera.
