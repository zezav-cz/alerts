// Package config loads and validates the alerts YAML configuration.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/zezav-cz/alerts/internal/check"
)

// Paths used when nothing else is given.
const (
	DefaultPath      = "/etc/alerts/config.yaml"
	DefaultStateFile = "/var/lib/alerts/state.json"
)

// Config is a validated configuration with every default filled in.
type Config struct {
	Host      string
	StateFile string
	Heartbeat *Heartbeat // nil when not configured
	Notifiers []Notifier
	Routes    []Route
	Checks    []Check
}

// Heartbeat is the dead man's switch ping.
type Heartbeat struct {
	URLFile   string
	Every     time.Duration
	FailAfter time.Duration
}

// Notifier is one delivery channel. TokenFile and ChatID apply to telegram,
// Command and Env to exec; Timeout to both.
type Notifier struct {
	Name      string
	Type      string
	TokenFile string
	ChatID    string
	Command   []string
	Env       map[string]string
	Timeout   time.Duration
}

// Route sends alerts matching all of its non-empty matchers to Notify.
type Route struct {
	States []check.State
	Checks []string
	Notify []string
}

// Matches reports whether the route applies to an alert for check name,
// routed by state s.
func (r Route) Matches(name string, s check.State) bool {
	return (len(r.States) == 0 || slices.Contains(r.States, s)) &&
		(len(r.Checks) == 0 || slices.Contains(r.Checks, name))
}

// Check is one scheduled check with its effective settings.
type Check struct {
	Name          string
	Type          string
	Command       []string
	Interval      time.Duration
	RetryInterval time.Duration
	Timeout       time.Duration
	Confirm       int
	Repeat        time.Duration
}

// Load reads and validates the file at path. An empty host defaults to the
// machine's short hostname.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s:\n%w", path, err)
	}
	if cfg.Host == "" {
		h, err := os.Hostname()
		if err != nil {
			return nil, fmt.Errorf("host is not set and the hostname is unavailable: %w", err)
		}
		cfg.Host, _, _ = strings.Cut(h, ".")
	}
	return cfg, nil
}

// Parse decodes and validates a configuration document. Unknown keys are
// errors. Validation problems are all reported together, one per line.
func Parse(data []byte) (*Config, error) {
	var raw rawConfig
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&raw); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("config is empty")
		}
		return nil, err
	}
	v := &validator{}
	cfg := v.build(&raw)
	if err := errors.Join(v.errs...); err != nil {
		return nil, err
	}
	return cfg, nil
}
