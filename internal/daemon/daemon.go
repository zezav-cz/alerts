// Package daemon is the long-running core. One goroutine, the core loop,
// owns all state: it schedules checks, runs results through the policy,
// persists, and hands alerts to per-notifier delivery. Checks and deliveries
// run in goroutines of their own and report back over channels.
package daemon

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/zezav-cz/alerts/internal/alert"
	"github.com/zezav-cz/alerts/internal/check"
	"github.com/zezav-cz/alerts/internal/config"
	"github.com/zezav-cz/alerts/internal/notify"
	"github.com/zezav-cz/alerts/internal/policy"
	"github.com/zezav-cz/alerts/internal/state"
)

const (
	tickEvery     = time.Second      // resolution of all scheduling
	startSpread   = 10 * time.Second // first runs are spread over this window
	maxQueueAge   = 24 * time.Hour   // undelivered alerts older than this are dropped
	backoffMin    = 10 * time.Second
	backoffMax    = 10 * time.Minute
	maxWait       = time.Hour // longest retry wait, even if a channel asks for more
	shutdownGrace = 10 * time.Second
)

// Options are the daemon's dependencies. Zero values select the real thing;
// tests replace them.
type Options struct {
	Logger     *slog.Logger
	Now        func() time.Time
	Jitter     func(max time.Duration) time.Duration // uniform in [0, max) by default
	Checkers   map[string]check.Checker              // by check name; default built from config
	Notifiers  map[string]notify.Notifier            // by notifier name; default built from config
	HTTPClient *http.Client
	SdNotify   func(state string) error // nil: not run under systemd
	Watchdog   time.Duration            // how often to send WATCHDOG=1; 0 disables
}

// Daemon is the running core. Build it with New, start it with Run.
type Daemon struct {
	cfg      *config.Config
	log      *slog.Logger
	now      func() time.Time
	client   *http.Client
	sdNotify func(string) error
	watchdog time.Duration

	checks    map[string]config.Check
	checkers  map[string]check.Checker
	notifiers map[string]notify.Notifier
	timeouts  map[string]time.Duration

	st       *state.State
	dirty    bool
	next     map[string]time.Time // next run per check
	running  map[string]bool      // check in flight
	sending  map[string]bool      // notifier in flight
	inflight int
	results  chan checkDone
	outcomes chan sendDone
	work     context.Context // canceled only after the shutdown grace
	stopWork context.CancelFunc

	heartbeatURL string
	nextBeat     time.Time
	lastWatchdog time.Time
}

type checkDone struct {
	name string
	res  check.Result
}

type sendDone struct {
	notifier string
	err      error
}

