package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zezav-cz/alerts/internal/alert"
	"github.com/zezav-cz/alerts/internal/check"
	"github.com/zezav-cz/alerts/internal/config"
	"github.com/zezav-cz/alerts/internal/notify"
	"github.com/zezav-cz/alerts/internal/state"
)

var t0 = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

// fakeChecker returns whatever state the test last set.
type fakeChecker struct {
	mu  sync.Mutex
	res check.Result
}

func (f *fakeChecker) set(s check.State) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.res = check.Result{State: s, Output: fmt.Sprintf("disk is %s", s)}
}

func (f *fakeChecker) Check(context.Context) check.Result {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.res
}

// recorder is a notifier that records deliveries, or fails with err.
type recorder struct {
	mu   sync.Mutex
	err  error
	sent []alert.Alert
}

func (r *recorder) Name() string { return "telegram" }

func (r *recorder) Send(_ context.Context, a alert.Alert) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.sent = append(r.sent, a)
	return nil
}

func (r *recorder) fail(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.err = err
}

// kinds lists deliveries as "kind:STATE".
func (r *recorder) kinds() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []string{}
	for _, a := range r.sent {
		out = append(out, fmt.Sprintf("%s:%s", a.Kind, a.State))
	}
	return out
}

// harness drives a Daemon deterministically: time is a variable, and the
// test, not Run, consumes the result and outcome channels.
type harness struct {
	t    *testing.T
	d    *Daemon
	now  time.Time
	disk *fakeChecker
	tg   *recorder
}

const standardDefaults = "interval: 1m, retry_interval: 30s, confirm: 3, repeat: 6h"

// testConfig takes the state file path and the contents of `defaults`.
const testConfig = `
host: z01
state_file: %s
defaults: {%s}
notifiers:
  - {name: telegram, type: exec, command: [/bin/true]}
routes:
  - notify: [telegram]
checks:
  - {name: disk, command: [/bin/true]}
`

// newHarness builds a daemon on testConfig. defaults replaces
// standardDefaults when not empty; extra is appended YAML.
func newHarness(t *testing.T, statePath string, now time.Time, disk *fakeChecker, tg *recorder, defaults, extra string) *harness {
	t.Helper()
	if defaults == "" {
		defaults = standardDefaults
	}
	cfg, err := config.Parse([]byte(fmt.Sprintf(testConfig, statePath, defaults) + extra))
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, now: now, disk: disk, tg: tg}
	h.d, err = New(cfg, Options{
		Now:       func() time.Time { return h.now },
		Jitter:    func(time.Duration) time.Duration { return 0 },
		Checkers:  map[string]check.Checker{"disk": disk},
		Notifiers: map[string]notify.Notifier{"telegram": tg},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.d.stopWork)
	return h
}

// advance moves time forward, runs what is due, and delivers what that
// produced, all before returning.
func (h *harness) advance(d time.Duration) {
	h.now = h.now.Add(d)
	h.d.tick(h.now)
	h.settle()
	h.d.tick(h.now) // dispatch alerts the results just enqueued
	h.settle()
}

func (h *harness) settle() {
	h.t.Helper()
	for h.d.inflight > 0 {
		select {
		case r := <-h.d.results:
			h.d.handleResult(r)
		case o := <-h.d.outcomes:
			h.d.handleOutcome(o)
		case <-time.After(5 * time.Second):
			h.t.Fatal("daemon did not settle")
		}
	}
}

func (h *harness) wantSent(want ...string) {
	h.t.Helper()
	if got := h.tg.kinds(); !slices.Equal(got, want) {
		h.t.Fatalf("sent %v, want %v", got, want)
	}
}

func TestProblemReminderFailedRecoveryAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	disk, tg := &fakeChecker{}, &recorder{}
	disk.set(check.OK)
	h := newHarness(t, path, t0, disk, tg, "", "")

	h.advance(0)
	h.wantSent()

	// Three critical results, the last two at the retry interval.
	disk.set(check.Critical)
	h.advance(time.Minute)
	h.advance(30 * time.Second)
	h.wantSent()
	h.advance(30 * time.Second)
	h.wantSent("problem:CRITICAL")
	problemAt := h.now

	// A reminder exactly six hours after the problem, not before.
	for h.now.Before(problemAt.Add(6*time.Hour - time.Minute)) {
		h.advance(time.Minute)
	}
	h.wantSent("problem:CRITICAL")
	h.advance(time.Minute)
	h.wantSent("problem:CRITICAL", "reminder:CRITICAL")

	// The recovery is confirmed but Telegram is down.
	tg.fail(errors.New("telegram: HTTP 502"))
	disk.set(check.OK)
	h.advance(time.Minute)
	h.advance(30 * time.Second)
	h.advance(30 * time.Second)
	q := h.d.st.Queues["telegram"]
	if len(q) != 1 || q[0].Alert.Kind != alert.Recovery || q[0].Attempts != 1 || !q[0].NextTry.Equal(h.now.Add(10*time.Second)) {
		t.Fatalf("queue after a failed delivery: %+v", q)
	}

	// Restart: a new daemon on the same state file, Telegram back up.
	tg2 := &recorder{}
	h2 := newHarness(t, path, h.now.Add(5*time.Second), disk, tg2, "", "")
	h2.advance(5 * time.Second)
	h2.wantSent("recovery:OK")
	if st := h2.d.st.Checks["disk"]; st.Confirmed != check.OK || st.Pending != 0 {
		t.Fatalf("check state after restart: %+v", st)
	}
	if len(h2.d.st.Queues) != 0 {
		t.Fatalf("queues not empty: %+v", h2.d.st.Queues)
	}
	h2.advance(time.Minute)
	h2.wantSent("recovery:OK")
}

func TestRestartKeepsAPendingConfirmation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	disk, tg := &fakeChecker{}, &recorder{}
	disk.set(check.Critical)
	h := newHarness(t, path, t0, disk, tg, "", "")
	h.advance(0)
	h.advance(30 * time.Second)
	if p := h.d.st.Checks["disk"].Pending; p != 2 {
		t.Fatalf("pending %d before restart", p)
	}

	h2 := newHarness(t, path, h.now, disk, tg, "", "")
	h2.advance(30 * time.Second)
	h2.wantSent("problem:CRITICAL")
}

func TestRetryAfterIsHonoured(t *testing.T) {
	disk, tg := &fakeChecker{}, &recorder{}
	disk.set(check.Critical)
	tg.fail(&notify.RetryAfterError{After: 5 * time.Minute, Err: errors.New("429")})
	h := newHarness(t, filepath.Join(t.TempDir(), "state.json"), t0, disk, tg, "confirm: 1", "")
	h.advance(0)
	if q := h.d.st.Queues["telegram"]; len(q) != 1 || !q[0].NextTry.Equal(t0.Add(5*time.Minute)) {
		t.Fatalf("queue %+v", q)
	}
}

