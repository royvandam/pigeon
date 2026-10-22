package server

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

	"github.com/royvandam/pigeon/internal/token"
	"github.com/royvandam/pigeon/pkg/quota"
	"github.com/royvandam/pigeon/pkg/storage"
)

// shareStatus returns what the Share dialog loads for name.
func shareStatus(t *testing.T, srv *testServer, cookie *http.Cookie, name string) (string, bool) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/shares?"+url.Values{"path": {name}}.Encode(), nil)
	r.AddCookie(cookie)
	w := serve(srv.Handler(), r)
	var response struct {
		URL    string `json:"url"`
		Active bool   `json:"active"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &response) != nil {
		t.Fatalf("share status %q: %d, %s", name, w.Code, w.Body)
	}
	return response.URL, response.Active
}

func TestShareFileLifecycleAndAnonymousDownload(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, 100)
	cookie, csrf := sessionFor(t, srv, "Test User")
	filename := `résumé "<tag>".html`
	writeStoredFile(t, srv, "test-user", filename, "<html>shared</html>")
	writeStoredFile(t, srv, "test-user", "private.txt", "private")
	link := shareRequest(t, srv, cookie, csrf, "create", filename)
	if !strings.HasPrefix(link, "/s/") || len(strings.TrimPrefix(link, "/s/")) != 43 || strings.Contains(link, "résumé") {
		t.Fatalf("share link does not contain a random token: %q", link)
	}
	// Only a hash is stored, so the URL is not shown again; creating a link
	// again replaces it, and the previous URL stops working.
	var stored string
	if err := srv.db.QueryRow(`SELECT token_hash FROM shares`).Scan(&stored); err != nil || stored != token.Hash(strings.TrimPrefix(link, "/s/")) {
		t.Fatalf("stored share token = %q, %v; want its hash", stored, err)
	}
	if got, active := shareStatus(t, srv, cookie, filename); got != "" || !active {
		t.Fatalf("share status after creation = %q, %v; want an active link without its URL", got, active)
	}
	replaced := shareRequest(t, srv, cookie, csrf, "create", filename)
	if replaced == link || !strings.HasPrefix(replaced, "/s/") {
		t.Fatalf("creating again did not replace the link: %s", replaced)
	}
	if got := publicDownload(srv, http.MethodGet, link).Code; got != http.StatusNotFound {
		t.Fatalf("replaced link still works: %d", got)
	}
	link = replaced
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		w := publicDownload(srv, method, link+"?path=private.txt&user_id=999")
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
	srv.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusPartialContent || w.Body.String() != "<html>" {
		t.Fatalf("public range: %d, %s", w.Code, w.Body)
	}
	// Private downloads still require authentication.
	if got := publicDownload(srv, http.MethodGet, "/download?path=private.txt").Code; got != http.StatusUnauthorized {
		t.Fatalf("unshared download is public: %d", got)
	}
	if got := shareRequest(t, srv, cookie, csrf, "revoke", filename); got != "" {
		t.Fatalf("revoke left an active link: %q", got)
	}
	if _, active := shareStatus(t, srv, cookie, filename); active {
		t.Fatal("share status after revoking reports an active link")
	}
	if got := publicDownload(srv, http.MethodGet, link).Code; got != http.StatusNotFound {
		t.Fatalf("revoked link still works: %d", got)
	}
	if fresh := shareRequest(t, srv, cookie, csrf, "create", filename); fresh == link || fresh == "" {
		t.Fatalf("recreating revived the old URL: %s", fresh)
	}
}

func TestSharedFolderZipIsScopedAndIncludesCurrentContents(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, 100)
	cookie, csrf := sessionFor(t, srv, "Test User")
	writeStoredFile(t, srv, "test-user", "project/docs/readme.txt", "shared")
	writeStoredFile(t, srv, "test-user", "private.txt", "private")
	writeStoredFile(t, srv, "test-user", "project/.upload-private/partial.txt", "staging")
	if err := os.MkdirAll(filepath.Join(srv.dir, "test-user", "project", "empty"), 0o750); err != nil {
		t.Fatal(err)
	}
	link := shareRequest(t, srv, cookie, csrf, "create", "project")
	writeStoredFile(t, srv, "test-user", "project/new.txt", "added later")
	w := publicDownload(srv, http.MethodGet, link)
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
		if !strings.HasPrefix(entry.Name, "project/") || strings.Contains(entry.Name, storage.ReservedPrefix) || strings.Contains(entry.Name, "private.txt") {
			t.Fatalf("shared ZIP leaked unrelated data: %s", entry.Name)
		}
	}
	for name, want := range map[string]string{"project/docs/readme.txt": "shared", "project/new.txt": "added later", "project/empty/": ""} {
		if got, exists := contents[name]; !exists || got != want {
			t.Errorf("ZIP entry %s = %q, exists=%t", name, got, exists)
		}
	}
	w = publicDownload(srv, http.MethodHead, link)
	if w.Code != http.StatusOK || w.Body.Len() != 0 || !strings.Contains(w.Header().Get("Content-Disposition"), "project.zip") {
		t.Fatalf("shared folder HEAD: %d, %s", w.Code, w.Body)
	}
}

func TestShareManagementAuthenticationCSRFAndAccountIsolation(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, 100)
	cookie, csrf := sessionFor(t, srv, "Test User")
	writeStoredFile(t, srv, "test-user", "same.txt", "first user")
	link := shareRequest(t, srv, cookie, csrf, "create", "same.txt")
	other, err := srv.accounts.Create(context.Background(), "Other", testPassword, false, quota.DefaultQuota)
	if err != nil {
		t.Fatal(err)
	}
	otherCookie, otherCSRF := sessionFor(t, srv, "Other")
	writeStoredFile(t, srv, other.Slug, "same.txt", "other user")
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		r := httptest.NewRequest(method, "/shares?path=same.txt", nil)
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated share management %s: %d", method, w.Code)
		}
	}
	for _, action := range []string{"create", "revoke"} {
		w := httptest.NewRecorder()
		srv.handleShares(w, formRequest("/shares", url.Values{"action": {action}, "path": {"same.txt"}}, cookie))
		if w.Code != http.StatusForbidden {
			t.Fatalf("missing CSRF %s: %d", action, w.Code)
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/shares?path=same.txt", nil)
	r.AddCookie(otherCookie)
	w := httptest.NewRecorder()
	srv.handleShares(w, r)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), link) {
		t.Fatalf("another user can read a private share token: %d, %s", w.Code, w.Body)
	}
	otherLink := shareRequest(t, srv, otherCookie, otherCSRF, "create", "same.txt")
	if otherLink == link {
		t.Fatal("users received the same link")
	}
	shareRequest(t, srv, otherCookie, otherCSRF, "revoke", "same.txt")
	if w := publicDownload(srv, http.MethodGet, link); w.Code != http.StatusOK || w.Body.String() != "first user" {
		t.Fatalf("another user revoked the first share: %d, %s", w.Code, w.Body)
	}
	for _, name := range []string{"", ".", "../test-user/same.txt", "/same.txt", ".upload-hidden/secret.txt"} {
		w := httptest.NewRecorder()
		srv.handleShares(w, formRequest("/shares", url.Values{"action": {"create"}, "path": {name}, "csrf_token": {csrf}}, cookie))
		if w.Code != http.StatusBadRequest {
			t.Errorf("invalid sharing path %q: %d", name, w.Code)
		}
	}
	w = httptest.NewRecorder()
	r = formRequest("/shares", url.Values{"action": {"revoke"}, "path": {"same.txt"}, "csrf_token": {csrf}}, cookie)
	r.Header.Set("Origin", "https://external.example")
	srv.handleShares(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-origin mutation: %d", w.Code)
	}
}

func TestFileMutationsRevokeSharesBeforePathsCanBeReused(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"rename", "move", "delete"} {
		t.Run(action, func(t *testing.T) {
			srv := newTestServer(t, 100)
			cookie, csrf := sessionFor(t, srv, "Test User")
			name := "写真_%"
			writeStoredFile(t, srv, "test-user", name+"/nested/readme.txt", "shared")
			writeStoredFile(t, srv, "test-user", name+"-other/private.txt", "keep link")
			writeStoredFile(t, srv, "test-user", "destination/existing.txt", "destination")
			folderLink := shareRequest(t, srv, cookie, csrf, "create", name)
			childLink := shareRequest(t, srv, cookie, csrf, "create", name+"/nested/readme.txt")
			otherLink := shareRequest(t, srv, cookie, csrf, "create", name+"-other/private.txt")
			form := url.Values{"action": {action}, "path": {name}, "name": {"renamed"}, "destination": {"destination"}, "csrf_token": {csrf}}
			w := httptest.NewRecorder()
			srv.handleFiles(w, formRequest("/files", form, cookie))
			if w.Code != http.StatusSeeOther {
				t.Fatalf("%s: %d, %s", action, w.Code, w.Body)
			}
			writeStoredFile(t, srv, "test-user", name+"/nested/readme.txt", "private replacement")
			for _, link := range []string{folderLink, childLink} {
				if w := publicDownload(srv, http.MethodGet, link); w.Code != http.StatusNotFound {
					t.Fatalf("%s old link leaked a replacement: %d, %s", action, w.Code, w.Body)
				}
			}
			if w := publicDownload(srv, http.MethodGet, otherLink); w.Code != http.StatusOK || w.Body.String() != "keep link" {
				t.Fatalf("similar prefix share was revoked: %d, %s", w.Code, w.Body)
			}
		})
	}
}

func TestRejectedAndNoOpFileChangesKeepShareLinks(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, 100)
	cookie, csrf := sessionFor(t, srv, "Test User")
	writeStoredFile(t, srv, "test-user", "readme.txt", "shared")
	writeStoredFile(t, srv, "test-user", "taken.txt", "taken")
	link := shareRequest(t, srv, cookie, csrf, "create", "readme.txt")
	for _, name := range []string{"taken.txt", "../escape", "readme.txt"} {
		w := httptest.NewRecorder()
		srv.handleFiles(w, formRequest("/files", url.Values{"action": {"rename"}, "path": {"readme.txt"}, "name": {name}, "csrf_token": {csrf}}, cookie))
		if w := publicDownload(srv, http.MethodGet, link); w.Code != http.StatusOK || w.Body.String() != "shared" {
			t.Fatalf("rejected/no-op rename to %q revoked the share: %d", name, w.Code)
		}
	}
}

func TestSharesSurviveRestartAndOlderDatabasesGainTheTable(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, 100)
	cookie, csrf := sessionFor(t, srv, "Test User")
	writeStoredFile(t, srv, "test-user", "readme.txt", "shared")
	if _, err := srv.db.Exec(`DROP TABLE shares`); err != nil {
		t.Fatal(err)
	}
	if err := srv.db.Close(); err != nil {
		t.Fatal(err)
	}
	srv.open(t)
	link := shareRequest(t, srv, cookie, csrf, "create", "readme.txt")
	if err := srv.db.Close(); err != nil {
		t.Fatal(err)
	}
	srv.open(t)
	if w := publicDownload(srv, http.MethodGet, link); w.Code != http.StatusOK || w.Body.String() != "shared" {
		t.Fatalf("restart invalidated a share: %d, %s", w.Code, w.Body)
	}
	if got, active := shareStatus(t, srv, cookie, "readme.txt"); got != "" || !active {
		t.Fatalf("share status after restart = %q, %v", got, active)
	}
}

func TestPlaintextShareTokensAreHashedOnUpgrade(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, 100)
	cookie, _ := sessionFor(t, srv, "Test User")
	writeStoredFile(t, srv, "test-user", "readme.txt", "shared")
	// Recreate the table as older versions stored it, with a plaintext token.
	plaintext := strings.Repeat("Q", 43)
	for _, statement := range []string{
		`DROP TABLE shares`,
		`CREATE TABLE shares (token TEXT PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  path TEXT NOT NULL, is_directory INTEGER NOT NULL CHECK (is_directory IN (0, 1)), created_at INTEGER NOT NULL, UNIQUE (user_id, path))`,
		`INSERT INTO shares (token, user_id, path, is_directory, created_at) SELECT '` + plaintext + `', id, 'readme.txt', 0, 0 FROM users`,
	} {
		if _, err := srv.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := srv.db.Close(); err != nil {
		t.Fatal(err)
	}
	srv.open(t)
	var stored string
	if err := srv.db.QueryRow(`SELECT token_hash FROM shares`).Scan(&stored); err != nil || stored != token.Hash(plaintext) {
		t.Fatalf("upgraded share token = %q, %v; want its hash", stored, err)
	}
	if w := publicDownload(srv, http.MethodGet, "/s/"+plaintext); w.Code != http.StatusOK || w.Body.String() != "shared" {
		t.Fatalf("upgrade broke an existing link: %d, %s", w.Code, w.Body)
	}
	if got, active := shareStatus(t, srv, cookie, "readme.txt"); got != "" || !active {
		t.Fatalf("share status after upgrade = %q, %v", got, active)
	}
	// Opening the upgraded database again leaves the hashes alone.
	if err := srv.db.Close(); err != nil {
		t.Fatal(err)
	}
	srv.open(t)
	if w := publicDownload(srv, http.MethodGet, "/s/"+plaintext); w.Code != http.StatusOK {
		t.Fatalf("second start broke an upgraded link: %d", w.Code)
	}
}

func TestDeletedUsersLosePublicAccessWhileUploadsArePreserved(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, 100)
	cookie, csrf := sessionFor(t, srv, "Test User")
	writeStoredFile(t, srv, "test-user", "readme.txt", "preserved")
	link := shareRequest(t, srv, cookie, csrf, "create", "readme.txt")
	u, err := srv.accounts.Authenticate(context.Background(), "Test User", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.accounts.ResetPassword(context.Background(), u.ID, "changed-password"); err != nil {
		t.Fatal(err)
	}
	if w := publicDownload(srv, http.MethodGet, link); w.Code != http.StatusOK {
		t.Fatalf("password resets unexpectedly revoked independent shares: %d", w.Code)
	}
	if err := srv.accounts.Delete(context.Background(), u.ID, 999); err != nil {
		t.Fatal(err)
	}
	if w := publicDownload(srv, http.MethodGet, link); w.Code != http.StatusNotFound {
		t.Fatalf("deleted user's link still works: %d", w.Code)
	}
	var count int
	if err := srv.db.QueryRow(`SELECT COUNT(*) FROM shares`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("deleted user's tokens remain stored: %d, %v", count, err)
	}
	if data, err := os.ReadFile(filepath.Join(srv.dir, "test-user", "readme.txt")); err != nil || string(data) != "preserved" {
		t.Fatalf("revocation removed uploaded data: %q, %v", data, err)
	}
}

func TestPublicSharesRejectMissingTokensSymlinksAndTypeChanges(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, 100)
	cookie, csrf := sessionFor(t, srv, "Test User")
	writeStoredFile(t, srv, "test-user", "readme.txt", "shared")
	writeStoredFile(t, srv, "test-user", "private.txt", "private")
	link := shareRequest(t, srv, cookie, csrf, "create", "readme.txt")
	for _, invalid := range []string{"/s/short", "/s/" + strings.Repeat("!", 43), "/s/" + strings.Repeat("x", 43), link + "/private.txt"} {
		w := publicDownload(srv, http.MethodGet, invalid)
		if w.Code != http.StatusNotFound || w.Header().Get("WWW-Authenticate") != "" {
			t.Errorf("invalid token triggered authentication or downloaded: %d", w.Code)
		}
	}
	w := publicDownload(srv, http.MethodPost, link)
	if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("public endpoint accepts mutations: %d", w.Code)
	}
	filename := filepath.Join(srv.dir, "test-user", "readme.txt")
	if err := os.Remove(filename); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("private.txt", filename); err != nil {
		t.Fatal(err)
	}
	if w := publicDownload(srv, http.MethodGet, link); w.Code != http.StatusNotFound {
		t.Fatalf("share followed a symlink: %d", w.Code)
	}
	if err := os.Remove(filename); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filename, 0o750); err != nil {
		t.Fatal(err)
	}
	if w := publicDownload(srv, http.MethodGet, link); w.Code != http.StatusNotFound {
		t.Fatalf("file share expanded into directory sharing: %d", w.Code)
	}
}

func TestPublicDownloadsRecheckRevocationAfterWaitingForStorageLock(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, 100)
	cookie, csrf := sessionFor(t, srv, "Test User")
	writeStoredFile(t, srv, "test-user", "readme.txt", "shared")
	link := shareRequest(t, srv, cookie, csrf, "create", "readme.txt")
	ctx := context.Background()
	u, err := srv.accounts.Authenticate(ctx, "Test User", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := srv.locks.Lock(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- publicDownload(srv, http.MethodGet, link) }()
	select {
	case w := <-done:
		unlock()
		t.Fatalf("public download did not wait for storage: %d", w.Code)
	case <-time.After(30 * time.Millisecond):
	}
	if err := srv.shares.Revoke(ctx, u.ID, "readme.txt"); err != nil {
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
