// Package alert defines the event alerts delivers and its human-readable
// text.
package alert

import (
	"fmt"
	"time"

	"github.com/zezav-cz/alerts/internal/check"
)

// Kind says why an alert was emitted.
type Kind string

// The alert kinds.
const (
	Problem  Kind = "problem"
	Recovery Kind = "recovery"
	Reminder Kind = "reminder"
	Test     Kind = "test"
)

// Alert is what notifiers deliver. Its JSON form is the stdin contract of
// exec notifiers.
type Alert struct {
	Kind          Kind        `json:"kind"`
	Host          string      `json:"host"`
	Check         string      `json:"check"`
	State         check.State `json:"state"`
	PreviousState check.State `json:"previous_state"`
	Output        string      `json:"output"`
	Since         time.Time   `json:"since"`
	Time          time.Time   `json:"time"`
	Text          string      `json:"text"`
}

// RouteState is the state an alert is routed by: the state recovered from
// for a recovery, so the "OK again" reaches whoever got the problem, and the
// alert's own state otherwise.
func (a Alert) RouteState() check.State {
	if a.Kind == Recovery {
		return a.PreviousState
	}
	return a.State
}

// NewProblem is a confirmed change into a non-OK state.
func NewProblem(host, name string, from, to check.State, output string, now time.Time) Alert {
	return Alert{
		Kind: Problem, Host: host, Check: name, State: to, PreviousState: from,
		Output: output, Since: now, Time: now,
		Text: fmt.Sprintf("%s %s %s %s\n%s", icon(to), host, name, to, output),
	}
}

// NewRecovery is a confirmed return to OK from a state entered at fromSince.
func NewRecovery(host, name string, from check.State, fromSince time.Time, output string, now time.Time) Alert {
	return Alert{
		Kind: Recovery, Host: host, Check: name, State: check.OK, PreviousState: from,
		Output: output, Since: now, Time: now,
		Text: fmt.Sprintf("✅ %s %s OK (was %s for %s)\n%s", host, name, from, Duration(now.Sub(fromSince)), output),
	}
}

// NewReminder repeats a problem that is still there.
func NewReminder(host, name string, state check.State, since time.Time, output string, now time.Time) Alert {
	return Alert{
		Kind: Reminder, Host: host, Check: name, State: state, PreviousState: state,
		Output: output, Since: since, Time: now,
		Text: fmt.Sprintf("🔁 %s %s still %s (for %s)\n%s", host, name, state, Duration(now.Sub(since)), output),
	}
}

// NewTest is the alert `alerts test-notify` sends.
func NewTest(host, version, notifier string, now time.Time) Alert {
	return Alert{
		Kind: Test, Host: host, Check: "test", State: check.OK, PreviousState: check.OK,
		Output: "test alert", Since: now, Time: now,
		Text: fmt.Sprintf("🧪 %s test alert from alerts %s via %s", host, version, notifier),
	}
}

func icon(s check.State) string {
	switch s {
	case check.Critical:
		return "🔴"
	case check.Warning:
		return "🟡"
	case check.Unknown:
		return "⚪"
	default:
		return "✅"
	}
}

// Duration formats d rounded to the minute, with zero parts left out: 2h13m,
// 6h, 45m. Anything that rounds to under a minute is "<1m".
func Duration(d time.Duration) string {
	d = d.Round(time.Minute)
	if d < time.Minute {
		return "<1m"
	}
	h, m := d/time.Hour, (d%time.Hour)/time.Minute
	switch {
	case h == 0:
		return fmt.Sprintf("%dm", m)
	case m == 0:
		return fmt.Sprintf("%dh", h)
	default:
		return fmt.Sprintf("%dh%dm", h, m)
	}
}
