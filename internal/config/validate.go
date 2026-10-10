package config

import (
	"fmt"
	"regexp"
	"slices"
	"time"

	"github.com/zezav-cz/alerts/internal/check"
)

// The YAML shape. Optional settings are pointers so "absent" and "zero" differ.
type rawConfig struct {
	Host      string        `yaml:"host"`
	StateFile string        `yaml:"state_file"`
	Defaults  rawSettings   `yaml:"defaults"`
	Heartbeat *rawHeartbeat `yaml:"heartbeat"`
	Notifiers []rawNotifier `yaml:"notifiers"`
	Routes    []rawRoute    `yaml:"routes"`
	Checks    []rawCheck    `yaml:"checks"`
}

type rawSettings struct {
	Interval      *string `yaml:"interval"`
	RetryInterval *string `yaml:"retry_interval"`
	Timeout       *string `yaml:"timeout"`
	Confirm       *int    `yaml:"confirm"`
	Repeat        *string `yaml:"repeat"`
}

type rawHeartbeat struct {
	URLFile   string  `yaml:"url_file"`
	Every     *string `yaml:"every"`
	FailAfter *string `yaml:"fail_after"`
}

type rawNotifier struct {
	Name      string            `yaml:"name"`
	Type      string            `yaml:"type"`
	TokenFile string            `yaml:"token_file"`
	ChatID    any               `yaml:"chat_id"` // any, to tell a quoted string from a number
	Command   []string          `yaml:"command"`
	Env       map[string]string `yaml:"env"`
	Timeout   *string           `yaml:"timeout"`
}

type rawRoute struct {
	States []string `yaml:"states"`
	Checks []string `yaml:"checks"`
	Notify []string `yaml:"notify"`
}

type rawCheck struct {
	Name        string   `yaml:"name"`
	Type        string   `yaml:"type"`
	Command     []string `yaml:"command"`
	rawSettings `yaml:",inline"`
}

// settings are the per-check knobs that `defaults` provides.
type settings struct {
	Interval, RetryInterval, Timeout, Repeat time.Duration
	Confirm                                  int
}

var builtinDefaults = settings{
	Interval:      time.Minute,
	RetryInterval: 30 * time.Second,
	Timeout:       30 * time.Second,
	Confirm:       3,
	Repeat:        6 * time.Hour,
}

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// problemStates are the states an alert can be routed by.
var problemStates = []check.State{check.Warning, check.Critical, check.Unknown}

type validator struct{ errs []error }

func (v *validator) addf(format string, args ...any) {
	v.errs = append(v.errs, fmt.Errorf(format, args...))
}

func (v *validator) build(raw *rawConfig) *Config {
	cfg := &Config{Host: raw.Host, StateFile: raw.StateFile}
	if cfg.StateFile == "" {
		cfg.StateFile = DefaultStateFile
	}
	base := v.settings("defaults", raw.Defaults, builtinDefaults)

	if h := raw.Heartbeat; h != nil {
		if h.URLFile == "" {
			v.addf("heartbeat.url_file: required")
		}
		cfg.Heartbeat = &Heartbeat{
			URLFile:   h.URLFile,
			Every:     v.duration("heartbeat.every", h.Every, 5*time.Minute, false),
			FailAfter: v.duration("heartbeat.fail_after", h.FailAfter, 15*time.Minute, false),
		}
	}

	notifierNames := map[string]bool{}
	for i, rn := range raw.Notifiers {
		where := v.name(fmt.Sprintf("notifiers[%d]", i), rn.Name, notifierNames)
		cfg.Notifiers = append(cfg.Notifiers, v.notifier(where, rn))
	}

	checkNames := map[string]bool{}
	for i, rc := range raw.Checks {
		where := v.name(fmt.Sprintf("checks[%d]", i), rc.Name, checkNames)
		c := Check{Name: rc.Name, Type: rc.Type, Command: rc.Command}
		if c.Type == "" {
			c.Type = "exec"
		} else if c.Type != "exec" {
			v.addf("%s.type: unknown type %q (only exec in this version)", where, c.Type)
		}
		if len(c.Command) == 0 {
			v.addf("%s.command: required", where)
		}
		s := v.settings(where, rc.rawSettings, base)
		c.Interval, c.RetryInterval, c.Timeout, c.Confirm, c.Repeat = s.Interval, s.RetryInterval, s.Timeout, s.Confirm, s.Repeat
		cfg.Checks = append(cfg.Checks, c)
	}

	if len(raw.Checks) == 0 {
		v.addf("checks: at least one check is required")
	}

	for i, rr := range raw.Routes {
		cfg.Routes = append(cfg.Routes, v.route(fmt.Sprintf("routes[%d]", i), rr, checkNames, notifierNames))
	}

	for _, c := range cfg.Checks {
		for _, s := range problemStates {
			if !slices.ContainsFunc(cfg.Routes, func(r Route) bool { return r.Matches(c.Name, s) }) {
				v.addf("check %s: a %s alert would match no route", c.Name, s)
			}
		}
	}
	return cfg
}

