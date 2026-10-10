package daemon

import (
	"context"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/zezav-cz/alerts/internal/alert"
)

// heartbeat pings the dead man's switch when due. If a notifier has had an
// alert undelivered for longer than fail_after it pings <url>/fail instead,
// so a broken channel is reported by the switch's own alerting.
func (d *Daemon) heartbeat(now time.Time) {
	hb := d.cfg.Heartbeat
	if hb == nil || now.Before(d.nextBeat) {
		return
	}
	d.nextBeat = now.Add(hb.Every)

	url, body := d.heartbeatURL, ""
	if name, age := d.oldestUndelivered(now); age > hb.FailAfter {
		url += "/fail"
		body = fmt.Sprintf("notifier %s has had an alert undelivered for %s", name, alert.Duration(age))
		d.log.Warn("reporting failure to the heartbeat", "notifier", name, "undelivered_for", age)
	}
	go func() {
		ctx, cancel := context.WithTimeout(d.work, 10*time.Second)
		defer cancel()
		if err := ping(ctx, d.client, url, body); err != nil {
			// The URL is a secret: anyone holding it can report this host healthy.
			d.log.Warn("heartbeat failed", "err", strings.ReplaceAll(err.Error(), d.heartbeatURL, "<heartbeat-url>"))
		}
	}()
}

// oldestUndelivered returns the notifier whose queue head has waited longest,
// and for how long.
func (d *Daemon) oldestUndelivered(now time.Time) (string, time.Duration) {
	var name string
	var age time.Duration
	for _, n := range slices.Sorted(maps.Keys(d.st.Queues)) {
		if q := d.st.Queues[n]; len(q) > 0 && now.Sub(q[0].Enqueued) > age {
			name, age = n, now.Sub(q[0].Enqueued)
		}
	}
	return name, age
}

func ping(ctx context.Context, client *http.Client, url, body string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}
