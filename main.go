// alertchain is an iptables-style notification router for Prometheus
// alerts.
//
// Subcommands:
//
//	serve   run the HTTP server and process alerts
//	trace   read an alert from a JSON file and trace its evaluation
//	check   validate the configuration file
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
)

// newFlagSet returns a FlagSet that exits with code 2 on parse errors
// (matching the stdlib default) and writes errors to stderr.
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

// parseConfigFlag is shared by all subcommands that need the config path.
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

	chain, err := LoadConfig(config)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	store, err := openStoreWithTimeout(dsn)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer store.Close()

	metrics := NewMetrics()

	chain.Mutes = store
	chain.History = store
	chain.Notifier = NewHTTPNotifier()
	chain.Logger = logger
	chain.Metrics = metrics

	mux := newServeMux(chain, store, metrics, logger)

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
	chain, err := LoadConfig(config)
	if err != nil {
		return err
	}
	alert, err := LoadAlertFromFile(alertFile)
	if err != nil {
		return err
	}
	return Trace(context.Background(), chain, nil, alert, os.Stdout)
}

func cmdCheck(args []string) error {
	config, _, err := parseConfigFlag(args, "check")
	if err != nil {
		return err
	}
	chain, err := LoadConfig(config)
	if err != nil {
		return err
	}
	return Check(chain, os.Stdout)
}

// cmdVerify runs a YAML table of routing expectations against the
// configuration. Exit code 0 means all cases passed; exit code 1 (via
// run's error return path) means one or more failed. The intended
// use is pre-deployment verification in CI.
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
	chain, err := LoadConfig(config)
	if err != nil {
		return err
	}
	cs, err := LoadVerifyCases(cases)
	if err != nil {
		return err
	}
	if !Verify(chain, cs, os.Stdout) {
		return fmt.Errorf("one or more verify cases failed")
	}
	return nil
}

// openStoreWithTimeout wraps OpenStore with a startup-time deadline.
// The connection and schema sanity check should complete promptly; if
// they do not, the process fails fast and the operator can investigate
// before traffic starts.
func openStoreWithTimeout(dsn string) (*Store, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return OpenStore(ctx, dsn)
}
