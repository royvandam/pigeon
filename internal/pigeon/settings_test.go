package pigeon

import (
	"context"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/royvandam/pigeon/pkg/auth"
	"github.com/royvandam/pigeon/pkg/quota"
	"github.com/royvandam/pigeon/pkg/server"
)

const testPassword = "test-password"

// testPasswordCost keeps the password hashes in fixtures fast.
const testPasswordCost = bcrypt.MinCost

func openTestStore(t *testing.T, path string) *store {
	t.Helper()
	s, err := openStore(context.Background(), path, testPasswordCost)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.db.Close() })
	return s
}

func TestNewDatabasesListenOnLoopback(t *testing.T) {
	t.Parallel()
	s := openTestStore(t, filepath.Join(t.TempDir(), "new.db"))
	got, err := s.loadSettings(context.Background())
	if err != nil || got.listen != "127.0.0.1:8080" || got.publicURL != "" {
		t.Fatalf("new database settings = %#v, %v", got, err)
	}
}

func TestPublicURLsAreValidatedAndPersisted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "app.db")
	s := openTestStore(t, path)
	if err := s.saveSettings(ctx, settings{dir: dir, listen: server.DefaultListenAddress, publicURL: "files.example.com"}); err == nil {
		t.Fatal("an invalid public URL was saved")
	}
	want := settings{dir: dir, listen: server.DefaultListenAddress, publicURL: "https://files.example.com"}
	if err := s.saveSettings(ctx, want); err != nil {
		t.Fatal(err)
	}
	if got, err := s.loadSettings(ctx); err != nil || got != want {
		t.Fatalf("settings = %#v, %v", got, err)
	}
	// Databases created before the setting existed gain an empty column.
	if _, err := s.db.Exec(`ALTER TABLE settings DROP COLUMN public_url`); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	s = openTestStore(t, path)
	if got, err := s.loadSettings(ctx); err != nil || got != (settings{dir: dir, listen: server.DefaultListenAddress}) {
		t.Fatalf("migrated settings = %#v, %v", got, err)
	}
}

func TestTrustedProxiesAreValidatedAndPersisted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "app.db")
	s := openTestStore(t, path)
	want := settings{dir: dir, listen: "127.0.0.1:9000", trustedProxies: "127.0.0.1, 10.0.0.0/8,"}
	if err := s.saveSettings(ctx, settings{dir: dir, listen: "127.0.0.1:9000", trustedProxies: "not-an-ip"}); err == nil {
		t.Fatal("invalid trusted proxies were saved")
	}
	if err := s.saveSettings(ctx, want); err != nil {
		t.Fatal(err)
	}
	if got, err := s.loadSettings(ctx); err != nil || got != want {
		t.Fatalf("settings = %#v, %v", got, err)
	}
	// Databases created before the setting existed gain an empty column.
	if _, err := s.db.Exec(`ALTER TABLE settings DROP COLUMN trusted_proxies`); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	s = openTestStore(t, path)
	if got, err := s.loadSettings(ctx); err != nil || got != (settings{dir: dir, listen: "127.0.0.1:9000"}) {
		t.Fatalf("migrated settings = %#v, %v", got, err)
	}
}

func TestDefaultQuotaAndDatabaseMigration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "app.db")
	s := openTestStore(t, path)
	u, err := s.accounts.Create(ctx, "Test User", testPassword, false, 100)
	if err != nil {
		t.Fatal(err)
	}
	token, err := auth.NewSessions(s.db).Create(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	want := settings{dir: dir, listen: "127.0.0.1:9000"}
	if err := s.saveSettings(ctx, want); err != nil {
		t.Fatal(err)
	}
	// Recreate the previous schema with a single upload limit and no quota.
	if _, err := s.db.Exec(`ALTER TABLE users DROP COLUMN quota_bytes;
ALTER TABLE settings ADD COLUMN max_bytes INTEGER NOT NULL DEFAULT 209715200 CHECK (max_bytes > 0);`); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	s = openTestStore(t, path)
	got, err := s.loadSettings(ctx)
	if err != nil || got != want {
		t.Fatalf("migration changed server settings: %#v, %v", got, err)
	}
	a, err := auth.NewSessions(s.db).Lookup(ctx, token)
	if err != nil || a.User.QuotaBytes != quota.DefaultQuota {
		t.Fatalf("migrated user's session/quota = %#v, %v", a, err)
	}
	var hasLimit bool
	if err := s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pragma_table_info('settings') WHERE name = 'max_bytes')`).Scan(&hasLimit); err != nil || hasLimit {
		t.Fatalf("single upload limit remains after migration: %t, %v", hasLimit, err)
	}
}
