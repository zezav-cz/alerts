package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zezav-cz/alerts/internal/check"
)

const full = `
host: z01
defaults:
  interval: 2m
  confirm: 2
heartbeat:
  url_file: /etc/alerts/heartbeat-url
notifiers:
  - name: telegram
    type: telegram
    token_file: /etc/alerts/telegram-token
    chat_id: "-100123456"
  - name: ntfy-phone
    type: exec
    command: [/usr/local/libexec/alerts/notify-ntfy]
    env: { NTFY_TOPIC_FILE: /etc/alerts/ntfy-topic }
    timeout: 10s
routes:
  - notify: [telegram]
  - states: [CRITICAL]
    notify: [ntfy-phone]
checks:
  - name: disk
    command: [/usr/lib/nagios/plugins/check_disk, -w, 15%, -c, 10%, -p, /]
  - name: cert-zezav
    type: exec
    command: [/usr/lib/nagios/plugins/check_curl, -H, zezav.cz, -S, -C, "14,7"]
    interval: 1h
    repeat: 0
`

func TestParseFull(t *testing.T) {
	cfg, err := Parse([]byte(full))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host != "z01" || cfg.StateFile != DefaultStateFile {
		t.Errorf("host %q, state file %q", cfg.Host, cfg.StateFile)
	}
	if hb := cfg.Heartbeat; hb == nil || hb.Every != 5*time.Minute || hb.FailAfter != 15*time.Minute {
		t.Errorf("heartbeat defaults: %+v", hb)
	}

	tg, ntfy := cfg.Notifiers[0], cfg.Notifiers[1]
	if tg.ChatID != "-100123456" || tg.TokenFile != "/etc/alerts/telegram-token" || tg.Timeout != 30*time.Second {
		t.Errorf("telegram: %+v", tg)
	}
	if ntfy.Env["NTFY_TOPIC_FILE"] != "/etc/alerts/ntfy-topic" || ntfy.Timeout != 10*time.Second {
		t.Errorf("ntfy: %+v", ntfy)
	}

	disk, cert := cfg.Checks[0], cfg.Checks[1]
	wantDisk := Check{
		Name: "disk", Type: "exec",
		Command:  []string{"/usr/lib/nagios/plugins/check_disk", "-w", "15%", "-c", "10%", "-p", "/"},
		Interval: 2 * time.Minute, RetryInterval: 30 * time.Second, Timeout: 30 * time.Second, Confirm: 2, Repeat: 6 * time.Hour,
	}
	if !equalCheck(disk, wantDisk) {
		t.Errorf("disk:\n got %+v\nwant %+v", disk, wantDisk)
	}
	if cert.Interval != time.Hour || cert.Repeat != 0 || cert.Confirm != 2 {
		t.Errorf("cert overrides: %+v", cert)
	}
	if !slices.Equal(cfg.Routes[1].States, []check.State{check.Critical}) {
		t.Errorf("route states: %v", cfg.Routes[1].States)
	}
}

func equalCheck(a, b Check) bool {
	return a.Name == b.Name && a.Type == b.Type && slices.Equal(a.Command, b.Command) &&
		a.Interval == b.Interval && a.RetryInterval == b.RetryInterval && a.Timeout == b.Timeout &&
		a.Confirm == b.Confirm && a.Repeat == b.Repeat
}

func TestRouteMatches(t *testing.T) {
	all := Route{Notify: []string{"x"}}
	crit := Route{States: []check.State{check.Critical}, Notify: []string{"x"}}
	disk := Route{Checks: []string{"disk"}, States: []check.State{check.Warning}, Notify: []string{"x"}}
	if !all.Matches("anything", check.Unknown) {
		t.Error("empty matchers must match everything")
	}
	if !crit.Matches("disk", check.Critical) || crit.Matches("disk", check.Warning) {
		t.Error("states matcher")
	}
	if !disk.Matches("disk", check.Warning) || disk.Matches("load", check.Warning) || disk.Matches("disk", check.Critical) {
		t.Error("both matchers must hold")
	}
}

// minimal is a valid document that each error case breaks in one place.
const minimal = `
notifiers:
  - {name: tg, type: telegram, token_file: /t, chat_id: "-1"}
routes:
  - notify: [tg]
checks:
  - {name: disk, command: [/bin/true]}
`

