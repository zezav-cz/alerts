package notify

import (
	"os"
	"testing"

	"github.com/zezav-cz/alerts/internal/proc"
)

// TestMain lets the test scripts find coreutils where /usr/bin has none, as
// in the Nix build sandbox: flake.nix sets ALERTS_TEST_TOOLS for the check.
func TestMain(m *testing.M) {
	if dir := os.Getenv("ALERTS_TEST_TOOLS"); dir != "" {
		proc.BaseEnv[0] += ":" + dir
	}
	os.Exit(m.Run())
}
