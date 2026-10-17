package uploader

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReadsRejectOldIdentityAfterPurgeAndUsernameReuse(t *testing.T) {
	cfg := newTestConfig(t, 100)
	ctx := context.Background()
	old, err := cfg.store.authenticate(ctx, "Test User", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	writeStoredFile(t, cfg, old.Slug, "old.txt", "old contents")
	if err := cfg.store.deleteUser(ctx, old.ID, 999); err != nil {
		t.Fatal(err)
	}
	if err := cfg.accounts().purge(ctx, old.ID); err != nil {
		t.Fatal(err)
	}
	fresh, err := cfg.store.createUser(ctx, old.Username, "replacement-password", false)
	if err != nil {
		t.Fatal(err)
	}
	writeStoredFile(t, cfg, fresh.Slug, "private.txt", "replacement account's private contents")
	if download, err := cfg.openPrivateDownload(ctx, old, "private.txt"); !errors.Is(err, errInvalidCredentials) {
		if download != nil {
			download.Close()
		}
		t.Fatalf("old identity can download replacement account files: %v", err)
	}
	w := httptest.NewRecorder()
	cfg.renderIndex(w, httptest.NewRequest(http.MethodGet, "/", nil), authentication{User: old}, "", "", "", http.StatusOK)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("old identity can browse replacement account: %d %s", w.Code, w.Body)
	}
	access, err := cfg.accessAccount(ctx, fresh)
	if err != nil {
		t.Fatalf("replacement account cannot access its own files: %v", err)
	}
	access.close()
	access.close() // Error rendering and deferred cleanup can both release access.
}

func TestReadAccessRechecksIdentityAfterWaiting(t *testing.T) {
	cfg := newTestConfig(t, 100)
	ctx := context.Background()
	u, err := cfg.store.authenticate(ctx, "Test User", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	release, err := cfg.store.lockUploads(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		access, err := cfg.accessAccount(ctx, u)
		if access != nil {
			access.close()
		}
		done <- err
	}()
	err = cfg.store.resetPassword(ctx, u.ID, "replacement-password")
	release()
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, errInvalidCredentials) {
			t.Fatalf("read accepted credentials revoked while waiting: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("read did not finish after the account lock was released")
	}
}

func TestPrivateDownloadReleasesAccessBeforeStreaming(t *testing.T) {
	cfg := newTestConfig(t, 100)
	ctx := context.Background()
	u, err := cfg.store.authenticate(ctx, "Test User", testPassword)
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
	access, err := cfg.accessAccount(waitCtx, u)
	if err != nil {
		t.Fatalf("download retained the account lock while streaming: %v", err)
	}
	access.close()
	// The opened handle stays bound to the item when its path is renamed.
	if err := os.Rename(filepath.Join(cfg.dir, u.Slug, "kept.txt"), filepath.Join(cfg.dir, u.Slug, "renamed.txt")); err != nil {
		t.Fatal(err)
	}
	writeStoredFile(t, cfg, u.Slug, "kept.txt", "replacement")
	w := httptest.NewRecorder()
	download.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/download?path=kept.txt", nil))
	if w.Code != http.StatusOK || w.Body.String() != "original" {
		t.Fatalf("download followed a reused path: %d %s", w.Code, w.Body)
	}
}
