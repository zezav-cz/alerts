// Package proc runs external commands the way alerts needs them: no shell,
// a minimal environment, and a hard timeout that kills the whole process
// group, so nothing a check forks outlives it.
package proc

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

// BaseEnv is the environment every command starts from.
var BaseEnv = []string{
	"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	"LANG=C",
}

// MaxCapture is how much of each of stdout and stderr is kept; the rest is
// read and discarded, so a check that floods its output cannot exhaust memory.
const MaxCapture = 64 << 10

// Output is what a finished command left behind.
type Output struct {
	Stdout   []byte
	Stderr   []byte
	TimedOut bool
}

// Run executes argv with BaseEnv plus env, feeding stdin when it is not nil.
// The error is exec's: nil, an *exec.ExitError, or a failure to start. When
// timeout expires the process group is killed and Output.TimedOut is set.
// Whatever the command leaves running in its group when it exits is killed
// too, and at most MaxCapture bytes of each output stream are kept.
func Run(ctx context.Context, argv []string, env []string, stdin []byte, timeout time.Duration) (Output, error) {
	if len(argv) == 0 {
		return Output{}, errors.New("empty command")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = append(slices.Clone(BaseEnv), env...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	// A process group of its own, so the kill also reaches anything forked.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second // bounds the stdin copy

	// Output goes through pipes read here rather than through exec's own
	// copying, so Wait returns when the command exits, not when the last
	// process holding its output closes it.
	outR, outW, err := os.Pipe()
	if err != nil {
		return Output{}, err
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		outR.Close()
		outW.Close()
		return Output{}, err
	}
	cmd.Stdout, cmd.Stderr = outW, errW
	err = cmd.Start()
	outW.Close() // the command holds its own copies now
	errW.Close()
	if err != nil {
		outR.Close()
		errR.Close()
		return Output{}, err
	}
	var stdout, stderr capture
	var readers sync.WaitGroup
	readers.Go(func() { stdout.drain(outR) })
	readers.Go(func() { stderr.drain(errR) })

	err = cmd.Wait()
	// The command is done: take whatever it left running in its group along.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	finish(&readers, outR, errR)
	return Output{
		Stdout:   stdout.buf,
		Stderr:   stderr.buf,
		TimedOut: err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded),
	}, err
}

// capture keeps the first MaxCapture bytes read and discards the rest.
type capture struct{ buf []byte }

func (c *capture) drain(r io.Reader) {
	chunk := make([]byte, 32<<10)
	for {
		n, err := r.Read(chunk)
		if room := MaxCapture - len(c.buf); room > 0 {
			c.buf = append(c.buf, chunk[:min(n, room)]...)
		}
		if err != nil {
			return
		}
	}
}

// finish waits for the readers to see end of file, and closes the pipes. A
// process that left the group (setsid) can keep them open; after two seconds
// reading stops regardless.
func finish(readers *sync.WaitGroup, pipes ...*os.File) {
	done := make(chan struct{})
	go func() {
		readers.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		for _, p := range pipes {
			p.Close()
		}
		<-done
	}
	for _, p := range pipes {
		p.Close()
	}
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
