package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"golang.org/x/term"
)

func main() {
	var (
		database = flag.String("db", "./uploader.db", "SQLite database for users and server settings")
		dir      = flag.String("dir", "", "Set and persist the upload directory (initial default: ./uploads)")
		maxSize  = flag.String("max", "", "Set and persist the upload limit (initial default: 200MB)")
		listen   = flag.String("listen", "", "Set and persist the listen address (initial default: 0.0.0.0:8080)")
	)
	flag.Parse()

	ctx := context.Background()
	s, err := openStore(ctx, *database)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer s.db.Close()
	settings, err := s.loadSettings(ctx)
	if err != nil {
		log.Fatalf("load settings: %v", err)
	}
	if *dir != "" {
		settings.dir, err = filepath.Abs(*dir)
		if err != nil {
			log.Fatalf("invalid -dir: %v", err)
		}
	}
	if *maxSize != "" {
		settings.maxBytes, err = parseSize(*maxSize)
		if err != nil {
			log.Fatalf("invalid -max: %v", err)
		}
	}
	if *listen != "" {
		settings.listen = *listen
	}
	if err := s.saveSettings(ctx, settings); err != nil {
		log.Fatalf("save settings: %v", err)
	}
	var readSecret func() (string, error)
	if term.IsTerminal(int(os.Stdin.Fd())) {
		readSecret = func() (string, error) {
			password, err := term.ReadPassword(int(os.Stdin.Fd()))
			return string(password), err
		}
	}
	if err := promptInitialAdmin(ctx, s, os.Stdin, os.Stdout, readSecret); err != nil {
		log.Fatalf("admin setup: %v", err)
	}
	if err := os.MkdirAll(settings.dir, 0o750); err != nil {
		log.Fatalf("create upload directory: %v", err)
	}

	cfg := config{dir: settings.dir, maxBytes: settings.maxBytes, listen: settings.listen, store: s}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("listening on http://%s", cfg.listen)
	log.Printf("upload dir: %s", cfg.dir)
	log.Printf("max upload: %d bytes", cfg.maxBytes)

	if err := runServer(ctx, newServer(cfg)); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
