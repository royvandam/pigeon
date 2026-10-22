package auth

import (
	"context"
	"database/sql"
	"errors"
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/royvandam/pigeon/internal/token"
	"github.com/royvandam/pigeon/pkg/account"
	"github.com/royvandam/pigeon/pkg/database"
	"github.com/royvandam/pigeon/pkg/quota"
)

const testPassword = "test-password"

func newTestAccounts(t *testing.T) (*sql.DB, *account.Store, account.User) {
	t.Helper()
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "pigeon.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	accounts, err := account.NewStore(db, bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	u, err := accounts.Create(context.Background(), "Test User", testPassword, false, quota.DefaultQuota)
	if err != nil {
		t.Fatal(err)
	}
	return db, accounts, u
}

func TestPasswordAttemptsRecoverOverTime(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_000_000, 0)
	limiter := newLimiter(func() time.Time { return now })
	for range AttemptBurst {
		if err := limiter.take("client"); err != nil {
			t.Fatal(err)
		}
	}
	var limited *TooManyAttemptsError
	if err := limiter.take("client"); !errors.As(err, &limited) || limited.Seconds() != 6 || limited.Wait() != "6 seconds" {
		t.Fatalf("exhausted client: %v", err)
	}
	now = now.Add(AttemptInterval / 2)
	if err := limiter.take("client"); !errors.As(err, &limited) || limited.Seconds() != 3 {
		t.Fatalf("half recovered: %v", err)
	}
	now = now.Add(AttemptInterval/2 - time.Second)
	if err := limiter.take("client"); !errors.As(err, &limited) || limited.Wait() != "1 second" {
		t.Fatalf("one second from recovery: %v", err)
	}
	now = now.Add(time.Second)
	if err := limiter.take("client"); err != nil {
		t.Fatalf("one attempt should have recovered: %v", err)
	}
	now = now.Add(AttemptBurst * AttemptInterval)
	limiter.prune(now)
	if len(limiter.recovered) != 0 {
		t.Fatalf("recovered clients were not forgotten: %v", limiter.recovered)
	}
}

func TestLimiterRefusesNewClientsWhenFull(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_000_000, 0)
	limiter := newLimiter(func() time.Time { return now })
	for i := range ClientLimit {
		limiter.recovered[netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)}).String()] = now.Add(AttemptInterval)
	}
	var limited *TooManyAttemptsError
	if err := limiter.take("new-client"); !errors.As(err, &limited) {
		t.Fatalf("new client admitted beyond the tracking limit: %v", err)
	}
	now = now.Add(AttemptInterval)
	if err := limiter.take("new-client"); err != nil {
		t.Fatalf("recovered clients should be pruned to admit new ones: %v", err)
	}
}

func TestOnlyFailedPasswordChecksUseAttempts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, accounts, _ := newTestAccounts(t)
	a := NewAuthenticator(accounts, time.Now)
	for range AttemptBurst + 2 {
		if _, err := a.CheckPassword(ctx, "client", "Test User", testPassword); err != nil {
			t.Fatal(err)
		}
	}
	if len(a.limiter.recovered) != 0 {
		t.Fatalf("successful checks left limiter state: %v", a.limiter.recovered)
	}
	if _, err := a.CheckPassword(ctx, "client", "Test User", "wrong-password"); !errors.Is(err, account.ErrInvalidCredentials) {
		t.Fatalf("wrong password: %v", err)
	}
	if _, err := a.CheckPassword(ctx, "client", "Nobody", testPassword); !errors.Is(err, account.ErrInvalidCredentials) {
		t.Fatalf("unknown username: %v", err)
	}
	if len(a.limiter.recovered) != 1 {
		t.Fatalf("failed checks did not use attempts: %v", a.limiter.recovered)
	}
}

func TestPasswordCheckWaitsForCapacityAndReturnsAttemptOnCancel(t *testing.T) {
	t.Parallel()
	_, accounts, _ := newTestAccounts(t)
	a := NewAuthenticator(accounts, time.Now)
	for range cap(a.limiter.checks) {
		a.limiter.checks <- struct{}{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := a.CheckPassword(ctx, "client", "Test User", testPassword); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("check ran beyond the concurrency cap: %v", err)
	}
	if len(a.limiter.recovered) != 0 {
		t.Fatalf("a canceled check used an attempt: %v", a.limiter.recovered)
	}
}

func TestIPv6ClientsAreGroupedByTheirSlash64(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ a, b string }{
		{"2001:db8:1:2::1", "2001:db8:1:2:ffff::1"},
		{"198.51.100.7", "198.51.100.7"},
	} {
		if ClientKey(netip.MustParseAddr(tt.a)) != ClientKey(netip.MustParseAddr(tt.b)) {
			t.Errorf("%s and %s are different clients", tt.a, tt.b)
		}
	}
	for _, tt := range []struct{ a, b string }{
		{"2001:db8:1:2::1", "2001:db8:1:3::1"},
		{"198.51.100.7", "198.51.100.8"},
	} {
		if ClientKey(netip.MustParseAddr(tt.a)) == ClientKey(netip.MustParseAddr(tt.b)) {
			t.Errorf("%s and %s share a limit", tt.a, tt.b)
		}
	}
}

func TestSessionsEndWithTheirIdentity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, accounts, u := newTestAccounts(t)
	s := NewSessions(db)
	secret, err := s.Create(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.Lookup(ctx, secret)
	if err != nil || a.User.ID != u.ID || a.TokenHash != token.Hash(secret) || len(a.CSRFToken) != token.Length || a.Basic {
		t.Fatalf("session = %#v, %v", a, err)
	}
	var stored string
	if err := db.QueryRow(`SELECT token_hash FROM sessions`).Scan(&stored); err != nil || stored != token.Hash(secret) {
		t.Fatalf("stored %q, %v; want only the token's hash", stored, err)
	}
	for _, invalid := range []string{"", "short", secret[1:] + "x"} {
		if _, err := s.Lookup(ctx, invalid); !errors.Is(err, account.ErrInvalidCredentials) {
			t.Errorf("Lookup(%q) = %v", invalid, err)
		}
	}
	if err := s.Delete(ctx, a.TokenHash); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Lookup(ctx, secret); !errors.Is(err, account.ErrInvalidCredentials) {
		t.Fatalf("a deleted session works: %v", err)
	}

	// A password reset ends sessions, and stops sign-ins that read the
	// account before it.
	secret, err = s.Create(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE users SET auth_version = auth_version + 1 WHERE id = ?`, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Lookup(ctx, secret); !errors.Is(err, account.ErrInvalidCredentials) {
		t.Fatalf("a session survived a credential change: %v", err)
	}
	if _, err := s.Create(ctx, u); !errors.Is(err, account.ErrInvalidCredentials) {
		t.Fatalf("a sign-in started before a credential change succeeded: %v", err)
	}
	u, err = accounts.Authenticate(ctx, u.Username, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	secret, err = s.Create(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE sessions SET expires_at = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Lookup(ctx, secret); !errors.Is(err, account.ErrInvalidCredentials) {
		t.Fatalf("an expired session works: %v", err)
	}
	if _, err := s.Create(ctx, u); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("expired sessions were not removed: %d, %v", count, err)
	}
}