// name validates a check or notifier name and returns the location prefix
// for further messages about the same entry.
func (v *validator) name(where, name string, seen map[string]bool) string {
	switch {
	case name == "":
		v.addf("%s.name: required", where)
		return where
	case !namePattern.MatchString(name):
		v.addf("%s.name: %q must match %s", where, name, namePattern)
	case seen[name]:
		v.addf("%s.name: duplicate %q", where, name)
	}
	seen[name] = true
	return fmt.Sprintf("%s (%s)", where, name)
}

func (v *validator) notifier(where string, rn rawNotifier) Notifier {
	n := Notifier{
		Name: rn.Name, Type: rn.Type, TokenFile: rn.TokenFile, Command: rn.Command, Env: rn.Env,
		Timeout: v.duration(where+".timeout", rn.Timeout, 30*time.Second, false),
	}
	switch rn.Type {
	case "telegram":
		if rn.TokenFile == "" {
			v.addf("%s.token_file: required for telegram", where)
		}
		switch id := rn.ChatID.(type) {
		case nil:
			v.addf("%s.chat_id: required for telegram", where)
		case string:
			if id == "" {
				v.addf("%s.chat_id: required for telegram", where)
			}
			n.ChatID = id
		default:
			v.addf(`%s.chat_id: must be a quoted string, e.g. chat_id: "%v"`, where, id)
		}
		if len(rn.Command) > 0 || len(rn.Env) > 0 {
			v.addf("%s: command and env apply to exec notifiers only", where)
		}
	case "exec":
		if len(rn.Command) == 0 {
			v.addf("%s.command: required for exec", where)
		}
		if rn.TokenFile != "" || rn.ChatID != nil {
			v.addf("%s: token_file and chat_id apply to telegram notifiers only", where)
		}
	case "":
		v.addf("%s.type: required (telegram or exec)", where)
	default:
		v.addf("%s.type: unknown type %q (want telegram or exec)", where, rn.Type)
	}
	return n
}

func (v *validator) route(where string, rr rawRoute, checks, notifiers map[string]bool) Route {
	r := Route{Checks: rr.Checks, Notify: rr.Notify}
	for _, name := range rr.States {
		s, err := check.ParseState(name)
		switch {
		case err != nil:
			v.addf("%s.states: %v", where, err)
		case s == check.OK:
			v.addf("%s.states: OK cannot be routed; a recovery follows the state it recovered from", where)
		default:
			r.States = append(r.States, s)
		}
	}
	for _, c := range rr.Checks {
		if !checks[c] {
			v.addf("%s.checks: unknown check %q", where, c)
		}
	}
	if len(rr.Notify) == 0 {
		v.addf("%s.notify: required", where)
	}
	for _, n := range rr.Notify {
		if !notifiers[n] {
			v.addf("%s.notify: unknown notifier %q", where, n)
		}
	}
	return r
}

func (v *validator) settings(where string, r rawSettings, base settings) settings {
	s := settings{
		Interval:      v.duration(where+".interval", r.Interval, base.Interval, false),
		RetryInterval: v.duration(where+".retry_interval", r.RetryInterval, base.RetryInterval, false),
		Timeout:       v.duration(where+".timeout", r.Timeout, base.Timeout, false),
		Repeat:        v.duration(where+".repeat", r.Repeat, base.Repeat, true),
		Confirm:       base.Confirm,
	}
	if r.Confirm != nil {
		if *r.Confirm < 1 {
			v.addf("%s.confirm: must be at least 1, got %d", where, *r.Confirm)
		} else {
			s.Confirm = *r.Confirm
		}
	}
	return s
}

// duration parses an optional duration, falling back to def when absent.
func (v *validator) duration(where string, s *string, def time.Duration, allowZero bool) time.Duration {
	if s == nil {
		return def
	}
	d, err := time.ParseDuration(*s)
	switch {
	case err != nil:
		v.addf("%s: %q is not a duration like 30s, 5m or 6h", where, *s)
	case d < 0 || (d == 0 && !allowZero):
		v.addf("%s: must be positive, got %s", where, *s)
	default:
		return d
	}
	return def
}
