package server

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

	"github.com/royvandam/pigeon/pkg/quota"
)

// cachedUsage returns the usage cached for account id, and whether there is one.
func cachedUsage(srv *testServer, id int64) (int64, bool) {
	used, _, ok := srv.usage.Cached(id)
	return used, ok
}

func TestUsageIsMeasuredOnceAndKeptUpToDate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	srv := newTestServer(t, 1000)
	clock := time.Now()
	srv.setUsageClock(func() time.Time { return clock })
	handler := srv.Handler()
	u, err := srv.accounts.Authenticate(ctx, "Test User", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf := sessionFor(t, srv, "Test User")
	writeStoredFile(t, srv, "test-user", "a/b.txt", "12345")
	writeStoredFile(t, srv, "test-user", "c.txt", "123")
	measured := func() int64 {
		t.Helper()
		used, err := diskUsage(ctx, filepath.Join(srv.dir, "test-user"), 1)
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
	if used, ok := cachedUsage(srv, u.ID); !ok || used != measured() {
		t.Fatalf("first page view cached %d, %v; folder holds %d", used, ok, measured())
	}
	// A file written outside Pigeon is not seen until the account is measured
	// again, so it shows whether a request walked the folder.
	writeStoredFile(t, srv, "test-user", "outside.txt", strings.Repeat("x", 100))
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
		if used, ok := cachedUsage(srv, u.ID); !ok || used != measured()-outside {
			t.Fatalf("after %s: cached %d, %v; want %d without the outside file", step.name, used, ok, measured()-outside)
		}
	}

	// The account is measured again after quota.RemeasureAfter.
	clock = clock.Add(quota.RemeasureAfter)
	page()
	if used, _ := cachedUsage(srv, u.ID); used != measured() {
		t.Fatalf("after remeasuring: cached %d, folder holds %d", used, measured())
	}
}

func TestAdminPageDoesNotCountAnUploadInProgress(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	srv := newTestServer(t, 100)
	clock := time.Now()
	srv.setUsageClock(func() time.Time { return clock })
	if _, err := srv.accounts.Create(ctx, "Admin", testPassword, true, quota.DefaultQuota); err != nil {
		t.Fatal(err)
	}
	u, err := srv.accounts.Authenticate(ctx, "Test User", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf := sessionFor(t, srv, "Test User")
	adminCookie, _ := sessionFor(t, srv, "Admin")
	finish := startGatedUpload(t, srv, cookie, csrf, "", uploadFile{"new.txt", "abc"})

	// The upload measured the account when it started. A long upload outlasts
	// quota.RemeasureAfter, so the admin page measures the account again while
	// the upload's staging folder exists. Counting that folder would charge the
	// upload twice once it is published.
	clock = clock.Add(quota.RemeasureAfter)
	r := httptest.NewRequest(http.MethodGet, "/admin", nil)
	r.AddCookie(adminCookie)
	if w := serve(srv.Handler(), r); w.Code != http.StatusOK {
		t.Fatalf("admin page: %d", w.Code)
	}
	used, measured, ok := srv.usage.Cached(u.ID)
	if !ok || !measured.Equal(clock) || used != 0 {
		t.Fatalf("admin page cached %d measured at %v, %v during the upload; want 0 measured at %v", used, measured, ok, clock)
	}
	if w := finish(); w.Code != http.StatusOK || w.Header().Get("X-Quota-Used") != "3" {
		t.Fatalf("upload: %d, used %s, %s", w.Code, w.Header().Get("X-Quota-Used"), w.Body)
	}
	if used, _ := cachedUsage(srv, u.ID); used != 3 {
		t.Fatalf("cached %d after the upload; want 3", used)
	}
}

func TestPurgeAndFailedChangesForgetUsage(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	ctx := context.Background()
	srv := newTestServer(t, 1000)
	u, err := srv.accounts.Authenticate(ctx, "Test User", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf := sessionFor(t, srv, "Test User")
	writeStoredFile(t, srv, "test-user", "folder/locked/file.txt", "data")
	writeStoredFile(t, srv, "test-user", "folder/free.txt", "data")
	locked := filepath.Join(srv.dir, "test-user", "folder", "locked")
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o750) })
	before, err := srv.files.Usage(ctx, u)
	if err != nil {
		t.Fatal(err)
	}

	// The deletion removes part of the folder before failing. The cached value
	// is dropped rather than adjusted, and the error page measures it again.
	form := url.Values{"action": {"delete"}, "path": {"folder"}, "csrf_token": {csrf}}
	if w := serve(srv.Handler(), formRequest("/files", form, cookie)); w.Code == http.StatusSeeOther {
		t.Fatal("deleting a folder with a read-only subfolder succeeded")
	}
	after, err := diskUsage(ctx, filepath.Join(srv.dir, "test-user"), 1)
	if err != nil || after >= before {
		t.Fatalf("the failed deletion removed nothing: %d of %d remain, %v", after, before, err)
	}
	if used, ok := cachedUsage(srv, u.ID); !ok || used != after {
		t.Fatalf("after a failed deletion: cached %d, %v; folder holds %d", used, ok, after)
	}

	if err := os.Chmod(locked, 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.files.Usage(ctx, u); err != nil {
		t.Fatal(err)
	}
	if err := srv.accounts.Delete(ctx, u.ID, 999); err != nil {
		t.Fatal(err)
	}
	if err := srv.manager.Purge(ctx, administrator, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := cachedUsage(srv, u.ID); ok {
		t.Fatal("purging kept the cached usage")
	}
}
