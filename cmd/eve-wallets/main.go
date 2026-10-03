// Command eve-wallets snapshots EVE Online wallet balances and serves their
// history on a local web page.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/escorbuto-petoruti/eve-wallets/internal/auth"
	"github.com/escorbuto-petoruti/eve-wallets/internal/collector"
	"github.com/escorbuto-petoruti/eve-wallets/internal/esi"
	"github.com/escorbuto-petoruti/eve-wallets/internal/scheduler"
	"github.com/escorbuto-petoruti/eve-wallets/internal/store"
	"github.com/escorbuto-petoruti/eve-wallets/internal/web"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

const (
	defaultAddr   = "127.0.0.1:8088"
	defaultEvery  = 30 * time.Minute
	minEvery      = time.Minute
	shutdownGrace = 10 * time.Second
	defaultAuth   = "eve-auth"
)

const usage = `Usage:
  eve-wallets collect [--db PATH]
  eve-wallets backfill [--db PATH]
  eve-wallets serve [--addr 127.0.0.1:8088] [--db PATH] [--every 30m] [--no-collect] [--no-backfill]
  eve-wallets wallets [--db PATH]
  eve-wallets label [--db PATH] <wallet-id> <name...>
  eve-wallets label [--db PATH] --clear <wallet-id>

Commands:
  collect  take one snapshot of every wallet and print a summary
  backfill store the last 30 days of history from the wallet journals
  serve    serve the charts on a loopback address; each cycle takes a snapshot and backfills the journal
  wallets  list the wallets with id, owner, division and displayed name
  label    set (or --clear) the name shown for a wallet; flags go before the id

Environment:
  EVE_AUTH_BIN    eve-auth executable (default "eve-auth")
  EVE_CLIENT_ID   passed through to eve-auth
  EVE_WALLETS_DB  database path (the --db flag wins)
`

// deps are the collaborators of run, replaced by fakes in tests.
type deps struct {
	stdout, stderr io.Writer
	getenv         func(string) string
	openStore      func(path string) (*store.Store, error)
	newTokens      func(bin string) auth.TokenSource
	newESI         func(userAgent string) collector.ESIClient
	listen         func(network, addr string) (net.Listener, error)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], deps{
		stdout:    os.Stdout,
		stderr:    os.Stderr,
		getenv:    os.Getenv,
		openStore: openSecureStore,
		newTokens: func(bin string) auth.TokenSource { return auth.NewEveAuth(auth.Options{Bin: bin}) },
		newESI: func(ua string) collector.ESIClient {
			return esi.New(esi.Options{UserAgent: ua})
		},
		listen: net.Listen,
	})
	stop()
	os.Exit(code)
}

// run executes one command line and returns the process exit code.
func run(ctx context.Context, args []string, d deps) int {
	if len(args) == 0 {
		fmt.Fprint(d.stderr, usage)
		return 2
	}
	switch args[0] {
	case "-h", "--help":
		fmt.Fprint(d.stdout, usage)
		return 0
	case "help":
		fmt.Fprint(d.stderr, usage)
		return 2
	case "collect":
		return runCollect(ctx, args[1:], d)
	case "backfill":
		return runBackfill(ctx, args[1:], d)
	case "serve":
		return runServe(ctx, args[1:], d)
	case "wallets":
		return runWallets(ctx, args[1:], d)
	case "label":
		return runLabel(ctx, args[1:], d)
	}
	fmt.Fprintf(d.stderr, "eve-wallets: unknown command %q\n\n%s", args[0], usage)
	return 2
}

// parseFlags parses args into fset. It returns (code, true) when the command
// must stop: 0 for -h, 2 for a flag error.
func parseFlags(fset *flag.FlagSet, args []string) (int, bool) {
	switch err := fset.Parse(args); {
	case err == nil:
		return 0, false
	case errors.Is(err, flag.ErrHelp):
		return 0, true
	default:
		return 2, true
	}
}

func newFlagSet(name string, d deps) *flag.FlagSet {
	fset := flag.NewFlagSet("eve-wallets "+name, flag.ContinueOnError)
	fset.SetOutput(d.stderr)
	return fset
}

func authBin(getenv func(string) string) string {
	if b := getenv("EVE_AUTH_BIN"); b != "" {
		return b
	}
	return defaultAuth
}

// buildCollector wires the collaborators around st. The eve-auth subprocess
// inherits the process environment, so EVE_CLIENT_ID needs no extra wiring.
func buildCollector(d deps, st *store.Store) *collector.Collector {
	return collector.New(collector.Deps{
		Auth:  d.newTokens(authBin(d.getenv)),
		ESI:   d.newESI("eve-wallets/" + version + " (local)"),
		Store: st,
	})
}

