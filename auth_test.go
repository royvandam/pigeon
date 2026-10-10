package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

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

func formRequest(path string, form url.Values, cookie *http.Cookie) *http.Request {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	return r
}

func TestAdminUsesSharedDeletionDialog(t *testing.T) {
	cfg := newTestConfig(t, 100)
	admin, err := cfg.store.createUser(context.Background(), "Admin", testPassword, true)
	if err != nil {
		t.Fatal(err)
	}
	cookie, _ := sessionFor(t, cfg, "Admin")
	r := httptest.NewRequest(http.MethodGet, "/admin", nil)
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	cfg.handleAdmin(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("admin page: %d, %s", w.Code, w.Body)
	}
	body := w.Body.String()
	buttons := regexp.MustCompile(`<button\b[^>]*data-dialog-action="delete-user"[^>]*>`).FindAllString(body, -1)
	if len(buttons) != 1 || !strings.Contains(buttons[0], `type="button"`) || !strings.Contains(buttons[0], `data-name="Test User"`) || strings.Contains(buttons[0], `data-user-id="`+strconv.FormatInt(admin.ID, 10)+`"`) {
		t.Fatalf("user deletion must confirm the selected account and exclude self: %v", buttons)
	}
	if strings.Count(body, `<dialog id="actionDialog"`) != 1 || !strings.Contains(body, `<form id="actionDialogForm" method="post"`) {
		t.Fatal("admin page did not render exactly one shared dialog")
	}
}

