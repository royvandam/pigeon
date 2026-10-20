package pigeon

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func shareRequest(t *testing.T, cfg config, cookie *http.Cookie, csrf, action, name string) string {
	t.Helper()
	w := httptest.NewRecorder()
	r := formRequest("/shares", url.Values{"action": {action}, "path": {name}, "csrf_token": {csrf}}, cookie)
	newServer(cfg).Handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("share %s %q: %d, %s", action, name, w.Code, w.Body)
	}
	var response struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response.URL
}

// shareStatus returns what the Share dialog loads for name.
func shareStatus(t *testing.T, cfg config, cookie *http.Cookie, name string) (string, bool) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/shares?"+url.Values{"path": {name}}.Encode(), nil)
	r.AddCookie(cookie)
	w := serve(newServer(cfg).Handler, r)
	var response struct {
		URL    string `json:"url"`
		Active bool   `json:"active"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &response) != nil {
		t.Fatalf("share status %q: %d, %s", name, w.Code, w.Body)
	}
	return response.URL, response.Active
}

func publicDownload(cfg config, method, link string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	newServer(cfg).Handler.ServeHTTP(w, httptest.NewRequest(method, link, nil))
	return w
}

func TestShareFileLifecycleAndAnonymousDownload(t *testing.T) {
	cfg := newTestConfig(t, 100)
	cookie, csrf := sessionFor(t, cfg, "Test User")
	filename := `résumé "<tag>".html`
	writeStoredFile(t, cfg, "test-user", filename, "<html>shared</html>")
	writeStoredFile(t, cfg, "test-user", "private.txt", "private")
	link := shareRequest(t, cfg, cookie, csrf, "create", filename)
	if !strings.HasPrefix(link, "/s/") || len(strings.TrimPrefix(link, "/s/")) != 43 || strings.Contains(link, "résumé") {
		t.Fatalf("share link does not contain a random token: %q", link)
	}
	// Only a hash is stored, so the URL is not shown again; creating a link
	// again replaces it, and the previous URL stops working.
	var stored string
	if err := cfg.store.db.QueryRow(`SELECT token_hash FROM shares`).Scan(&stored); err != nil || stored != tokenHash(strings.TrimPrefix(link, "/s/")) {
		t.Fatalf("stored share token = %q, %v; want its hash", stored, err)
	}
	if got, active := shareStatus(t, cfg, cookie, filename); got != "" || !active {
		t.Fatalf("share status after creation = %q, %v; want an active link without its URL", got, active)
	}
	replaced := shareRequest(t, cfg, cookie, csrf, "create", filename)
	if replaced == link || !strings.HasPrefix(replaced, "/s/") {
		t.Fatalf("creating again did not replace the link: %s", replaced)
	}
	if got := publicDownload(cfg, http.MethodGet, link).Code; got != http.StatusNotFound {
		t.Fatalf("replaced link still works: %d", got)
	}
	link = replaced
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		w := publicDownload(cfg, method, link+"?path=private.txt&user_id=999")
		kind, params, err := mime.ParseMediaType(w.Header().Get("Content-Disposition"))
		if w.Code != http.StatusOK || kind != "attachment" || params["filename"] != filename || err != nil || w.Header().Get("Content-Type") != "application/octet-stream" || w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("public %s: %d, %v, %v", method, w.Code, w.Header(), err)
		}
		if method == http.MethodGet && w.Body.String() != "<html>shared</html>" || method == http.MethodHead && w.Body.Len() != 0 {
			t.Fatalf("public %s body: %q", method, w.Body)
		}
	}
	r := httptest.NewRequest(http.MethodGet, link, nil)
	r.Header.Set("Range", "bytes=0-5")
	w := httptest.NewRecorder()
	newServer(cfg).Handler.ServeHTTP(w, r)
	if w.Code != http.StatusPartialContent || w.Body.String() != "<html>" {
		t.Fatalf("public range: %d, %s", w.Code, w.Body)
	}
	// Private downloads still require authentication.
	if got := publicDownload(cfg, http.MethodGet, "/download?path=private.txt").Code; got != http.StatusUnauthorized {
		t.Fatalf("unshared download is public: %d", got)
	}
	if got := shareRequest(t, cfg, cookie, csrf, "revoke", filename); got != "" {
		t.Fatalf("revoke left an active link: %q", got)
	}
	if _, active := shareStatus(t, cfg, cookie, filename); active {
		t.Fatal("share status after revoking reports an active link")
	}
	if got := publicDownload(cfg, http.MethodGet, link).Code; got != http.StatusNotFound {
		t.Fatalf("revoked link still works: %d", got)
	}
	if fresh := shareRequest(t, cfg, cookie, csrf, "create", filename); fresh == link || fresh == "" {
		t.Fatalf("recreating revived the old URL: %s", fresh)
	}
}

func TestSharedFolderZipIsScopedAndIncludesCurrentContents(t *testing.T) {
	cfg := newTestConfig(t, 100)
	cookie, csrf := sessionFor(t, cfg, "Test User")
	writeStoredFile(t, cfg, "test-user", "project/docs/readme.txt", "shared")
	writeStoredFile(t, cfg, "test-user", "private.txt", "private")
	writeStoredFile(t, cfg, "test-user", "project/.upload-private/partial.txt", "staging")
	if err := os.MkdirAll(filepath.Join(cfg.dir, "test-user", "project", "empty"), 0o750); err != nil {
		t.Fatal(err)
	}
	link := shareRequest(t, cfg, cookie, csrf, "create", "project")
	writeStoredFile(t, cfg, "test-user", "project/new.txt", "added later")
	w := publicDownload(cfg, http.MethodGet, link)
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("shared folder: %d, %s", w.Code, w.Body)
	}
	archive, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	contents := make(map[string]string)
	for _, entry := range archive.File {
		reader, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(reader)
		reader.Close()
		if err != nil {
			t.Fatal(err)
		}
		contents[entry.Name] = string(data)
		if !strings.HasPrefix(entry.Name, "project/") || strings.Contains(entry.Name, stagingPrefix) || strings.Contains(entry.Name, "private.txt") {
			t.Fatalf("shared ZIP leaked unrelated data: %s", entry.Name)
		}
	}
	for name, want := range map[string]string{"project/docs/readme.txt": "shared", "project/new.txt": "added later", "project/empty/": ""} {
		if got, exists := contents[name]; !exists || got != want {
			t.Errorf("ZIP entry %s = %q, exists=%t", name, got, exists)
		}
	}
	w = publicDownload(cfg, http.MethodHead, link)
	if w.Code != http.StatusOK || w.Body.Len() != 0 || !strings.Contains(w.Header().Get("Content-Disposition"), "project.zip") {
		t.Fatalf("shared folder HEAD: %d, %s", w.Code, w.Body)
	}
}

func TestShareManagementAuthenticationCSRFAndAccountIsolation(t *testing.T) {
	cfg := newTestConfig(t, 100)
	cookie, csrf := sessionFor(t, cfg, "Test User")
	writeStoredFile(t, cfg, "test-user", "same.txt", "first user")
	link := shareRequest(t, cfg, cookie, csrf, "create", "same.txt")
	other, err := cfg.store.createUser(context.Background(), "Other", testPassword, false)
	if err != nil {
		t.Fatal(err)
	}
	otherCookie, otherCSRF := sessionFor(t, cfg, "Other")
	writeStoredFile(t, cfg, other.Slug, "same.txt", "other user")
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		r := httptest.NewRequest(method, "/shares?path=same.txt", nil)
		w := httptest.NewRecorder()
		newServer(cfg).Handler.ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated share management %s: %d", method, w.Code)
		}
	}
	for _, action := range []string{"create", "revoke"} {
		w := httptest.NewRecorder()
		cfg.handleShares(w, formRequest("/shares", url.Values{"action": {action}, "path": {"same.txt"}}, cookie))
		if w.Code != http.StatusForbidden {
			t.Fatalf("missing CSRF %s: %d", action, w.Code)
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/shares?path=same.txt", nil)
	r.AddCookie(otherCookie)
	w := httptest.NewRecorder()
	cfg.handleShares(w, r)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), link) {
		t.Fatalf("another user can read a private share token: %d, %s", w.Code, w.Body)
	}
	otherLink := shareRequest(t, cfg, otherCookie, otherCSRF, "create", "same.txt")
	if otherLink == link {
		t.Fatal("users received the same link")
	}
	shareRequest(t, cfg, otherCookie, otherCSRF, "revoke", "same.txt")
	if w := publicDownload(cfg, http.MethodGet, link); w.Code != http.StatusOK || w.Body.String() != "first user" {
		t.Fatalf("another user revoked the first share: %d, %s", w.Code, w.Body)
	}
	for _, name := range []string{"", ".", "../test-user/same.txt", "/same.txt", ".upload-hidden/secret.txt"} {
		w := httptest.NewRecorder()
		cfg.handleShares(w, formRequest("/shares", url.Values{"action": {"create"}, "path": {name}, "csrf_token": {csrf}}, cookie))
		if w.Code != http.StatusBadRequest {
			t.Errorf("invalid sharing path %q: %d", name, w.Code)
		}
	}
	w = httptest.NewRecorder()
	r = formRequest("/shares", url.Values{"action": {"revoke"}, "path": {"same.txt"}, "csrf_token": {csrf}}, cookie)
	r.Header.Set("Origin", "https://external.example")
	cfg.handleShares(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-origin mutation: %d", w.Code)
	}
}

func TestFileMutationsRevokeSharesBeforePathsCanBeReused(t *testing.T) {
	for _, action := range []string{"rename", "move", "delete"} {
		t.Run(action, func(t *testing.T) {
			cfg := newTestConfig(t, 100)
			cookie, csrf := sessionFor(t, cfg, "Test User")
			name := "写真_%"
			writeStoredFile(t, cfg, "test-user", name+"/nested/readme.txt", "shared")
			writeStoredFile(t, cfg, "test-user", name+"-other/private.txt", "keep link")
			writeStoredFile(t, cfg, "test-user", "destination/existing.txt", "destination")
			folderLink := shareRequest(t, cfg, cookie, csrf, "create", name)
			childLink := shareRequest(t, cfg, cookie, csrf, "create", name+"/nested/readme.txt")
			otherLink := shareRequest(t, cfg, cookie, csrf, "create", name+"-other/private.txt")
			form := url.Values{"action": {action}, "path": {name}, "name": {"renamed"}, "destination": {"destination"}, "csrf_token": {csrf}}
			w := httptest.NewRecorder()
			cfg.handleFiles(w, formRequest("/files", form, cookie))
			if w.Code != http.StatusSeeOther {
				t.Fatalf("%s: %d, %s", action, w.Code, w.Body)
			}
			writeStoredFile(t, cfg, "test-user", name+"/nested/readme.txt", "private replacement")
			for _, link := range []string{folderLink, childLink} {
				if w := publicDownload(cfg, http.MethodGet, link); w.Code != http.StatusNotFound {
					t.Fatalf("%s old link leaked a replacement: %d, %s", action, w.Code, w.Body)
				}
			}
			if w := publicDownload(cfg, http.MethodGet, otherLink); w.Code != http.StatusOK || w.Body.String() != "keep link" {
				t.Fatalf("similar prefix share was revoked: %d, %s", w.Code, w.Body)
			}
		})
	}
}

func TestRejectedAndNoOpFileChangesKeepShareLinks(t *testing.T) {
	cfg := newTestConfig(t, 100)
	cookie, csrf := sessionFor(t, cfg, "Test User")
	writeStoredFile(t, cfg, "test-user", "readme.txt", "shared")
	writeStoredFile(t, cfg, "test-user", "taken.txt", "taken")
	link := shareRequest(t, cfg, cookie, csrf, "create", "readme.txt")
	for _, name := range []string{"taken.txt", "../escape", "readme.txt"} {
		w := httptest.NewRecorder()
		cfg.handleFiles(w, formRequest("/files", url.Values{"action": {"rename"}, "path": {"readme.txt"}, "name": {name}, "csrf_token": {csrf}}, cookie))
		if w := publicDownload(cfg, http.MethodGet, link); w.Code != http.StatusOK || w.Body.String() != "shared" {
			t.Fatalf("rejected/no-op rename to %q revoked the share: %d", name, w.Code)
		}
	}
}

func TestSharesSurviveRestartAndOlderDatabasesGainTheTable(t *testing.T) {
	cfg := newTestConfig(t, 100)
	cookie, csrf := sessionFor(t, cfg, "Test User")
	writeStoredFile(t, cfg, "test-user", "readme.txt", "shared")
	ctx := context.Background()
	var dbPath string
	if err := cfg.store.db.QueryRow(`SELECT file FROM pragma_database_list WHERE name = 'main'`).Scan(&dbPath); err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.store.db.Exec(`DROP TABLE shares`); err != nil {
		t.Fatal(err)
	}
	if err := cfg.store.db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := openStore(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.store = s
	link := shareRequest(t, cfg, cookie, csrf, "create", "readme.txt")
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = openStore(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	cfg.store = s
	if w := publicDownload(cfg, http.MethodGet, link); w.Code != http.StatusOK || w.Body.String() != "shared" {
		t.Fatalf("restart invalidated a share: %d, %s", w.Code, w.Body)
	}
	if got, active := shareStatus(t, cfg, cookie, "readme.txt"); got != "" || !active {
		t.Fatalf("share status after restart = %q, %v", got, active)
	}
}

func TestPlaintextShareTokensAreHashedOnUpgrade(t *testing.T) {
	cfg := newTestConfig(t, 100)
	cookie, _ := sessionFor(t, cfg, "Test User")
	writeStoredFile(t, cfg, "test-user", "readme.txt", "shared")
	ctx := context.Background()
	var dbPath string
	if err := cfg.store.db.QueryRow(`SELECT file FROM pragma_database_list WHERE name = 'main'`).Scan(&dbPath); err != nil {
		t.Fatal(err)
	}
	// Recreate the table as older versions stored it, with a plaintext token.
	token := strings.Repeat("Q", 43)
	for _, statement := range []string{
		`DROP TABLE shares`,
		`CREATE TABLE shares (token TEXT PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  path TEXT NOT NULL, is_directory INTEGER NOT NULL CHECK (is_directory IN (0, 1)), created_at INTEGER NOT NULL, UNIQUE (user_id, path))`,
		`INSERT INTO shares (token, user_id, path, is_directory, created_at) SELECT '` + token + `', id, 'readme.txt', 0, 0 FROM users`,
	} {
		if _, err := cfg.store.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := cfg.store.db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := openStore(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	cfg.store = s
	var stored string
	if err := s.db.QueryRow(`SELECT token_hash FROM shares`).Scan(&stored); err != nil || stored != tokenHash(token) {
		t.Fatalf("upgraded share token = %q, %v; want its hash", stored, err)
	}
	if w := publicDownload(cfg, http.MethodGet, "/s/"+token); w.Code != http.StatusOK || w.Body.String() != "shared" {
		t.Fatalf("upgrade broke an existing link: %d, %s", w.Code, w.Body)
	}
	if got, active := shareStatus(t, cfg, cookie, "readme.txt"); got != "" || !active {
		t.Fatalf("share status after upgrade = %q, %v", got, active)
	}
	// Opening the upgraded database again leaves the hashes alone.
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	if s, err = openStore(ctx, dbPath); err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	cfg.store = s
	if w := publicDownload(cfg, http.MethodGet, "/s/"+token); w.Code != http.StatusOK {
		t.Fatalf("second start broke an upgraded link: %d", w.Code)
	}
}

func TestDeletedUsersLosePublicAccessWhileUploadsArePreserved(t *testing.T) {
	cfg := newTestConfig(t, 100)
	cookie, csrf := sessionFor(t, cfg, "Test User")
	writeStoredFile(t, cfg, "test-user", "readme.txt", "preserved")
	link := shareRequest(t, cfg, cookie, csrf, "create", "readme.txt")
	u, err := cfg.store.authenticate(context.Background(), "Test User", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.store.resetPassword(context.Background(), u.ID, "changed-password"); err != nil {
		t.Fatal(err)
	}
	if w := publicDownload(cfg, http.MethodGet, link); w.Code != http.StatusOK {
		t.Fatalf("password resets unexpectedly revoked independent shares: %d", w.Code)
	}
	if err := cfg.store.deleteUser(context.Background(), u.ID, 999); err != nil {
		t.Fatal(err)
	}
	if w := publicDownload(cfg, http.MethodGet, link); w.Code != http.StatusNotFound {
		t.Fatalf("deleted user's link still works: %d", w.Code)
	}
	var count int
	if err := cfg.store.db.QueryRow(`SELECT COUNT(*) FROM shares`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("deleted user's tokens remain stored: %d, %v", count, err)
	}
	if data, err := os.ReadFile(filepath.Join(cfg.dir, "test-user", "readme.txt")); err != nil || string(data) != "preserved" {
		t.Fatalf("revocation removed uploaded data: %q, %v", data, err)
	}
}

func TestPublicSharesRejectMissingTokensSymlinksAndTypeChanges(t *testing.T) {
	cfg := newTestConfig(t, 100)
	cookie, csrf := sessionFor(t, cfg, "Test User")
	writeStoredFile(t, cfg, "test-user", "readme.txt", "shared")
	writeStoredFile(t, cfg, "test-user", "private.txt", "private")
	link := shareRequest(t, cfg, cookie, csrf, "create", "readme.txt")
	for _, invalid := range []string{"/s/short", "/s/" + strings.Repeat("!", 43), "/s/" + strings.Repeat("x", 43), link + "/private.txt"} {
		w := publicDownload(cfg, http.MethodGet, invalid)
		if w.Code != http.StatusNotFound || w.Header().Get("WWW-Authenticate") != "" {
			t.Errorf("invalid token triggered authentication or downloaded: %d", w.Code)
		}
	}
	w := publicDownload(cfg, http.MethodPost, link)
	if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("public endpoint accepts mutations: %d", w.Code)
	}
	filename := filepath.Join(cfg.dir, "test-user", "readme.txt")
	if err := os.Remove(filename); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("private.txt", filename); err != nil {
		t.Fatal(err)
	}
	if w := publicDownload(cfg, http.MethodGet, link); w.Code != http.StatusNotFound {
		t.Fatalf("share followed a symlink: %d", w.Code)
	}
	if err := os.Remove(filename); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filename, 0o750); err != nil {
		t.Fatal(err)
	}
	if w := publicDownload(cfg, http.MethodGet, link); w.Code != http.StatusNotFound {
		t.Fatalf("file share expanded into directory sharing: %d", w.Code)
	}
}

func TestPublicDownloadsRecheckRevocationAfterWaitingForStorageLock(t *testing.T) {
	cfg := newTestConfig(t, 100)
	cookie, csrf := sessionFor(t, cfg, "Test User")
	writeStoredFile(t, cfg, "test-user", "readme.txt", "shared")
	link := shareRequest(t, cfg, cookie, csrf, "create", "readme.txt")
	ctx := context.Background()
	u, err := cfg.store.authenticate(ctx, "Test User", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := cfg.store.lockUploads(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- publicDownload(cfg, http.MethodGet, link) }()
	select {
	case w := <-done:
		unlock()
		t.Fatalf("public download did not wait for storage: %d", w.Code)
	case <-time.After(30 * time.Millisecond):
	}
	if err := cfg.store.revokeShare(ctx, u.ID, "readme.txt"); err != nil {
		unlock()
		t.Fatal(err)
	}
	unlock()
	select {
	case w := <-done:
		if w.Code != http.StatusNotFound {
			t.Fatalf("queued download used a revoked share: %d", w.Code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("public download did not finish after unlock")
	}
}