// runCollector parses the flags of a one-shot command, opens the store and
// hands the wired collector to do. what names the operation in error messages.
func runCollector(ctx context.Context, name, what string, args []string, d deps, do func(*collector.Collector) error) int {
	fset := newFlagSet(name, d)
	dbFlag := fset.String("db", "", "database path")
	if code, stop := parseFlags(fset, args); stop {
		return code
	}
	path, err := resolveDBPath(*dbFlag, d.getenv)
	if err != nil {
		fmt.Fprintf(d.stderr, "eve-wallets: %v\n", err)
		return 1
	}
	st, err := d.openStore(path)
	if err != nil {
		fmt.Fprintf(d.stderr, "eve-wallets: %v\n", err)
		return 1
	}
	defer st.Close()

	if err := do(buildCollector(d, st)); err != nil {
		fmt.Fprintf(d.stderr, "eve-wallets: %s failed: %v\n", what, err)
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
			fmt.Fprintf(d.stderr, "eve-wallets: eve-auth not found: install it on PATH or set EVE_AUTH_BIN (currently %q)\n", authBin(d.getenv))
		}
		return 1
	}
	return 0
}

func runCollect(ctx context.Context, args []string, d deps) int {
	return runCollector(ctx, "collect", "collection", args, d, func(c *collector.Collector) error {
		rep, err := c.Run(ctx)
		if err != nil {
			return err
		}
		printReport(d.stdout, rep)
		return nil
	})
}

func runBackfill(ctx context.Context, args []string, d deps) int {
	return runCollector(ctx, "backfill", "backfill", args, d, func(c *collector.Collector) error {
		rep, err := c.Backfill(ctx)
		if err != nil {
			return err
		}
		printBackfillReport(d.stdout, rep)
		return nil
	})
}

// printBackfillReport writes a short human summary of rep. It holds no tokens.
func printBackfillReport(w io.Writer, rep collector.BackfillReport) {
	fmt.Fprintf(w, "Wallets processed: %d\n", len(rep.Wallets))
	for _, bw := range rep.Wallets {
		label := bw.OwnerName
		if bw.Kind == store.KindCorporation {
			label = fmt.Sprintf("%s (division %d)", bw.OwnerName, bw.Division)
		}
		fmt.Fprintf(w, "  %-11s %s: %d points\n", bw.Kind, label, bw.Points)
	}
	fmt.Fprintf(w, "Journal points stored: %d\n", rep.Points())
	fmt.Fprintf(w, "Entries without balance: %d\n", rep.NoBalance())
	if len(rep.Skipped) > 0 {
		fmt.Fprintf(w, "Skipped: %d\n", len(rep.Skipped))
		for _, k := range rep.Skipped {
			fmt.Fprintf(w, "  %s: %s\n", k.Owner, k.Reason)
		}
	}
	if len(rep.Errors) > 0 {
		fmt.Fprintf(w, "Errors: %d\n", len(rep.Errors))
		for _, e := range rep.Errors {
			fmt.Fprintf(w, "  %s\n", e.Error())
		}
	}
	if rep.RateLimited {
		fmt.Fprintf(w, "ESI rate limit reached: the run is partial; retry after %s\n", rep.RetryAfter)
	}
}

// printReport writes a short human summary of rep. It holds no tokens.
func printReport(w io.Writer, rep collector.Report) {
	fmt.Fprintf(w, "Snapshots taken: %d\n", len(rep.Snapshots))
	for _, s := range rep.Snapshots {
		label := s.OwnerName
		if s.Kind == store.KindCorporation {
			label = fmt.Sprintf("%s (division %d)", s.OwnerName, s.Division)
		}
		fmt.Fprintf(w, "  %-11s %s: %s ISK\n", s.Kind, label, formatCents(s.Cents))
	}
	if len(rep.Skipped) > 0 {
		fmt.Fprintf(w, "Skipped: %d\n", len(rep.Skipped))
		for _, k := range rep.Skipped {
			fmt.Fprintf(w, "  %s: %s\n", k.Owner, k.Reason)
		}
	}
	if len(rep.Errors) > 0 {
		fmt.Fprintf(w, "Errors: %d\n", len(rep.Errors))
		for _, e := range rep.Errors {
			fmt.Fprintf(w, "  %s\n", e.Error())
		}
	}
	if rep.RateLimited {
		fmt.Fprintf(w, "ESI rate limit reached: the run is partial; retry after %s\n", rep.RetryAfter)
	}
}

// formatCents renders integer ISK cents without going through floats.
func formatCents(cents int64) string {
	sign := ""
	if cents < 0 {
		sign, cents = "-", -cents
	}
	return fmt.Sprintf("%s%d.%02d", sign, cents/100, cents%100)
}

