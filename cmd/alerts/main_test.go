package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zezav-cz/alerts/internal/alert"
)

// writeConfig writes a config whose disk check exits with code and whose
// hook notifier captures into dir; it returns the config path.
func writeConfig(t *testing.T, dir string, code int) string {
	t.Helper()
	doc := fmt.Sprintf(`
host: z01
state_file: %[1]s/state.json
notifiers:
  - name: hook
    type: exec
    command: [testdata/capture]
    env: {CAPTURE_DIR: %[1]s}
routes:
  - notify: [hook]
checks:
  - name: disk
    command: [testdata/plugin, "%[2]d", "DISK %[2]d | perf=1"]
`, dir, code)
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func call(args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestVersion(t *testing.T) {
	if code, out, _ := call("--version"); code != 0 || out != "dev\n" {
		t.Fatalf("%d %q", code, out)
	}
}

func TestUsageErrors(t *testing.T) {
	for _, args := range [][]string{{}, {"frobnicate"}, {"run", "--nope"}, {"validate", "extra"}, {"--nope"}} {
		if code, _, stderr := call(args...); code != 2 || !strings.Contains(stderr, "usage:") {
			t.Errorf("%v: code %d, stderr %q", args, code, stderr)
		}
	}
}

func TestValidate(t *testing.T) {
	dir := t.TempDir()
	if code, out, _ := call("--config", writeConfig(t, dir, 0), "validate"); code != 0 || out != "config OK: 1 checks, 1 notifiers, 1 routes\n" {
		t.Fatalf("%d %q", code, out)
	}
	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(bad, []byte("checks: [{name: Disk}]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := call("--config", bad, "validate")
	if code != 1 || !strings.Contains(stderr, "invalid config") || !strings.Contains(stderr, `"Disk" must match`) {
		t.Fatalf("%d %q", code, stderr)
	}
}

func TestRunShowsResultAndPolicy(t *testing.T) {
	dir := t.TempDir()
	code, out, _ := call("--config", writeConfig(t, dir, 2), "run", "disk")
	if code != 2 || !strings.HasPrefix(out, "CRITICAL  disk  DISK 2  (") || !strings.Contains(out, "policy: confirmed OK, CRITICAL pending 1/3, no alert yet") {
		t.Fatalf("%d %q", code, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "state.json")); !os.IsNotExist(err) {
		t.Error("run wrote state")
	}
	if code, _, stderr := call("--config", writeConfig(t, dir, 0), "run", "load"); code != 1 || !strings.Contains(stderr, `no check named "load"`) {
		t.Fatalf("%d %q", code, stderr)
	}
}

func TestTestNotify(t *testing.T) {
	dir := t.TempDir()
	code, out, stderr := call("--config", writeConfig(t, dir, 0), "test-notify", "hook")
	if code != 0 || out != "test alert delivered through hook\n" {
		t.Fatalf("%d %q %q", code, out, stderr)
	}
	b, err := os.ReadFile(filepath.Join(dir, "stdin.json"))
	if err != nil {
		t.Fatal(err)
	}
	var a alert.Alert
	if err := json.Unmarshal(b, &a); err != nil || a.Kind != alert.Test || a.Text != "🧪 z01 test alert from alerts dev via hook" {
		t.Fatalf("%+v %v", a, err)
	}
	if code, _, stderr := call("--config", writeConfig(t, dir, 0), "test-notify", "sms"); code != 1 || !strings.Contains(stderr, `no notifier named "sms"`) {
		t.Fatalf("%d %q", code, stderr)
	}
}

// writeChecks writes a config with a hook notifier capturing into dir and
// one check per entry, each a testdata plugin exiting with the given code.
func writeChecks(t *testing.T, dir string, codes map[string]int) string {
	t.Helper()
	doc := fmt.Sprintf(`
host: z01
state_file: %[1]s/state.json
notifiers:
  - {name: hook, type: exec, command: [testdata/capture], env: {CAPTURE_DIR: %[1]s}}
routes:
  - notify: [hook]
checks:
`, dir)
	for name, code := range codes {
		doc += fmt.Sprintf("  - {name: %s, command: [testdata/plugin, \"%d\", \"%s is %d | perf=%d\", \"note from %s\"]}\n", name, code, name, code, code, name)
	}
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunWithoutANameRunsEveryCheck(t *testing.T) {
	dir := t.TempDir()
	cfg := writeChecks(t, dir, map[string]int{"disk": 0, "load": 2, "swap": 1, "zfs": 3})
	code, out, stderr := call("--config", cfg, "run")
	if code != 2 {
		t.Errorf("exit %d, want 2 (the worst state); stderr %q", code, stderr)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	want := []string{"CRITICAL  load", "UNKNOWN   zfs", "WARNING   swap", "OK        disk"}
	if len(lines) != len(want)+1 {
		t.Fatalf("output:\n%s", out)
	}
	for i, prefix := range want {
		if !strings.HasPrefix(lines[i], prefix) {
			t.Errorf("line %d = %q, want it to start with %q", i, lines[i], prefix)
		}
	}
	if !strings.Contains(lines[0], "load is 2  (") || strings.Contains(lines[0], "perf=") {
		t.Errorf("first line %q: want the output without performance data", lines[0])
	}
	if lines[4] != "4 checks: 1 critical, 1 unknown, 1 warning, 1 ok" {
		t.Errorf("summary %q", lines[4])
	}
	if strings.Contains(out, "policy:") {
		t.Error("the policy line belongs to a single check only")
	}
	for _, f := range []string{"state.json", "stdin.json"} {
		if _, err := os.Stat(filepath.Join(dir, f)); !os.IsNotExist(err) {
			t.Errorf("run left %s behind: it must neither save state nor notify", f)
		}
	}
}

func TestRunNamedChecksOnly(t *testing.T) {
	dir := t.TempDir()
	cfg := writeChecks(t, dir, map[string]int{"disk": 0, "load": 2, "swap": 1})
	code, out, _ := call("--config", cfg, "run", "disk", "swap")
	if code != 1 || strings.Contains(out, "load") || !strings.Contains(out, "2 checks: 1 warning, 1 ok") {
		t.Fatalf("%d %q", code, out)
	}
	if code, _, stderr := call("--config", cfg, "run", "disk", "nope"); code != 1 || !strings.Contains(stderr, `no check named "nope"`) {
		t.Fatalf("%d %q", code, stderr)
	}
}

func TestRunVerboseShowsTheRawTestOutput(t *testing.T) {
	dir := t.TempDir()
	cfg := writeChecks(t, dir, map[string]int{"disk": 1})
	code, out, _ := call("--config", cfg, "run", "-v")
	if code != 1 {
		t.Errorf("exit %d", code)
	}
	for _, want := range []string{
		"WARNING   disk  disk is 1",
		"  command  testdata/plugin 1 'disk is 1 | perf=1' 'note from disk'",
		"  exit     1",
		"  stdout   disk is 1 | perf=1",
		"  stderr   note from disk",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}
