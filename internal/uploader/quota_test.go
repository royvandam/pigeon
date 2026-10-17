package uploader

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestDefaultQuotaAndDatabaseMigration(t *testing.T) {
	cfg := newTestConfig(t, 100)
	ctx := context.Background()
	u, err := cfg.store.authenticate(ctx, "Test User", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	token, err := cfg.store.createSession(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	want := settings{dir: cfg.dir, listen: "127.0.0.1:9000"}
	if err := cfg.store.saveSettings(ctx, want); err != nil {
		t.Fatal(err)
	}
	// Recreate the previous schema with a single upload limit and no quota.
	if _, err := cfg.store.db.Exec(`ALTER TABLE users DROP COLUMN quota_bytes;
ALTER TABLE settings ADD COLUMN max_bytes INTEGER NOT NULL DEFAULT 209715200 CHECK (max_bytes > 0);`); err != nil {
		t.Fatal(err)
	}
	if err := cfg.store.db.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(filepath.Dir(cfg.dir), "app.db")
	s, err := openStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	got, err := s.loadSettings(ctx)
	if err != nil || got != want {
		t.Fatalf("migration changed server settings: %#v, %v", got, err)
	}
	a, err := s.session(ctx, token)
	if err != nil || a.User.QuotaBytes != defaultUserQuota {
		t.Fatalf("migrated user's session/quota = %#v, %v", a, err)
	}
	var hasLimit bool
	if err := s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pragma_table_info('settings') WHERE name = 'max_bytes')`).Scan(&hasLimit); err != nil || hasLimit {
		t.Fatalf("single upload limit remains after migration: %t, %v", hasLimit, err)
	}
	created, err := s.createUser(ctx, "New User", testPassword, false)
	if err != nil || created.QuotaBytes != 1<<30 {
		t.Fatalf("new user's default quota = %#v, %v", created, err)
	}
	if err := s.setQuota(ctx, a.User.ID, 12345); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = openStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	a, err = s.session(ctx, token)
	if err != nil || a.User.QuotaBytes != 12345 {
		t.Fatalf("quota did not survive restart: %#v, %v", a, err)
	}
}

func TestQuotaCountsExistingFilesAndReclaimsRemovedFiles(t *testing.T) {
	cfg := newTestConfig(t, 10)
	dir := filepath.Join(cfg.dir, "test-user", "existing")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "original.txt")
	if err := os.WriteFile(path, []byte("abcdef"), 0o640); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	cfg.handleUpload(w, uploadRequest(t, "next.txt", "1234"))
	if w.Code != http.StatusOK || w.Header().Get("X-Quota-Used") != "10" || w.Header().Get("X-Quota-Remaining") != "0" {
		t.Fatalf("upload at remaining quota: %d, %v, %s", w.Code, w.Header(), w.Body)
	}
	body := &readTrackingBody{}
	r := httptest.NewRequest(http.MethodPost, "/upload", nil)
	r.Body = body
	r.SetBasicAuth("Test User", testPassword)
	r.Header.Set("Content-Type", "multipart/form-data; boundary=test")
	w = httptest.NewRecorder()
	cfg.handleUpload(w, r)
	if w.Code != http.StatusRequestEntityTooLarge || body.read {
		t.Fatalf("full quota should reject without reading: %d, body read = %t", w.Code, body.read)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	cfg.handleUpload(w, uploadRequest(t, "reclaimed.txt", "123456"))
	if w.Code != http.StatusOK || w.Header().Get("X-Quota-Used") != "10" {
		t.Fatalf("removed files did not free storage: %d, %s", w.Code, w.Body)
	}
}

func TestConcurrentUploadsCannotExceedQuota(t *testing.T) {
	cfg := newTestConfig(t, 100)
	cookie, csrf := sessionFor(t, cfg, "Test User")
	const count = 8
	start := make(chan struct{})
	results := make(chan int, count)
	var workers sync.WaitGroup
	for i := 0; i < count; i++ {
		r := uploadRequest(t, "file-"+strconv.Itoa(i)+".txt", strings.Repeat("x", 60))
		r.Header.Del("Authorization")
		r.AddCookie(cookie)
		r.Header.Set("X-CSRF-Token", csrf)
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			w := httptest.NewRecorder()
			cfg.handleUpload(w, r)
			results <- w.Code
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	success := 0
	for status := range results {
		if status == http.StatusOK {
			success++
		} else if status != http.StatusRequestEntityTooLarge {
			t.Fatalf("unexpected upload status %d", status)
		}
	}
	used, err := diskUsage(context.Background(), filepath.Join(cfg.dir, "test-user"))
	files, readErr := readUploadDir(cfg.dir, "test-user")
	if success != 1 || used != 60 || err != nil || readErr != nil || len(files) != 1 {
		t.Fatalf("concurrent quota enforcement: success=%d, used=%d, files=%d, errors=%v/%v", success, used, len(files), err, readErr)
	}
}

func TestAdminCanAssignAndChangeQuotas(t *testing.T) {
	cfg := newTestConfig(t, 10)
	ctx := context.Background()
	if _, err := cfg.store.createUser(ctx, "Admin", testPassword, true); err != nil {
		t.Fatal(err)
	}
	cookie, csrf := sessionFor(t, cfg, "Admin")
	form := url.Values{"action": {"create"}, "username": {"Quota User"}, "quota": {"20B"}, "csrf_token": {csrf}}
	w := httptest.NewRecorder()
	cfg.handleAdmin(w, formRequest("/admin", form, cookie))
	if w.Code != http.StatusOK {
		t.Fatalf("create user with quota: %d, %s", w.Code, w.Body)
	}
	passphrase := createdPassphrase(t, w)
	u, err := cfg.store.authenticate(ctx, "Quota User", passphrase)
	if err != nil || u.QuotaBytes != 20 {
		t.Fatalf("assigned quota = %#v, %v", u, err)
	}
	userCookie, userCSRF := sessionForPassword(t, cfg, "Quota User", passphrase)
	r := uploadRequest(t, "file.txt", "12345678")
	r.Header.Del("Authorization")
	r.AddCookie(userCookie)
	r.Header.Set("X-CSRF-Token", userCSRF)
	w = httptest.NewRecorder()
	cfg.handleUpload(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("user upload: %d, %s", w.Code, w.Body)
	}
	form = url.Values{"action": {"quota"}, "user_id": {strconv.FormatInt(u.ID, 10)}, "quota": {"4B"}, "csrf_token": {csrf}}
	w = httptest.NewRecorder()
	cfg.handleAdmin(w, formRequest("/admin", form, cookie))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("change quota: %d, %s", w.Code, w.Body)
	}
	a, err := cfg.store.session(ctx, userCookie.Value)
	if err != nil || a.User.QuotaBytes != 4 {
		t.Fatalf("existing session did not see changed quota: %#v, %v", a, err)
	}
	w = httptest.NewRecorder()
	r = uploadRequest(t, "blocked.txt", "a")
	r.Header.Del("Authorization")
	r.AddCookie(userCookie)
	r.Header.Set("X-CSRF-Token", userCSRF)
	cfg.handleUpload(w, r)
	if w.Code != http.StatusRequestEntityTooLarge || w.Header().Get("X-Quota-Remaining") != "0" {
		t.Fatalf("lowered quota still allows uploads: %d, %s", w.Code, w.Body)
	}
	used, err := diskUsage(ctx, filepath.Join(cfg.dir, u.Slug))
	if err != nil || used != 8 {
		t.Fatalf("quota reduction removed stored data: %d, %v", used, err)
	}
	form.Set("quota", "12B")
	w = httptest.NewRecorder()
	cfg.handleAdmin(w, formRequest("/admin", form, cookie))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("increase quota: %d, %s", w.Code, w.Body)
	}
	r = uploadRequest(t, "allowed.txt", "1234")
	r.Header.Del("Authorization")
	r.AddCookie(userCookie)
	r.Header.Set("X-CSRF-Token", userCSRF)
	w = httptest.NewRecorder()
	cfg.handleUpload(w, r)
	if w.Code != http.StatusOK || w.Header().Get("X-Quota-Used") != "12" {
		t.Fatalf("increased quota not used: %d, %s", w.Code, w.Body)
	}
	form.Set("quota", "0B")
	w = httptest.NewRecorder()
	cfg.handleAdmin(w, formRequest("/admin", form, cookie))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid quota accepted: %d", w.Code)
	}
	u, err = cfg.store.authenticate(ctx, "Quota User", passphrase)
	if err != nil || u.QuotaBytes != 12 {
		t.Fatalf("invalid update changed quota: %#v, %v", u, err)
	}
}

func TestQuotaValidationAndCanceledLock(t *testing.T) {
	for _, quota := range []int64{0, -1, math.MaxInt64} {
		if err := validateQuota(quota); err == nil {
			t.Errorf("accepted quota %d", quota)
		}
	}
	cfg := newTestConfig(t, 10)
	unlock, err := cfg.store.lockUploads(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := cfg.store.lockUploads(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled request waited for the quota lock: %v", err)
	}
}
