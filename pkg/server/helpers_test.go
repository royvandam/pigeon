package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/royvandam/pigeon/pkg/account"
	"github.com/royvandam/pigeon/pkg/auth"
	"github.com/royvandam/pigeon/pkg/database"
	"github.com/royvandam/pigeon/pkg/files"
	"github.com/royvandam/pigeon/pkg/quota"
	"github.com/royvandam/pigeon/pkg/share"
	"github.com/royvandam/pigeon/pkg/storage"
	"github.com/royvandam/pigeon/pkg/storage/local"
)

// Helpers shared by the tests in this package. Keep helpers used by a single
// test file in that file.

const testPassword = "test-password"

// testPasswordCost keeps the many password hashes in fixtures fast.
const testPasswordCost = bcrypt.MinCost

// administrator acts for the tests that change accounts directly. The account
// manager trusts its caller to have authenticated the actor.
var administrator = account.User{ID: 999, Username: "Test Administrator", Admin: true}

// testServer is a server with the parts that tests reach into.
type testServer struct {
	*Server
	dir     string // the upload root
	dbPath  string
	db      *sql.DB
	storage storage.Provider
	locks   *account.Locks
	usage   *quota.Cache
}

// newTestServer serves one account, Test User, with quotaBytes of quota.
func newTestServer(t *testing.T, quotaBytes int64) *testServer {
	t.Helper()
	root := t.TempDir()
	provider, err := local.NewProvider(filepath.Join(root, "uploads"))
	if err != nil {
		t.Fatal(err)
	}
	srv := &testServer{dir: provider.Directory(), dbPath: filepath.Join(root, "app.db"), storage: provider,
		locks: account.NewLocks(), usage: quota.NewCache(time.Now)}
	srv.open(t)
	if _, err := srv.accounts.Create(context.Background(), "Test User", testPassword, false, quotaBytes); err != nil {
		t.Fatal(err)
	}
	return srv
}

// open opens the database and builds the server on it, as a restart would.
func (srv *testServer) open(t *testing.T) {
	t.Helper()
	db, err := database.Open(context.Background(), srv.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	accounts, err := account.NewStore(db, testPasswordCost)
	if err != nil {
		t.Fatal(err)
	}
	srv.db = db
	srv.Server, err = New(Configuration{}, Dependencies{
		Accounts:      accounts,
		Authenticator: auth.NewAuthenticator(accounts, time.Now),
		Sessions:      auth.NewSessions(db),
		Shares:        share.NewStore(db),
	})
	if err != nil {
		t.Fatal(err)
	}
	// A one-byte allocation unit keeps quota arithmetic in tests exact; tests of
	// the production unit use productionUnitServer.
	srv.wire(1)
}

// wire builds the account manager and file service from the server's stores,
// storage, locks, and usage cache, charging unit per file and folder.
func (srv *testServer) wire(unit int64) {
	srv.manager = account.NewManager(srv.accounts, srv.locks, srv.usage, srv.storage)
	srv.files = files.NewService(srv.storage, srv.accounts, srv.locks, srv.usage, srv.shares, unit)
}

// setUsageClock gives the server a new usage cache that reads the time from
// clock.
func (srv *testServer) setUsageClock(clock func() time.Time) {
	srv.usage = quota.NewCache(clock)
	srv.wire(srv.files.AllocationUnit())
}

// activeUsers lists the accounts that have not been deleted.
func activeUsers(ctx context.Context, srv *testServer) ([]account.User, error) {
	users, err := srv.accounts.List(ctx)
	return slices.DeleteFunc(users, func(u account.User) bool { return u.Deleted }), err
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

func sessionFor(t *testing.T, srv *testServer, username string) (*http.Cookie, string) {
	t.Helper()
	return sessionForPassword(t, srv, username, testPassword)
}

func sessionForPassword(t *testing.T, srv *testServer, username, password string) (*http.Cookie, string) {
	t.Helper()
	u, err := srv.accounts.Authenticate(context.Background(), username, password)
	if err != nil {
		t.Fatal(err)
	}
	token, err := srv.sessions.Create(context.Background(), u)
	if err != nil {
		t.Fatal(err)
	}
	a, err := srv.sessions.Lookup(context.Background(), token)
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

func writeStoredFile(t *testing.T, srv *testServer, slug, name, contents string) {
	t.Helper()
	filename := filepath.Join(srv.dir, slug, filepath.FromSlash(name))
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

func stagingFolders(t *testing.T, srv *testServer) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(srv.dir, "test-user", storage.ReservedPrefix+"*"))
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

func shareRequest(t *testing.T, srv *testServer, cookie *http.Cookie, csrf, action, name string) string {
	t.Helper()
	w := httptest.NewRecorder()
	r := formRequest("/shares", url.Values{"action": {action}, "path": {name}, "csrf_token": {csrf}}, cookie)
	srv.Handler().ServeHTTP(w, r)
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

func publicDownload(srv *testServer, method, link string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, httptest.NewRequest(method, link, nil))
	return w
}

func deletedPurgeAccount(t *testing.T, srv *testServer) account.User {
	t.Helper()
	u, err := srv.accounts.Create(context.Background(), "Retired User", testPassword, false, quota.DefaultQuota)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.accounts.Delete(context.Background(), u.ID, 999); err != nil {
		t.Fatal(err)
	}
	return u
}

type gatedUploadBody struct {
	io.ReadCloser
	started chan struct{}
	release chan struct{}
}

func (b *gatedUploadBody) Read(p []byte) (int, error) {
	select {
	case <-b.started:
	default:
		close(b.started)
	}
	<-b.release
	return b.ReadCloser.Read(p)
}

// startGatedUpload begins a session upload whose body blocks until finish is
// called, and waits until the handler starts reading it.
func startGatedUpload(t *testing.T, srv *testServer, cookie *http.Cookie, csrf, directory string, files ...uploadFile) (finish func() *httptest.ResponseRecorder) {
	t.Helper()
	r := uploadFilesRequest(t, files...)
	r.URL.RawQuery = url.Values{"dir": {directory}}.Encode()
	r.Header.Del("Authorization")
	r.AddCookie(cookie)
	r.Header.Set("X-CSRF-Token", csrf)
	body := &gatedUploadBody{ReadCloser: r.Body, started: make(chan struct{}), release: make(chan struct{})}
	r.Body = body
	done := make(chan struct{})
	w := httptest.NewRecorder()
	go func() {
		srv.handleUpload(w, r)
		close(done)
	}()
	select {
	case <-body.started:
	case <-time.After(5 * time.Second):
		t.Fatal("upload did not start reading")
	}
	var once sync.Once
	finish = func() *httptest.ResponseRecorder {
		once.Do(func() { close(body.release); <-done })
		return w
	}
	t.Cleanup(func() { finish() })
	return finish
}

// diskUsage measures the folder dir, which is not charged itself, as the
// usage cache would.
func diskUsage(ctx context.Context, dir string, unit int64) (int64, error) {
	provider, err := local.NewProvider(filepath.Dir(dir))
	if err != nil {
		return 0, err
	}
	tree, err := provider.Open(ctx, filepath.Base(dir))
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer tree.Close()
	return quota.Measure(ctx, tree, "", unit)
}

// createShare creates a link for u's item at name and returns its URL path.
func createShare(ctx context.Context, srv *testServer, u account.User, name string, directory bool) (string, error) {
	secret, err := srv.shares.Create(ctx, u, name, directory)
	return "/s/" + secret, err
}
