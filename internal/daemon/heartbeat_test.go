package daemon

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zezav-cz/alerts/internal/check"
	"github.com/zezav-cz/alerts/internal/config"
)

type received struct{ path, body string }

// heartbeatServer records every ping and returns the URL file pointing at it.
func heartbeatServer(t *testing.T) (urlFile string, pings chan received) {
	t.Helper()
	pings = make(chan received, 100)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		pings <- received{r.URL.Path, string(b)}
	}))
	t.Cleanup(srv.Close)
	urlFile = filepath.Join(t.TempDir(), "heartbeat-url")
	if err := os.WriteFile(urlFile, []byte(srv.URL+"/ping/abc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return urlFile, pings
}

func nextPing(t *testing.T, pings chan received) received {
	t.Helper()
	select {
	case p := <-pings:
		return p
	case <-time.After(5 * time.Second):
		t.Fatal("no heartbeat")
		return received{}
	}
}

func noPing(t *testing.T, pings chan received) {
	t.Helper()
	select {
	case p := <-pings:
		t.Fatalf("unexpected heartbeat %+v", p)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestHeartbeat(t *testing.T) {
	urlFile, pings := heartbeatServer(t)
	disk, tg := &fakeChecker{}, &recorder{}
	disk.set(check.OK)
	extra := fmt.Sprintf("heartbeat: {url_file: %s, every: 5m, fail_after: 15m}\n", urlFile)
	h := newHarness(t, filepath.Join(t.TempDir(), "state.json"), t0, disk, tg, "confirm: 1, interval: 1m", extra)

	h.advance(0)
	if p := nextPing(t, pings); p.path != "/ping/abc" || p.body != "" {
		t.Fatalf("first ping %+v", p)
	}
	h.advance(time.Minute)
	noPing(t, pings)

	// Telegram breaks; the alert waits in the queue.
	tg.fail(errors.New("down"))
	disk.set(check.Critical)
	h.advance(time.Minute)
	for range 3 {
		h.advance(time.Minute)
	}
	if p := nextPing(t, pings); p.path != "/ping/abc" {
		t.Fatalf("ping while the alert is young: %+v", p)
	}

	// Past fail_after, the ping turns into /fail.
	for range 15 {
		h.advance(time.Minute)
	}
	var p received
	for p = nextPing(t, pings); p.path != "/ping/abc/fail"; p = nextPing(t, pings) {
	}
	if !strings.Contains(p.body, "notifier telegram has had an alert undelivered for") {
		t.Fatalf("fail body %q", p.body)
	}
}

func TestNewRequiresAReadableHeartbeatURL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	extra := "heartbeat: {url_file: /nonexistent/heartbeat-url}\n"
	cfg, err := config.Parse([]byte(fmt.Sprintf(testConfig, path, standardDefaults) + extra))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(cfg, Options{}); err == nil || !strings.Contains(err.Error(), "heartbeat") {
		t.Fatalf("err = %v", err)
	}
}