func TestNewFailsWhenStateIsNotWritable(t *testing.T) {
	cfg, err := config.Parse([]byte(fmt.Sprintf(testConfig, "/nonexistent/dir/state.json", standardDefaults)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(cfg, Options{Notifiers: map[string]notify.Notifier{"telegram": &recorder{}}}); err == nil || !strings.Contains(err.Error(), "state file not writable") {
		t.Fatalf("err = %v", err)
	}
}

func TestRetryFarInTheFutureMeansTheClockSteppedBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	old := state.New()
	old.Queues["telegram"] = []state.Queued{{
		Alert:    alert.NewProblem("z01", "disk", check.OK, check.Critical, "x", t0),
		Enqueued: t0, Attempts: 3, NextTry: t0.Add(48 * time.Hour),
	}}
	if err := state.Save(path, old); err != nil {
		t.Fatal(err)
	}
	disk, tg := &fakeChecker{}, &recorder{}
	disk.set(check.OK)
	h := newHarness(t, path, t0.Add(time.Minute), disk, tg, "", "")
	h.advance(0)
	h.wantSent("problem:CRITICAL")
}

func TestBackoff(t *testing.T) {
	want := []time.Duration{10 * time.Second, 20 * time.Second, 40 * time.Second, 80 * time.Second,
		160 * time.Second, 320 * time.Second, 10 * time.Minute, 10 * time.Minute}
	for i, w := range want {
		if got := backoff(i + 1); got != w {
			t.Errorf("backoff(%d) = %s, want %s", i+1, got, w)
		}
	}
}

func TestUndeliveredAlertsExpire(t *testing.T) {
	disk, tg := &fakeChecker{}, &recorder{}
	disk.set(check.Critical)
	tg.fail(errors.New("down"))
	h := newHarness(t, filepath.Join(t.TempDir(), "state.json"), t0, disk, tg, "confirm: 1, interval: 1h, repeat: 0", "")
	h.advance(0)
	if len(h.d.st.Queues["telegram"]) != 1 {
		t.Fatal("alert not queued")
	}
	h.advance(24*time.Hour - time.Second)
	if len(h.d.st.Queues["telegram"]) != 1 {
		t.Fatal("dropped too early")
	}
	h.advance(time.Second)
	if q, ok := h.d.st.Queues["telegram"]; ok {
		t.Fatalf("still queued after 24h: %+v", q)
	}
}

func TestStateIsWrittenOnlyWhenSomethingChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	disk, tg := &fakeChecker{}, &recorder{}
	disk.set(check.OK)
	h := newHarness(t, path, t0, disk, tg, "", "")
	h.advance(0)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, t0, t0); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		h.advance(time.Minute)
	}
	after, _ := os.Stat(path)
	if !after.ModTime().Equal(t0) || after.Size() != before.Size() {
		t.Fatal("healthy results rewrote the state file")
	}
}

func TestNewPrunesRemovedChecksAndNotifiers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	old := state.New()
	old.Checks["gone"] = old.Checks["disk"]
	old.Queues["sms"] = []state.Queued{{Alert: alert.NewTest("z01", "dev", "sms", t0)}}
	if err := state.Save(path, old); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, path, t0, &fakeChecker{}, &recorder{}, "", "")
	if _, ok := h.d.st.Checks["gone"]; ok {
		t.Error("removed check kept")
	}
	if _, ok := h.d.st.Queues["sms"]; ok {
		t.Error("removed notifier's queue kept")
	}
	if _, ok := h.d.st.Checks["disk"]; !ok {
		t.Error("configured check missing")
	}
}

func TestNewBuildsExecCheckers(t *testing.T) {
	cfg, err := config.Parse([]byte(fmt.Sprintf(testConfig, filepath.Join(t.TempDir(), "s.json"), standardDefaults)))
	if err != nil {
		t.Fatal(err)
	}
	d, err := New(cfg, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer d.stopWork()
	if c, ok := d.checkers["disk"].(check.Exec); !ok || c.Command[0] != "/bin/true" || c.Timeout != 30*time.Second {
		t.Errorf("checker %#v", d.checkers["disk"])
	}
	if _, ok := d.notifiers["telegram"].(*notify.Exec); !ok {
		t.Errorf("notifier %#v", d.notifiers["telegram"])
	}
}

func TestRunNotifiesSystemdAndSavesOnShutdown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	cfg, err := config.Parse([]byte(fmt.Sprintf(testConfig, path, standardDefaults)))
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var sent []string
	disk := &fakeChecker{}
	disk.set(check.OK)
	d, err := New(cfg, Options{
		Checkers:  map[string]check.Checker{"disk": disk},
		Notifiers: map[string]notify.Notifier{"telegram": &recorder{}},
		Jitter:    func(time.Duration) time.Duration { return 0 },
		Watchdog:  time.Millisecond,
		SdNotify: func(s string) error {
			mu.Lock()
			defer mu.Unlock()
			sent = append(sent, s)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- d.Run(ctx) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not return")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sent) < 3 || sent[0] != "READY=1" || sent[1] != "WATCHDOG=1" || sent[len(sent)-1] != "STOPPING=1" {
		t.Errorf("sd_notify sequence %v", sent)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("state not saved: %v", err)
	}
}
