package pigeon

import (
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strconv"
	"strings"

	"github.com/royvandam/pigeon/pkg/account"
	"github.com/royvandam/pigeon/pkg/auth"
)

var errInvalidTrustedProxy = errors.New("trusted proxies must be IP addresses or CIDR ranges")

func setRetryAfter(w http.ResponseWriter, err *auth.TooManyAttemptsError) {
	w.Header().Set("Retry-After", strconv.Itoa(err.Seconds()))
}

// checkPassword verifies credentials for the client that sent r, applying the
// per-client attempt limit and the server-wide cap on concurrent checks.
func (cfg config) checkPassword(r *http.Request, username, password string) (account.User, error) {
	client := r.RemoteAddr // Only unusual listeners provide no IP address.
	if addr := clientAddress(r, cfg.trustedProxies); addr.IsValid() {
		client = auth.ClientKey(addr)
	}
	return cfg.authenticator.CheckPassword(r.Context(), client, username, password)
}

// clientAddress returns the address of the client that sent r. Requests from a
// trusted proxy are attributed to the nearest untrusted address in their
// X-Forwarded-For header, read from the right because proxies append to it.
func clientAddress(r *http.Request, trusted []netip.Prefix) netip.Addr {
	remote, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}
	}
	addr := remote.Addr().Unmap().WithZone("")
	if !isTrustedProxy(addr, trusted) {
		return addr
	}
	var hops []string
	for _, value := range r.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(value, ",")...)
	}
	for i := len(hops) - 1; i >= 0; i-- {
		hop, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			return addr // A malformed entry cannot be attributed further.
		}
		addr = hop.Unmap().WithZone("")
		if !isTrustedProxy(addr, trusted) {
			return addr
		}
	}
	return addr
}

func isTrustedProxy(addr netip.Addr, trusted []netip.Prefix) bool {
	for _, prefix := range trusted {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// parseTrustedProxies reads a comma-separated list of IP addresses and CIDR
// ranges. An empty list trusts no proxy.
func parseTrustedProxies(value string) ([]netip.Prefix, error) {
	var prefixes []netip.Prefix
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if prefix, err := netip.ParsePrefix(item); err == nil {
			prefixes = append(prefixes, prefix.Masked())
		} else if addr, err := netip.ParseAddr(item); err == nil && addr.Zone() == "" {
			prefixes = append(prefixes, netip.PrefixFrom(addr.Unmap(), addr.Unmap().BitLen()))
		} else {
			return nil, fmt.Errorf("%w: %q", errInvalidTrustedProxy, item)
		}
	}
	return prefixes, nil
}
