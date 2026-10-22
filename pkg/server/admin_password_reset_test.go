package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/royvandam/pigeon/pkg/account"
	"github.com/royvandam/pigeon/pkg/quota"
)

func TestAdminGeneratesResetCredentials(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, 100)
	ctx := context.Background()
	if _, err := srv.accounts.Create(ctx, "Admin", testPassword, true, quota.DefaultQuota); err != nil {
		t.Fatal(err)
	}
	adminCookie, csrf := sessionFor(t, srv, "Admin")
	target, err := srv.accounts.Authenticate(ctx, "Test User", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	writeStoredFile(t, srv, target.Slug, "kept.txt", "preserved")
	link, err := createShare(ctx, srv, target, "kept.txt", false)
	if err != nil {
		t.Fatal(err)
	}
	previousPassword := testPassword
	for _, suppliedPassword := range []string{"", "client-chosen-password"} {
		name := "without a supplied password"
		if suppliedPassword != "" {
			name = "ignores a supplied password"
		}
		t.Run(name, func(t *testing.T) {
			var oldTokens []string
			for range 2 {
				token, err := srv.sessions.Create(ctx, target)
				if err != nil {
					t.Fatal(err)
				}
				oldTokens = append(oldTokens, token)
			}
			form := url.Values{"action": {"reset"}, "user_id": {strconv.FormatInt(target.ID, 10)}, "csrf_token": {csrf}}
			if suppliedPassword != "" {
				form.Set("password", suppliedPassword)
			}
			w := httptest.NewRecorder()
			srv.handleAdmin(w, formRequest("/admin", form, adminCookie))
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Password reset: Test User") {
				t.Fatalf("password reset: %d %s", w.Code, w.Body)
			}
			passphrase := displayedPassphrase(t, w)
			if passphrase == previousPassword || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Location") != "" || len(w.Result().Cookies()) != 0 || strings.Contains(fmt.Sprint(w.Header()), passphrase) {
				t.Fatal("reset credential must be fresh and confined to the uncached response body")
			}
			fresh, err := srv.accounts.Authenticate(ctx, target.Username, passphrase)
			if err != nil || fresh.ID != target.ID || fresh.Admin != target.Admin || fresh.Slug != target.Slug || fresh.QuotaBytes != target.QuotaBytes || fresh.Version != target.Version+1 {
				t.Fatalf("reset changed account details or issued an unusable credential: %#v, %v", fresh, err)
			}
			for _, password := range []string{previousPassword, "client-chosen-password"} {
				if _, err := srv.accounts.Authenticate(ctx, target.Username, password); !errors.Is(err, account.ErrInvalidCredentials) {
					t.Fatalf("previous or client-supplied password still works: %v", err)
				}
			}
			var hash []byte
			if err := srv.db.QueryRow(`SELECT password_hash FROM users WHERE id = ?`, target.ID).Scan(&hash); err != nil {
				t.Fatal(err)
			}
			if !bytes.HasPrefix(hash, []byte("$2")) || bytes.Contains(hash, []byte(passphrase)) {
				t.Fatal("reset credential was not stored as a bcrypt hash")
			}
			for _, token := range oldTokens {
				if _, err := srv.sessions.Lookup(ctx, token); !errors.Is(err, account.ErrInvalidCredentials) {
					t.Fatalf("reset left an existing session valid: %v", err)
				}
			}
			if _, err := srv.sessions.Create(ctx, target); !errors.Is(err, account.ErrInvalidCredentials) {
				t.Fatalf("reset accepted a login using the previous account version: %v", err)
			}
			if _, err := srv.sessions.Lookup(ctx, adminCookie.Value); err != nil {
				t.Fatalf("reset signed out the administrator of a different account: %v", err)
			}
			if download := publicDownload(srv, http.MethodGet, link); download.Code != http.StatusOK || download.Body.String() != "preserved" {
				t.Fatalf("reset changed files or revoked a share: %d", download.Code)
			}
			r := httptest.NewRequest(http.MethodGet, "/admin", nil)
			r.AddCookie(adminCookie)
			w = httptest.NewRecorder()
			srv.handleAdmin(w, r)
			if w.Code != http.StatusOK || strings.Contains(w.Body.String(), passphrase) || strings.Contains(w.Body.String(), `id="issuedPassphrase"`) || strings.Contains(w.Body.String(), `name="password"`) {
				t.Fatal("management page disclosed a reset credential or still prompts for a password")
			}
			target, previousPassword = fresh, passphrase
		})
	}
}

