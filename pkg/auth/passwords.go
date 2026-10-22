package auth

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"runtime"
	"sync"
	"time"

	"github.com/royvandam/pigeon/pkg/account"
)

// Password checks run bcrypt, which costs tens of milliseconds of CPU. Each
// client may start a limited number of checks: a check takes an attempt before
// bcrypt runs and a successful sign-in returns it, so only failures count.
// A server-wide cap bounds concurrent checks from many clients.
const (
	AttemptBurst    = 10
	AttemptInterval = 6 * time.Second // one attempt recovers per interval
	ClientLimit     = 10000           // clients tracked before new ones wait
)

// TooManyAttemptsError reports when the client may try again.
type TooManyAttemptsError struct {
	RetryAfter time.Duration
}

func (e *TooManyAttemptsError) Error() string {
	return "too many sign-in attempts; try again in " + e.Wait()
}

// Wait describes the delay for people, such as "1 second" or "6 seconds".
func (e *TooManyAttemptsError) Wait() string {
	if e.Seconds() == 1 {
		return "1 second"
	}
	return fmt.Sprintf("%d seconds", e.Seconds())
}

// Seconds is the delay in whole seconds, rounded up, as Retry-After sends it.
func (e *TooManyAttemptsError) Seconds() int {
	return max(1, int(math.Ceil(e.RetryAfter.Seconds())))
}

// Authenticator checks passwords, limiting failed attempts per client and the
// number of checks running at once.
type Authenticator struct {
	accounts *account.Store
	limiter  *limiter
}

// NewAuthenticator checks passwords against accounts. It reads the time from
// clock, such as time.Now, to let attempts recover.
func NewAuthenticator(accounts *account.Store, clock func() time.Time) *Authenticator {
	return &Authenticator{accounts: accounts, limiter: newLimiter(clock)}
}

// CheckPassword verifies credentials sent by client, which identifies the
// sender, such as ClientKey of its address. It fails with a
// *TooManyAttemptsError before checking the password once the client has
// used its attempts, and with account.ErrInvalidCredentials for a wrong
// username or password.
func (a *Authenticator) CheckPassword(ctx context.Context, client, username, password string) (account.User, error) {
	if err := a.limiter.take(client); err != nil {
		return account.User{}, err
	}
	select {
	case a.limiter.checks <- struct{}{}:
	case <-ctx.Done():
		a.limiter.refund(client)
		return account.User{}, ctx.Err()
	}
	u, err := a.accounts.Authenticate(ctx, username, password)
	<-a.limiter.checks
	if !errors.Is(err, account.ErrInvalidCredentials) {
		a.limiter.refund(client) // Only wrong credentials count against the client.
	}
	return u, err
}

// ClientKey identifies the client at addr for the attempt limit. IPv6 clients
// are grouped by their /64 network, which one host or site typically controls
// entirely.
func ClientKey(addr netip.Addr) string {
	if addr.Is6() {
		prefix, err := addr.Prefix(64)
		if err == nil {
			return prefix.String()
		}
	}
	return addr.String()
}

// limiter is a token bucket in its exact integer form (GCRA). Each client
// records when its attempts will have fully recovered; every attempt moves that
// time one interval later, and an attempt is refused when the client would owe
// more than the whole burst.
type limiter struct {
	mu        sync.Mutex
	recovered map[string]time.Time
	checks    chan struct{}
	now       func() time.Time
}

func newLimiter(clock func() time.Time) *limiter {
	return &limiter{
		recovered: make(map[string]time.Time),
		checks:    make(chan struct{}, runtime.GOMAXPROCS(0)),
		now:       clock,
	}
}

// take uses one of client's attempts, or reports when the next one recovers.
func (l *limiter) take(client string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	recovered, tracked := l.recovered[client]
	if !tracked && len(l.recovered) >= ClientLimit {
		l.prune(now)
		if len(l.recovered) >= ClientLimit {
			return &TooManyAttemptsError{RetryAfter: AttemptInterval}
		}
	}
	next := maxTime(recovered, now).Add(AttemptInterval)
	if owed := next.Sub(now); owed > AttemptBurst*AttemptInterval {
		return &TooManyAttemptsError{RetryAfter: owed - AttemptBurst*AttemptInterval}
	}
	l.recovered[client] = next
	return nil
}

// refund returns an attempt after a successful sign-in.
func (l *limiter) refund(client string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	recovered, tracked := l.recovered[client]
	if !tracked {
		return
	}
	if recovered = recovered.Add(-AttemptInterval); recovered.After(l.now()) {
		l.recovered[client] = recovered
	} else {
		delete(l.recovered, client)
	}
}

// prune forgets clients whose attempts have fully recovered. Callers hold mu.
func (l *limiter) prune(now time.Time) {
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
