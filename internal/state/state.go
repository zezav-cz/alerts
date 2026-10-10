// Package state persists what the daemon must not forget across restarts:
// each check's policy state and every notifier's undelivered alerts.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/zezav-cz/alerts/internal/alert"
	"github.com/zezav-cz/alerts/internal/policy"
)

// Version is the state file format this build reads and writes.
const Version = 1

// Queued is an alert waiting for one notifier.
type Queued struct {
	Alert    alert.Alert `json:"alert"`
	Enqueued time.Time   `json:"enqueued"`
	Attempts int         `json:"attempts"`
	NextTry  time.Time   `json:"next_try"`
}

// State is the whole persisted state.
type State struct {
	Version int                          `json:"version"`
	Checks  map[string]policy.CheckState `json:"checks"`
	Queues  map[string][]Queued          `json:"queues"`
}

// New returns an empty state.
func New() *State {
	return &State{Version: Version, Checks: map[string]policy.CheckState{}, Queues: map[string][]Queued{}}
}

// Read parses the state file without side effects. A missing file is an
// empty state, not an error.
func Read(path string) (*State, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return New(), nil
	}
	if err != nil {
		return nil, err
	}
	s := New()
	if err := json.Unmarshal(data, s); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if s.Version != Version {
		return nil, fmt.Errorf("%s: unsupported state version %d", path, s.Version)
	}
	if s.Checks == nil {
		s.Checks = map[string]policy.CheckState{}
	}
	if s.Queues == nil {
		s.Queues = map[string][]Queued{}
	}
	return s, nil
}

// Load is Read for the daemon. A state file that cannot be used is moved
// aside to path+".corrupt" and replaced by an empty state: the worst case is
// a repeated alert, never silence. The returned state is never nil; a non-nil
// error says what was discarded and should be logged.
func Load(path string) (*State, error) {
	s, err := Read(path)
	if err == nil {
		return s, nil
	}
	if rerr := os.Rename(path, path+".corrupt"); rerr != nil {
		err = fmt.Errorf("%w (moving it aside failed too: %v)", err, rerr)
	}
	return New(), fmt.Errorf("starting with empty state: %w", err)
}

// Save writes s atomically: a temporary file in the same directory, fsynced,
// then renamed over path. An interrupted Save leaves the old file intact.
func Save(path string, s *State) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".state-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // a no-op once the rename succeeded
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
