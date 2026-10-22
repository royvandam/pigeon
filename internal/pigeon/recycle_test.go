package pigeon

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/royvandam/pigeon/internal/token"
)

func TestRecycleUserPreservesAccountAndRevokesPreviousAccess(t *testing.T) {
	t.Parallel()
	for _, administrator := range []bool{false, true} {
		name := "user"
		if administrator {
			name = "administrator"
		}
		t.Run(name, func(t *testing.T) {
			cfg := newTestConfig(t, 100)
			ctx := context.Background()
			actor, err := cfg.store.createUser(ctx, "Admin", testPassword, true)
			if err != nil {
				t.Fatal(err)
			}
			old, err := cfg.store.createUserWithQuota(ctx, "Élise Smith", testPassword, administrator, 2<<30)
			if err != nil {
				t.Fatal(err)
			}
			cookie, csrf := sessionFor(t, cfg, old.Username)
			writeStoredFile(t, cfg, old.Slug, "project/readme.txt", "preserved")
			link := shareRequest(t, cfg, cookie, csrf, "create", "project/readme.txt")
			if err := cfg.store.deleteUser(ctx, old.ID, actor.ID); err != nil {
				t.Fatal(err)
			}

			// Simulate credentials left behind by an earlier application version.
			if _, err := cfg.store.db.Exec(`INSERT INTO sessions (token_hash, user_id, user_version, csrf_token, expires_at) VALUES (?, ?, ?, ?, ?)`, token.Hash(cookie.Value), old.ID, old.Version, csrf, time.Now().Add(time.Hour).Unix()); err != nil {
				t.Fatal(err)
			}
			if _, err := cfg.store.db.Exec(`INSERT INTO shares (token_hash, user_id, path, is_directory, created_at) VALUES (?, ?, ?, 0, ?)`, token.Hash(strings.TrimPrefix(link, "/s/")), old.ID, "project/readme.txt", time.Now().Unix()); err != nil {
				t.Fatal(err)
			}
			fresh, err := cfg.store.recycleUser(ctx, old.ID, "fresh-recycled-password")
			if err != nil || fresh.ID != old.ID || fresh.Username != old.Username || fresh.Slug != old.Slug || fresh.Admin != old.Admin || fresh.QuotaBytes != old.QuotaBytes || fresh.Version <= old.Version || fresh.Deleted {
				t.Fatalf("recycled account = %#v, %v", fresh, err)
			}
			if _, err := cfg.store.authenticate(ctx, fresh.Username, "fresh-recycled-password"); err != nil {
				t.Fatalf("new password cannot sign in: %v", err)
			}
			if _, err := cfg.store.authenticate(ctx, old.Username, testPassword); !errors.Is(err, errInvalidCredentials) {
				t.Fatalf("old password works after recycling: %v", err)
			}
			if _, err := cfg.store.session(ctx, cookie.Value); !errors.Is(err, errInvalidCredentials) {
				t.Fatalf("old session works after recycling: %v", err)
			}
			if _, err := cfg.store.createSession(ctx, old); !errors.Is(err, errInvalidCredentials) {
				t.Fatalf("login started before deletion survived recycling: %v", err)
			}
			if _, err := cfg.store.userQuota(ctx, old); !errors.Is(err, errInvalidCredentials) {
				t.Fatalf("request started before deletion survived recycling: %v", err)
			}
			if _, err := cfg.store.createShare(ctx, old, "project/readme.txt", false); !errors.Is(err, errInvalidCredentials) {
				t.Fatalf("stale account can create a share: %v", err)
			}
			if w := publicDownload(cfg, http.MethodGet, link); w.Code != http.StatusNotFound {
				t.Fatalf("old share works after recycling: %d", w.Code)
			}
			for _, table := range []string{"sessions", "shares"} {
				var count int
				if err := cfg.store.db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE user_id = ?`, old.ID).Scan(&count); err != nil || count != 0 {
					t.Fatalf("stale %s remain: count=%d, %v", table, count, err)
				}
			}
			if data, err := os.ReadFile(filepath.Join(cfg.dir, fresh.Slug, "project/readme.txt")); err != nil || string(data) != "preserved" {
				t.Fatalf("recycling changed uploads: %q, %v", data, err)
			}
			newLink, err := cfg.store.createShare(ctx, fresh, "project/readme.txt", false)
			if err != nil || newLink == link {
				t.Fatalf("new share reused an old token: %q, %v", newLink, err)
			}
			if w := publicDownload(cfg, http.MethodGet, newLink); w.Code != http.StatusOK || w.Body.String() != "preserved" {
				t.Fatalf("new account cannot share preserved files: %d, %s", w.Code, w.Body)
			}
		})
	}
}

func TestRecycleUserRejectsActiveMissingAndRepeatedAccounts(t *testing.T) {
	t.Parallel()
	cfg := newTestConfig(t, 100)
	ctx := context.Background()
	u, err := cfg.store.authenticate(ctx, "Test User", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{u.ID, -1, 999} {
		if _, err := cfg.store.recycleUser(ctx, id, "new-password"); !errors.Is(err, errDeletedUserNotFound) {
			t.Fatalf("recycled active or missing ID %d: %v", id, err)
		}
	}
	if _, err := cfg.store.authenticate(ctx, u.Username, testPassword); err != nil {
		t.Fatalf("failed recycling changed an active password: %v", err)
	}
	if err := cfg.store.deleteUser(ctx, u.ID, 999); err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.store.recycleUser(ctx, u.ID, "short"); err == nil {
		t.Fatal("recycling accepted an invalid password")
	}
	results := make(chan struct {
		password string
		err      error
	}, 2)
	for _, password := range []string{"first-recycled-password", "second-recycled-password"} {
		go func() {
			_, err := cfg.store.recycleUser(ctx, u.ID, password)
			results <- struct {
				password string
				err      error
			}{password, err}
		}()
	}
	winner := ""
	for range 2 {
		result := <-results
		if result.err == nil {
			if winner != "" {
				t.Fatal("both concurrent recycling requests succeeded")
			}
			winner = result.password
		} else if !errors.Is(result.err, errDeletedUserNotFound) {
			t.Fatal(result.err)
		}
	}
	if winner == "" {
		t.Fatal("neither recycling request succeeded")
	}
	if _, err := cfg.store.authenticate(ctx, u.Username, winner); err != nil {
		t.Fatalf("losing recycling request overwrote the password: %v", err)
	}
}

func TestRecycleUserWaitsForUploadsAndRollsBackCleanupFailures(t *testing.T) {
	t.Parallel()
	cfg := newTestConfig(t, 100)
	ctx := context.Background()
	cookie, csrf := sessionFor(t, cfg, "Test User")
	old, err := cfg.store.authenticate(ctx, "Test User", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.store.deleteUser(ctx, old.ID, 999); err != nil {
		t.Fatal(err)
	}
	unlock, err := cfg.store.lockUploads(ctx, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if unlock != nil {
			unlock()
		}
	}()
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := cfg.store.recycleUser(canceled, old.ID, "fresh-recycled-password"); !errors.Is(err, context.Canceled) {
		t.Fatalf("recycling ignored a canceled wait for active uploads: %v", err)
	}
	unlock()
	unlock = nil
	if _, err := cfg.store.db.Exec(`INSERT INTO sessions (token_hash, user_id, user_version, csrf_token, expires_at) VALUES (?, ?, ?, ?, ?)`, token.Hash(cookie.Value), old.ID, old.Version, csrf, time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.store.db.Exec(`CREATE TRIGGER fail_recycle BEFORE DELETE ON sessions BEGIN SELECT RAISE(ABORT, 'test cleanup failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.store.recycleUser(ctx, old.ID, "fresh-recycled-password"); err == nil {
		t.Fatal("recycling ignored a failed credential cleanup")
	}
	var deleted bool
	var version int64
	if err := cfg.store.db.QueryRow(`SELECT deleted_at IS NOT NULL, auth_version FROM users WHERE id = ?`, old.ID).Scan(&deleted, &version); err != nil || !deleted || version != old.Version {
		t.Fatalf("failed recycling partially restored the account: deleted=%t, version=%d, err=%v", deleted, version, err)
	}
	if _, err := cfg.store.authenticate(ctx, old.Username, "fresh-recycled-password"); !errors.Is(err, errInvalidCredentials) {
		t.Fatalf("failed recycling enabled login: %v", err)
	}
}
