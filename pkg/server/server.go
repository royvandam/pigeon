// Package server serves Pigeon's web interface and HTTP API. Handlers parse
// requests, call the account, authentication, share, and file services, and
// render pages or API responses.
package server

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/royvandam/pigeon/pkg/account"
	"github.com/royvandam/pigeon/pkg/auth"
	"github.com/royvandam/pigeon/pkg/configuration"
	"github.com/royvandam/pigeon/pkg/files"
)

const (
	// DefaultListenAddress lets a TLS-terminating reverse proxy on the same
	// host reach the server without exposing plain HTTP to the network.
	DefaultListenAddress = "127.0.0.1:8080"
	// DefaultFormReadTimeout is how long a form body may take. Form bodies are
	// at most maxFormBytes, so even a slow connection sends one well within it.
	DefaultFormReadTimeout = 30 * time.Second
	// DefaultUploadIdleTimeout is how long an upload may go without receiving
	// body data before it is abandoned.
	DefaultUploadIdleTimeout = 60 * time.Second
)

// Configuration holds a server's settings. Zero values select the defaults.
type Configuration struct {
	// ListenAddress is the host:port to serve plain HTTP on.
	ListenAddress string `json:"listenAddress"`
	// PublicURL is the origin people use, such as https://files.example.com,
	// or empty when unknown. HTTPS makes cookies Secure and enables HSTS.
	PublicURL string `json:"publicURL"`
	// TrustedProxies are the IP addresses and CIDR ranges of reverse proxies
	// whose X-Forwarded-For header identifies clients.
	TrustedProxies []string `json:"trustedProxies"`
	// FormReadTimeout limits how long a form body may take to arrive.
	FormReadTimeout configuration.Duration `json:"formReadTimeout"`
	// UploadIdleTimeout limits how long an upload may go without receiving
	// body data.
	UploadIdleTimeout configuration.Duration `json:"uploadIdleTimeout"`
}

// Dependencies are the services a server uses.
type Dependencies struct {
	Accounts      *account.Store
	Manager       *account.Manager
	Authenticator *auth.Authenticator
	Sessions      *auth.Sessions
	Files         *files.Service
}

// Server serves the web interface and HTTP API.
type Server struct {
	listen string
	// publicURL is the origin people use; nil when unknown.
	publicURL *url.URL
	// trustedProxies may identify clients through X-Forwarded-For.
	trustedProxies []netip.Prefix
	formTimeout    time.Duration
	uploadTimeout  time.Duration

	accounts      *account.Store
	manager       *account.Manager
	authenticator *auth.Authenticator
	sessions      *auth.Sessions
	files         *files.Service
	uploads       *uploadTracker
}

// New returns a server with the settings in cfg, which it validates.
func New(cfg Configuration, deps Dependencies) (*Server, error) {
	s := &Server{
		accounts:      deps.Accounts,
		manager:       deps.Manager,
		authenticator: deps.Authenticator,
		sessions:      deps.Sessions,
		files:         deps.Files,
		uploads:       newUploadTracker(),
	}
	if err := s.configure(cfg); err != nil {
		return nil, err
	}
	return s, nil
}

// Validate reports whether cfg is a valid server configuration.
func (cfg Configuration) Validate() error {
	return new(Server).configure(cfg)
}

// Normalize returns cfg as the server would apply it: with defaults for zero
// values, the public URL as an origin with a lowercase host, and trusted
// proxies as CIDR ranges.
func (cfg Configuration) Normalize() (Configuration, error) {
	var s Server
	if err := s.configure(cfg); err != nil {
		return Configuration{}, err
	}
	normalized := Configuration{
		ListenAddress:     s.listen,
		FormReadTimeout:   configuration.Duration(s.formTimeout),
		UploadIdleTimeout: configuration.Duration(s.uploadTimeout),
	}
	if s.publicURL != nil {
		normalized.PublicURL = s.publicURL.String()
	}
	for _, prefix := range s.trustedProxies {
		normalized.TrustedProxies = append(normalized.TrustedProxies, prefix.String())
	}
	return normalized, nil
}

