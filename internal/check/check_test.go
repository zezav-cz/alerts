package check

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestExec(t *testing.T) {
	long := strings.Repeat("é", 600)
	cases := []struct {
		name    string
		command []string
		state   State
		output  string
	}{
		{"ok strips perfdata", []string{"testdata/plugin", "0", "DISK OK - free 31% | /=1B;2;3"}, OK, "DISK OK - free 31%"},
		{"warning", []string{"testdata/plugin", "1", "LOAD WARNING"}, Warning, "LOAD WARNING"},
		{"critical", []string{"testdata/plugin", "2", "DISK CRITICAL"}, Critical, "DISK CRITICAL"},
		{"unknown", []string{"testdata/plugin", "3", "bad args"}, Unknown, "bad args"},
		{"out of range exit code", []string{"testdata/plugin", "4", "weird"}, Unknown, "exit status 4: weird"},
		{"out of range without output", []string{"testdata/plugin", "7"}, Unknown, "exit status 7"},
		{"stderr when stdout is empty", []string{"testdata/plugin", "2", "", "boom"}, Critical, "boom"},
		{"no output at all", []string{"testdata/plugin", "0"}, OK, "(no output)"},
		{"truncated to 500 characters", []string{"testdata/plugin", "0", long}, OK, strings.Repeat("é", 500)},
		{"missing command", []string{"/nonexistent/check"}, Unknown, "cannot run /nonexistent/check: no such file or directory"},
		{"empty command", nil, Unknown, "empty command"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := Exec{Command: tc.command, Timeout: 5 * time.Second}.Check(context.Background())
			if res.State != tc.state || res.Output != tc.output {
				t.Errorf("got %s %q, want %s %q", res.State, res.Output, tc.state, tc.output)
			}
		})
	}
}

// The raw output is what `alerts run -v` shows: everything the command
// printed, performance data and stderr included, and how it exited.
func TestExecKeepsTheRawOutput(t *testing.T) {
	res := Exec{Command: []string{"testdata/plugin", "2", "DISK 2 | perf=1", "a warning"}, Timeout: 5 * time.Second}.Check(context.Background())
	if res.Exit != 2 || res.Stdout != "DISK 2 | perf=1\n" || res.Stderr != "a warning\n" {
		t.Errorf("exit %d, stdout %q, stderr %q", res.Exit, res.Stdout, res.Stderr)
	}
	res = Exec{Command: []string{"/nonexistent/check"}, Timeout: time.Second}.Check(context.Background())
	if res.Exit != -1 {
		t.Errorf("a command that never ran has exit %d, want -1", res.Exit)
	}
}

func TestExecTimeout(t *testing.T) {
	res := Exec{Command: []string{"testdata/sleep"}, Timeout: 200 * time.Millisecond}.Check(context.Background())
	if res.State != Unknown || res.Output != "timed out after 200ms" {
		t.Errorf("got %s %q", res.State, res.Output)
	}
	if res.Duration < 200*time.Millisecond || res.Duration > 2*time.Second {
		t.Errorf("duration %s", res.Duration)
	}
}