func TestParseErrors(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		want string
	}{
		{"empty", ``, "config is empty"},
		{"unknown key", minimal + "bogus: 1\n", "field bogus not found"},
		{"unknown nested key", strings.Replace(minimal, "command: [/bin/true]", "command: [/bin/true], every: 1m", 1), "field every not found"},
		{"bad duration", minimal + "defaults: {interval: soon}\n", `defaults.interval: "soon" is not a duration`},
		{"duration without unit", minimal + "defaults: {timeout: 30}\n", `defaults.timeout: "30" is not a duration`},
		{"zero interval", minimal + "defaults: {interval: 0s}\n", "defaults.interval: must be positive"},
		{"confirm below one", minimal + "defaults: {confirm: 0}\n", "defaults.confirm: must be at least 1"},
		{"unquoted chat id", strings.Replace(minimal, `chat_id: "-1"`, `chat_id: -100123`, 1), `chat_id: must be a quoted string, e.g. chat_id: "-100123"`},
		{"missing token file", strings.Replace(minimal, "token_file: /t, ", "", 1), "notifiers[0] (tg).token_file: required for telegram"},
		{"unknown notifier type", strings.Replace(minimal, "type: telegram", "type: email", 1), `unknown type "email"`},
		{"bad name", strings.Replace(minimal, "name: disk", "name: Disk", 1), `checks[0].name: "Disk" must match`},
		{"duplicate check", minimal + "  - {name: disk, command: [/bin/false]}\n", `checks[1].name: duplicate "disk"`},
		{"check without command", strings.Replace(minimal, "command: [/bin/true]", "command: []", 1), "checks[0] (disk).command: required"},
		{"unknown check type", strings.Replace(minimal, "{name: disk,", "{name: disk, type: http,", 1), `unknown type "http" (only exec`},
		{"route to unknown notifier", strings.Replace(minimal, "notify: [tg]", "notify: [tg, sms]", 1), `routes[0].notify: unknown notifier "sms"`},
		{"route for unknown check", strings.Replace(minimal, "notify: [tg]", "{checks: [load], notify: [tg]}", 1), `routes[0].checks: unknown check "load"`},
		{"route with OK", strings.Replace(minimal, "notify: [tg]", "{states: [OK], notify: [tg]}", 1), "OK cannot be routed"},
		{"route with bad state", strings.Replace(minimal, "notify: [tg]", "{states: [critical], notify: [tg]}", 1), `unknown state "critical"`},
		{"unrouted state", strings.Replace(minimal, "notify: [tg]", "{states: [CRITICAL], notify: [tg]}", 1), "check disk: a WARNING alert would match no route"},
		{"no checks", strings.Split(minimal, "checks:")[0], "checks: at least one check is required"},
		{"heartbeat without url file", minimal + "heartbeat: {every: 1m}\n", "heartbeat.url_file: required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.doc))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v\nwant it to contain %q", err, tc.want)
			}
		})
	}
}

func TestParseExecNotifierWithoutCommand(t *testing.T) {
	doc := strings.Replace(minimal, "  - {name: tg, type: telegram, token_file: /t, chat_id: \"-1\"}",
		"  - {name: tg, type: telegram, token_file: /t, chat_id: \"-1\"}\n  - {name: hook, type: exec}", 1)
	_, err := Parse([]byte(doc))
	if err == nil || !strings.Contains(err.Error(), "notifiers[1] (hook).command: required for exec") {
		t.Fatalf("error = %v", err)
	}
}

func TestParseReportsAllErrors(t *testing.T) {
	doc := strings.Replace(strings.Replace(minimal, "name: disk", "name: Disk", 1), "notify: [tg]", "notify: [sms]", 1)
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("no error")
	}
	if lines := strings.Split(err.Error(), "\n"); len(lines) < 2 {
		t.Fatalf("want several errors, got %q", err)
	}
}

func TestLoadDefaultsHostToShortHostname(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(minimal), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	h, _ := os.Hostname()
	if want, _, _ := strings.Cut(h, "."); cfg.Host != want {
		t.Errorf("host %q, want %q", cfg.Host, want)
	}
}

func TestLoadPrefixesErrorsWithThePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("bogus: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.HasPrefix(err.Error(), path+":") {
		t.Fatalf("error = %v", err)
	}
}
