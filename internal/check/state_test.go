package check

import (
	"encoding/json"
	"testing"
)

func TestStateNames(t *testing.T) {
	for s, want := range map[State]string{OK: "OK", Warning: "WARNING", Critical: "CRITICAL", Unknown: "UNKNOWN", State(7): "State(7)"} {
		if got := s.String(); got != want {
			t.Errorf("State(%d).String() = %q, want %q", int(s), got, want)
		}
	}
}

func TestParseState(t *testing.T) {
	s, err := ParseState("CRITICAL")
	if err != nil || s != Critical {
		t.Fatalf("ParseState(CRITICAL) = %v, %v", s, err)
	}
	if _, err := ParseState("critical"); err == nil {
		t.Fatal("ParseState accepted lower case")
	}
}

func TestStateJSON(t *testing.T) {
	b, err := json.Marshal(map[string]State{"s": Warning})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"s":"WARNING"}` {
		t.Fatalf("marshal = %s", b)
	}
	var back map[string]State
	if err := json.Unmarshal(b, &back); err != nil || back["s"] != Warning {
		t.Fatalf("unmarshal = %v, %v", back, err)
	}
	if _, err := json.Marshal(State(9)); err == nil {
		t.Fatal("marshalled an invalid state")
	}
}
