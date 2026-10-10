# List every recipe.
_default:
    @just --list

# Repair formatting across the working tree.
fmt:
    pre-commit run --all-files

# Verify without modifying anything — same hooks, sandboxed.
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

# Build the release binaries and SHA256SUMS for VERSION into dist/.
release-build version:
    scripts/release-build {{version}}
