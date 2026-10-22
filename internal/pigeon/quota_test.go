package pigeon

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/royvandam/pigeon/pkg/quota"
)

func TestDefaultQuotaAndDatabaseMigration(t *testing.T) {
	t.Parallel()
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
	s, err := openStore(ctx, path, testPasswordCost)
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	got, err := s.loadSettings(ctx)
	if err != nil || got != want {
		t.Fatalf("migration changed server settings: %#v, %v", got, err)
	}
	a, err := s.session(ctx, token)
	if err != nil || a.User.QuotaBytes != quota.DefaultQuota {
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
	s, err = openStore(ctx, path, testPasswordCost)
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
	t.Parallel()
	cfg := newTestConfig(t, 10)
	dir := filepath.Join(cfg.dir, "test-user", "existing")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "original.txt")
	// The existing folder takes one unit; with this file it uses six of ten bytes.
	if err := os.WriteFile(path, []byte("abcde"), 0o640); err != nil {
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
	cookie, csrf := sessionFor(t, cfg, "Test User")
	form := url.Values{"action": {"delete"}, "path": {"existing/original.txt"}, "csrf_token": {csrf}}
	if w := serve(newServer(cfg).Handler, formRequest("/files", form, cookie)); w.Code != http.StatusSeeOther {
		t.Fatalf("delete: %d, %s", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	cfg.handleUpload(w, uploadRequest(t, "reclaimed.txt", "12345"))
	if w.Code != http.StatusOK || w.Header().Get("X-Quota-Used") != "10" {
		t.Fatalf("removed files did not free storage: %d, %s", w.Code, w.Body)
	}
}

func TestConcurrentUploadsCannotExceedQuota(t *testing.T) {
	t.Parallel()
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
	used, err := diskUsage(context.Background(), filepath.Join(cfg.dir, "test-user"), 1)
	files, readErr := readUploadDir(cfg.dir, "test-user")
	if success != 1 || used != 60 || err != nil || readErr != nil || len(files) != 1 {
		t.Fatalf("concurrent quota enforcement: success=%d, used=%d, files=%d, errors=%v/%v", success, used, len(files), err, readErr)
	}
}

func TestAdminCanAssignAndChangeQuotas(t *testing.T) {
	t.Parallel()
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
	passphrase := displayedPassphrase(t, w)
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
	used, err := diskUsage(ctx, filepath.Join(cfg.dir, u.Slug), 1)
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

func TestCanceledRequestsDoNotWaitForTheAccountLock(t *testing.T) {
	t.Parallel()
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

// productionUnitConfig uses the real 4 KiB allocation unit (audit A3).
func productionUnitConfig(t *testing.T, quota int64) config {
	t.Helper()
	cfg := newTestConfig(t, quota)
	cfg.allocationUnit = 0
	return cfg
}

func TestQuotaChargesEmptyFilesAndFolders(t *testing.T) {
	t.Parallel()
	cfg := productionUnitConfig(t, 16<<10)
	cookie, csrf := sessionFor(t, cfg, "Test User")
	handler := newServer(cfg).Handler
	page := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(cookie)
	handler.ServeHTTP(page, r)
	if !strings.Contains(page.Body.String(), `data-allocation-unit="4096"`) {
		t.Fatal("the explorer does not tell upload.js the allocation unit")
	}

	// Two new folders and an empty file each take one 4 KiB unit.
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, uploadFilesRequest(t, uploadFile{"a/b/empty.txt", ""}))
	if w.Code != http.StatusOK || w.Header().Get("X-Quota-Used") != "12288" || w.Header().Get("X-Quota-Remaining") != "4096" {
		t.Fatalf("empty file in new folders: %d, %v, %s", w.Code, w.Header(), w.Body)
	}

	mkdir := func(name string) int {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, formRequest("/files", url.Values{"action": {"mkdir"}, "name": {name}, "csrf_token": {csrf}}, cookie))
		return w.Code
	}
	if status := mkdir("last"); status != http.StatusSeeOther {
		t.Fatalf("folder within quota: %d", status)
	}
	if status := mkdir("over"); status != http.StatusRequestEntityTooLarge {
		t.Fatalf("folder beyond quota: %d", status)
	}
	if _, err := os.Stat(filepath.Join(cfg.dir, "test-user", "over")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected folder was created: %v", err)
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, uploadFilesRequest(t, uploadFile{"another-empty.txt", ""}))
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("empty file beyond quota: %d, %s", w.Code, w.Body)
	}
	used, err := cfg.filesFor(user{Slug: "test-user"}).usage(context.Background())
	if err != nil || used != 16<<10 {
		t.Fatalf("usage = %d, %v; want the full quota", used, err)
	}
}

func TestQuotaRoundsFileContentsUpToWholeUnits(t *testing.T) {
	t.Parallel()
	cfg := productionUnitConfig(t, 8<<10)
	w := httptest.NewRecorder()
	cfg.handleUpload(w, uploadRequest(t, "too-big.bin", strings.Repeat("x", 8<<10+1)))
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("file needing three units: %d, %s", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	cfg.handleUpload(w, uploadRequest(t, "fits.bin", strings.Repeat("x", 4<<10+1)))
	if w.Code != http.StatusOK || w.Header().Get("X-Quota-Used") != "8192" || uploadResult(t, w, "bytes") != "4097" {
		t.Fatalf("file needing two units: %d, %v, %s", w.Code, w.Header(), w.Body)
	}
}

// The audit reproduction: one small request of empty files in deep folders
// created 31,001 inodes while usage stayed at zero.
func TestQuotaBoundsDeepEmptyFileUploads(t *testing.T) {
	t.Parallel()
	cfg := productionUnitConfig(t, 1<<20)
	deep := strings.Repeat("x/", 60)
	var files []uploadFile
	for i := range 500 {
		files = append(files, uploadFile{"d" + strconv.Itoa(i) + "/" + deep + "f", ""})
	}
	w := httptest.NewRecorder()
	cfg.handleUpload(w, uploadFilesRequest(t, files...))
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("deep empty files: %d, %s", w.Code, w.Body)
	}
	entries, err := readUploadDir(cfg.dir, "test-user")
	if err != nil || len(entries) != 0 {
		t.Fatalf("rejected batch left entries: %v, %v", entries, err)
	}
	// What fits is bounded by the quota: 1 MiB allows at most 256 entries.
	var allowed []uploadFile
	for i := range 4 {
		allowed = append(allowed, uploadFile{"d" + strconv.Itoa(i) + "/" + deep + "f", ""})
	}
	w = httptest.NewRecorder()
	cfg.handleUpload(w, uploadFilesRequest(t, allowed...))
	if w.Code != http.StatusOK || w.Header().Get("X-Quota-Used") != strconv.Itoa(4*62*4096) {
		t.Fatalf("four deep empty files (62 entries each): %d, %v, %s", w.Code, w.Header(), w.Body)
	}
}
