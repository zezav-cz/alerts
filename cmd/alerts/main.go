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
	"regexp"
	"slices"
	"strings"
	"sync"
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
  run [-v] [check...]     run every check (or the named ones) now and print
                          the results, worst first; nothing is sent or saved.
                          -v adds each check's command and raw output. With
                          one check, also what the policy would do. Exits
                          with the worst state (0 OK ... 2 CRITICAL, 3 UNKNOWN)
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

	argc := map[string]int{"daemon": 0, "validate": 0, "run": -1, "test-notify": 1} // -1: any
	rest := fs.Args()
	if len(rest) == 0 {
		fs.Usage()
		return 2
	}
	cmd, cmdArgs := rest[0], rest[1:]
	verbose := false
	if cmd == "run" {
		runFlags := flag.NewFlagSet("alerts run", flag.ContinueOnError)
		runFlags.SetOutput(stderr)
		runFlags.Usage = func() { fmt.Fprint(stderr, usage) }
		runFlags.BoolVar(&verbose, "v", false, "")
		runFlags.BoolVar(&verbose, "verbose", false, "")
		if err := runFlags.Parse(cmdArgs); err != nil {
			return 2
		}
		cmdArgs = runFlags.Args()
	}
	if n, ok := argc[cmd]; !ok || (n >= 0 && len(cmdArgs) != n) {
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
		return runChecks(cfg, cmdArgs, verbose, stdout, stderr)
	case "test-notify":
		return testNotify(cfg, cmdArgs[0], stdout, stderr)
	default:
		return runDaemon(cfg, *debug, stderr)
	}
}

// severity orders states for display and for the exit code: a check that
// cannot run is worse than a warning, a critical worse than both.
var severity = map[check.State]int{check.OK: 0, check.Warning: 1, check.Unknown: 2, check.Critical: 3}

// runChecks runs the named checks, or every check when names is empty, all
// at once and exactly as the daemon would, and prints the results worst
// first. It never sends anything and never writes state. It returns the
// worst state, so a script can act on it.
func runChecks(cfg *config.Config, names []string, verbose bool, stdout, stderr io.Writer) int {
	selected := cfg.Checks
	if len(names) > 0 {
		selected = nil
		for _, name := range names {
			i := slices.IndexFunc(cfg.Checks, func(c config.Check) bool { return c.Name == name })
			if i < 0 {
				fmt.Fprintf(stderr, "alerts: no check named %q\n", name)
				return 1
			}
			selected = append(selected, cfg.Checks[i])
		}
	}

	results := make([]check.Result, len(selected))
	var wg sync.WaitGroup
	for i, c := range selected {
		wg.Go(func() { results[i] = check.Exec{Command: c.Command, Timeout: c.Timeout}.Check(context.Background()) })
	}
	wg.Wait()

	order := make([]int, len(selected))
	width := 0
	for i, c := range selected {
		order[i] = i
		width = max(width, len(c.Name))
	}
	slices.SortStableFunc(order, func(a, b int) int {
		if d := severity[results[b].State] - severity[results[a].State]; d != 0 {
			return d
		}
		return strings.Compare(selected[a].Name, selected[b].Name)
	})

	counts := map[check.State]int{}
	worst := check.OK
	for _, i := range order {
		c, r := selected[i], results[i]
		counts[r.State]++
		if severity[r.State] > severity[worst] {
			worst = r.State
		}
		fmt.Fprintf(stdout, "%-8s  %-*s  %s  (%s)\n", r.State, width, c.Name, r.Output, r.Duration.Round(time.Millisecond))
		if verbose {
			printRaw(stdout, c, r)
		}
	}
	fmt.Fprintln(stdout, summary(len(selected), counts))

	if len(selected) == 1 {
		explainPolicy(cfg, selected[0], results[0], stdout, stderr)
	}
	return int(worst)
}

// printRaw shows how a check ran: the command as a shell would take it, its
// exit code, and everything it printed.
func printRaw(w io.Writer, c config.Check, r check.Result) {
	fmt.Fprintf(w, "  command  %s\n", shellJoin(c.Command))
	fmt.Fprintf(w, "  exit     %d\n", r.Exit)
	for _, stream := range []struct{ label, text string }{{"stdout", r.Stdout}, {"stderr", r.Stderr}} {
		lines := strings.Split(strings.TrimRight(stream.text, "\n"), "\n")
		if stream.text == "" {
			lines = []string{"(empty)"}
		}
		for i, line := range lines {
			label := ""
			if i == 0 {
				label = stream.label
			}
			fmt.Fprintf(w, "  %-6s   %s\n", label, line)
		}
	}
}

// summary is the closing line: "4 checks: 1 critical, 1 unknown, 1 warning, 1 ok".
func summary(n int, counts map[check.State]int) string {
	noun := "checks"
	if n == 1 {
		noun = "check"
	}
	var parts []string
	for _, s := range []check.State{check.Critical, check.Unknown, check.Warning, check.OK} {
		if counts[s] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[s], strings.ToLower(s.String())))
		}
	}
	return fmt.Sprintf("%d %s: %s", n, noun, strings.Join(parts, ", "))
}

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// shellJoin quotes the arguments that need it, so the command can be pasted
// into a shell to run it by hand.
func shellJoin(argv []string) string {
	quoted := make([]string, len(argv))
	for i, a := range argv {
		if shellSafe.MatchString(a) {
			quoted[i] = a
		} else {
			quoted[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
	}
	return strings.Join(quoted, " ")
}

// explainPolicy says what the daemon would do with this result, from the
// saved state, without changing it.
func explainPolicy(cfg *config.Config, c config.Check, res check.Result, stdout, stderr io.Writer) {
	st, err := state.Read(cfg.StateFile)
	if err != nil {
		fmt.Fprintf(stderr, "alerts: state unreadable, assuming a fresh start: %v\n", err)
		st = state.New()
	}
	now := time.Now()
	prev, ok := st.Checks[c.Name]
	if !ok {
		prev = policy.Initial(now)
	}
	next, a := policy.Evaluate(cfg.Host, c.Name, prev, res, now, policy.Settings{Confirm: c.Confirm, Repeat: c.Repeat})
	switch {
	case a != nil:
		fmt.Fprintf(stdout, "policy: would send a %s to %s:\n%s\n", a.Kind, strings.Join(notify.Route(cfg.Routes, *a), ", "), a.Text)
	case next.Pending > 0:
		fmt.Fprintf(stdout, "policy: confirmed %s, %s pending %d/%d, no alert yet\n", next.Confirmed, next.PendingState, next.Pending, c.Confirm)
	default:
		fmt.Fprintf(stdout, "policy: confirmed %s, no alert\n", next.Confirmed)
	}
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
