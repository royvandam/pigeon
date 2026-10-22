package server

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
	"github.com/royvandam/pigeon/pkg/account"
	"github.com/royvandam/pigeon/pkg/quota"
)

func TestRecycleUserPreservesAccountAndRevokesPreviousAccess(t *testing.T) {
	t.Parallel()
	for _, administrator := range []bool{false, true} {
		name := "user"
		if administrator {
			name = "administrator"
		}
		t.Run(name, func(t *testing.T) {
			srv := newTestServer(t, 100)
			ctx := context.Background()
			actor, err := srv.accounts.Create(ctx, "Admin", testPassword, true, quota.DefaultQuota)
			if err != nil {
				t.Fatal(err)
			}
			old, err := srv.accounts.Create(ctx, "Élise Smith", testPassword, administrator, 2<<30)
			if err != nil {
				t.Fatal(err)
			}
			cookie, csrf := sessionFor(t, srv, old.Username)
			writeStoredFile(t, srv, old.Slug, "project/readme.txt", "preserved")
			link := shareRequest(t, srv, cookie, csrf, "create", "project/readme.txt")
			if err := srv.accounts.Delete(ctx, old.ID, actor.ID); err != nil {
				t.Fatal(err)
			}

			// Simulate credentials left behind by an earlier application version.
			if _, err := srv.db.Exec(`INSERT INTO sessions (token_hash, user_id, user_version, csrf_token, expires_at) VALUES (?, ?, ?, ?, ?)`, token.Hash(cookie.Value), old.ID, old.Version, csrf, time.Now().Add(time.Hour).Unix()); err != nil {
				t.Fatal(err)
			}
			if _, err := srv.db.Exec(`INSERT INTO shares (token_hash, user_id, path, is_directory, created_at) VALUES (?, ?, ?, 0, ?)`, token.Hash(strings.TrimPrefix(link, "/s/")), old.ID, "project/readme.txt", time.Now().Unix()); err != nil {
				t.Fatal(err)
			}
			credentials, err := srv.manager.Recycle(ctx, actor, old.ID)
			fresh := credentials.User
			if err != nil || fresh.ID != old.ID || fresh.Username != old.Username || fresh.Slug != old.Slug || fresh.Admin != old.Admin || fresh.QuotaBytes != old.QuotaBytes || fresh.Version <= old.Version || fresh.Deleted {
				t.Fatalf("recycled account = %#v, %v", fresh, err)
			}
			if _, err := srv.accounts.Authenticate(ctx, fresh.Username, credentials.Passphrase); err != nil {
				t.Fatalf("new password cannot sign in: %v", err)
			}
			if _, err := srv.accounts.Authenticate(ctx, old.Username, testPassword); !errors.Is(err, account.ErrInvalidCredentials) {
				t.Fatalf("old password works after recycling: %v", err)
			}
			if _, err := srv.sessions.Lookup(ctx, cookie.Value); !errors.Is(err, account.ErrInvalidCredentials) {
				t.Fatalf("old session works after recycling: %v", err)
			}
			if _, err := srv.sessions.Create(ctx, old); !errors.Is(err, account.ErrInvalidCredentials) {
				t.Fatalf("login started before deletion survived recycling: %v", err)
			}
			if _, err := srv.accounts.Quota(ctx, old); !errors.Is(err, account.ErrInvalidCredentials) {
				t.Fatalf("request started before deletion survived recycling: %v", err)
			}
			if _, err := createShare(ctx, srv, old, "project/readme.txt", false); !errors.Is(err, account.ErrInvalidCredentials) {
				t.Fatalf("stale account can create a share: %v", err)
			}
			if w := publicDownload(srv, http.MethodGet, link); w.Code != http.StatusNotFound {
				t.Fatalf("old share works after recycling: %d", w.Code)
			}
			for _, table := range []string{"sessions", "shares"} {
				var count int
				if err := srv.db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE user_id = ?`, old.ID).Scan(&count); err != nil || count != 0 {
					t.Fatalf("stale %s remain: count=%d, %v", table, count, err)
				}
			}
			if data, err := os.ReadFile(filepath.Join(srv.dir, fresh.Slug, "project/readme.txt")); err != nil || string(data) != "preserved" {
				t.Fatalf("recycling changed uploads: %q, %v", data, err)
			}
			newLink, err := createShare(ctx, srv, fresh, "project/readme.txt", false)
			if err != nil || newLink == link {
				t.Fatalf("new share reused an old token: %q, %v", newLink, err)
			}
			if w := publicDownload(srv, http.MethodGet, newLink); w.Code != http.StatusOK || w.Body.String() != "preserved" {
				t.Fatalf("new account cannot share preserved files: %d, %s", w.Code, w.Body)
			}
		})
	}
}

func TestRecycleUserRejectsActiveMissingAndRepeatedAccounts(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, 100)
	ctx := context.Background()
	u, err := srv.accounts.Authenticate(ctx, "Test User", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{u.ID, -1, 999} {
		if _, err := srv.manager.Recycle(ctx, administrator, id); !errors.Is(err, account.ErrDeletedUserNotFound) {
			t.Fatalf("recycled active or missing ID %d: %v", id, err)
		}
	}
	if _, err := srv.accounts.Authenticate(ctx, u.Username, testPassword); err != nil {
		t.Fatalf("failed recycling changed an active password: %v", err)
	}
	if err := srv.accounts.Delete(ctx, u.ID, 999); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.accounts.Recycle(ctx, u.ID, "short"); err == nil {
		t.Fatal("recycling accepted an invalid password")
	}
	results := make(chan struct {
		password string
		err      error
	}, 2)
	for range 2 {
		go func() {
			credentials, err := srv.manager.Recycle(ctx, administrator, u.ID)
			results <- struct {
				password string
				err      error
			}{credentials.Passphrase, err}
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
		} else if !errors.Is(result.err, account.ErrDeletedUserNotFound) {
			t.Fatal(result.err)
		}
	}
	if winner == "" {
		t.Fatal("neither recycling request succeeded")
	}
	if _, err := srv.accounts.Authenticate(ctx, u.Username, winner); err != nil {
		t.Fatalf("losing recycling request overwrote the password: %v", err)
	}
}

func TestRecycleUserWaitsForUploadsAndRollsBackCleanupFailures(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, 100)
	ctx := context.Background()
	cookie, csrf := sessionFor(t, srv, "Test User")
	old, err := srv.accounts.Authenticate(ctx, "Test User", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.accounts.Delete(ctx, old.ID, 999); err != nil {
		t.Fatal(err)
	}
	unlock, err := srv.locks.Lock(ctx, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if unlock != nil {
			unlock()
		}
	}()
	// A context that is already canceled would also fail in the database, so
	// only a wait that times out shows that recycling waits for the lock.
	waitCtx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if _, err := srv.manager.Recycle(waitCtx, administrator, old.ID); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("recycling did not wait for active uploads: %v", err)
	}
	unlock()
	unlock = nil
	if _, err := srv.db.Exec(`INSERT INTO sessions (token_hash, user_id, user_version, csrf_token, expires_at) VALUES (?, ?, ?, ?, ?)`, token.Hash(cookie.Value), old.ID, old.Version, csrf, time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.db.Exec(`CREATE TRIGGER fail_recycle BEFORE DELETE ON sessions BEGIN SELECT RAISE(ABORT, 'test cleanup failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.accounts.Recycle(ctx, old.ID, "fresh-recycled-password"); err == nil {
		t.Fatal("recycling ignored a failed credential cleanup")
	}
	var deleted bool
	var version int64
	if err := srv.db.QueryRow(`SELECT deleted_at IS NOT NULL, auth_version FROM users WHERE id = ?`, old.ID).Scan(&deleted, &version); err != nil || !deleted || version != old.Version {
		t.Fatalf("failed recycling partially restored the account: deleted=%t, version=%d, err=%v", deleted, version, err)
	}
	if _, err := srv.accounts.Authenticate(ctx, old.Username, "fresh-recycled-password"); !errors.Is(err, account.ErrInvalidCredentials) {
		t.Fatalf("failed recycling enabled login: %v", err)
	}
}
