package sdnotify

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestSendWithoutSocketIsANoOp(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", "")
	if err := Send("READY=1"); err != nil {
		t.Fatal(err)
	}
}

func TestSend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notify.sock")
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	t.Setenv("NOTIFY_SOCKET", path)

	if err := Send("READY=1"); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := conn.Read(buf)
	if err != nil || string(buf[:n]) != "READY=1" {
		t.Fatalf("read %q, %v", buf[:n], err)
	}
}

func TestWatchdogInterval(t *testing.T) {
	t.Setenv("WATCHDOG_PID", "")
	t.Setenv("WATCHDOG_USEC", "")
	if got := WatchdogInterval(); got != 0 {
		t.Errorf("unset: %s", got)
	}
	t.Setenv("WATCHDOG_USEC", "60000000")
	if got := WatchdogInterval(); got != 30*time.Second {
		t.Errorf("60s watchdog: %s", got)
	}
	t.Setenv("WATCHDOG_PID", strconv.Itoa(os.Getpid()))
	if got := WatchdogInterval(); got != 30*time.Second {
		t.Errorf("our pid: %s", got)
	}
	t.Setenv("WATCHDOG_PID", "1")
	if got := WatchdogInterval(); got != 0 {
		t.Errorf("another pid: %s", got)
	}
}
