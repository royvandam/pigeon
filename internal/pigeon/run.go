package pigeon

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
	flags := flag.NewFlagSet("pigeon", flag.ContinueOnError)
	var (
		database = flags.String("db", "./pigeon.db", "SQLite database for users and server settings")
		dir      = flags.String("dir", "", "Set and persist the upload directory (initial default: ./uploads)")
		listen   = flags.String("listen", "", "Set and persist the listen address (initial default: 0.0.0.0:8080)")
		proxies  = flags.String("trusted-proxies", "", "Set and persist comma-separated reverse proxy addresses or CIDR ranges whose X-Forwarded-For header identifies clients; an empty value clears them")
	)
	if err := flags.Parse(args); err != nil {
		return err
	}
	proxiesSet := false
	flags.Visit(func(f *flag.Flag) { proxiesSet = proxiesSet || f.Name == "trusted-proxies" })

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
	if proxiesSet {
		settings.trustedProxies = *proxies
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

	trustedProxies, err := parseTrustedProxies(settings.trustedProxies)
	if err != nil {
		return err
	}
	cfg := config{dir: settings.dir, listen: settings.listen, store: s, trustedProxies: trustedProxies}
	log.Printf("listening on http://%s", cfg.listen)
	if len(trustedProxies) > 0 {
		log.Printf("trusted proxies: %s", settings.trustedProxies)
	}
	log.Printf("upload dir: %s", cfg.dir)
	log.Printf("default user quota: %s", humanBytes(defaultUserQuota))

	if err := runServer(ctx, newServer(cfg)); err != nil {
		return fmt.Errorf("server error: %w", err)
	}
	return nil
}