func TestAdminSelfResetShowsCredentialsAfterSigningOut(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, 100)
	ctx := context.Background()
	admin, err := srv.accounts.Create(ctx, "Admin", testPassword, true, quota.DefaultQuota)
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf := sessionFor(t, srv, admin.Username)
	otherToken, err := srv.sessions.Create(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{"action": {"reset"}, "user_id": {strconv.FormatInt(admin.ID, 10)}, "csrf_token": {csrf}}
	w := httptest.NewRecorder()
	srv.handleAdmin(w, formRequest("/admin", form, cookie))
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Location") != "" {
		t.Fatalf("self-reset did not display an uncached result: %d %v", w.Code, w.Header())
	}
	passphrase := displayedPassphrase(t, w)
	body := w.Body.String()
	if !strings.Contains(body, "Password reset: Admin") || !strings.Contains(body, "You have been signed out.") || !strings.Contains(body, `href="/login"`) || strings.Contains(body, "<form") || strings.Contains(body, `id="actionDialog"`) {
		t.Fatal("self-reset must display the new passphrase and sign-in link without authenticated controls")
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != sessionCookieName || cookies[0].MaxAge != -1 || cookies[0].Value != "" || !cookies[0].HttpOnly || strings.Contains(fmt.Sprint(w.Header()), passphrase) {
		t.Fatal("self-reset did not clear the session cookie or leaked the passphrase into headers")
	}
	for _, token := range []string{cookie.Value, otherToken} {
		if _, err := srv.sessions.Lookup(ctx, token); !errors.Is(err, account.ErrInvalidCredentials) {
			t.Fatalf("self-reset preserved an existing session: %v", err)
		}
	}
	if _, err := srv.accounts.Authenticate(ctx, admin.Username, testPassword); !errors.Is(err, account.ErrInvalidCredentials) {
		t.Fatal("self-reset preserved the previous password")
	}
	if _, err := srv.sessions.Create(ctx, admin); !errors.Is(err, account.ErrInvalidCredentials) {
		t.Fatalf("self-reset accepted a login already in progress: %v", err)
	}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		r := formRequest("/admin", form, cookie)
		if method == http.MethodGet {
			r = httptest.NewRequest(method, "/admin", nil)
			r.AddCookie(cookie)
		}
		w = httptest.NewRecorder()
		srv.handleAdmin(w, r)
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/login" || strings.Contains(w.Body.String(), passphrase) || strings.Contains(w.Body.String(), `id="issuedPassphrase"`) {
			t.Fatal("signed-out requests recovered credentials or repeated the reset")
		}
	}
	fresh, err := srv.accounts.Authenticate(ctx, admin.Username, passphrase)
	if err != nil {
		t.Fatalf("self-reset passphrase cannot sign in: %v", err)
	}
	if _, err := srv.sessions.Create(ctx, fresh); err != nil {
		t.Fatalf("self-reset prevented signing in with the new credential: %v", err)
	}
}

