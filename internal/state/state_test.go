package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zezav-cz/alerts/internal/alert"
	"github.com/zezav-cz/alerts/internal/check"
	"github.com/zezav-cz/alerts/internal/policy"
)

var t0 = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

func sample() *State {
	s := New()
	s.Checks["disk"] = policy.CheckState{Confirmed: check.Critical, Since: t0, PendingState: check.Critical, LastOutput: "DISK CRITICAL", LastAlert: t0}
	s.Queues["telegram"] = []Queued{{
		Alert:    alert.NewProblem("z01", "disk", check.OK, check.Critical, "DISK CRITICAL", t0),
		Enqueued: t0, Attempts: 2, NextTry: t0.Add(time.Minute),
	}}
	return s
}

func TestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := Save(path, sample()); err != nil {
		t.Fatal(err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	disk := got.Checks["disk"]
	if disk.Confirmed != check.Critical || !disk.Since.Equal(t0) || disk.LastOutput != "DISK CRITICAL" {
		t.Errorf("check state %+v", disk)
	}
	q := got.Queues["telegram"]
	if len(q) != 1 || q[0].Attempts != 2 || q[0].Alert.Text != "🔴 z01 disk CRITICAL\nDISK CRITICAL" || !q[0].NextTry.Equal(t0.Add(time.Minute)) {
		t.Errorf("queue %+v", q)
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Errorf("temporary files left behind: %v", entries)
	}
}

func TestInvalidUTF8OutputSurvives(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := New()
	s.Checks["disk"] = policy.CheckState{LastOutput: "bad \xff byte"}
	if err := Save(path, s); err != nil {
		t.Fatal(err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if out := got.Checks["disk"].LastOutput; out != "bad \uFFFD byte" {
		t.Errorf("output %q", out)
	}
}

func TestMissingFileIsEmptyState(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "state.json"))
	if err != nil || len(s.Checks) != 0 || s.Version != Version {
		t.Fatalf("Load = %+v, %v", s, err)
	}
}

func TestCorruptFileIsMovedAside(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "starting with empty state") {
		t.Fatalf("err = %v", err)
	}
	if s == nil || len(s.Checks) != 0 {
		t.Fatalf("state %+v", s)
	}
	if b, rerr := os.ReadFile(path + ".corrupt"); rerr != nil || string(b) != "{not json" {
		t.Fatalf("corrupt copy: %q, %v", b, rerr)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("original still in place: %v", err)
	}
}

func TestUnsupportedVersionIsMovedAside(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"version": 2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "unsupported state version 2") {
		t.Fatalf("err = %v", err)
	}
}

func TestReadHasNoSideEffects(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path); err == nil {
		t.Fatal("Read accepted garbage")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Read moved the file: %v", err)
	}
}

func TestFailedSaveKeepsTheOldFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := Save(path, sample()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if err := Save(path, New()); err == nil {
		t.Fatal("Save into a read-only directory succeeded")
	}
	got, err := Read(path)
	if err != nil || got.Checks["disk"].Confirmed != check.Critical {
		t.Fatalf("old state lost: %+v, %v", got, err)
	}
}
