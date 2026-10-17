package uploader

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"golang.org/x/term"
)

// Run parses the command arguments and serves uploads until ctx is canceled.
func Run(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("uploader", flag.ContinueOnError)
	var (
		database = flags.String("db", "./uploader.db", "SQLite database for users and server settings")
		dir      = flags.String("dir", "", "Set and persist the upload directory (initial default: ./uploads)")
		listen   = flags.String("listen", "", "Set and persist the listen address (initial default: 0.0.0.0:8080)")
	)
	if err := flags.Parse(args); err != nil {
		return err
	}

	s, err := openStore(ctx, *database)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer s.db.Close()
	settings, err := s.loadSettings(ctx)
	if err != nil {
		return fmt.Errorf("load settings: %w", err)
	}
	if *dir != "" {
		settings.dir, err = filepath.Abs(*dir)
		if err != nil {
			return fmt.Errorf("invalid -dir: %w", err)
		}
	}
	if *listen != "" {
		settings.listen = *listen
	}
	if err := s.saveSettings(ctx, settings); err != nil {
		return fmt.Errorf("save settings: %w", err)
	}
	var readSecret func() (string, error)
	if term.IsTerminal(int(os.Stdin.Fd())) {
		readSecret = func() (string, error) {
			password, err := term.ReadPassword(int(os.Stdin.Fd()))
			return string(password), err
		}
	}
	if err := promptInitialAdmin(ctx, s, os.Stdin, os.Stdout, readSecret); err != nil {
		return fmt.Errorf("admin setup: %w", err)
	}
	if err := os.MkdirAll(settings.dir, 0o750); err != nil {
		return fmt.Errorf("create upload directory: %w", err)
	}

	cfg := config{dir: settings.dir, listen: settings.listen, store: s}
	log.Printf("listening on http://%s", cfg.listen)
	log.Printf("upload dir: %s", cfg.dir)
	log.Printf("default user quota: %s", humanBytes(defaultUserQuota))

	if err := runServer(ctx, newServer(cfg)); err != nil {
		return fmt.Errorf("server error: %w", err)
	}
	return nil
}
