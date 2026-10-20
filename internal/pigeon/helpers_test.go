package pigeon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// Helpers shared by the tests in this package. Keep helpers used by a single
// test file in that file.

const testPassword = "test-password"

// testPasswordCost keeps the many password hashes in fixtures fast.
const testPasswordCost = bcrypt.MinCost

func newTestConfig(t *testing.T, quota int64) config {
	t.Helper()
	root := t.TempDir()
	s, err := openStore(context.Background(), filepath.Join(root, "app.db"), testPasswordCost)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.db.Close() })
	if _, err := s.createUserWithQuota(context.Background(), "Test User", testPassword, false, quota); err != nil {
		t.Fatal(err)
	}
	// A one-byte allocation unit keeps quota arithmetic in tests exact; tests of
	// the production unit set allocationUnit explicitly.
	return config{dir: filepath.Join(root, "uploads"), store: s, allocationUnit: 1}
}

func serve(handler http.Handler, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func formRequest(path string, form url.Values, cookie *http.Cookie) *http.Request {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	return r
}

func sessionFor(t *testing.T, cfg config, username string) (*http.Cookie, string) {
	t.Helper()
	return sessionForPassword(t, cfg, username, testPassword)
}

func sessionForPassword(t *testing.T, cfg config, username, password string) (*http.Cookie, string) {
	t.Helper()
	u, err := cfg.store.authenticate(context.Background(), username, password)
	if err != nil {
		t.Fatal(err)
	}
	token, err := cfg.store.createSession(context.Background(), u)
	if err != nil {
		t.Fatal(err)
	}
	a, err := cfg.store.session(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: sessionCookieName, Value: token}, a.CSRFToken
}

func displayedPassphrase(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	match := regexp.MustCompile(`<code id="issuedPassphrase"[^>]*>([^<]+)</code>`).FindStringSubmatch(w.Body.String())
	if len(match) != 2 || len(strings.Split(match[1], "-")) != 4 {
		t.Fatal("credential response did not display a four-word passphrase")
	}
	return match[1]
}

func writeStoredFile(t *testing.T, cfg config, slug, name, contents string) {
	t.Helper()
	filename := filepath.Join(cfg.dir, slug, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(filename), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(contents), 0o640); err != nil {
		t.Fatal(err)
	}
}

func readUploadDir(root, slug string) ([]os.DirEntry, error) {
	files, err := os.ReadDir(filepath.Join(root, slug))
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	return files, err
}

func stagingFolders(t *testing.T, cfg config) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(cfg.dir, "test-user", stagingPrefix+"*"))
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

func uploadRequest(t *testing.T, filename, contents string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.WriteField("note", "metadata before the file"); err != nil {
		t.Fatal(err)
	}
	part, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(part, contents); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/upload", &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.SetBasicAuth("Test User", testPassword)
	return r
}

type uploadFile struct {
	name     string
	contents string
}

func uploadFilesRequest(t *testing.T, files ...uploadFile) *http.Request {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for _, file := range files {
		part, err := mw.CreateFormFile("file", file.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(part, file.contents); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/upload", &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.SetBasicAuth("Test User", testPassword)
	return r
}

func uploadResult(t *testing.T, w *httptest.ResponseRecorder, key string) string {
	t.Helper()
	for _, line := range strings.Split(w.Body.String(), "\n") {
		if value, ok := strings.CutPrefix(line, key+"="); ok {
			return value
		}
	}
	t.Fatalf("missing %s in upload response: %s", key, w.Body)
	return ""
}

type readTrackingBody struct {
	read bool
}

func (b *readTrackingBody) Read([]byte) (int, error) {
	b.read = true
	return 0, errors.New("unexpected body read")
}

func (*readTrackingBody) Close() error { return nil }

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

func publicDownload(cfg config, method, link string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	newServer(cfg).Handler.ServeHTTP(w, httptest.NewRequest(method, link, nil))
	return w
}

func deletedPurgeAccount(t *testing.T, cfg config) user {
	t.Helper()
	u, err := cfg.store.createUser(context.Background(), "Retired User", testPassword, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.store.deleteUser(context.Background(), u.ID, 999); err != nil {
		t.Fatal(err)
	}
	return u
}
