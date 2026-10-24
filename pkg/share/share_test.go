package share

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/royvandam/pigeon/internal/token"
	"github.com/royvandam/pigeon/pkg/account"
	"github.com/royvandam/pigeon/pkg/database"
	"github.com/royvandam/pigeon/pkg/quota"
)

// clock is a test's current time, which it can move forward.
type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

func newTestStore(t *testing.T) (*sql.DB, *account.Store, *Store) {
	db, accounts, s, _ := newClockedStore(t)
	return db, accounts, s
}

func newClockedStore(t *testing.T) (*sql.DB, *account.Store, *Store, *clock) {
	t.Helper()
	db, err := database.Open(context.Background(), database.Configuration{Path: filepath.Join(t.TempDir(), "pigeon.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	accounts, err := account.NewStore(db, bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	c := &clock{now: time.Unix(1_800_000_000, 0)}
	return db, accounts, NewStore(db, c.Now), c
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
	if status, err := s.Status(ctx, u, "report.pdf", Download); err != nil || status.Active {
		t.Fatalf("active before creation: %+v, %v", status, err)
	}
	first, _, err := s.Create(ctx, u, "report.pdf", Download, false, 0)
	if err != nil || len(first) != token.Length {
		t.Fatalf("Create = %q, %v", first, err)
	}
	var stored string
	if err := db.QueryRow(`SELECT token_hash FROM shares`).Scan(&stored); err != nil || stored != token.Hash(first) {
		t.Fatalf("stored %q, %v; want only the token's hash", stored, err)
	}
	link, err := s.Find(ctx, first)
	if err != nil || link.User.ID != u.ID || link.User.Slug != u.Slug || link.User.Version != u.Version ||
		link.Path != "report.pdf" || link.Kind != Download || link.Directory || !link.Expires.IsZero() {
		t.Fatalf("Find = %#v, %v", link, err)
	}
	second, _, err := s.Create(ctx, u, "report.pdf", Download, false, 0)
	if err != nil || second == first {
		t.Fatalf("replacement = %q, %v", second, err)
	}
	if _, err := s.Find(ctx, first); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a replaced link still works: %v", err)
	}
	if status, err := s.Status(ctx, u, "report.pdf", Download); err != nil || !status.Active || !status.Expires.IsZero() {
		t.Fatalf("status after replacement: %+v, %v", status, err)
	}
	if err := s.Revoke(ctx, u.ID, "report.pdf", Download); err != nil {
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
		secret, _, err := s.Create(ctx, u, name, Download, false, 0)
		if err != nil {
			t.Fatal(err)
		}
		tokens[name] = secret
	}
	// Upload links are revoked with the download links of the same paths.
	upload, _, err := s.Create(ctx, u, "a_b/deep", Upload, true, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	tokens["a_b/deep (upload)"] = upload
	otherToken, _, err := s.Create(ctx, other, "a_b", Download, true, 0)
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
	secret, _, err := s.Create(ctx, u, "report.pdf", Download, false, 0)
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
	if _, _, err := s.Create(ctx, u, "other.pdf", Download, false, 0); !errors.Is(err, account.ErrInvalidCredentials) {
		t.Fatalf("a stale identity created a link: %v", err)
	}
	if status, err := s.Status(ctx, u, "report.pdf", Download); err != nil || status.Active {
		t.Fatalf("a stale identity sees its links: %+v, %v", status, err)
	}
	if err := accounts.Delete(ctx, u.ID, 999); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Find(ctx, secret); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a deleted account's link works: %v", err)
	}
}

func TestAFolderHasOneLinkOfEachKind(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, accounts, s := newTestStore(t)
	u := createAccount(t, accounts, "Jane Doe")
	download, _, err := s.Create(ctx, u, "inbox", Download, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	upload, _, err := s.Create(ctx, u, "inbox", Upload, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if link, err := s.Find(ctx, upload); err != nil || link.Kind != Upload || link.Path != "inbox" || !link.Directory {
		t.Fatalf("upload link = %#v, %v", link, err)
	}
	// Revoking one kind keeps the other.
	if err := s.Revoke(ctx, u.ID, "inbox", Upload); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Find(ctx, upload); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a revoked upload link works: %v", err)
	}
	if link, err := s.Find(ctx, download); err != nil || link.Kind != Download {
		t.Fatalf("download link after revoking the upload link: %#v, %v", link, err)
	}
	for _, test := range []struct {
		kind      Kind
		directory bool
		lifetime  time.Duration
	}{
		{Upload, false, 0},
		{"edit", true, 0},
		{Download, false, -time.Second},
		{Download, false, MaxLifetime + time.Second},
	} {
		if _, _, err := s.Create(ctx, u, "report.pdf", test.kind, test.directory, test.lifetime); !errors.Is(err, ErrInvalidLink) {
			t.Errorf("Create(%q, directory %t, %v) = %v; want ErrInvalidLink", test.kind, test.directory, test.lifetime, err)
		}
	}
	for name, want := range map[string]Kind{"": Download, "download": Download, "upload": Upload} {
		if kind, err := ParseKind(name); err != nil || kind != want {
			t.Errorf("ParseKind(%q) = %q, %v", name, kind, err)
		}
	}
	if _, err := ParseKind("Upload"); !errors.Is(err, ErrInvalidLink) {
		t.Errorf("ParseKind accepted a differently cased kind: %v", err)
	}
}

func TestLinksStopWorkingWhenTheyExpire(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, accounts, s, c := newClockedStore(t)
	u := createAccount(t, accounts, "Jane Doe")
	secret, expires, err := s.Create(ctx, u, "inbox", Upload, true, 24*time.Hour)
	if err != nil || !expires.Equal(c.now.Add(24*time.Hour)) {
		t.Fatalf("Create = %v, %v; want an expiry a day from now", expires, err)
	}
	if link, err := s.Find(ctx, secret); err != nil || !link.Expires.Equal(expires) {
		t.Fatalf("Find = %#v, %v", link, err)
	}
	if status, err := s.Status(ctx, u, "inbox", Upload); err != nil || !status.Active || !status.Expires.Equal(expires) {
		t.Fatalf("Status = %+v, %v", status, err)
	}
	c.now = expires
	if _, err := s.Find(ctx, secret); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an expired link works: %v", err)
	}
	if status, err := s.Status(ctx, u, "inbox", Upload); err != nil || status.Active {
		t.Fatalf("an expired link is active: %+v, %v", status, err)
	}
	// Creating any link removes expired ones, and an item can be shared again.
	if _, _, err := s.Create(ctx, u, "other", Download, true, 0); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM shares WHERE token_hash = ?`, token.Hash(secret)).Scan(&count); err != nil || count != 0 {
		t.Fatalf("expired links kept: %d, %v", count, err)
	}
	if _, _, err := s.Create(ctx, u, "inbox", Upload, true, time.Hour); err != nil {
		t.Fatal(err)
	}
}
