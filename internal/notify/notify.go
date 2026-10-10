// Package notify delivers alerts through pluggable channels.
package notify

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/zezav-cz/alerts/internal/alert"
	"github.com/zezav-cz/alerts/internal/check"
	"github.com/zezav-cz/alerts/internal/config"
)

// Notifier delivers one alert. A nil error means delivered; any error is
// retried by the caller.
type Notifier interface {
	Name() string
	Send(ctx context.Context, a alert.Alert) error
}

// RetryAfterError is a failure that names the earliest sensible retry.
type RetryAfterError struct {
	After time.Duration
	Err   error
}

func (e *RetryAfterError) Error() string { return e.Err.Error() }
func (e *RetryAfterError) Unwrap() error { return e.Err }

// RetryAfter returns the wait a failure asks for, or 0 if it names none.
func RetryAfter(err error) time.Duration {
	var r *RetryAfterError
	if errors.As(err, &r) {
		return r.After
	}
	return 0
}

// Build constructs the notifier a config entry describes. For telegram it
// reads the token file, so a missing or empty token fails here, at start-up.
func Build(c config.Notifier, client *http.Client) (Notifier, error) {
	switch c.Type {
	case "telegram":
		b, err := os.ReadFile(c.TokenFile)
		if err != nil {
			return nil, fmt.Errorf("notifier %s: %w", c.Name, err)
		}
		token := strings.TrimSpace(string(b))
		if token == "" {
			return nil, fmt.Errorf("notifier %s: %s is empty", c.Name, c.TokenFile)
		}
		return NewTelegram(c.Name, token, c.ChatID, client), nil
	case "exec":
		return NewExec(c.Name, c.Command, c.Env, c.Timeout), nil
	}
	return nil, fmt.Errorf("notifier %s: unknown type %q", c.Name, c.Type)
}

// Route returns the notifiers an alert goes to: the union of every matching
// route, in order of first appearance. A problem that moves between two
// non-OK states is routed by both, so whoever was told about the higher
// state also hears that it ended.
func Route(routes []config.Route, a alert.Alert) []string {
	states := []check.State{a.RouteState()}
	if a.Kind == alert.Problem && a.PreviousState != check.OK {
		states = append(states, a.PreviousState)
	}
	var names []string
	for _, r := range routes {
		if !slices.ContainsFunc(states, func(s check.State) bool { return r.Matches(a.Check, s) }) {
			continue
		}
		for _, n := range r.Notify {
			if !slices.Contains(names, n) {
				names = append(names, n)
			}
		}
	}
	return names
}