func TestBrowserLoginLogout(t *testing.T) {
	cfg := newTestConfig(t, 100)
	w := httptest.NewRecorder()
	cfg.handleIndex(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/login" {
		t.Fatalf("unauthenticated index: %d, %s", w.Code, w.Header().Get("Location"))
	}
	w = httptest.NewRecorder()
	cfg.handleLogin(w, httptest.NewRequest(http.MethodGet, "/login", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("login page: %d %s", w.Code, w.Body)
	}
	csrf := regexp.MustCompile(`name="csrf_token" value="([^"]+)"`).FindStringSubmatch(w.Body.String())
	if len(csrf) != 2 {
		t.Fatal("login page is missing a CSRF token")
	}
	loginCookie := w.Result().Cookies()[0]
	if !loginCookie.HttpOnly || loginCookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("login cookie lacks security attributes")
	}
	form := url.Values{"username": {"test user"}, "password": {testPassword}, "csrf_token": {csrf[1]}}
	w = httptest.NewRecorder()
	cfg.handleLogin(w, formRequest("/login", form, loginCookie))
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" {
		t.Fatalf("login: %d %s", w.Code, w.Body)
	}
	var sessionCookie *http.Cookie
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == sessionCookieName {
			sessionCookie = cookie
		}
	}
	if sessionCookie == nil || !sessionCookie.HttpOnly || sessionCookie.SameSite != http.SameSiteLaxMode {
		t.Fatal("login did not set a protected session cookie")
	}
	a, err := cfg.store.session(context.Background(), sessionCookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(sessionCookie)
	cfg.handleIndex(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Test User") {
		t.Fatalf("signed-in page: %d %s", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	cfg.handleLogout(w, formRequest("/logout", url.Values{"csrf_token": {a.CSRFToken}}, sessionCookie))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("logout: %d %s", w.Code, w.Body)
	}
	if _, err := cfg.store.session(context.Background(), sessionCookie.Value); !errors.Is(err, errInvalidCredentials) {
		t.Fatalf("logout left a valid session: %v", err)
	}
}

func TestUploadRejectsMissingCSRFBeforeReadingBody(t *testing.T) {
	cfg := newTestConfig(t, 100)
	cookie, csrf := sessionFor(t, cfg, "Test User")
	for _, token := range []string{"", "wrong-token"} {
		body := &readTrackingBody{}
		r := httptest.NewRequest(http.MethodPost, "/upload", nil)
		r.Body = body
		r.AddCookie(cookie)
		r.Header.Set("X-CSRF-Token", token)
		w := httptest.NewRecorder()
		cfg.handleUpload(w, r)
		if w.Code != http.StatusForbidden || body.read {
			t.Fatalf("CSRF rejection: status %d, body read %t", w.Code, body.read)
		}
	}
	r := uploadRequest(t, "report.txt", "hello")
	r.Header.Del("Authorization")
	r.AddCookie(cookie)
	r.Header.Set("X-CSRF-Token", csrf)
	w := httptest.NewRecorder()
	cfg.handleUpload(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("session upload: %d %s", w.Code, w.Body)
	}
}

func TestAdminPermissionsCSRFAndUserLifecycle(t *testing.T) {
	cfg := newTestConfig(t, 100)
	ctx := context.Background()
	admin, err := cfg.store.createUser(ctx, "Admin", testPassword, true)
	if err != nil {
		t.Fatal(err)
	}
	userCookie, _ := sessionFor(t, cfg, "Test User")
	adminCookie, csrf := sessionFor(t, cfg, "Admin")
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		r := httptest.NewRequest(method, "/admin", nil)
		r.AddCookie(userCookie)
		w := httptest.NewRecorder()
		cfg.handleAdmin(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("regular user's %s /admin: %d", method, w.Code)
		}
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/admin", nil)
	r.AddCookie(adminCookie)
	cfg.handleAdmin(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Create a user") {
		t.Fatalf("management page: %d %s", w.Code, w.Body)
	}
	create := url.Values{"action": {"create"}, "username": {"Alice Smith"}}
	w = httptest.NewRecorder()
	cfg.handleAdmin(w, formRequest("/admin", create, adminCookie))
	if w.Code != http.StatusForbidden {
		t.Fatalf("admin mutation without CSRF: %d", w.Code)
	}
	create.Set("csrf_token", csrf)
	w = httptest.NewRecorder()
	cfg.handleAdmin(w, formRequest("/admin", create, adminCookie))
	if w.Code != http.StatusOK {
		t.Fatalf("create user: %d %s", w.Code, w.Body)
	}
	passphrase := createdPassphrase(t, w)
	alice, err := cfg.store.authenticate(ctx, "Alice Smith", passphrase)
	if err != nil || alice.Admin || alice.Slug != "alice-smith" {
		t.Fatalf("created user: %#v, %v", alice, err)
	}
	oldToken, err := cfg.store.createSession(ctx, alice)
	if err != nil {
		t.Fatal(err)
	}
	reset := url.Values{"action": {"reset"}, "user_id": {strconv.FormatInt(alice.ID, 10)}, "password": {"replacement-password"}, "csrf_token": {csrf}}
	w = httptest.NewRecorder()
	cfg.handleAdmin(w, formRequest("/admin", reset, adminCookie))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("reset user: %d %s", w.Code, w.Body)
	}
	if _, err := cfg.store.session(ctx, oldToken); !errors.Is(err, errInvalidCredentials) {
		t.Fatalf("password reset did not revoke session: %v", err)
	}
	if _, err := cfg.store.createSession(ctx, alice); !errors.Is(err, errInvalidCredentials) {
		t.Fatalf("password reset accepted a login already in progress: %v", err)
	}
	if _, err := cfg.store.authenticate(ctx, "Alice Smith", passphrase); !errors.Is(err, errInvalidCredentials) {
		t.Fatal("old password still works")
	}
	alice, err = cfg.store.authenticate(ctx, "Alice Smith", "replacement-password")
	if err != nil {
		t.Fatal(err)
	}
	newToken, err := cfg.store.createSession(ctx, alice)
	if err != nil {
		t.Fatal(err)
	}
	deleteForm := url.Values{"action": {"delete"}, "user_id": {strconv.FormatInt(alice.ID, 10)}, "csrf_token": {csrf}}
	w = httptest.NewRecorder()
	cfg.handleAdmin(w, formRequest("/admin", deleteForm, adminCookie))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("delete user: %d %s", w.Code, w.Body)
	}
	if _, err := cfg.store.session(ctx, newToken); !errors.Is(err, errInvalidCredentials) {
		t.Fatal("deleted user's session still works")
	}
	if _, err := cfg.store.authenticate(ctx, "Alice Smith", "replacement-password"); !errors.Is(err, errInvalidCredentials) {
		t.Fatal("deleted user can authenticate")
	}
	if err := cfg.store.deleteUser(ctx, admin.ID, admin.ID); !errors.Is(err, errDeleteSelf) {
		t.Fatalf("admin could delete self: %v", err)
	}
	if err := cfg.store.deleteUser(ctx, admin.ID, 999); !errors.Is(err, errLastAdmin) {
		t.Fatalf("last admin could be deleted: %v", err)
	}
	users, err := cfg.store.listUsers(ctx)
	if err != nil || len(users) != 2 {
		t.Fatalf("deleted user remains in management list: %#v, %v", users, err)
	}
}

func TestSessionExpirationAndCrossOriginRequests(t *testing.T) {
	cfg := newTestConfig(t, 100)
	cookie, csrf := sessionFor(t, cfg, "Test User")
	r := uploadRequest(t, "file.txt", "data")
	r.Header.Del("Authorization")
	r.AddCookie(cookie)
	r.Header.Set("X-CSRF-Token", csrf)
	r.Header.Set("Origin", "https://other.example")
	w := httptest.NewRecorder()
	cfg.handleUpload(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-origin upload: %d", w.Code)
	}
	if _, err := cfg.store.db.Exec(`UPDATE sessions SET expires_at = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.store.session(context.Background(), cookie.Value); !errors.Is(err, errInvalidCredentials) {
		t.Fatal("expired session still works")
	}
}