// New builds the daemon: checkers and notifiers from the config, state from
// disk. It fails on anything that would stop alerts from being delivered,
// such as an unreadable token or heartbeat file.
func New(cfg *config.Config, opts Options) (*Daemon, error) {
	d := &Daemon{
		cfg:       cfg,
		log:       opts.Logger,
		now:       opts.Now,
		client:    opts.HTTPClient,
		sdNotify:  opts.SdNotify,
		watchdog:  opts.Watchdog,
		checks:    map[string]config.Check{},
		checkers:  map[string]check.Checker{},
		notifiers: map[string]notify.Notifier{},
		timeouts:  map[string]time.Duration{},
		next:      map[string]time.Time{},
		running:   map[string]bool{},
		sending:   map[string]bool{},
		results:   make(chan checkDone, len(cfg.Checks)),
		outcomes:  make(chan sendDone, len(cfg.Notifiers)),
	}
	if d.log == nil {
		d.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if d.now == nil {
		d.now = time.Now
	}
	if d.client == nil {
		d.client = &http.Client{Timeout: 30 * time.Second}
	}
	jitter := opts.Jitter
	if jitter == nil {
		jitter = func(max time.Duration) time.Duration { return rand.N(max) }
	}

	for _, c := range cfg.Checks {
		d.checks[c.Name] = c
		if ch, ok := opts.Checkers[c.Name]; ok {
			d.checkers[c.Name] = ch
		} else {
			d.checkers[c.Name] = check.Exec{Command: c.Command, Timeout: c.Timeout}
		}
	}
	for _, n := range cfg.Notifiers {
		d.timeouts[n.Name] = n.Timeout
		if nt, ok := opts.Notifiers[n.Name]; ok {
			d.notifiers[n.Name] = nt
			continue
		}
		nt, err := notify.Build(n, d.client)
		if err != nil {
			return nil, err
		}
		d.notifiers[n.Name] = nt
	}
	if hb := cfg.Heartbeat; hb != nil {
		b, err := os.ReadFile(hb.URLFile)
		if err != nil {
			return nil, fmt.Errorf("heartbeat: %w", err)
		}
		if d.heartbeatURL = strings.TrimSpace(string(b)); d.heartbeatURL == "" {
			return nil, fmt.Errorf("heartbeat: %s is empty", hb.URLFile)
		}
	}

	st, err := state.Load(cfg.StateFile)
	if err != nil {
		d.log.Error("state file unusable", "err", err)
	}
	d.st = st
	now := d.now()
	for name := range st.Checks {
		if _, ok := d.checks[name]; !ok {
			delete(st.Checks, name)
		}
	}
	for name, q := range st.Queues {
		if _, ok := d.notifiers[name]; !ok {
			d.log.Warn("dropping the queue of a removed notifier", "notifier", name, "alerts", len(q))
			delete(st.Queues, name)
		}
	}
	for _, c := range cfg.Checks {
		if _, ok := st.Checks[c.Name]; !ok {
			st.Checks[c.Name] = policy.Initial(now)
		}
		d.next[c.Name] = now.Add(jitter(startSpread))
	}
	// Writing once now turns a missing or read-only state directory into a
	// start-up failure instead of an error logged every second.
	if err := state.Save(cfg.StateFile, st); err != nil {
		return nil, fmt.Errorf("state file not writable: %w", err)
	}
	d.work, d.stopWork = context.WithCancel(context.Background())
	return d, nil
}

// Run is the core loop. It returns after ctx is canceled and in-flight work
// has finished or the shutdown grace has passed.
func (d *Daemon) Run(ctx context.Context) error {
	d.notify("READY=1")
	ticker := time.NewTicker(tickEvery)
	defer ticker.Stop()
	d.tick(d.now())
	for {
		select {
		case <-ctx.Done():
			return d.shutdown()
		case <-ticker.C:
			d.tick(d.now())
		case r := <-d.results:
			d.handleResult(r)
		case o := <-d.outcomes:
			d.handleOutcome(o)
		}
	}
}

// tick starts whatever is due at now.
func (d *Daemon) tick(now time.Time) {
	for _, c := range d.cfg.Checks {
		if !d.running[c.Name] && !now.Before(d.next[c.Name]) {
			d.startCheck(c.Name)
		}
	}
	for _, n := range d.cfg.Notifiers {
		d.dispatch(n.Name, now)
	}
	d.heartbeat(now)
	if d.watchdog > 0 && now.Sub(d.lastWatchdog) >= d.watchdog {
		d.notify("WATCHDOG=1")
		d.lastWatchdog = now
	}
	d.persist()
}

func (d *Daemon) startCheck(name string) {
	d.running[name] = true
	d.inflight++
	checker := d.checkers[name]
	go func() { d.results <- checkDone{name: name, res: checker.Check(d.work)} }()
}

func (d *Daemon) handleResult(r checkDone) {
	d.inflight--
	d.running[r.name] = false
	now := d.now()
	c := d.checks[r.name]
	prev := d.st.Checks[r.name]
	next, a := policy.Evaluate(d.cfg.Host, c.Name, prev, r.res, now, policy.Settings{Confirm: c.Confirm, Repeat: c.Repeat})
	d.st.Checks[c.Name] = next
	d.log.Debug("check finished", "check", c.Name, "state", r.res.State, "output", r.res.Output,
		"duration", r.res.Duration, "confirmed", next.Confirmed, "pending", next.Pending)
	// Output-only changes are not worth a disk write; they ride along with the next one.
	if a != nil || next.Pending != prev.Pending || next.Confirmed != prev.Confirmed {
		d.dirty = true
	}
	if a != nil {
		d.enqueue(*a, now)
	}
	if next.Pending > 0 {
		d.next[c.Name] = now.Add(c.RetryInterval)
	} else {
		d.next[c.Name] = now.Add(c.Interval)
	}
	d.persist()
}

func (d *Daemon) enqueue(a alert.Alert, now time.Time) {
	targets := notify.Route(d.cfg.Routes, a)
	d.log.Info("alert", "kind", a.Kind, "check", a.Check, "state", a.State, "previous", a.PreviousState, "notifiers", targets)
	if len(targets) == 0 {
		d.log.Error("alert matches no route", "check", a.Check, "state", a.RouteState())
	}
	for _, n := range targets {
		d.st.Queues[n] = append(d.st.Queues[n], state.Queued{Alert: a, Enqueued: now, NextTry: now})
	}
	d.dirty = true
}

// dispatch drops expired alerts from a notifier's queue and starts
// delivering its head when that is due.
func (d *Daemon) dispatch(name string, now time.Time) {
	if d.sending[name] {
		return
	}
	q := d.st.Queues[name]
	for len(q) > 0 && now.Sub(q[0].Enqueued) >= maxQueueAge {
		d.log.Error("dropping an undelivered alert", "notifier", name, "check", q[0].Alert.Check,
			"kind", q[0].Alert.Kind, "attempts", q[0].Attempts)
		q = q[1:]
		d.dirty = true
	}
	d.setQueue(name, q)
	if len(q) == 0 {
		return
	}
	// A retry further out than any wait we set means the wall clock stepped
	// back since (rpi01 has no RTC): retry now rather than stall.
	if q[0].NextTry.After(now.Add(maxWait)) {
		q[0].NextTry = now
	}
	if now.Before(q[0].NextTry) {
		return
	}
	d.sending[name] = true
	d.inflight++
	n, a, timeout := d.notifiers[name], q[0].Alert, d.timeouts[name]
	go func() {
		ctx, cancel := context.WithTimeout(d.work, timeout)
		defer cancel()
		d.outcomes <- sendDone{notifier: name, err: n.Send(ctx, a)}
	}()
}

func (d *Daemon) handleOutcome(o sendDone) {
	d.inflight--
	d.sending[o.notifier] = false
	q := d.st.Queues[o.notifier]
	if len(q) == 0 {
		return
	}
	head := &q[0]
	if o.err == nil {
		if head.Attempts > 0 {
			d.log.Info("delivered after retrying", "notifier", o.notifier, "check", head.Alert.Check, "attempts", head.Attempts+1)
		}
		d.setQueue(o.notifier, q[1:])
	} else {
		head.Attempts++
		wait := backoff(head.Attempts)
		if ra := notify.RetryAfter(o.err); ra > wait {
			wait = min(ra, maxWait)
		}
		head.NextTry = d.now().Add(wait)
		d.log.Warn("delivery failed", "notifier", o.notifier, "check", head.Alert.Check,
			"attempt", head.Attempts, "retry_in", wait, "err", o.err)
	}
	d.dirty = true
	d.persist()
}

func (d *Daemon) setQueue(name string, q []state.Queued) {
	if len(q) == 0 {
		delete(d.st.Queues, name)
	} else {
		d.st.Queues[name] = q
	}
}

// backoff is 10s after the first failure, doubling, capped at 10 minutes.
func backoff(attempts int) time.Duration {
	wait := backoffMin
	for i := 1; i < attempts && wait < backoffMax; i++ {
		wait *= 2
	}
	return min(wait, backoffMax)
}

func (d *Daemon) persist() {
	if !d.dirty {
		return
	}
	if err := state.Save(d.cfg.StateFile, d.st); err != nil {
		d.log.Error("saving state", "err", err) // still dirty, so retried next tick
		return
	}
	d.dirty = false
}

func (d *Daemon) shutdown() error {
	d.notify("STOPPING=1")
	d.log.Info("shutting down", "in_flight", d.inflight)
	grace := time.After(shutdownGrace)
	for d.inflight > 0 {
		select {
		case r := <-d.results:
			d.handleResult(r)
		case o := <-d.outcomes:
			d.handleOutcome(o)
		case <-grace:
			d.log.Warn("abandoning in-flight work", "count", d.inflight)
			d.inflight = 0
		}
	}
	d.stopWork()
	d.dirty = true
	d.persist()
	return nil
}

func (d *Daemon) notify(s string) {
	if d.sdNotify == nil {
		return
	}
	if err := d.sdNotify(s); err != nil {
		d.log.Warn("sd_notify failed", "state", s, "err", err)
	}
}
