// Command kadr is the Kadr media platform: API server, admin UI, migrations
// and diagnostics in one binary.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/UdjinStolboviy/kadr-madia/internal/config"
	"github.com/UdjinStolboviy/kadr-madia/internal/crypto"
	"github.com/UdjinStolboviy/kadr-madia/internal/observability"
)

// Injected at build time: -ldflags "-X main.version=… -X main.commit=…".
var (
	version = "dev"
	commit  = "unknown"
)

const usage = `Usage: kadr <command> [arguments]

Commands:
  serve [--fake-engine]       run the API and admin servers (--fake-engine: no libvips needed)
  migrate up                  apply pending database migrations
  migrate status              list migrations and their state
  admin create --email EMAIL  create an administrator (password prompted, or --password-stdin)
  doctor                      check external tools, directories, database and keys
  version                     print version information

Configuration is read from KADR_* environment variables (see docs/OPERATIONS.md).
Set KADR_DEV=1 for local development defaults (./var/, generated master key).
`

// errUsage signals a command-line mistake (exit code 2).
var errUsage = errors.New("usage error")

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch args[0] {
	case "serve":
		err = cmdServe(ctx, args[1:], stderr)
	case "migrate":
		err = cmdMigrate(ctx, args[1:], stdout)
	case "admin":
		err = cmdAdmin(ctx, args[1:], stdin, stdout, stderr)
	case "doctor":
		var ok bool
		ok, err = cmdDoctor(ctx, args[1:], stdout)
		if err == nil && !ok {
			return 1
		}
	case "version", "--version", "-v":
		fmt.Fprintf(stdout, "kadr %s (commit %s, %s, %s/%s)\n", version, commit, runtime.Version(), runtime.GOOS, runtime.GOARCH)
	case "help", "--help", "-h":
		fmt.Fprint(stdout, usage)
	default:
		fmt.Fprintf(stderr, "kadr: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
	if err != nil {
		fmt.Fprintf(stderr, "kadr: %v\n", err)
		if errors.Is(err, errUsage) {
			return 2
		}
		return 1
	}
	return 0
}

// loadConfig loads and validates configuration and prepares directories.
func loadConfig() (*config.Config, error) {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if err := cfg.EnsureDirs(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// loadKeyring reads the master key, generating one first in dev mode.
func loadKeyring(cfg *config.Config, log *slog.Logger) (*crypto.Keyring, error) {
	if cfg.Dev {
		created, err := crypto.EnsureKeyFile(cfg.MasterKeyFile)
		if err != nil {
			return nil, err
		}
		if created {
			log.Warn("generated a new dev master key", "path", cfg.MasterKeyFile)
		}
	}
	return crypto.LoadKeyFile(cfg.MasterKeyFile)
}

func newLogger(cfg *config.Config, w io.Writer) *slog.Logger {
	return observability.NewLogger(w, cfg.LogLevel, cfg.Dev)
}
