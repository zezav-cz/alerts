// Package sdnotify speaks systemd's notify protocol (READY=1, WATCHDOG=1,
// STOPPING=1) without a library.
package sdnotify

import (
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// Send writes state to $NOTIFY_SOCKET. Without that variable, i.e. when not
// run by systemd with Type=notify, it does nothing.
func Send(state string) error {
	addr := os.Getenv("NOTIFY_SOCKET")
	if addr == "" {
		return nil
	}
	if strings.HasPrefix(addr, "@") {
		addr = "\x00" + addr[1:] // abstract socket
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: addr, Net: "unixgram"})
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.Write([]byte(state))
	return err
}

// WatchdogInterval is how often to send WATCHDOG=1: half of $WATCHDOG_USEC,
// or 0 when systemd expects no keep-alives from this process.
func WatchdogInterval() time.Duration {
	if pid := os.Getenv("WATCHDOG_PID"); pid != "" && pid != strconv.Itoa(os.Getpid()) {
		return 0
	}
	usec, err := strconv.ParseInt(os.Getenv("WATCHDOG_USEC"), 10, 64)
	if err != nil || usec <= 0 {
		return 0
	}
	return time.Duration(usec) * time.Microsecond / 2
}
