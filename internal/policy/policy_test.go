package policy

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/zezav-cz/alerts/internal/check"
)

var t0 = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

const (
	O = check.OK
	W = check.Warning
	C = check.Critical
	U = check.Unknown
)

// feed applies states one minute apart, starting a minute after start, and
// returns the final state and one "kind:STATE" entry per emitted alert
// ("-" where no alert came out).
func feed(start CheckState, s Settings, states ...check.State) (CheckState, []string) {
	st, out := start, []string{}
	for i, state := range states {
		now := t0.Add(time.Duration(i+1) * time.Minute)
		next, a := Evaluate("z01", "disk", st, check.Result{State: state, Output: fmt.Sprintf("out %d", i)}, now, s)
		st = next
		if a == nil {
			out = append(out, "-")
		} else {
			out = append(out, fmt.Sprintf("%s:%s", a.Kind, a.State))
		}
	}
	return st, out
}

func TestConfirmation(t *testing.T) {
	s := Settings{Confirm: 3, Repeat: 6 * time.Hour}
	cases := []struct {
		name   string
		start  CheckState
		states []check.State
		want   []string
	}{
		{"healthy stays quiet", Initial(t0), []check.State{O, O, O}, []string{"-", "-", "-"}},
		{"third critical confirms", Initial(t0), []check.State{C, C, C}, []string{"-", "-", "problem:CRITICAL"}},
		{"flapping never confirms", Initial(t0), []check.State{C, O, C, O, C, O}, []string{"-", "-", "-", "-", "-", "-"}},
		{"mixed problems confirm as the latest", Initial(t0), []check.State{W, C, C}, []string{"-", "-", "problem:CRITICAL"}},
		{"unknown is a problem", Initial(t0), []check.State{U, U, U}, []string{"-", "-", "problem:UNKNOWN"}},
		{"escalation", confirmed(W), []check.State{C, C, C}, []string{"-", "-", "problem:CRITICAL"}},
		{"de-escalation", confirmed(C), []check.State{W, W, W}, []string{"-", "-", "problem:WARNING"}},
		{"recovery is confirmed too", confirmed(C), []check.State{O, O, O}, []string{"-", "-", "recovery:OK"}},
		{"recovery interrupted", confirmed(C), []check.State{O, O, C, O}, []string{"-", "-", "-", "-"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, got := feed(tc.start, s, tc.states...); !slices.Equal(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// confirmed is a check that has been in state s since t0, alerted at t0.
func confirmed(s check.State) CheckState {
	return CheckState{Confirmed: s, Since: t0, PendingState: s, LastAlert: t0}
}

func TestConfirmOneAlertsImmediately(t *testing.T) {
	_, got := feed(Initial(t0), Settings{Confirm: 1}, C, O)
	if want := []string{"problem:CRITICAL", "recovery:OK"}; !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestPendingCounter(t *testing.T) {
	st, _ := feed(Initial(t0), Settings{Confirm: 3}, C, C)
	if st.Pending != 2 || st.PendingState != C || st.Confirmed != O {
		t.Errorf("after two criticals: %+v", st)
	}
	st, _ = feed(st, Settings{Confirm: 3}, O)
	if st.Pending != 0 || st.PendingState != O {
		t.Errorf("after OK: %+v", st)
	}
}

func TestProblemAlertFields(t *testing.T) {
	st, a := Evaluate("z01", "disk", CheckState{Confirmed: W, Since: t0, PendingState: C, Pending: 2}, check.Result{State: C, Output: "DISK CRITICAL"}, t0.Add(time.Hour), Settings{Confirm: 3})
	if a == nil {
		t.Fatal("no alert")
	}
	if a.PreviousState != W || a.State != C || a.Output != "DISK CRITICAL" || !a.Time.Equal(t0.Add(time.Hour)) {
		t.Errorf("alert %+v", a)
	}
	if st.Confirmed != C || !st.Since.Equal(t0.Add(time.Hour)) || !st.LastAlert.Equal(t0.Add(time.Hour)) || st.Pending != 0 {
		t.Errorf("state %+v", st)
	}
}

func TestRecoveryText(t *testing.T) {
	prev := CheckState{Confirmed: C, Since: t0, Pending: 2, PendingState: O}
	_, a := Evaluate("z01", "disk", prev, check.Result{State: O, Output: "DISK OK - free space: / 31%"}, t0.Add(2*time.Hour+13*time.Minute), Settings{Confirm: 3})
	if a == nil || a.Text != "✅ z01 disk OK (was CRITICAL for 2h13m)\nDISK OK - free space: / 31%" {
		t.Fatalf("alert %+v", a)
	}
}

func TestReminders(t *testing.T) {
	s := Settings{Confirm: 3, Repeat: 6 * time.Hour}
	st := confirmed(C)
	at := func(d time.Duration) (CheckState, bool) {
		next, a := Evaluate("z01", "disk", st, check.Result{State: C, Output: "still bad"}, t0.Add(d), s)
		return next, a != nil
	}

	if _, sent := at(6*time.Hour - time.Minute); sent {
		t.Error("reminder before repeat elapsed")
	}
	next, sent := at(6 * time.Hour)
	if !sent || !next.LastAlert.Equal(t0.Add(6*time.Hour)) {
		t.Fatalf("no reminder at 6h: %+v", next)
	}
	st = next
	if _, sent := at(6*time.Hour + time.Minute); sent {
		t.Error("reminder again a minute later")
	}
	if _, sent := at(12 * time.Hour); !sent {
		t.Error("no second reminder at 12h")
	}

	_, a := Evaluate("z01", "disk", confirmed(C), check.Result{State: C, Output: "x"}, t0.Add(6*time.Hour), s)
	if a == nil || a.Text != "🔁 z01 disk still CRITICAL (for 6h)\nx" || !a.Since.Equal(t0) {
		t.Errorf("reminder %+v", a)
	}
}

func TestNoReminderWhileRecoveryIsPending(t *testing.T) {
	_, a := Evaluate("z01", "disk", confirmed(C), check.Result{State: O}, t0.Add(7*time.Hour), Settings{Confirm: 3, Repeat: 6 * time.Hour})
	if a != nil {
		t.Errorf("got %s while an OK result was pending", a.Kind)
	}
}

func TestRepeatZeroDisablesReminders(t *testing.T) {
	_, a := Evaluate("z01", "disk", confirmed(C), check.Result{State: C}, t0.Add(100*time.Hour), Settings{Confirm: 3})
	if a != nil {
		t.Errorf("got %s with repeat 0", a.Kind)
	}
}

func TestNoReminderWhenOK(t *testing.T) {
	_, a := Evaluate("z01", "disk", Initial(t0), check.Result{State: O}, t0.Add(100*time.Hour), Settings{Confirm: 3, Repeat: time.Hour})
	if a != nil {
		t.Errorf("got %s for a healthy check", a.Kind)
	}
}
