package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/royvandam/pigeon/internal/token"
	"github.com/royvandam/pigeon/pkg/account"
	"github.com/royvandam/pigeon/pkg/quota"
	"github.com/royvandam/pigeon/pkg/storage"
)

func TestPurgeRemovesUploadsAndCredentialsAndReleasesUsername(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, 100)
	ctx := context.Background()
	u := deletedPurgeAccount(t, srv)
	writeStoredFile(t, srv, u.Slug, "nested/kept.txt", "old content")
	writeStoredFile(t, srv, u.Slug, ".upload-incomplete/part", "unfinished")
	writeStoredFile(t, srv, "test-user", "kept.txt", "other user's content")
	if err := os.MkdirAll(filepath.Join(srv.dir, u.Slug, "empty"), 0o750); err != nil {
		t.Fatal(err)
	}
	// Foreign keys must also clear credentials left behind by older versions.
	if _, err := srv.db.Exec(`INSERT INTO sessions (token_hash, user_id, user_version, csrf_token, expires_at) VALUES (?, ?, 0, ?, ?)`, "legacy", u.ID, strings.Repeat("a", 43), time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.db.Exec(`INSERT INTO shares (token_hash, user_id, path, is_directory, created_at) VALUES (?, ?, 'nested/kept.txt', 0, ?)`, token.Hash(strings.Repeat("b", 43)), u.ID, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	if err := srv.manager.Purge(ctx, administrator, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(srv.dir, u.Slug)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("purge left account uploads behind: %v", err)
	}
	for _, table := range []string{"users", "sessions", "shares"} {
		column := "user_id"
		if table == "users" {
			column = "id"
		}
		var count int
		if err := srv.db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE `+column+` = ?`, u.ID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("purged %s remain: %d, %v", table, count, err)
		}
	}
	if data, err := os.ReadFile(filepath.Join(srv.dir, "test-user/kept.txt")); err != nil || string(data) != "other user's content" {
		t.Fatalf("purge changed another account's files: %q, %v", data, err)
	}
	fresh, err := srv.accounts.Create(ctx, "Retired_User", "new-account-password", false, quota.DefaultQuota)
	if err != nil || fresh.Slug != u.Slug || fresh.ID == u.ID {
		t.Fatalf("purged username or account ID was not safely released: %#v, %v", fresh, err)
	}
	tree, err := srv.storage.Create(ctx, fresh.Slug)
	if err != nil {
		t.Fatal(err)
	}
	tree.Close()
	entries, err := os.ReadDir(filepath.Join(srv.dir, fresh.Slug))
	if err != nil || len(entries) != 0 {
		t.Fatalf("new account inherited old uploads: %v, %v", entries, err)
	}
	if err := srv.manager.Purge(ctx, administrator, u.ID); !errors.Is(err, account.ErrDeletedUserNotFound) {
		t.Fatalf("repeated purge accepted an old ID: %v", err)
	}
	if _, err := srv.sessions.Create(ctx, u); !errors.Is(err, account.ErrInvalidCredentials) {
		t.Fatalf("purged identity could create a session: %v", err)
	}
}

func TestPurgeRejectsActiveAccountsAndHandlesMissingUploads(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"active", "no uploads directory", "no user directory"} {
		t.Run(state, func(t *testing.T) {
			srv := newTestServer(t, 100)
			ctx := context.Background()
			u, err := srv.accounts.Authenticate(ctx, "Test User", testPassword)
			if err != nil {
				t.Fatal(err)
			}
			if state == "active" {
				writeStoredFile(t, srv, u.Slug, "kept.txt", "preserved")
				if err := srv.manager.Purge(ctx, administrator, u.ID); !errors.Is(err, account.ErrDeletedUserNotFound) {
					t.Fatalf("active account was purged: %v", err)
				}
				if _, err := srv.accounts.Authenticate(ctx, u.Username, testPassword); err != nil {
					t.Fatalf("rejected purge changed an active account: %v", err)
				}
				if data, err := os.ReadFile(filepath.Join(srv.dir, u.Slug, "kept.txt")); err != nil || string(data) != "preserved" {
					t.Fatalf("rejected purge removed uploads: %q %v", data, err)
				}
				return
			}
			if state == "no user directory" {
				writeStoredFile(t, srv, "another", "kept.txt", "preserved")
			} else if err := os.Remove(srv.dir); err != nil { // Created by the provider.
				t.Fatal(err)
			}
			if err := srv.accounts.Delete(ctx, u.ID, 999); err != nil {
				t.Fatal(err)
			}
			if err := srv.manager.Purge(ctx, administrator, u.ID); err != nil {
				t.Fatalf("purge requires an unused upload directory: %v", err)
			}
			if _, err := srv.accounts.Create(ctx, u.Username, "new-account-password", false, quota.DefaultQuota); err != nil {
				t.Fatalf("username still reserved: %v", err)
			}
		})
	}
}

func TestPurgeFailuresKeepDeletedAccountReserved(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"database", "storage", "unsafe slug"} {
		t.Run(failure, func(t *testing.T) {
			srv := newTestServer(t, 100)
			ctx := context.Background()
			u := deletedPurgeAccount(t, srv)
			writeStoredFile(t, srv, u.Slug, "kept.txt", "preserved")
			directory, provider := srv.dir, srv.storage
			switch failure {
			case "database":
				if _, err := srv.db.Exec(`CREATE TRIGGER fail_purge BEFORE DELETE ON users BEGIN SELECT RAISE(ABORT, 'test delete failure'); END`); err != nil {
					t.Fatal(err)
				}
			case "storage":
				srv.manager = account.NewManager(srv.accounts, srv.locks, srv.usage, failingRemoval{provider})
			case "unsafe slug":
				if _, err := srv.db.Exec(`UPDATE users SET slug = '../outside' WHERE id = ?`, u.ID); err != nil {
					t.Fatal(err)
				}
			}
			if err := srv.manager.Purge(ctx, administrator, u.ID); err == nil {
				t.Fatal("purging ignored a failure")
			}
			var deleted bool
			if err := srv.db.QueryRow(`SELECT deleted_at IS NOT NULL FROM users WHERE id = ?`, u.ID).Scan(&deleted); err != nil || !deleted {
				t.Fatalf("failed purge released or reactivated the account: %t, %v", deleted, err)
			}
			if data, err := os.ReadFile(filepath.Join(directory, u.Slug, "kept.txt")); err != nil || string(data) != "preserved" {
				t.Fatalf("failed validation removed uploads: %q, %v", data, err)
			}
			if _, err := srv.accounts.Create(ctx, u.Username, "new-account-password", false, quota.DefaultQuota); !errors.Is(err, account.ErrUserConflict) {
				t.Fatalf("failed purge allowed username reuse: %v", err)
			}
			if _, err := srv.db.Exec(`DROP TRIGGER IF EXISTS fail_purge`); err != nil {
				t.Fatal(err)
			}
			if _, err := srv.db.Exec(`UPDATE users SET slug = ? WHERE id = ?`, u.Slug, u.ID); err != nil {
				t.Fatal(err)
			}
			srv.manager = account.NewManager(srv.accounts, srv.locks, srv.usage, provider)
			if err := srv.manager.Purge(ctx, administrator, u.ID); err != nil {
				t.Fatalf("purge could not be retried: %v", err)
			}
		})
	}
}

func TestPurgeDoesNotFollowSymbolicLinks(t *testing.T) {
	t.Parallel()
	for _, linkedAccountRoot := range []bool{false, true} {
		name := "nested link"
		if linkedAccountRoot {
			name = "account root link"
		}
		t.Run(name, func(t *testing.T) {
			srv := newTestServer(t, 100)
			u := deletedPurgeAccount(t, srv)
			writeStoredFile(t, srv, "test-user", "kept.txt", "preserved")
			link := filepath.Join(srv.dir, u.Slug)
			if !linkedAccountRoot {
				writeStoredFile(t, srv, u.Slug, "kept.txt", "old")
				link = filepath.Join(link, "linked")
			}
			if err := os.Symlink(filepath.Join(srv.dir, "test-user"), link); err != nil {
				t.Fatal(err)
			}
			if err := srv.manager.Purge(context.Background(), administrator, u.ID); err != nil {
				t.Fatal(err)
			}
			if data, err := os.ReadFile(filepath.Join(srv.dir, "test-user/kept.txt")); err != nil || string(data) != "preserved" {
				t.Fatalf("purge followed a symlink: %q, %v", data, err)
			}
		})
	}
}

func TestPurgeFileRemovalFailureCanBeRetried(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root bypasses the directory permissions needed for this test")
	}
	srv := newTestServer(t, 100)
	ctx := context.Background()
	u := deletedPurgeAccount(t, srv)
	writeStoredFile(t, srv, u.Slug, "kept.txt", "preserved")
	directory := filepath.Join(srv.dir, u.Slug)
	if err := os.Chmod(directory, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(directory, 0o750) })
	if err := srv.manager.Purge(ctx, administrator, u.ID); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("purge ignored a file removal failure: %v", err)
	}
	if id, err := srv.accounts.DeletedIDForUsername(ctx, u.Username); err != nil || id != u.ID {
		t.Fatalf("failed removal freed the account: %d %v", id, err)
	}
	if _, err := srv.accounts.Create(ctx, u.Username, "new-account-password", false, quota.DefaultQuota); !errors.Is(err, account.ErrUserConflict) {
		t.Fatalf("failed removal allowed username reuse: %v", err)
	}
	if err := os.Chmod(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := srv.manager.Purge(ctx, administrator, u.ID); err != nil {
		t.Fatalf("retry after repairing storage failed: %v", err)
	}
}

func TestPurgeWaitsForUploadsAndSerializesRecycling(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, 100)
	ctx := context.Background()
	u := deletedPurgeAccount(t, srv)
	writeStoredFile(t, srv, u.Slug, "kept.txt", "preserved")
	unlock, err := srv.locks.Lock(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	err = srv.manager.Purge(waitCtx, administrator, u.ID)
	cancel()
	unlock()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("purge did not wait for the active upload: %v", err)
	}
	if _, err := os.Stat(filepath.Join(srv.dir, u.Slug, "kept.txt")); err != nil {
		t.Fatalf("canceled purge removed uploads: %v", err)
	}
	results := make(chan error, 2)
	var passphrase string // Read after both results arrive.
	go func() { results <- srv.manager.Purge(ctx, administrator, u.ID) }()
	go func() {
		credentials, err := srv.manager.Recycle(ctx, administrator, u.ID)
		passphrase = credentials.Passphrase
		results <- err
	}()
	successes := 0
	for range 2 {
		if err := <-results; err == nil {
			successes++
		} else if !errors.Is(err, account.ErrDeletedUserNotFound) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatalf("purge and recycle both changed the account: %d successes", successes)
	}
	_, err = srv.accounts.Authenticate(ctx, u.Username, passphrase)
	if err == nil {
		if data, err := os.ReadFile(filepath.Join(srv.dir, u.Slug, "kept.txt")); err != nil || string(data) != "preserved" {
			t.Fatalf("purge removed a recycled account's uploads: %q, %v", data, err)
		}
	} else if !errors.Is(err, account.ErrInvalidCredentials) {
		t.Fatal(err)
	} else if _, err := os.Stat(filepath.Join(srv.dir, u.Slug)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("purged account kept its upload directory: %v", err)
	}
}

// failingRemoval is a storage provider that cannot remove namespaces.
type failingRemoval struct {
	storage.Provider
}

func (failingRemoval) Remove(context.Context, string) error {
	return errors.New("storage unavailable")
}
