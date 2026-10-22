package share

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/royvandam/pigeon/internal/token"
	"github.com/royvandam/pigeon/pkg/account"
	"github.com/royvandam/pigeon/pkg/database"
	"github.com/royvandam/pigeon/pkg/quota"
)

func newTestStore(t *testing.T) (*sql.DB, *account.Store, *Store) {
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
	return db, accounts, NewStore(db)
}

func createAccount(t *testing.T, accounts *account.Store, username string) account.User {
	t.Helper()
	u, err := accounts.Create(context.Background(), username, "test-password", false, quota.DefaultQuota)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestLinksAreStoredAsHashesAndReplacedOnRequest(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, accounts, s := newTestStore(t)
	u := createAccount(t, accounts, "Jane Doe")
	if active, err := s.Active(ctx, u, "report.pdf"); err != nil || active {
		t.Fatalf("active before creation: %t, %v", active, err)
	}
	first, err := s.Create(ctx, u, "report.pdf", false)
	if err != nil || len(first) != token.Length {
		t.Fatalf("Create = %q, %v", first, err)
	}
	var stored string
	if err := db.QueryRow(`SELECT token_hash FROM shares`).Scan(&stored); err != nil || stored != token.Hash(first) {
		t.Fatalf("stored %q, %v; want only the token's hash", stored, err)
	}
	link, err := s.Find(ctx, first)
	if err != nil || link.User.ID != u.ID || link.User.Slug != u.Slug || link.Path != "report.pdf" || link.Directory {
		t.Fatalf("Find = %#v, %v", link, err)
	}
	second, err := s.Create(ctx, u, "report.pdf", false)
	if err != nil || second == first {
		t.Fatalf("replacement = %q, %v", second, err)
	}
	if _, err := s.Find(ctx, first); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a replaced link still works: %v", err)
	}
	if active, err := s.Active(ctx, u, "report.pdf"); err != nil || !active {
		t.Fatalf("active after replacement: %t, %v", active, err)
	}
	if err := s.Revoke(ctx, u.ID, "report.pdf"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Find(ctx, second); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a revoked link still works: %v", err)
	}
}

func TestFindRejectsMalformedTokens(t *testing.T) {
	t.Parallel()
	_, _, s := newTestStore(t)
	for _, secret := range []string{"", "short", strings.Repeat("!", token.Length), strings.Repeat("A", token.Length+1)} {
		if _, err := s.Find(context.Background(), secret); !errors.Is(err, ErrNotFound) {
			t.Errorf("Find(%q) = %v", secret, err)
		}
	}
}

func TestRevokePathRemovesLinksBelowAPathOnly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, accounts, s := newTestStore(t)
	u := createAccount(t, accounts, "Jane Doe")
	other := createAccount(t, accounts, "John Doe")
	tokens := map[string]string{}
	// Names with SQL LIKE wildcards must match only themselves.
	for _, name := range []string{"a_b", "a_b/inner.txt", "a_b/deep/x", "aXb/kept.txt", "a_b.txt", "a%/kept.txt"} {
		secret, err := s.Create(ctx, u, name, false)
		if err != nil {
			t.Fatal(err)
		}
		tokens[name] = secret
	}
	otherToken, err := s.Create(ctx, other, "a_b", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RevokePath(ctx, u.ID, "a_b"); err != nil {
		t.Fatal(err)
	}
	for name, secret := range tokens {
		_, err := s.Find(ctx, secret)
		revoked := name == "a_b" || strings.HasPrefix(name, "a_b/")
		if revoked != errors.Is(err, ErrNotFound) {
			t.Errorf("link for %q: %v; revoked = %t", name, err, revoked)
		}
	}
	if _, err := s.Find(ctx, otherToken); err != nil {
		t.Fatalf("another account's link was revoked: %v", err)
	}
}

func TestLinksFollowTheirAccount(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, accounts, s := newTestStore(t)
	u := createAccount(t, accounts, "Jane Doe")
	secret, err := s.Create(ctx, u, "report.pdf", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := accounts.ResetPassword(ctx, u.ID, "replacement-password"); err != nil {
		t.Fatal(err)
	}
	// Links outlive a password change, but an identity read before it can no
	// longer create or see them.
	if _, err := s.Find(ctx, secret); err != nil {
		t.Fatalf("a link stopped working after a password change: %v", err)
	}
	if _, err := s.Create(ctx, u, "other.pdf", false); !errors.Is(err, account.ErrInvalidCredentials) {
		t.Fatalf("a stale identity created a link: %v", err)
	}
	if active, err := s.Active(ctx, u, "report.pdf"); err != nil || active {
		t.Fatalf("a stale identity sees its links: %t, %v", active, err)
	}
	if err := accounts.Delete(ctx, u.ID, 999); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Find(ctx, secret); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a deleted account's link works: %v", err)
	}
}