func runServe(ctx context.Context, args []string, d deps) int {
	fset := newFlagSet("serve", d)
	addr := fset.String("addr", defaultAddr, "listen address (loopback only)")
	dbFlag := fset.String("db", "", "database path")
	every := fset.Duration("every", defaultEvery, "collection interval (minimum 1m)")
	noCollect := fset.Bool("no-collect", false, "serve without collecting in the background")
	noBackfill := fset.Bool("no-backfill", false, "take snapshots only; do not backfill the journal each cycle")
	if code, stop := parseFlags(fset, args); stop {
		return code
	}
	if err := checkLoopbackAddr(*addr); err != nil {
		fmt.Fprintf(d.stderr, "eve-wallets: %v\n", err)
		return 2
	}
	if *every < minEvery {
		fmt.Fprintf(d.stderr, "eve-wallets: --every must be at least %s\n", minEvery)
		return 2
	}
	path, err := resolveDBPath(*dbFlag, d.getenv)
	if err != nil {
		fmt.Fprintf(d.stderr, "eve-wallets: %v\n", err)
		return 1
	}
	st, err := d.openStore(path)
	if err != nil {
		fmt.Fprintf(d.stderr, "eve-wallets: %v\n", err)
		return 1
	}
	defer st.Close()

	ln, err := d.listen("tcp", *addr)
	if err != nil {
		fmt.Fprintf(d.stderr, "eve-wallets: listen: %v\n", err)
		return 1
	}

	var (
		mu     sync.Mutex
		status web.StatusSnapshot
	)
	loopCtx, stopLoop := context.WithCancel(ctx)
	defer stopLoop()
	var loopDone sync.WaitGroup
	if !*noCollect {
		loop := &scheduler.Loop{
			Every: *every,
			Run:   newCycle(buildCollector(d, st), !*noBackfill),
			OnResult: func(rep collector.Report, err error) {
				snap := web.StatusFromReport(rep)
				if err != nil && ctx.Err() == nil {
					snap.Errors = append(snap.Errors, "collection failed: "+err.Error())
					fmt.Fprintf(d.stderr, "eve-wallets: collection failed: %v\n", err)
				}
				mu.Lock()
				status = snap
				mu.Unlock()
			},
		}
		loopDone.Add(1)
		go func() {
			defer loopDone.Done()
			if err := loop.Start(loopCtx); err != nil {
				fmt.Fprintf(d.stderr, "eve-wallets: scheduler: %v\n", err)
			}
		}()
	}

	srv := &http.Server{
		Handler: web.New(web.Deps{Store: st, Status: func() web.StatusSnapshot {
			mu.Lock()
			defer mu.Unlock()
			return status
		}}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 16,
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	fmt.Fprintf(d.stdout, "Serving on http://%s (database %s)\n", ln.Addr(), path)

	code := 0
	select {
	case <-ctx.Done():
	case err := <-serveErr:
		fmt.Fprintf(d.stderr, "eve-wallets: server: %v\n", err)
		code = 1
	}
	// Stop the loop first: it writes to the store that is closed on return.
	stopLoop()
	loopDone.Wait()
	shutCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		fmt.Fprintf(d.stderr, "eve-wallets: shutdown: %v\n", err)
		code = 1
	}
	return code
}

// checkLoopbackAddr refuses any listen address that is not loopback. The API
// has no authentication, so there is deliberately no way around this check.
func checkLoopbackAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid --addr %q: %v", addr, err)
	}
	if n, err := strconv.Atoi(port); err != nil || n < 0 || n > 65535 {
		return fmt.Errorf("invalid --addr %q: bad port", addr)
	}
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("refusing --addr %q: the API has no authentication, so only loopback addresses (127.0.0.1, ::1, localhost) are allowed", addr)
}

// resolveDBPath picks the database path: flag, EVE_WALLETS_DB,
// $XDG_DATA_HOME/eve-wallets/wallets.db, then ~/.local/share/eve-wallets/wallets.db.
func resolveDBPath(flagValue string, getenv func(string) string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if p := getenv("EVE_WALLETS_DB"); p != "" {
		return p, nil
	}
	if x := getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, "eve-wallets", "wallets.db"), nil
	}
	if home := getenv("HOME"); home != "" {
		return filepath.Join(home, ".local", "share", "eve-wallets", "wallets.db"), nil
	}
	return "", errors.New("cannot determine the database path: set --db, EVE_WALLETS_DB or HOME")
}

// openSecureStore opens the database with a private directory (0700) and
// private files (0600), including the WAL side files.
func openSecureStore(path string) (*store.Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	// Create the file private first so SQLite's side files inherit the mode.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create database file: %w", err)
	}
	_ = f.Close()
	st, err := store.Open(path)
	if err != nil {
		return nil, err
	}
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Chmod(p, 0o600); err != nil && !errors.Is(err, fs.ErrNotExist) {
			_ = st.Close()
			return nil, fmt.Errorf("restrict database permissions: %w", err)
		}
	}
	return st, nil
}
