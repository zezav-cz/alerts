// Package proc runs external commands the way alerts needs them: no shell,
// a minimal environment, and a hard timeout that kills the whole process
// group, so nothing a check forks outlives it.
package proc

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os/exec"
	"slices"
	"strings"
	"syscall"
	"time"
)

// BaseEnv is the environment every command starts from.
var BaseEnv = []string{
	"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	"LANG=C",
}

// Output is what a finished command left behind.
type Output struct {
	Stdout   []byte
	Stderr   []byte
	TimedOut bool
}

// Run executes argv with BaseEnv plus env, feeding stdin when it is not nil.
// The error is exec's: nil, an *exec.ExitError, or a failure to start. When
// timeout expires the process group is killed and Output.TimedOut is set.
func Run(ctx context.Context, argv []string, env []string, stdin []byte, timeout time.Duration) (Output, error) {
	if len(argv) == 0 {
		return Output{}, errors.New("empty command")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = append(slices.Clone(BaseEnv), env...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	// A process group of its own, so the kill also reaches anything forked.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	// Stop waiting for pipes held open by a child that escaped the group.
	cmd.WaitDelay = 2 * time.Second

	err := cmd.Run()
	return Output{
		Stdout:   stdout.Bytes(),
		Stderr:   stderr.Bytes(),
		TimedOut: err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded),
	}, err
}

// FirstLine returns the first non-blank line of b, trimmed.
func FirstLine(b []byte) string {
	for line := range strings.Lines(string(b)) {
		if s := strings.TrimSpace(line); s != "" {
			return s
		}
	}
	return ""
}

// Reason turns an error from Run into a short cause without exec's
// wrapping: "exit status 4", "signal: killed", "no such file or directory".
func Reason(err error) string {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.Error()
	}
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err.Error()
	}
	var execErr *exec.Error
	if errors.As(err, &execErr) {
		return execErr.Err.Error()
	}
	return err.Error()
}
