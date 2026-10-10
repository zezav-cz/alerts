package proc

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunCapturesOutputAndExitCode(t *testing.T) {
	out, err := Run(context.Background(), []string{"testdata/echo-exit", "2", "hello", "oops"}, nil, nil, 5*time.Second)
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 {
		t.Fatalf("err = %v, want exit status 2", err)
	}
	if got := FirstLine(out.Stdout); got != "hello" {
		t.Errorf("stdout first line = %q", got)
	}
	if got := FirstLine(out.Stderr); got != "oops" {
		t.Errorf("stderr first line = %q", got)
	}
	if out.TimedOut {
		t.Error("TimedOut set without a timeout")
	}
}

func TestRunPassesStdinAndMinimalEnv(t *testing.T) {
	t.Setenv("LEAKED_FROM_PARENT", "1")
	out, err := Run(context.Background(), []string{"testdata/stdin-env"}, []string{"EXTRA=yes"}, []byte("payload"), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out.Stdout)
	for _, want := range []string{"stdin=payload", "EXTRA=yes", "LANG=C", "PATH=/usr/local/sbin:"} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "LEAKED_FROM_PARENT") {
		t.Errorf("parent environment leaked:\n%s", got)
	}
}

func TestRunTimeoutKillsTheWholeGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	start := time.Now()
	out, err := Run(context.Background(), []string{"testdata/fork-and-wait", pidFile}, nil, nil, 300*time.Millisecond)
	if err == nil || !out.TimedOut {
		t.Fatalf("err = %v, TimedOut = %v; want a timeout", err, out.TimedOut)
	}
	if took := time.Since(start); took > 1500*time.Millisecond {
		t.Errorf("Run took %s; the grandchild kept it waiting", took)
	}
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("grandchild %d still alive after the timeout", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestRunCapsCapturedOutput(t *testing.T) {
	out, err := Run(context.Background(), []string{"testdata/flood"}, nil, nil, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Stdout) != MaxCapture {
		t.Errorf("captured %d bytes of stdout, want the first %d", len(out.Stdout), MaxCapture)
	}
	if got := FirstLine(out.Stdout); got != "first line" {
		t.Errorf("first line %q", got)
	}
}

func TestRunReapsWhatACommandLeavesBehind(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	start := time.Now()
	out, err := Run(context.Background(), []string{"testdata/background", pidFile}, nil, nil, 10*time.Second)
	if err != nil {
		t.Fatalf("err = %v; a command that exited 0 must count as success", err)
	}
	if got := FirstLine(out.Stdout); got != "OK - fine" {
		t.Errorf("first line %q", got)
	}
	if took := time.Since(start); took > time.Second {
		t.Errorf("Run took %s waiting for the leftover child", took)
	}
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("leftover child %d still alive", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestReason(t *testing.T) {
	_, err := Run(context.Background(), []string{"/nonexistent/check"}, nil, nil, time.Second)
	if got := Reason(err); got != "no such file or directory" {
		t.Errorf("missing absolute path: %q", got)
	}
	_, err = Run(context.Background(), []string{"no-such-command-anywhere"}, nil, nil, time.Second)
	if got := Reason(err); got != "executable file not found in $PATH" {
		t.Errorf("missing command on PATH: %q", got)
	}
	_, err = Run(context.Background(), []string{"testdata/echo-exit", "4"}, nil, nil, time.Second)
	if got := Reason(err); got != "exit status 4" {
		t.Errorf("exit code: %q", got)
	}
}

func TestFirstLine(t *testing.T) {
	for in, want := range map[string]string{"": "", "\n\n  a b \nc": "a b", "only": "only", "x\r\ny": "x"} {
		if got := FirstLine([]byte(in)); got != want {
			t.Errorf("FirstLine(%q) = %q, want %q", in, got, want)
		}
	}
}
