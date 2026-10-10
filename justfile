# List every recipe.
_default:
    @just --list

# Repair formatting across the working tree.
fmt:
    pre-commit run --all-files

# Verify without modifying anything: hooks, sandboxed tests, release build.
lint:
    nix flake check

# Run the test suite with the race detector.
test:
    go test -race ./...

# Build a static binary for this machine into bin/.
build:
    CGO_ENABLED=0 go build -trimpath -o bin/alerts ./cmd/alerts

# lint + test.
ci: lint test

# Build the release artifacts for VERSION (e.g. v0.1.0) into dist/, as CI does.
release-build version:
    ALERTS_VERSION={{version}} nix build --impure .#release -o dist