func TestAdminRejectedResetsDoNotChangeOrDisplayCredentials(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, 100)
	ctx := context.Background()
	admin, err := srv.accounts.Create(ctx, "Admin", testPassword, true, quota.DefaultQuota)
	if err != nil {
		t.Fatal(err)
	}
	retired, err := srv.accounts.Create(ctx, "Retired", testPassword, false, quota.DefaultQuota)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.accounts.Delete(ctx, retired.ID, admin.ID); err != nil {
		t.Fatal(err)
	}
	adminCookie, csrf := sessionFor(t, srv, admin.Username)
	userCookie, userCSRF := sessionFor(t, srv, "Test User")
	a, err := srv.sessions.Lookup(ctx, userCookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	targetID := strconv.FormatInt(a.User.ID, 10)
	var originalHash []byte
	if err := srv.db.QueryRow(`SELECT password_hash FROM users WHERE id = ?`, a.User.ID).Scan(&originalHash); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, id, csrf, origin string
		cookie                 *http.Cookie
		get                    bool
		status                 int
	}{
		{name: "anonymous", id: targetID, status: http.StatusSeeOther},
		{name: "regular user", id: targetID, csrf: userCSRF, cookie: userCookie, status: http.StatusForbidden},
		{name: "missing CSRF", id: targetID, cookie: adminCookie, status: http.StatusForbidden},
		{name: "wrong CSRF", id: targetID, csrf: "wrong", cookie: adminCookie, status: http.StatusForbidden},
		{name: "cross origin", id: targetID, csrf: csrf, cookie: adminCookie, origin: "https://outside.example", status: http.StatusForbidden},
		{name: "missing ID", csrf: csrf, cookie: adminCookie, status: http.StatusBadRequest},
		{name: "invalid ID", id: "invalid", csrf: csrf, cookie: adminCookie, status: http.StatusBadRequest},
		{name: "unknown ID", id: "999", csrf: csrf, cookie: adminCookie, status: http.StatusBadRequest},
		{name: "deleted ID", id: strconv.FormatInt(retired.ID, 10), csrf: csrf, cookie: adminCookie, status: http.StatusBadRequest},
		{name: "GET cannot reset", id: targetID, csrf: csrf, cookie: adminCookie, get: true, status: http.StatusOK},
	} {
		t.Run(tt.name, func(t *testing.T) {
			form := url.Values{"action": {"reset"}, "user_id": {tt.id}, "csrf_token": {tt.csrf}}
			r := formRequest("/admin", form, tt.cookie)
			if tt.get {
				r = httptest.NewRequest(http.MethodGet, "/admin?"+form.Encode(), nil)
				r.AddCookie(tt.cookie)
			}
			r.Header.Set("Origin", tt.origin)
			w := httptest.NewRecorder()
			srv.handleAdmin(w, r)
			if w.Code != tt.status || strings.Contains(w.Body.String(), `id="issuedPassphrase"`) {
				t.Fatalf("rejected reset issued credentials: %d %s", w.Code, w.Body)
			}
		})
	}
	// A session cleanup failure must roll back both the password and its version.
	if _, err := srv.db.Exec(`CREATE TRIGGER fail_reset_cleanup BEFORE DELETE ON sessions
WHEN OLD.user_id = ` + targetID + ` BEGIN SELECT RAISE(ABORT, 'session cleanup failed'); END`); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	srv.handleAdmin(w, formRequest("/admin", url.Values{"action": {"reset"}, "user_id": {targetID}, "csrf_token": {csrf}}, adminCookie))
	if w.Code != http.StatusBadRequest || strings.Contains(w.Body.String(), `id="issuedPassphrase"`) {
		t.Fatal("failed session cleanup disclosed credentials or completed the reset")
	}
	var hash []byte
	var version int64
	if err := srv.db.QueryRow(`SELECT password_hash, auth_version FROM users WHERE id = ?`, a.User.ID).Scan(&hash, &version); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(hash, originalHash) || version != a.User.Version {
		t.Fatal("rejected resets or a rolled-back reset changed the password or version")
	}
	if _, err := srv.sessions.Lookup(ctx, userCookie.Value); err != nil {
		t.Fatalf("rejected reset invalidated the existing session: %v", err)
	}
}
