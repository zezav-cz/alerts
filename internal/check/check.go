package check

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/zezav-cz/alerts/internal/proc"
)

// Result is one run of a check.
type Result struct {
	State    State
	Output   string
	Duration time.Duration
}

// Checker runs a check once. Every problem, including the check being unable
// to run at all, is reported as a Result rather than an error.
type Checker interface {
	Check(ctx context.Context) Result
}

// maxOutput caps the output line, in characters.
const maxOutput = 500

// Exec runs a command following the plugin convention: the exit code is the
// state and the first line of output is the message.
type Exec struct {
	Command []string
	Timeout time.Duration
}

// Check runs the command once.
func (e Exec) Check(ctx context.Context) Result {
	if len(e.Command) == 0 {
		return Result{State: Unknown, Output: "empty command"}
	}
	start := time.Now()
	out, err := proc.Run(ctx, e.Command, nil, nil, e.Timeout)
	res := Result{Duration: time.Since(start)}

	line := clean(proc.FirstLine(out.Stdout))
	if line == "" {
		line = clean(proc.FirstLine(out.Stderr))
	}

	var exitErr *exec.ExitError
	switch {
	case out.TimedOut:
		res.State, res.Output = Unknown, fmt.Sprintf("timed out after %s", e.Timeout)
	case err == nil:
		res.State, res.Output = OK, line
	case errors.As(err, &exitErr) && exitErr.ExitCode() >= 1 && exitErr.ExitCode() <= 3:
		res.State, res.Output = State(exitErr.ExitCode()), line
	case errors.As(err, &exitErr):
		res.State, res.Output = Unknown, proc.Reason(err)
		if line != "" {
			res.Output += ": " + line
		}
	default:
		res.State, res.Output = Unknown, fmt.Sprintf("cannot run %s: %s", e.Command[0], proc.Reason(err))
	}
	if res.Output == "" {
		res.Output = "(no output)"
	}
	return res
}

// clean drops performance data (everything from the first "|"), trims, and
// caps the line at maxOutput characters.
func clean(line string) string {
	if i := strings.IndexByte(line, '|'); i >= 0 {
		line = line[:i]
	}
	line = strings.TrimSpace(line)
	if utf8.RuneCountInString(line) > maxOutput {
		line = string([]rune(line)[:maxOutput])
	}
	return line
}
