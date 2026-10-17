package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"element42/upload/internal/uploader"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := uploader.Run(ctx, os.Args[1:]); err != nil && !errors.Is(err, flag.ErrHelp) {
		log.Fatal(err)
	}
}
