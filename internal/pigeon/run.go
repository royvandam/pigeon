package pigeon

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/term"

	"github.com/royvandam/pigeon/pkg/account"
	"github.com/royvandam/pigeon/pkg/quota"
	"github.com/royvandam/pigeon/pkg/storage/local"
)

// Run parses the command arguments and serves uploads until ctx is canceled.
func Run(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("pigeon", flag.ContinueOnError)
	var (
		database = flags.String("db", "./pigeon.db", "SQLite database for users and server settings")
		dir      = flags.String("dir", "", "Set and persist the upload directory (initial default: ./uploads)")
		listen   = flags.String("listen", "", "Set and persist the listen address (initial default: "+defaultListen+")")
		proxies  = flags.String("trusted-proxies", "", "Set and persist comma-separated reverse proxy addresses or CIDR ranges whose X-Forwarded-For header identifies clients; an empty value clears them")
		public   = flags.String("public-url", "", "Set and persist the origin people use, such as https://files.example.com; HTTPS makes cookies Secure and enables HSTS; an empty value clears it")
	)
	if err := flags.Parse(args); err != nil {
		return err
	}
	// These flags can be set to an empty value to clear them.
	set := map[string]bool{}
	flags.Visit(func(f *flag.Flag) { set[f.Name] = true })

	s, err := openStore(ctx, *database, account.DefaultPasswordCost)
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
	if set["trusted-proxies"] {
		settings.trustedProxies = *proxies
	}
	if set["public-url"] {
		settings.publicURL = *public
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
	if err := promptInitialAdmin(ctx, s.accounts, os.Stdin, os.Stdout, readSecret); err != nil {
		return fmt.Errorf("admin setup: %w", err)
	}
	provider, err := local.NewProvider(settings.dir)
	if err != nil {
		return fmt.Errorf("create upload directory: %w", err)
	}
	removed, err := provider.RemoveStaleUploads(ctx)
	if removed > 0 {
		log.Printf("removed %d unfinished uploads left from the previous run", removed)
	}
	if err != nil {
		log.Printf("warning: could not remove every unfinished upload: %v", err)
	}

	trustedProxies, err := parseTrustedProxies(settings.trustedProxies)
	if err != nil {
		return err
	}
	publicURL, err := parsePublicURL(settings.publicURL)
	if err != nil {
		return err
	}
	locks, usage := account.NewLocks(), quota.NewCache(time.Now)
	cfg := config{dir: settings.dir, listen: settings.listen, store: s, storage: provider, locks: locks, usage: usage,
		manager: account.NewManager(s.accounts, locks, usage, provider), trustedProxies: trustedProxies, publicURL: publicURL}
	log.Printf("listening on http://%s", cfg.listen)
	if publicURL != nil {
		log.Printf("public URL: %s", publicURL)
	}
	if warning := plainHTTPWarning(cfg.listen, publicURL); warning != "" {
		log.Print(warning)
	}
	if len(trustedProxies) > 0 {
		log.Printf("trusted proxies: %s", settings.trustedProxies)
	}
	log.Printf("upload dir: %s", cfg.dir)
	log.Printf("default user quota: %s", humanBytes(quota.DefaultQuota))

	if err := runServer(ctx, newServer(cfg)); err != nil {
		return fmt.Errorf("server error: %w", err)
	}
	return nil
}
