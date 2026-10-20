package pigeon

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestAdminPurgeAndRecreateAccount(t *testing.T) {
	t.Parallel()
	cfg := newTestConfig(t, 100)
	ctx := context.Background()
	if _, err := cfg.store.createUser(ctx, "Admin", testPassword, true); err != nil {
		t.Fatal(err)
	}
	u := deletedPurgeAccount(t, cfg)
	writeStoredFile(t, cfg, u.Slug, "kept.txt", "old content")
	cookie, csrf := sessionFor(t, cfg, "Admin")
	r := httptest.NewRequest(http.MethodGet, "/admin", nil)
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	cfg.handleAdmin(w, r)
	buttons := regexp.MustCompile(`<button\b[^>]*data-dialog-action="purge-user"[^>]*>`).FindAllString(w.Body.String(), -1)
	if w.Code != http.StatusOK || len(buttons) != 1 || !strings.Contains(buttons[0], `type="button"`) || !strings.Contains(buttons[0], `data-user-id="`+strconv.FormatInt(u.ID, 10)+`"`) {
		t.Fatalf("deleted account did not offer purge confirmation: %d %v", w.Code, buttons)
	}
	form := url.Values{"action": {"purge"}, "user_id": {strconv.FormatInt(u.ID, 10)}, "csrf_token": {csrf}}
	w = httptest.NewRecorder()
	cfg.handleAdmin(w, formRequest("/admin", form, cookie))
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/admin?result=purged" {
		t.Fatalf("purge failed: %d %s", w.Code, w.Body)
	}
	if _, err := os.Stat(filepath.Join(cfg.dir, u.Slug)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("purged uploads remain: %v", err)
	}
	w = httptest.NewRecorder()
	cfg.handleAdmin(w, r)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), `id="deletedUsers"`) {
		t.Fatalf("purged user remains in deleted list: %d %s", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	cfg.handleAdmin(w, formRequest("/admin", url.Values{"action": {"create"}, "username": {u.Username}, "csrf_token": {csrf}}, cookie))
	if w.Code != http.StatusOK {
		t.Fatalf("purged username could not be reused: %d %s", w.Code, w.Body)
	}
	passphrase := displayedPassphrase(t, w)
	fresh, err := cfg.store.authenticate(ctx, u.Username, passphrase)
	if err != nil || fresh.ID == u.ID {
		t.Fatalf("recreated account reused the purged identity: %#v %v", fresh, err)
	}
	w = httptest.NewRecorder()
	cfg.handleAdmin(w, formRequest("/admin", form, cookie))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("repeated purge succeeded: %d %s", w.Code, w.Body)
	}
	if _, err := cfg.store.authenticate(ctx, fresh.Username, passphrase); err != nil {
		t.Fatalf("repeated purge removed the replacement account: %v", err)
	}
}

func TestAdminPurgeRequiresPermissionAndExplicitPOST(t *testing.T) {
	t.Parallel()
	cfg := newTestConfig(t, 100)
	ctx := context.Background()
	admin, err := cfg.store.createUser(ctx, "Admin", testPassword, true)
	if err != nil {
		t.Fatal(err)
	}
	u := deletedPurgeAccount(t, cfg)
	writeStoredFile(t, cfg, u.Slug, "kept.txt", "preserved")
	adminCookie, csrf := sessionFor(t, cfg, admin.Username)
	userCookie, userCSRF := sessionFor(t, cfg, "Test User")
	for _, tt := range []struct {
		name, id, csrf, origin string
		cookie                 *http.Cookie
		get                    bool
		status                 int
	}{
		{name: "regular user", id: strconv.FormatInt(u.ID, 10), csrf: userCSRF, cookie: userCookie, status: http.StatusForbidden},
		{name: "missing CSRF", id: strconv.FormatInt(u.ID, 10), cookie: adminCookie, status: http.StatusForbidden},
		{name: "cross origin", id: strconv.FormatInt(u.ID, 10), csrf: csrf, origin: "https://outside.example", cookie: adminCookie, status: http.StatusForbidden},
		{name: "active account", id: strconv.FormatInt(admin.ID, 10), csrf: csrf, cookie: adminCookie, status: http.StatusBadRequest},
		{name: "invalid ID", id: "invalid", csrf: csrf, cookie: adminCookie, status: http.StatusBadRequest},
		{name: "unknown ID", id: "999", csrf: csrf, cookie: adminCookie, status: http.StatusBadRequest},
		{name: "GET cannot purge", id: strconv.FormatInt(u.ID, 10), csrf: csrf, cookie: adminCookie, get: true, status: http.StatusOK},
	} {
		t.Run(tt.name, func(t *testing.T) {
			form := url.Values{"action": {"purge"}, "user_id": {tt.id}, "csrf_token": {tt.csrf}}
			r := formRequest("/admin", form, tt.cookie)
			if tt.get {
				r = httptest.NewRequest(http.MethodGet, "/admin?"+form.Encode(), nil)
				r.AddCookie(tt.cookie)
			}
			r.Header.Set("Origin", tt.origin)
			w := httptest.NewRecorder()
			cfg.handleAdmin(w, r)
			if w.Code != tt.status {
				t.Fatalf("unexpected purge response: %d %s", w.Code, w.Body)
			}
			if data, err := os.ReadFile(filepath.Join(cfg.dir, u.Slug, "kept.txt")); err != nil || string(data) != "preserved" {
				t.Fatalf("rejected request removed uploads: %q %v", data, err)
			}
			if id, err := cfg.store.deletedUserIDForUsername(ctx, u.Username); err != nil || id != u.ID {
				t.Fatalf("rejected request removed the account: %d %v", id, err)
			}
		})
	}
}

func TestAdminPurgeFailureKeepsAccountReserved(t *testing.T) {
	t.Parallel()
	cfg := newTestConfig(t, 100)
	ctx := context.Background()
	if _, err := cfg.store.createUser(ctx, "Admin", testPassword, true); err != nil {
		t.Fatal(err)
	}
	u := deletedPurgeAccount(t, cfg)
	writeStoredFile(t, cfg, u.Slug, "kept.txt", "preserved")
	if _, err := cfg.store.db.Exec(`CREATE TRIGGER fail_purge BEFORE DELETE ON users BEGIN SELECT RAISE(ABORT, 'test delete failure'); END`); err != nil {
		t.Fatal(err)
	}
	cookie, csrf := sessionFor(t, cfg, "Admin")
	w := httptest.NewRecorder()
	cfg.handleAdmin(w, formRequest("/admin", url.Values{"action": {"purge"}, "user_id": {strconv.FormatInt(u.ID, 10)}, "csrf_token": {csrf}}, cookie))
	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), "Its username remains reserved") || w.Header().Get("Location") != "" {
		t.Fatalf("failed purge reported success: %d %s", w.Code, w.Body)
	}
	if _, err := cfg.store.createUser(ctx, u.Username, "new-account-password", false); !errors.Is(err, errUserConflict) {
		t.Fatalf("failed purge released the username: %v", err)
	}
}
