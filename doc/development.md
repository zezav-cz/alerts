# Development

## Setup

`direnv allow` once; the devshell then provides Go, golangci-lint, just and gh, and installs the git hooks (pre-commit, commit-msg with commitizen for Conventional Commits). Without direnv, prefix commands with `nix develop -c`.

## The gates

- `just test` runs `go test -race ./...` in the devshell. The race detector needs cgo and a C toolchain, which is why the devshell is `mkShell` and not `mkShellNoCC`.
- `just lint` runs `nix flake check`, which in the sandbox runs every hook (gofmt, govet, staticcheck, golangci-lint, shellcheck, check-yaml, nixfmt, whitespace), builds `.#alerts` — whose build runs the whole test suite, without the race detector because the build is static — and builds `.#release`, the cross-compiled release artifacts. Nix only sees files git knows about: `git add` new files before `just lint`, or the check silently ignores them.
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

- Test helper scripts live in each package's `testdata/` and must be executable (`chmod +x`); git records the bit. They use `#!/bin/sh` and only POSIX tools, because they run with the minimal `PATH` that `internal/proc` gives every command. The Nix sandbox has no `/usr/bin`, so `flake.nix` sets `ALERTS_TEST_TOOLS` to coreutils' `bin` and each package that runs scripts appends it to that `PATH` in `tools_test.go`; a new package with script-based tests needs the same file.
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

Push a tag on `main`: `vX.Y.Z` for a release, `vX.Y.Z-rc.N` (any `-` suffix) for a pre-release.

```sh
git tag -a v0.2.0-rc.1 -m v0.2.0-rc.1
git push origin v0.2.0-rc.1
```

The release workflow re-runs both gates, builds `.#release` with the tag passed in as `ALERTS_VERSION` (`nix build --impure`, the only impure input), checks that the binary reports the tag, and creates the GitHub Release with `alerts_linux_amd64`, `alerts_linux_arm64` and `SHA256SUMS`, marked as a pre-release when the tag has a suffix. The build is reproducible: `just release-build <tag>` on any machine produces the same checksums. Then bump the version and checksums in `repo/bolt`'s Hiera. A plain `nix build` without `ALERTS_VERSION` stamps the binary `dev-<commit>` instead.
