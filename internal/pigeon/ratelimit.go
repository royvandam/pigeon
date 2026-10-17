package pigeon

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/netip"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Password checks run bcrypt, which costs tens of milliseconds of CPU. Each
// client may start a limited number of checks: a check takes an attempt before
// bcrypt runs and a successful sign-in returns it, so only failures count.
// A server-wide cap bounds concurrent checks from many clients.
const (
	authAttemptBurst    = 10
	authAttemptInterval = 6 * time.Second // one attempt recovers per interval
	authClientLimit     = 10000           // clients tracked before new ones wait
)

var errInvalidTrustedProxy = errors.New("trusted proxies must be IP addresses or CIDR ranges")

// tooManyAttemptsError reports when the client may try again.
type tooManyAttemptsError struct {
	retryAfter time.Duration
}

func (e *tooManyAttemptsError) Error() string {
	return "too many sign-in attempts; try again in " + e.wait()
}

// wait describes the delay for people, such as "1 second" or "6 seconds".
func (e *tooManyAttemptsError) wait() string {
	if e.seconds() == 1 {
		return "1 second"
	}
	return fmt.Sprintf("%d seconds", e.seconds())
}

func (e *tooManyAttemptsError) seconds() int {
	return max(1, int(math.Ceil(e.retryAfter.Seconds())))
}

func setRetryAfter(w http.ResponseWriter, err *tooManyAttemptsError) {
	w.Header().Set("Retry-After", strconv.Itoa(err.seconds()))
}

// authLimiter is a token bucket in its exact integer form (GCRA). Each client
// records when its attempts will have fully recovered; every attempt moves that
// time one interval later, and an attempt is refused when the client would owe
// more than the whole burst.
type authLimiter struct {
	mu        sync.Mutex
	recovered map[string]time.Time
	checks    chan struct{}
	now       func() time.Time
}

func newAuthLimiter() *authLimiter {
	return &authLimiter{
		recovered: make(map[string]time.Time),
		checks:    make(chan struct{}, runtime.GOMAXPROCS(0)),
		now:       time.Now,
	}
}

// take uses one of client's attempts, or reports when the next one recovers.
func (l *authLimiter) take(client string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	recovered, tracked := l.recovered[client]
	if !tracked && len(l.recovered) >= authClientLimit {
		l.prune(now)
		if len(l.recovered) >= authClientLimit {
			return &tooManyAttemptsError{retryAfter: authAttemptInterval}
		}
	}
	next := maxTime(recovered, now).Add(authAttemptInterval)
	if owed := next.Sub(now); owed > authAttemptBurst*authAttemptInterval {
		return &tooManyAttemptsError{retryAfter: owed - authAttemptBurst*authAttemptInterval}
	}
	l.recovered[client] = next
	return nil
}

// refund returns an attempt after a successful sign-in.
func (l *authLimiter) refund(client string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	recovered, tracked := l.recovered[client]
	if !tracked {
		return
	}
	if recovered = recovered.Add(-authAttemptInterval); recovered.After(l.now()) {
		l.recovered[client] = recovered
	} else {
		delete(l.recovered, client)
	}
}

// prune forgets clients whose attempts have fully recovered. Callers hold mu.
func (l *authLimiter) prune(now time.Time) {
	for client, recovered := range l.recovered {
		if !recovered.After(now) {
			delete(l.recovered, client)
		}
	}
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// checkPassword verifies credentials for the client that sent r, applying the
// per-client attempt limit and the server-wide cap on concurrent checks.
func (cfg config) checkPassword(r *http.Request, username, password string) (user, error) {
	limiter := cfg.store.authLimiter
	client := r.RemoteAddr // Only unusual listeners provide no IP address.
	if addr := clientAddress(r, cfg.trustedProxies); addr.IsValid() {
		client = clientKey(addr)
	}
	if err := limiter.take(client); err != nil {
		return user{}, err
	}
	select {
	case limiter.checks <- struct{}{}:
	case <-r.Context().Done():
		limiter.refund(client)
		return user{}, r.Context().Err()
	}
	u, err := cfg.store.authenticate(r.Context(), username, password)
	<-limiter.checks
	if !errors.Is(err, errInvalidCredentials) {
		limiter.refund(client) // Only wrong credentials count against the client.
	}
	return u, err
}

// clientKey groups IPv6 clients by their /64 network, which one host or site
// typically controls entirely.
func clientKey(addr netip.Addr) string {
	if addr.Is6() {
		prefix, err := addr.Prefix(64)
		if err == nil {
			return prefix.String()
		}
	}
	return addr.String()
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
