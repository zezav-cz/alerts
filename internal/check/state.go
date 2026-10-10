// Package check runs checks that follow the monitoring-plugins convention:
// one line of output and an exit code of 0, 1, 2 or 3.
package check

import "fmt"

// State is a check's outcome, numbered like the plugin exit codes.
type State int

// The four plugin states, in exit-code order.
const (
	OK State = iota
	Warning
	Critical
	Unknown
)

var stateNames = [...]string{"OK", "WARNING", "CRITICAL", "UNKNOWN"}

// String returns the upper-case name, or State(n) for a value out of range.
func (s State) String() string {
	if s >= OK && s <= Unknown {
		return stateNames[s]
	}
	return fmt.Sprintf("State(%d)", int(s))
}

// ParseState is the inverse of String for the four valid states.
func ParseState(name string) (State, error) {
	for i, n := range stateNames {
		if n == name {
			return State(i), nil
		}
	}
	return 0, fmt.Errorf("unknown state %q (want OK, WARNING, CRITICAL or UNKNOWN)", name)
}

// MarshalText encodes the state by name, so JSON carries "CRITICAL", not 2.
func (s State) MarshalText() ([]byte, error) {
	if s < OK || s > Unknown {
		return nil, fmt.Errorf("invalid state %d", int(s))
	}
	return []byte(s.String()), nil
}

// UnmarshalText decodes a state name.
func (s *State) UnmarshalText(b []byte) error {
	v, err := ParseState(string(b))
	if err != nil {
		return err
	}
	*s = v
	return nil
}
