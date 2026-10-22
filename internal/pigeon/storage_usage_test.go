package pigeon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// cachedUsage returns the usage cached for account id, and whether there is one.
func cachedUsage(cfg config, id int64) (int64, bool) {
	c := cfg.store.usage
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[id]
	return entry.used, ok
}

func TestUsageIsMeasuredOnceAndKeptUpToDate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cfg := newTestConfig(t, 1000)
	handler := newServer(cfg).Handler
	clock := time.Now()
	cfg.store.usage.now = func() time.Time { return clock }
	u, err := cfg.store.authenticate(ctx, "Test User", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf := sessionFor(t, cfg, "Test User")
	writeStoredFile(t, cfg, "test-user", "a/b.txt", "12345")
	writeStoredFile(t, cfg, "test-user", "c.txt", "123")
	measured := func() int64 {
		t.Helper()
		used, err := diskUsage(ctx, filepath.Join(cfg.dir, "test-user"), 1)
		if err != nil {
			t.Fatal(err)
		}
		return used
	}
	page := func() {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.AddCookie(cookie)
		if w := serve(handler, r); w.Code != http.StatusOK {
			t.Fatalf("explorer: %d", w.Code)
		}
	}

	page()
	if used, ok := cachedUsage(cfg, u.ID); !ok || used != measured() {
		t.Fatalf("first page view cached %d, %v; folder holds %d", used, ok, measured())
	}
	// A file written outside Pigeon is not seen until the account is measured
	// again, so it shows whether a request walked the folder.
	writeStoredFile(t, cfg, "test-user", "outside.txt", strings.Repeat("x", 100))
	const outside = 100

	steps := []struct {
		name string
		run  func() int
	}{
		{"upload into a new folder", func() int {
			r := uploadFilesRequest(t, uploadFile{"u/x.txt", "1234"})
			r.Header.Del("Authorization")
			r.AddCookie(cookie)
			r.Header.Set("X-CSRF-Token", csrf)
			return serve(handler, r).Code
		}},
		{"new folder", func() int {
			return serve(handler, formRequest("/files", url.Values{"action": {"mkdir"}, "name": {"m"}, "csrf_token": {csrf}}, cookie)).Code
		}},
		{"move", func() int {
			return serve(handler, formRequest("/files", url.Values{"action": {"move"}, "path": {"c.txt"}, "destination": {"m"}, "csrf_token": {csrf}}, cookie)).Code
		}},
		{"rename", func() int {
			return serve(handler, formRequest("/files", url.Values{"action": {"rename"}, "path": {"m"}, "name": {"n"}, "csrf_token": {csrf}}, cookie)).Code
		}},
		{"delete a folder", func() int {
			return serve(handler, formRequest("/files", url.Values{"action": {"delete"}, "path": {"a"}, "csrf_token": {csrf}}, cookie)).Code
		}},
		{"delete a file", func() int {
			return serve(handler, formRequest("/files", url.Values{"action": {"delete"}, "path": {"u/x.txt"}, "csrf_token": {csrf}}, cookie)).Code
		}},
	}
	for _, step := range steps {
		if code := step.run(); code != http.StatusOK && code != http.StatusSeeOther {
			t.Fatalf("%s: %d", step.name, code)
		}
		page()
		if used, ok := cachedUsage(cfg, u.ID); !ok || used != measured()-outside {
			t.Fatalf("after %s: cached %d, %v; want %d without the outside file", step.name, used, ok, measured()-outside)
		}
	}

	// The account is measured again after usageRemeasureAfter.
	clock = clock.Add(usageRemeasureAfter)
	page()
	if used, _ := cachedUsage(cfg, u.ID); used != measured() {
		t.Fatalf("after remeasuring: cached %d, folder holds %d", used, measured())
	}
}

func TestAdminPageDoesNotCountAnUploadInProgress(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cfg := newTestConfig(t, 100)
	clock := time.Now()
	cfg.store.usage.now = func() time.Time { return clock }
	if _, err := cfg.store.createUser(ctx, "Admin", testPassword, true); err != nil {
		t.Fatal(err)
	}
	u, err := cfg.store.authenticate(ctx, "Test User", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf := sessionFor(t, cfg, "Test User")
	adminCookie, _ := sessionFor(t, cfg, "Admin")
	finish := startGatedUpload(t, cfg, cookie, csrf, "", uploadFile{"new.txt", "abc"})

	// The upload measured the account when it started. A long upload outlasts
	// usageRemeasureAfter, so the admin page measures the account again while
	// the upload's staging folder exists. Counting that folder would charge the
	// upload twice once it is published.
	clock = clock.Add(usageRemeasureAfter)
	r := httptest.NewRequest(http.MethodGet, "/admin", nil)
	r.AddCookie(adminCookie)
	if w := serve(newServer(cfg).Handler, r); w.Code != http.StatusOK {
		t.Fatalf("admin page: %d", w.Code)
	}
	cfg.store.usage.mu.Lock()
	entry, ok := cfg.store.usage.entries[u.ID]
	cfg.store.usage.mu.Unlock()
	if !ok || !entry.measured.Equal(clock) || entry.used != 0 {
		t.Fatalf("admin page cached %+v, %v during the upload; want 0 measured at %v", entry, ok, clock)
	}
	if w := finish(); w.Code != http.StatusOK || w.Header().Get("X-Quota-Used") != "3" {
		t.Fatalf("upload: %d, used %s, %s", w.Code, w.Header().Get("X-Quota-Used"), w.Body)
	}
	if used, _ := cachedUsage(cfg, u.ID); used != 3 {
		t.Fatalf("cached %d after the upload; want 3", used)
	}
}

func TestPurgeAndFailedChangesForgetUsage(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	ctx := context.Background()
	cfg := newTestConfig(t, 1000)
	u, err := cfg.store.authenticate(ctx, "Test User", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf := sessionFor(t, cfg, "Test User")
	writeStoredFile(t, cfg, "test-user", "folder/locked/file.txt", "data")
	writeStoredFile(t, cfg, "test-user", "folder/free.txt", "data")
	locked := filepath.Join(cfg.dir, "test-user", "folder", "locked")
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o750) })
	before, err := cfg.storedUsage(ctx, u)
	if err != nil {
		t.Fatal(err)
	}

	// The deletion removes part of the folder before failing. The cached value
	// is dropped rather than adjusted, and the error page measures it again.
	form := url.Values{"action": {"delete"}, "path": {"folder"}, "csrf_token": {csrf}}
	if w := serve(newServer(cfg).Handler, formRequest("/files", form, cookie)); w.Code == http.StatusSeeOther {
		t.Fatal("deleting a folder with a read-only subfolder succeeded")
	}
	after, err := diskUsage(ctx, filepath.Join(cfg.dir, "test-user"), 1)
	if err != nil || after >= before {
		t.Fatalf("the failed deletion removed nothing: %d of %d remain, %v", after, before, err)
	}
	if used, ok := cachedUsage(cfg, u.ID); !ok || used != after {
		t.Fatalf("after a failed deletion: cached %d, %v; folder holds %d", used, ok, after)
	}

	if err := os.Chmod(locked, 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.storedUsage(ctx, u); err != nil {
		t.Fatal(err)
	}
	if err := cfg.store.deleteUser(ctx, u.ID, 999); err != nil {
		t.Fatal(err)
	}
	if err := cfg.accounts().purge(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := cachedUsage(cfg, u.ID); ok {
		t.Fatal("purging kept the cached usage")
	}
}
