// Command synapass is the Synapass control plane.
//
// One binary serves every role so that a native install is a single file to copy,
// a single systemd unit to enable, and a single thing to upgrade. Subcommands
// select the role:
//
//	synapass serve          run the gateway and its background loops (default)
//	synapass migrate        apply database migrations and exit
//	synapass config         print the resolved configuration and exit
//	synapass version        print build identity and exit
//	synapass health         probe a running instance and exit
//	synapass native         install, supervise or check a host installation
//	                          (install | up | doctor)
//
// The Docker image uses the same binary, which is what keeps the containerised and
// native deployment paths from drifting apart.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/shadowsafin/synapass/internal/config"
	"github.com/shadowsafin/synapass/internal/logging"
	"github.com/shadowsafin/synapass/internal/version"
)

// exit codes
//
// A distinct code for a configuration error lets an init system or a CI job tell
// "the operator mistyped a value" from "the process crashed", which are handled
// very differently.
const (
	exitOK          = 0
	exitFailure     = 1
	exitUsage       = 2
	exitConfig      = 3
	exitUnavailable = 4
)

func main() {
	os.Exit(run(os.Args[1:]))
}

// run parses arguments and dispatches a subcommand.
func run(args []string) int {
	// A leading flag is accepted before the subcommand so both `synapass serve`
	// and `synapass -config x.yaml serve` work, which is what operators expect
	// from a service binary.
	command := "serve"
	var commandArgs []string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command = args[0]
		commandArgs = args[1:]
	} else {
		commandArgs = args
	}

	fs := flag.NewFlagSet("synapass", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := fs.String("config", "", "path to a YAML or JSON configuration file")
	logLevel := fs.String("log-level", "", "override the configured log level (debug, info, warn, error)")
	logFormat := fs.String("log-format", "", "override the configured log format (json, console)")
	showVersion := fs.Bool("version", false, "print build identity and exit")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Synapass %s\n\n", version.Short())
		fmt.Fprintf(os.Stderr, "Usage: synapass [flags] [command]\n\nCommands:\n")
		fmt.Fprintf(os.Stderr, "  serve     run the gateway and its background loops (default)\n")
		fmt.Fprintf(os.Stderr, "  migrate   apply database migrations and exit\n")
		fmt.Fprintf(os.Stderr, "  config    print the resolved configuration and exit\n")
		fmt.Fprintf(os.Stderr, "  version   print build identity and exit\n")
		fmt.Fprintf(os.Stderr, "  health    probe a running instance and exit\n")
		fmt.Fprintf(os.Stderr, "  native    install, supervise or check a host installation\n\nFlags:\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(commandArgs); err != nil {
		return exitUsage
	}

	if *showVersion || command == "version" {
		fmt.Println(version.String())
		return exitOK
	}

	if err := dispatch(command, *configPath, *logLevel, *logFormat, fs.Args()); err != nil {
		var exitErr *exitError
		if ok := asExitError(err, &exitErr); ok {
			fmt.Fprintf(os.Stderr, "synapass: %v\n", exitErr.err)
			return exitErr.code
		}
		fmt.Fprintf(os.Stderr, "synapass: %v\n", err)
		return exitFailure
	}
	return exitOK
}

// exitError carries an explicit process exit code.
type exitError struct {
	code int
	err  error
}

// Error implements the error interface.
func (e *exitError) Error() string { return e.err.Error() }

// Unwrap exposes the cause.
func (e *exitError) Unwrap() error { return e.err }

// asExitError reports whether err is an exitError and extracts it.
func asExitError(err error, target **exitError) bool {
	for err != nil {
		if e, ok := err.(*exitError); ok {
			*target = e
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}

// dispatch runs the requested subcommand.
func dispatch(command, configPath, logLevel, logFormat string, rest []string) error {
	if command == "native" {
		return dispatchNative(configPath, logLevel, logFormat, rest)
	}
	// A config file that was named explicitly must exist; one found by the default
	// search order may be absent, since environment variables alone are a valid
	// configuration.
	cfg, err := config.Load(configPath, configPath != "")
	if err != nil {
		return &exitError{code: exitConfig, err: err}
	}

	// Flag overrides are applied after everything else so a command-line value
	// always wins, which is what makes an interactive debugging run predictable.
	if logLevel != "" {
		cfg.Logging.Level = logLevel
	}
	if logFormat != "" {
		cfg.Logging.Format = logFormat
	}

	logger := logging.New(cfg.Logging, logging.Options{
		Service:     cfg.App.Name,
		Instance:    cfg.App.InstanceID,
		Environment: cfg.App.Environment,
	})

	switch command {
	case "serve":
		return runServe(cfg, logger)
	case "migrate":
		return runMigrate(cfg, logger)
	case "config":
		return runConfig(cfg)
	case "health":
		return runHealthCheck(cfg, rest)
	default:
		return &exitError{code: exitUsage, err: fmt.Errorf("unknown command %q", command)}
	}
}

// dispatchNative enters the native deployment wrapper, which manages its own
// validation: doctor must run against a broken configuration to diagnose it,
// and install writes the files that fix it. Resolution and parse errors are
// still fatal here; Finalize (materialize plus validate) happens inside the
// subcommand, which decides what a validation failure means.
func dispatchNative(configPath, logLevel, logFormat string, rest []string) error {
	cfg, err := config.LoadUnchecked(configPath, configPath != "")
	if err != nil {
		return &exitError{code: exitConfig, err: err}
	}
	logger := logging.New(cfg.Logging, logging.Options{
		Service:     cfg.App.Name,
		Instance:    cfg.App.InstanceID,
		Environment: cfg.App.Environment,
	})
	return runNative(cfg, logger, configPath, logLevel, logFormat, rest)
}

// runConfig prints the resolved configuration with secrets redacted.
//
// This is the fastest way to answer "what did the gateway actually load?", which
// is otherwise a matter of reasoning about four layers of overrides.
func runConfig(cfg *config.Config) error {
	fmt.Printf("# configuration source: %s\n", configSource(cfg))
	fmt.Printf("# resolved with defaults + config file + SYNAPASS_* environment variables\n\n")

	encoder := yaml.NewEncoder(os.Stdout)
	// An explicit indent keeps the output readable when it is long enough to scroll.
	encoder.SetIndent(2)
	defer encoder.Close()
	return encoder.Encode(cfg.Redacted())
}

// configSource names where configuration was loaded from.
func configSource(cfg *config.Config) string {
	if cfg.ConfigFile != "" {
		return cfg.ConfigFile
	}
	return "(environment and defaults only; no config file was found)"
}

// signalContext returns a context cancelled on SIGINT or SIGTERM.
//
// SIGTERM is what an orchestrator sends before killing a container, and SIGINT is
// what a terminal sends, so both must trigger the same graceful shutdown path.
func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// shutdownTimeout bounds a graceful shutdown.
func shutdownTimeout(cfg *config.Config) time.Duration {
	if d := cfg.HTTP.ShutdownTimeout.Std(); d > 0 {
		return d
	}
	return 30 * time.Second
}
