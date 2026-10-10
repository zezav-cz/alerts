package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zezav-cz/alerts/internal/alert"
	"github.com/zezav-cz/alerts/internal/check"
	"github.com/zezav-cz/alerts/internal/config"
)

var t0 = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

func problem() alert.Alert {
	return alert.NewProblem("z01", "disk", check.OK, check.Critical, "DISK CRITICAL <b>_x_</b>", t0)
}

// telegramServer answers every request with status and body, and records
// what it was sent.
func telegramServer(t *testing.T, status int, body string) (*httptest.Server, *[]map[string]any, *[]string) {
	t.Helper()
	var got []map[string]any
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		got = append(got, m)
		paths = append(paths, r.URL.Path)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &got, &paths
}

func newTestTelegram(srv *httptest.Server) *Telegram {
	tg := NewTelegram("telegram", "123:SECRET", "-100123", srv.Client())
	tg.baseURL = srv.URL
	return tg
}

func TestTelegramSendsPlainText(t *testing.T) {
	srv, got, paths := telegramServer(t, 200, `{"ok":true,"result":{}}`)
	if err := newTestTelegram(srv).Send(context.Background(), problem()); err != nil {
		t.Fatal(err)
	}
	if (*paths)[0] != "/bot123:SECRET/sendMessage" {
		t.Errorf("path %q", (*paths)[0])
	}
	m := (*got)[0]
	if m["chat_id"] != "-100123" || m["text"] != problem().Text {
		t.Errorf("payload %v", m)
	}
	if _, set := m["parse_mode"]; set {
		t.Error("parse_mode set; the text would need escaping")
	}
	if lp, _ := m["link_preview_options"].(map[string]any); lp["is_disabled"] != true {
		t.Errorf("link previews not disabled: %v", m["link_preview_options"])
	}
}

func TestTelegramFailures(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		want       string
		retryAfter time.Duration
	}{
		{"rate limited", 429, `{"ok":false,"description":"Too Many Requests: retry after 37","parameters":{"retry_after":37}}`, "HTTP 429", 37 * time.Second},
		{"server error", 502, `<html>bad gateway</html>`, "telegram: HTTP 502", 0},
		{"wrong chat", 400, `{"ok":false,"description":"Bad Request: chat not found"}`, "chat not found", 0},
		{"ok false on 200", 200, `{"ok":false,"description":"odd"}`, "request not ok: odd", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _, _ := telegramServer(t, tc.status, tc.body)
			err := newTestTelegram(srv).Send(context.Background(), problem())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
			if got := RetryAfter(err); got != tc.retryAfter {
				t.Errorf("RetryAfter = %s, want %s", got, tc.retryAfter)
			}
		})
	}
}

func TestTelegramRedactsTheToken(t *testing.T) {
	tg := NewTelegram("telegram", "123:SECRET", "-1", &http.Client{Timeout: time.Second})
	tg.baseURL = "http://127.0.0.1:1" // nothing listens here
	err := tg.Send(context.Background(), problem())
	if err == nil {
		t.Fatal("expected a connection error")
	}
	if strings.Contains(err.Error(), "SECRET") || !strings.Contains(err.Error(), "<token>") {
		t.Errorf("token not redacted: %v", err)
	}
}

func TestExecWritesTheAlertToStdin(t *testing.T) {
	dir := t.TempDir()
	n := NewExec("hook", []string{"testdata/capture"}, map[string]string{"CAPTURE_DIR": dir, "TOPIC": "zezav"}, 5*time.Second)
	if err := n.Send(context.Background(), problem()); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "stdin.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got alert.Alert
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	want := problem()
	if got.Kind != want.Kind || got.Check != "disk" || got.State != check.Critical || got.Text != want.Text || !got.Time.Equal(t0) {
		t.Errorf("stdin alert %+v", got)
	}
	env, _ := os.ReadFile(filepath.Join(dir, "env"))
	if !strings.Contains(string(env), "TOPIC=zezav") {
		t.Errorf("env not passed:\n%s", env)
	}
}

func TestExecNotifierMayIgnoreStdin(t *testing.T) {
	if err := NewExec("hook", []string{"true"}, nil, 5*time.Second).Send(context.Background(), problem()); err != nil {
		t.Fatalf("a command that exits 0 without reading stdin failed: %v", err)
	}
}

func TestExecFailure(t *testing.T) {
	err := NewExec("hook", []string{"testdata/fail"}, nil, 5*time.Second).Send(context.Background(), problem())
	if err == nil || err.Error() != "testdata/fail: exit status 1: no route to ntfy.sh" {
		t.Fatalf("err = %v", err)
	}
	err = NewExec("hook", []string{"testdata/slow"}, nil, 200*time.Millisecond).Send(context.Background(), problem())
	if err == nil || err.Error() != "testdata/slow: timed out after 200ms" {
		t.Fatalf("err = %v", err)
	}
}

func TestBuild(t *testing.T) {
	dir := t.TempDir()
	token := filepath.Join(dir, "token")
	if err := os.WriteFile(token, []byte("123:SECRET\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	n, err := Build(config.Notifier{Name: "tg", Type: "telegram", TokenFile: token, ChatID: "-1"}, http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	if tg := n.(*Telegram); tg.token != "123:SECRET" || tg.Name() != "tg" {
		t.Errorf("telegram %+v", tg)
	}

	if _, err := Build(config.Notifier{Name: "tg", Type: "telegram", TokenFile: filepath.Join(dir, "missing")}, http.DefaultClient); err == nil {
		t.Error("missing token file accepted")
	}
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(config.Notifier{Name: "tg", Type: "telegram", TokenFile: empty}, http.DefaultClient); err == nil {
		t.Error("empty token file accepted")
	}
	if n, err := Build(config.Notifier{Name: "hook", Type: "exec", Command: []string{"/bin/true"}}, nil); err != nil || n.Name() != "hook" {
		t.Errorf("exec: %v, %v", n, err)
	}
}

func TestRoute(t *testing.T) {
	routes := []config.Route{
		{Notify: []string{"telegram"}},
		{States: []check.State{check.Critical}, Notify: []string{"phone", "telegram"}},
		{Checks: []string{"throttle"}, Notify: []string{"parents"}},
	}
	cases := []struct {
		name string
		a    alert.Alert
		want []string
	}{
		{"warning", alert.NewProblem("z01", "disk", check.OK, check.Warning, "", t0), []string{"telegram"}},
		{"critical deduplicates", alert.NewProblem("z01", "disk", check.OK, check.Critical, "", t0), []string{"telegram", "phone"}},
		{"recovery follows the problem", alert.NewRecovery("z01", "disk", check.Critical, t0, "", t0), []string{"telegram", "phone"}},
		{"check matcher", alert.NewProblem("rpi01", "throttle", check.OK, check.Warning, "", t0), []string{"telegram", "parents"}},
	}
	for _, tc := range cases {
		if got := Route(routes, tc.a); !slices.Equal(got, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
