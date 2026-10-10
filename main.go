package main

import (
	"context"
	"flag"
	"log"
	"math"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	var (
		dir      = flag.String("dir", "./uploads", "Directory where uploaded files are written")
		maxSize  = flag.String("max", "200MB", "Maximum upload size (e.g. 10MB, 1GB, 500K)")
		password = flag.String("password", "", "Shared password required to upload")
		listen   = flag.String("listen", "0.0.0.0:8080", "Listen address")
	)
	flag.Parse()

	if *password == "" {
		log.Fatal("missing required flag: -password")
	}

	maxBytes, err := parseSize(*maxSize)
	if err != nil {
		log.Fatalf("invalid -max: %v", err)
	}
	if maxBytes <= 0 {
		log.Fatal("-max must be > 0")
	}
	if maxBytes > math.MaxInt64-multipartOverhead {
		log.Fatal("-max is too large to allow for multipart overhead")
	}

	if err := os.MkdirAll(*dir, 0o755); err != nil {
		log.Fatalf("failed to create dir %q: %v", *dir, err)
	}

	cfg := config{
		dir:      *dir,
		maxBytes: maxBytes,
		password: *password,
		listen:   *listen,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("listening on http://%s", cfg.listen)
	log.Printf("upload dir: %s", cfg.dir)
	log.Printf("max upload: %d bytes", cfg.maxBytes)

	if err := runServer(ctx, newServer(cfg)); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
