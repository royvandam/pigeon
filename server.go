package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

type config struct {
	dir    string
	listen string
	store  *store
}

func newServer(cfg config) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/", cfg.handleIndex)
	mux.HandleFunc("/upload", cfg.handleUpload)
	mux.HandleFunc("/upload-progress", cfg.handleUploadProgress)
	mux.HandleFunc("/files", cfg.handleFiles)
	mux.HandleFunc("/download", cfg.handleDownload)
	mux.HandleFunc("/shares", cfg.handleShares)
	mux.HandleFunc("/s/{token}", cfg.handleSharedDownload)
	mux.HandleFunc("/login", cfg.handleLogin)
	mux.HandleFunc("/logout", cfg.handleLogout)
	mux.HandleFunc("/admin", cfg.handleAdmin)

	return &http.Server{
		Addr:              cfg.listen,
		Handler:           logRequests(mux),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}

func runServer(ctx context.Context, srv *http.Server) error {
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			_ = srv.Close()
			return fmt.Errorf("shutdown: %w", err)
		}
		return nil
	}
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		requestPath := r.URL.Path
		if strings.HasPrefix(requestPath, "/s/") {
			requestPath = "/s/[redacted]" // Share tokens grant access to private files.
		}
		log.Printf("%s %s %s", r.Method, requestPath, time.Since(start).Truncate(time.Millisecond))
	})
}
