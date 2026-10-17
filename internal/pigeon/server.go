package pigeon

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
	mux.HandleFunc("/assets/", cfg.handleAsset)
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
		Handler:           logRequests(securityHeaders(mux)),
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

// Pages load only embedded same-origin assets and contain no inline scripts or
// styles, so the policy allows nothing else. Handlers may tighten the referrer
// policy, as public share links do.
const contentSecurityPolicy = "default-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'; object-src 'none'"

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY") // For browsers without frame-ancestors.
		next.ServeHTTP(w, r)
	})
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
