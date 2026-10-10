// Command alerts runs monitoring-plugins style checks and delivers alerts.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/zezav-cz/alerts/internal/alert"
	"github.com/zezav-cz/alerts/internal/check"
	"github.com/zezav-cz/alerts/internal/config"
	"github.com/zezav-cz/alerts/internal/daemon"
	"github.com/zezav-cz/alerts/internal/notify"
	"github.com/zezav-cz/alerts/internal/policy"
	"github.com/zezav-cz/alerts/internal/sdnotify"
	"github.com/zezav-cz/alerts/internal/state"
)

// version is set at release time with -ldflags "-X main.version=vX.Y.Z".
var version = "dev"

const usage = `usage: alerts [flags] <command> [argument]

commands:
  daemon                  run checks and deliver alerts (the systemd service)
  validate                check the config and exit
  run <check>             run one check and show what the policy would do
  test-notify <notifier>  send a test alert through one notifier

flags:
  --config PATH           config file (default /etc/alerts/config.yaml)
  --debug                 log every check result (daemon)
  --version               print the version and exit
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("alerts", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usage) }
	cfgPath := fs.String("config", config.DefaultPath, "")
	debug := fs.Bool("debug", false, "")
	showVersion := fs.Bool("version", false, "")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *showVersion {
		fmt.Fprintln(stdout, version)
		return 0
	}

	argc := map[string]int{"daemon": 0, "validate": 0, "run": 1, "test-notify": 1}
	rest := fs.Args()
	if len(rest) == 0 {
		fs.Usage()
		return 2
	}
	cmd, cmdArgs := rest[0], rest[1:]
	if n, ok := argc[cmd]; !ok || len(cmdArgs) != n {
		fmt.Fprintf(stderr, "alerts: bad command line: %s\n\n", strings.Join(rest, " "))
		fs.Usage()
		return 2
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "alerts: invalid config: %v\n", err)
		return 1
	}
	switch cmd {
	case "validate":
		fmt.Fprintf(stdout, "config OK: %d checks, %d notifiers, %d routes\n", len(cfg.Checks), len(cfg.Notifiers), len(cfg.Routes))
		return 0
	case "run":
		return runCheck(cfg, cmdArgs[0], stdout, stderr)
	case "test-notify":
		return testNotify(cfg, cmdArgs[0], stdout, stderr)
	default:
		return runDaemon(cfg, *debug, stderr)
	}
}

// runCheck runs one check and reports what the policy would do with the
// result, without writing state or sending anything.
func runCheck(cfg *config.Config, name string, stdout, stderr io.Writer) int {
	i := slices.IndexFunc(cfg.Checks, func(c config.Check) bool { return c.Name == name })
	if i < 0 {
		fmt.Fprintf(stderr, "alerts: no check named %q\n", name)
		return 1
	}
	c := cfg.Checks[i]
	res := check.Exec{Command: c.Command, Timeout: c.Timeout}.Check(context.Background())
	fmt.Fprintf(stdout, "%s: %s (%s)\n", res.State, res.Output, res.Duration.Round(time.Millisecond))

	st, err := state.Read(cfg.StateFile)
	if err != nil {
		fmt.Fprintf(stderr, "alerts: state unreadable, assuming a fresh start: %v\n", err)
		st = state.New()
	}
	now := time.Now()
	prev, ok := st.Checks[name]
	if !ok {
		prev = policy.Initial(now)
	}
	next, a := policy.Evaluate(cfg.Host, name, prev, res, now, policy.Settings{Confirm: c.Confirm, Repeat: c.Repeat})
	switch {
	case a != nil:
		fmt.Fprintf(stdout, "policy: would send a %s to %s:\n%s\n", a.Kind, strings.Join(notify.Route(cfg.Routes, *a), ", "), a.Text)
	case next.Pending > 0:
		fmt.Fprintf(stdout, "policy: confirmed %s, %s pending %d/%d, no alert yet\n", next.Confirmed, next.PendingState, next.Pending, c.Confirm)
	default:
		fmt.Fprintf(stdout, "policy: confirmed %s, no alert\n", next.Confirmed)
	}
	return 0
}

func testNotify(cfg *config.Config, name string, stdout, stderr io.Writer) int {
	i := slices.IndexFunc(cfg.Notifiers, func(n config.Notifier) bool { return n.Name == name })
	if i < 0 {
		fmt.Fprintf(stderr, "alerts: no notifier named %q\n", name)
		return 1
	}
	nc := cfg.Notifiers[i]
	n, err := notify.Build(nc, &http.Client{Timeout: nc.Timeout})
	if err != nil {
		fmt.Fprintf(stderr, "alerts: %v\n", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), nc.Timeout)
	defer cancel()
	if err := n.Send(ctx, alert.NewTest(cfg.Host, version, name, time.Now())); err != nil {
		fmt.Fprintf(stderr, "alerts: delivery through %s failed: %v\n", name, err)
		return 1
	}
	fmt.Fprintf(stdout, "test alert delivered through %s\n", name)
	return 0
}

func runDaemon(cfg *config.Config, debug bool, stderr io.Writer) int {
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	// No timestamps: journald adds its own.
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	}))
	d, err := daemon.New(cfg, daemon.Options{
		Logger:   log,
		SdNotify: sdnotify.Send,
		Watchdog: sdnotify.WatchdogInterval(),
	})
	if err != nil {
		log.Error("cannot start", "err", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	log.Info("started", "version", version, "host", cfg.Host, "checks", len(cfg.Checks), "notifiers", len(cfg.Notifiers))
	if err := d.Run(ctx); err != nil {
		log.Error("stopped", "err", err)
		return 1
	}
	return 0
}