// configure applies the settings in cfg, with defaults for zero values.
func (s *Server) configure(cfg Configuration) error {
	s.listen = cmp.Or(cfg.ListenAddress, DefaultListenAddress)
	_, port, err := net.SplitHostPort(s.listen)
	if err != nil {
		return fmt.Errorf("invalid listen address: %w", err)
	}
	if _, err := strconv.ParseUint(port, 10, 16); err != nil {
		return fmt.Errorf("invalid listen port: %w", err)
	}
	s.formTimeout = cmp.Or(time.Duration(cfg.FormReadTimeout), DefaultFormReadTimeout)
	s.uploadTimeout = cmp.Or(time.Duration(cfg.UploadIdleTimeout), DefaultUploadIdleTimeout)
	if s.formTimeout < 0 || s.uploadTimeout < 0 {
		return errors.New("timeouts cannot be negative")
	}
	if s.publicURL, err = parsePublicURL(cfg.PublicURL); err != nil {
		return err
	}
	s.trustedProxies, err = parseTrustedProxies(cfg.TrustedProxies)
	return err
}

// Handler routes requests to the server's handlers.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/assets/", s.handleAsset)
	mux.HandleFunc("/upload", s.handleUpload)
	mux.HandleFunc("/upload-progress", s.handleUploadProgress)
	mux.HandleFunc("/files", s.handleFiles)
	mux.HandleFunc("/download", s.handleDownload)
	mux.HandleFunc("/shares", s.handleShares)
	mux.HandleFunc("/s/{token}", s.handleSharedDownload)
	mux.HandleFunc("/u/{token}", s.handleUploadLink)
	mux.HandleFunc("/u/{token}/progress", s.handleUploadLinkProgress)
	mux.HandleFunc("/login", s.handleLogin)
	mux.HandleFunc("/logout", s.handleLogout)
	mux.HandleFunc("/admin", s.handleAdmin)
	return logRequests(securityHeaders(mux, s.httpsOnly()))
}

// Run serves HTTP until ctx is canceled, then gives active requests up to 10
// seconds to finish.
func (s *Server) Run(ctx context.Context) error {
	slog.Info("listening", "url", "http://"+s.listen)
	if s.publicURL != nil {
		slog.Info("public URL", "url", s.publicURL)
	}
	if warning := plainHTTPWarning(s.listen, s.publicURL); warning != "" {
		slog.Warn(warning)
	}
	if len(s.trustedProxies) > 0 {
		proxies := make([]string, len(s.trustedProxies))
		for i, prefix := range s.trustedProxies {
			proxies[i] = prefix.String()
		}
		slog.Info("trusted proxies", "proxies", strings.Join(proxies, ", "))
	}
	return runServer(ctx, &http.Server{
		Addr:              s.listen,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	})
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

func securityHeaders(next http.Handler, httpsOnly bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		if httpsOnly {
			h.Set("Strict-Transport-Security", strictTransportSecurity)
		}
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
		// Attribute values are quoted when they hold a decoded newline or other
		// control character, which therefore cannot start a forged log line.
		slog.Debug("request", "method", r.Method, "path", redactShareToken(r.URL.Path),
			"duration", time.Since(start).Truncate(time.Millisecond))
	})
}

// redactShareToken hides everything after the first "s" or "u" path segment,
// because share tokens grant access to private files and folders. Looking for
// the segment anywhere also covers paths such as //s/<token> and
// /x/../u/<token>, which the router redirects to a share route only after this
// request is logged.
func redactShareToken(requestPath string) string {
	i, segment := -1, ""
	for _, prefix := range []string{"/s/", "/u/"} {
		if j := strings.Index(requestPath, prefix); j >= 0 && (i < 0 || j < i) {
			i, segment = j, prefix
		}
	}
	if i >= 0 {
		return requestPath[:i] + segment + "[redacted]"
	}
	return requestPath
}
