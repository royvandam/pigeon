// Pigeon is a self-hosted file sharing server with individual accounts, disk
// quotas, and share links. It reads its settings from a configuration file:
//
//	pigeon pigeon.json
//
// main builds each part of Pigeon from its section of the configuration and
// connects them; the parts themselves live in the packages under pkg/.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/royvandam/pigeon/internal/size"
	"github.com/royvandam/pigeon/pkg/account"
	"github.com/royvandam/pigeon/pkg/auth"
	"github.com/royvandam/pigeon/pkg/configuration"
	"github.com/royvandam/pigeon/pkg/database"
	"github.com/royvandam/pigeon/pkg/files"
	"github.com/royvandam/pigeon/pkg/quota"
	"github.com/royvandam/pigeon/pkg/server"
	"github.com/royvandam/pigeon/pkg/share"
	storageconfiguration "github.com/royvandam/pigeon/pkg/storage/configuration"
)

// ApplicationConfiguration is the structure of the configuration file. Each
// section is the configuration type of the package that builds that part of
// Pigeon.
type ApplicationConfiguration struct {
	Logging  LoggingConfiguration               `json:"logging"`
	Database database.Configuration             `json:"database"`
	Storage  storageconfiguration.Configuration `json:"storage"`
	HTTP     server.Configuration               `json:"http"`
}

// LoggingConfiguration holds the logging settings.
type LoggingConfiguration struct {
	// Level is the least severe level logged: "debug", "info", "warn", or
	// "error". The zero value is info.
	Level slog.Level `json:"level"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout); err != nil && !errors.Is(err, flag.ErrHelp) {
		log.Fatal(err)
	}
}

// run reads the configuration file named in args and serves until ctx is
// canceled. First-run setup reads from stdin and writes to stdout.
func run(ctx context.Context, args []string, stdin *os.File, stdout io.Writer) error {
	flags := flag.NewFlagSet("pigeon", flag.ContinueOnError)
	flags.Usage = func() {
		fmt.Fprint(flags.Output(), "Usage: pigeon <configuration file>\n\n"+
			"Pigeon reads its settings from a JSON configuration file. See\n"+
			"https://royvandam.github.io/pigeon/administration/configuration/\n")
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		flags.Usage()
		return errors.New("expected the path of a configuration file")
	}
	var cfg ApplicationConfiguration
	if err := configuration.LoadFile(flags.Arg(0), &cfg); err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	return serve(ctx, cfg, stdin, stdout)
}

// serve builds Pigeon from cfg and serves until ctx is canceled.
func serve(ctx context.Context, cfg ApplicationConfiguration, stdin *os.File, stdout io.Writer) error {
	slog.SetLogLoggerLevel(cfg.Logging.Level)
	if err := cfg.HTTP.Validate(); err != nil {
		return fmt.Errorf("http: %w", err)
	}
	if err := cfg.Storage.Validate(); err != nil {
		return fmt.Errorf("storage: %w", err)
	}
	if err := cfg.Database.Validate(); err != nil {
		return fmt.Errorf("database: %w", err)
	}
	db, err := database.Open(ctx, cfg.Database)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer db.Close()
	provider, err := storageconfiguration.NewProviderFromConfiguration(cfg.Storage)
	if err != nil {
		return fmt.Errorf("storage: %w", err)
	}
	accounts, err := account.NewStore(db, account.DefaultPasswordCost)
	if err != nil {
		return err
	}
	var readSecret func() (string, error)
	if term.IsTerminal(int(stdin.Fd())) {
		readSecret = func() (string, error) {
			password, err := term.ReadPassword(int(stdin.Fd()))
			return string(password), err
		}
	}
	if err := promptInitialAdmin(ctx, accounts, stdin, stdout, readSecret); err != nil {
		return fmt.Errorf("admin setup: %w", err)
	}
	// No upload can be running yet, so every staged upload is stale.
	removed, err := provider.RemoveStaleUploads(ctx)
	if removed > 0 {
		slog.Info("removed unfinished uploads left from the previous run", "count", removed)
	}
	if err != nil {
		slog.Warn("could not remove every unfinished upload", "err", err)
	}

	locks, usage := account.NewLocks(), quota.NewCache(time.Now)
	sessions, shares := auth.NewSessions(db), share.NewStore(db)
	srv, err := server.New(cfg.HTTP, server.Dependencies{
		Accounts:      accounts,
		Manager:       account.NewManager(accounts, locks, usage, provider),
		Authenticator: auth.NewAuthenticator(accounts, time.Now),
		Sessions:      sessions,
		Shares:        shares,
		Files:         files.NewService(provider, accounts, locks, usage, shares, quota.DefaultAllocationUnit),
	})
	if err != nil {
		return fmt.Errorf("http: %w", err)
	}
	slog.Info("storage", "provider", provider)
	slog.Info("default user quota", "quota", size.Format(quota.DefaultQuota))
	if err := srv.Run(ctx); err != nil {
		return fmt.Errorf("server error: %w", err)
	}
	return nil
}
