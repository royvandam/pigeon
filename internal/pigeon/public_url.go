package pigeon

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
)

// The default lets a TLS-terminating reverse proxy on the same host reach the
// server without exposing plain HTTP to the network.
const defaultListen = "127.0.0.1:8080"

// HSTS covers only this host, so other services on sibling subdomains are
// unaffected.
const strictTransportSecurity = "max-age=31536000"

var errInvalidPublicURL = errors.New("public URL must be an http or https origin, such as https://files.example.com")

// parsePublicURL reads the external origin people use to reach the server. An
// empty value means it is unknown. Paths are not supported: the server's routes
// live at the root.
func parsePublicURL(value string) (*url.URL, error) {
	if value == "" {
		return nil, nil
	}
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil ||
		u.Opaque != "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("%w: %q", errInvalidPublicURL, value)
	}
	return &url.URL{Scheme: u.Scheme, Host: strings.ToLower(u.Host)}, nil
}

// httpsOnly reports whether people reach the server over HTTPS, either
// directly or through a TLS-terminating reverse proxy.
func (cfg config) httpsOnly() bool {
	return cfg.publicURL != nil && cfg.publicURL.Scheme == "https"
}

// secureCookies marks cookies Secure on TLS connections and whenever the public
// URL uses HTTPS, including requests a proxy forwards over plain HTTP.
func (cfg config) secureCookies(r *http.Request) bool {
	return r.TLS != nil || cfg.httpsOnly()
}

// sameOrigin rejects cross-site browser requests. The Origin may match the
// request's Host or, when a proxy rewrites Host, the configured public URL.
func (cfg config) sameOrigin(r *http.Request) bool {
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return false
	}
	if u.Host == r.Host {
		return true
	}
	return cfg.publicURL != nil && u.Scheme == cfg.publicURL.Scheme && strings.EqualFold(u.Host, cfg.publicURL.Host)
}

// plainHTTPWarning explains the risk of serving plain HTTP beyond this host, or
// returns "" when the listener is loopback-only or traffic arrives over HTTPS.
func plainHTTPWarning(listen string, publicURL *url.URL) string {
	if publicURL != nil && publicURL.Scheme == "https" {
		return ""
	}
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return ""
	}
	if host == "localhost" {
		return ""
	}
	if addr, err := netip.ParseAddr(host); err == nil && addr.IsLoopback() {
		return ""
	}
	return fmt.Sprintf("warning: serving plain HTTP on %s; passwords and sessions cross the network unencrypted. "+
		"Put Pigeon behind an HTTPS reverse proxy and set -public-url https://…, or listen on %s", listen, defaultListen)
}
