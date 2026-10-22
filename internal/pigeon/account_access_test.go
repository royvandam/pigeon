package pigeon

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/royvandam/pigeon/pkg/account"
	"github.com/royvandam/pigeon/pkg/auth"
	"github.com/royvandam/pigeon/pkg/quota"
)

func TestReadsRejectOldIdentityAfterPurgeAndUsernameReuse(t *testing.T) {
	t.Parallel()
	cfg := newTestConfig(t, 100)
	ctx := context.Background()
	old, err := cfg.store.accounts.Authenticate(ctx, "Test User", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	writeStoredFile(t, cfg, old.Slug, "old.txt", "old contents")
	if err := cfg.store.accounts.Delete(ctx, old.ID, 999); err != nil {
		t.Fatal(err)
	}
	if err := cfg.manager.Purge(ctx, administrator, old.ID); err != nil {
		t.Fatal(err)
	}
	fresh, err := cfg.store.accounts.Create(ctx, old.Username, "replacement-password", false, quota.DefaultQuota)
	if err != nil {
		t.Fatal(err)
	}
	writeStoredFile(t, cfg, fresh.Slug, "private.txt", "replacement account's private contents")
	if download, err := cfg.openPrivateDownload(ctx, old, "private.txt"); !errors.Is(err, account.ErrInvalidCredentials) {
		if download != nil {
			download.Close()
		}
		t.Fatalf("old identity can download replacement account files: %v", err)
	}
	w := httptest.NewRecorder()
	cfg.renderIndex(w, httptest.NewRequest(http.MethodGet, "/", nil), auth.Authentication{User: old}, "", "", "", http.StatusOK)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("old identity can browse replacement account: %d %s", w.Code, w.Body)
	}
	access, err := cfg.files.Open(ctx, fresh)
	if err != nil {
		t.Fatalf("replacement account cannot access its own files: %v", err)
	}
	access.Close()
	access.Close() // Error rendering and deferred cleanup can both release access.
}

func TestReadAccessRechecksIdentityAfterWaiting(t *testing.T) {
	t.Parallel()
	cfg := newTestConfig(t, 100)
	ctx := context.Background()
	u, err := cfg.store.accounts.Authenticate(ctx, "Test User", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	release, err := cfg.locks.Lock(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		access, err := cfg.files.Open(ctx, u)
		if access != nil {
			access.Close()
		}
		done <- err
	}()
	_, err = cfg.store.accounts.ResetPassword(ctx, u.ID, "replacement-password")
	release()
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, account.ErrInvalidCredentials) {
			t.Fatalf("read accepted credentials revoked while waiting: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("read did not finish after the account lock was released")
	}
}

func TestPrivateDownloadReleasesAccessBeforeStreaming(t *testing.T) {
	t.Parallel()
	cfg := newTestConfig(t, 100)
	ctx := context.Background()
	u, err := cfg.store.accounts.Authenticate(ctx, "Test User", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	writeStoredFile(t, cfg, u.Slug, "kept.txt", "original")
	download, err := cfg.openPrivateDownload(ctx, u, "kept.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer download.Close()
	waitCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	access, err := cfg.files.Open(waitCtx, u)
	if err != nil {
		t.Fatalf("download retained the account lock while streaming: %v", err)
	}
	access.Close()
	// The opened handle stays bound to the item when its path is renamed.
	if err := os.Rename(filepath.Join(cfg.dir, u.Slug, "kept.txt"), filepath.Join(cfg.dir, u.Slug, "renamed.txt")); err != nil {
		t.Fatal(err)
	}
	writeStoredFile(t, cfg, u.Slug, "kept.txt", "replacement")
	w := httptest.NewRecorder()
	serveDownload(w, httptest.NewRequest(http.MethodGet, "/download?path=kept.txt", nil), download)
	if w.Code != http.StatusOK || w.Body.String() != "original" {
		t.Fatalf("download followed a reused path: %d %s", w.Code, w.Body)
	}
}
