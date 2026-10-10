package alert

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/zezav-cz/alerts/internal/check"
)

var t0 = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

func TestTexts(t *testing.T) {
	cases := []struct {
		name string
		got  Alert
		want string
	}{
		{"critical", NewProblem("z01", "disk", check.OK, check.Critical, "DISK CRITICAL - free space: / 4%", t0),
			"🔴 z01 disk CRITICAL\nDISK CRITICAL - free space: / 4%"},
		{"warning", NewProblem("rpi01", "throttle", check.OK, check.Warning, "WARNING - under-voltage detected", t0),
			"🟡 rpi01 throttle WARNING\nWARNING - under-voltage detected"},
		{"unknown", NewProblem("z01", "cert-zezav", check.OK, check.Unknown, "timed out after 30s", t0),
			"⚪ z01 cert-zezav UNKNOWN\ntimed out after 30s"},
		{"recovery", NewRecovery("z01", "disk", check.Critical, t0, "DISK OK - free space: / 31%", t0.Add(2*time.Hour+13*time.Minute)),
			"✅ z01 disk OK (was CRITICAL for 2h13m)\nDISK OK - free space: / 31%"},
		{"reminder", NewReminder("z01", "disk", check.Critical, t0, "DISK CRITICAL - free space: / 3%", t0.Add(6*time.Hour)),
			"🔁 z01 disk still CRITICAL (for 6h)\nDISK CRITICAL - free space: / 3%"},
		{"test", NewTest("z01", "v1.0.0", "telegram", t0),
			"🧪 z01 test alert from alerts v1.0.0 via telegram"},
	}
	for _, tc := range cases {
		if tc.got.Text != tc.want {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, tc.got.Text, tc.want)
		}
	}
}

func TestRouteState(t *testing.T) {
	rec := NewRecovery("z01", "disk", check.Critical, t0, "ok", t0)
	if rec.State != check.OK || rec.RouteState() != check.Critical {
		t.Errorf("recovery: state %s, route state %s", rec.State, rec.RouteState())
	}
	prob := NewProblem("z01", "disk", check.OK, check.Warning, "w", t0)
	if prob.RouteState() != check.Warning {
		t.Errorf("problem route state %s", prob.RouteState())
	}
}

func TestDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0: "<1m", 29 * time.Second: "<1m", 30 * time.Second: "1m", 45 * time.Minute: "45m",
		6 * time.Hour: "6h", 2*time.Hour + 13*time.Minute: "2h13m", 30 * time.Hour: "30h",
	} {
		if got := Duration(d); got != want {
			t.Errorf("Duration(%s) = %q, want %q", d, got, want)
		}
	}
}

func TestJSONFieldNames(t *testing.T) {
	b, err := json.Marshal(NewProblem("z01", "disk", check.OK, check.Critical, "out", t0))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"kind": "problem", "host": "z01", "check": "disk", "state": "CRITICAL", "previous_state": "OK",
		"output": "out", "since": "2026-10-10T12:00:00Z", "time": "2026-10-10T12:00:00Z",
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %v, want %v", k, m[k], v)
		}
	}
	if _, ok := m["text"]; !ok || len(m) != 9 {
		t.Errorf("unexpected field set: %v", m)
	}
}
