package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/zezav-cz/alerts/internal/alert"
	"github.com/zezav-cz/alerts/internal/proc"
)

// Exec hands the alert as JSON on stdin to a command. Exit 0 is delivered.
type Exec struct {
	name    string
	command []string
	env     map[string]string
	timeout time.Duration
}

// NewExec returns an exec notifier.
func NewExec(name string, command []string, env map[string]string, timeout time.Duration) *Exec {
	return &Exec{name: name, command: command, env: env, timeout: timeout}
}

// Name returns the notifier's configured name.
func (e *Exec) Name() string { return e.name }

// Send runs the command once with the alert on stdin.
func (e *Exec) Send(ctx context.Context, a alert.Alert) error {
	if len(e.command) == 0 {
		return errors.New("empty command")
	}
	payload, err := json.Marshal(a)
	if err != nil {
		return err
	}
	env := make([]string, 0, len(e.env))
	for _, k := range slices.Sorted(maps.Keys(e.env)) {
		env = append(env, k+"="+e.env[k])
	}
	out, err := proc.Run(ctx, e.command, env, payload, e.timeout)
	switch {
	case out.TimedOut:
		return fmt.Errorf("%s: timed out after %s", e.command[0], e.timeout)
	case err != nil:
		msg := fmt.Sprintf("%s: %s", e.command[0], proc.Reason(err))
		if line := proc.FirstLine(out.Stderr); line != "" {
			msg += ": " + line
		}
		return errors.New(msg)
	}
	return nil
}
