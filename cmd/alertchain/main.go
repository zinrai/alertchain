// alertchain is an iptables-style notification router for Prometheus
// alerts.
//
// Subcommands:
//
//	serve   run the HTTP server and process alerts
//	trace   read an alert from a JSON file and trace its evaluation
//	check   validate the configuration file
//	verify  verify routing behavior against a YAML case table
//	version print build version
//
// See README.md for the configuration file format and design notes.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/zinrai/alertchain/internal/alertchain"
	"github.com/zinrai/alertchain/internal/api"
	"github.com/zinrai/alertchain/internal/store"
	"github.com/zinrai/alertchain/internal/ui"
)

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	return fs
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		usage()
		return fmt.Errorf("no subcommand given")
	}
	sub := os.Args[1]
	args := os.Args[2:]

	switch sub {
	case "serve":
		return cmdServe(args)
	case "trace":
		return cmdTrace(args)
	case "check":
		return cmdCheck(args)
	case "verify":
		return cmdVerify(args)
	case "version":
		printVersion()
		return nil
	case "-h", "--help", "help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown subcommand %q", sub)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `Usage: alertchain <subcommand> [flags]

Subcommands:
  serve    run the HTTP server
  trace    read an alert from a JSON file and trace evaluation
  check    validate the configuration file
  verify   verify routing behavior against a YAML case table
  version  print build version

Run "alertchain <subcommand> -h" for subcommand-specific flags.
`)
}

func parseConfigFlag(args []string, sub string) (string, []string, error) {
	fs := newFlagSet(sub)
	var config string
	fs.StringVar(&config, "config", "alertchain.yaml", "path to config file")
	if err := fs.Parse(args); err != nil {
		return "", nil, err
	}
	return config, fs.Args(), nil
}

func cmdServe(args []string) error {
	fs := newFlagSet("serve")
	var (
		config string
		listen string
	)
	fs.StringVar(&config, "config", "alertchain.yaml", "path to config file")
	fs.StringVar(&listen, "listen", ":9093", "HTTP listen address")
	if err := fs.Parse(args); err != nil {
		return err
	}

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return fmt.Errorf("DATABASE_URL environment variable is required")
	}

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	cfg, err := alertchain.LoadConfig(config)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	chain := cfg.Chain
	db, err := openStoreWithTimeout(dsn)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer db.Close()

	metrics := alertchain.NewMetrics()

	chain.Mutes = db
	chain.History = db
	chain.Notifier = alertchain.NewHTTPNotifier()
	chain.Logger = logger
	chain.Metrics = metrics

	mux := api.NewServeMux(chain, db, metrics, logger)
	if cfg.UI.Enabled {
		ui.Mount(mux, db, logger, cfg.UI)
	} else {
		mux.HandleFunc("GET /{$}", noUIRootHandler)
	}

	srv := &http.Server{
		Addr:              listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("alertchain serving", "listen", listen)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutdown requested")
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}

// noUIRootHandler responds at "/" when the UI is disabled, listing the
// remaining HTTP endpoints in plain text.
func noUIRootHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintln(w, "alertchain")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Endpoints:")
	fmt.Fprintln(w, "  POST /api/v2/alerts")
	fmt.Fprintln(w, "  GET/POST /api/v1/mutes  GET/DELETE /api/v1/mutes/{id}")
	fmt.Fprintln(w, "  GET /metrics")
	fmt.Fprintln(w, "  GET /-/healthy  GET /-/ready")
}

func cmdTrace(args []string) error {
	fs := newFlagSet("trace")
	var (
		config    string
		alertFile string
	)
	fs.StringVar(&config, "config", "alertchain.yaml", "path to config file")
	fs.StringVar(&alertFile, "alert-file", "", "path to JSON file containing one alert to trace")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if alertFile == "" {
		return fmt.Errorf("--alert-file is required")
	}
	cfg, err := alertchain.LoadConfig(config)
	if err != nil {
		return err
	}
	alert, err := alertchain.LoadAlertFromFile(alertFile)
	if err != nil {
		return err
	}
	return alertchain.Trace(context.Background(), cfg.Chain, nil, alert, os.Stdout)
}

func cmdCheck(args []string) error {
	config, _, err := parseConfigFlag(args, "check")
	if err != nil {
		return err
	}
	cfg, err := alertchain.LoadConfig(config)
	if err != nil {
		return err
	}
	return alertchain.Check(cfg.Chain, os.Stdout)
}

// cmdVerify runs a YAML table of routing expectations against the
// configuration. Exit code 0 means all cases passed; exit code 1 (via
// run's error return path) means one or more failed.
func cmdVerify(args []string) error {
	fs := newFlagSet("verify")
	var (
		config string
		cases  string
	)
	fs.StringVar(&config, "config", "alertchain.yaml", "path to config file")
	fs.StringVar(&cases, "verify-cases", "", "path to verify cases YAML file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if cases == "" {
		return fmt.Errorf("--verify-cases is required")
	}
	cfg, err := alertchain.LoadConfig(config)
	if err != nil {
		return err
	}
	cs, err := alertchain.LoadVerifyCases(cases)
	if err != nil {
		return err
	}
	if !alertchain.Verify(cfg.Chain, cs, os.Stdout) {
		return fmt.Errorf("one or more verify cases failed")
	}
	return nil
}

// openStoreWithTimeout wraps store.OpenStore with a startup-time deadline.
func openStoreWithTimeout(dsn string) (*store.Store, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return store.OpenStore(ctx, dsn)
}
