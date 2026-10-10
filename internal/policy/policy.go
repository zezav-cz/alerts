// Package policy decides when a check result becomes an alert. It is a pure
// function of its inputs: no I/O and no clock of its own.
package policy

import (
	"time"

	"github.com/zezav-cz/alerts/internal/alert"
	"github.com/zezav-cz/alerts/internal/check"
)

// Settings are the per-check knobs the policy needs.
type Settings struct {
	Confirm int           // consecutive differing results before the state changes
	Repeat  time.Duration // reminder period while not OK; 0 disables reminders
}

// CheckState is what the policy remembers about one check between results.
type CheckState struct {
	Confirmed    check.State `json:"confirmed"`
	Since        time.Time   `json:"since"`
	Pending      int         `json:"pending"`
	PendingState check.State `json:"pending_state"`
	LastOutput   string      `json:"last_output"`
	LastAlert    time.Time   `json:"last_alert,omitzero"`
}

// Initial is the state of a check never seen before: OK since now, so a
// healthy check sends nothing and an existing problem alerts once confirmed.
func Initial(now time.Time) CheckState {
	return CheckState{Confirmed: check.OK, Since: now, PendingState: check.OK}
}

// Evaluate applies one result and returns the new state plus the alert to
// send, if any. At most one alert comes out of one result.
func Evaluate(host, name string, prev CheckState, r check.Result, now time.Time, s Settings) (CheckState, *alert.Alert) {
	next := prev
	next.LastOutput = r.Output

	if r.State == prev.Confirmed {
		next.Pending, next.PendingState = 0, prev.Confirmed
		if next.Confirmed != check.OK && s.Repeat > 0 && now.Sub(next.LastAlert) >= s.Repeat {
			a := alert.NewReminder(host, name, next.Confirmed, next.Since, r.Output, now)
			next.LastAlert = now
			return next, &a
		}
		return next, nil
	}

	next.Pending++
	next.PendingState = r.State
	if next.Pending < s.Confirm {
		return next, nil
	}

	var a alert.Alert
	if r.State == check.OK {
		a = alert.NewRecovery(host, name, prev.Confirmed, prev.Since, r.Output, now)
	} else {
		a = alert.NewProblem(host, name, prev.Confirmed, r.State, r.Output, now)
	}
	next.Confirmed, next.Since = r.State, now
	next.Pending, next.PendingState = 0, r.State
	next.LastAlert = now
	return next, &a
}
