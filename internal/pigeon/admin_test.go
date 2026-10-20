package pigeon

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestAdminUserRecycling(t *testing.T) {
	t.Parallel()
	cfg := newTestConfig(t, 100)
	ctx := context.Background()
	admin, err := cfg.store.createUser(ctx, "Admin", testPassword, true)
	if err != nil {
		t.Fatal(err)
	}
	old, err := cfg.store.createUserWithQuota(ctx, "Élise Smith", testPassword, true, 2<<30)
	if err != nil {
		t.Fatal(err)
	}
	writeStoredFile(t, cfg, old.Slug, "kept.txt", "preserved")
	if err := cfg.store.deleteUser(ctx, old.ID, admin.ID); err != nil {
		t.Fatal(err)
	}
	cookie, csrf := sessionFor(t, cfg, admin.Username)
	r := httptest.NewRequest(http.MethodGet, "/admin", nil)
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	cfg.handleAdmin(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `id="deletedUsers"`) || !strings.Contains(w.Body.String(), `data-dialog-action="recycle-user" data-name="Élise Smith"`) || !strings.Contains(w.Body.String(), `data-role="Administrator" data-quota="2.00 GB"`) {
		t.Fatalf("deleted account was not offered for recycling: %d %s", w.Code, w.Body)
	}
	for _, username := range []string{"ÉLISE SMITH", "elise_smith"} {
		w = httptest.NewRecorder()
		cfg.handleAdmin(w, formRequest("/admin", url.Values{"action": {"create"}, "username": {username}, "quota": {"1GB"}, "csrf_token": {csrf}}, cookie))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `id="recycleCandidateTitle"`) || strings.Contains(w.Body.String(), `id="issuedPassphrase"`) {
			t.Fatalf("reserved name did not offer explicit recycling: %d %s", w.Code, w.Body)
		}
		if _, err := cfg.store.authenticate(ctx, old.Username, testPassword); !errors.Is(err, errInvalidCredentials) {
			t.Fatalf("creation automatically restored the account: %v", err)
		}
	}
	form := url.Values{"action": {"recycle"}, "user_id": {strconv.FormatInt(old.ID, 10)}, "csrf_token": {csrf}, "password": {"client-chosen-password"}}
	w = httptest.NewRecorder()
	cfg.handleAdmin(w, formRequest("/admin", form, cookie))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "User recycled: Élise Smith") || strings.Contains(w.Body.String(), `id="deletedUsers"`) {
		t.Fatalf("recycling failed or left the account deleted: %d %s", w.Code, w.Body)
	}
	passphrase := displayedPassphrase(t, w)
	if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Location") != "" || len(w.Result().Cookies()) != 0 {
		t.Fatal("recycled credential must appear only in an uncached response")
	}
	fresh, err := cfg.store.authenticate(ctx, old.Username, passphrase)
	if err != nil || fresh.ID != old.ID || fresh.Admin != old.Admin || fresh.QuotaBytes != old.QuotaBytes {
		t.Fatalf("recycled account lost its identity, role or quota: %#v, %v", fresh, err)
	}
	if _, err := cfg.store.authenticate(ctx, old.Username, "client-chosen-password"); !errors.Is(err, errInvalidCredentials) {
		t.Fatalf("recycling accepted a client supplied password: %v", err)
	}
	w = httptest.NewRecorder()
	cfg.handleAdmin(w, r)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), passphrase) || strings.Contains(w.Body.String(), `id="issuedPassphrase"`) {
		t.Fatal("GET disclosed the recycled passphrase")
	}
	w = httptest.NewRecorder()
	cfg.handleAdmin(w, formRequest("/admin", form, cookie))
	if w.Code != http.StatusBadRequest || strings.Contains(w.Body.String(), `id="issuedPassphrase"`) {
		t.Fatal("repeated recycling succeeded or disclosed an unused credential")
	}
	if _, err := cfg.store.authenticate(ctx, old.Username, passphrase); err != nil {
		t.Fatalf("repeated recycling changed the password: %v", err)
	}
}

func TestAdminRecyclingRequiresPermissionAndExplicitPOST(t *testing.T) {
	t.Parallel()
	cfg := newTestConfig(t, 100)
	ctx := context.Background()
	admin, err := cfg.store.createUser(ctx, "Admin", testPassword, true)
	if err != nil {
		t.Fatal(err)
	}
	old, err := cfg.store.createUser(ctx, "Retired", testPassword, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.store.deleteUser(ctx, old.ID, admin.ID); err != nil {
		t.Fatal(err)
	}
	adminCookie, csrf := sessionFor(t, cfg, admin.Username)
	userCookie, userCSRF := sessionFor(t, cfg, "Test User")
	for _, tt := range []struct {
		name, method, id, csrf, origin string
		cookie                         *http.Cookie
		status                         int
	}{
		{name: "regular user", method: http.MethodPost, id: strconv.FormatInt(old.ID, 10), cookie: userCookie, csrf: userCSRF, status: http.StatusForbidden},
		{name: "missing CSRF", method: http.MethodPost, id: strconv.FormatInt(old.ID, 10), cookie: adminCookie, status: http.StatusForbidden},
		{name: "wrong CSRF", method: http.MethodPost, id: strconv.FormatInt(old.ID, 10), cookie: adminCookie, csrf: "wrong", status: http.StatusForbidden},
		{name: "cross origin", method: http.MethodPost, id: strconv.FormatInt(old.ID, 10), cookie: adminCookie, csrf: csrf, origin: "https://outside.example", status: http.StatusForbidden},
		{name: "missing ID", method: http.MethodPost, cookie: adminCookie, csrf: csrf, status: http.StatusBadRequest},
		{name: "unknown ID", method: http.MethodPost, id: "999", cookie: adminCookie, csrf: csrf, status: http.StatusBadRequest},
		{name: "active ID", method: http.MethodPost, id: strconv.FormatInt(admin.ID, 10), cookie: adminCookie, csrf: csrf, status: http.StatusBadRequest},
		{name: "GET cannot recycle", method: http.MethodGet, id: strconv.FormatInt(old.ID, 10), cookie: adminCookie, csrf: csrf, status: http.StatusOK},
	} {
		t.Run(tt.name, func(t *testing.T) {
			form := url.Values{"action": {"recycle"}, "user_id": {tt.id}, "csrf_token": {tt.csrf}}
			r := formRequest("/admin", form, tt.cookie)
			if tt.method == http.MethodGet {
				r = httptest.NewRequest(http.MethodGet, "/admin?"+form.Encode(), nil)
				r.AddCookie(tt.cookie)
			}
			r.Header.Set("Origin", tt.origin)
			w := httptest.NewRecorder()
			cfg.handleAdmin(w, r)
			if w.Code != tt.status || strings.Contains(w.Body.String(), `id="issuedPassphrase"`) {
				t.Fatalf("unexpected recycling response: %d %s", w.Code, w.Body)
			}
			deleted, err := cfg.store.deletedUserIDForUsername(ctx, old.Username)
			if err != nil || deleted != old.ID {
				t.Fatalf("rejected request restored the account: %d %v", deleted, err)
			}
		})
	}
}

func TestAdminGeneratesUserCredentials(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, quota string
		admin       bool
		quotaBytes  int64
	}{
		{name: "default quota", quotaBytes: defaultUserQuota},
		{name: "custom quota and administrator", quota: "2GB", admin: true, quotaBytes: 2 << 30},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := newTestConfig(t, 100)
			ctx := context.Background()
			if _, err := cfg.store.createUser(ctx, "Admin", testPassword, true); err != nil {
				t.Fatal(err)
			}
			cookie, csrf := sessionFor(t, cfg, "Admin")
			form := url.Values{"action": {"create"}, "username": {" Alice Smith "}, "csrf_token": {csrf}}
			if tt.quota != "" {
				form.Set("quota", tt.quota)
			}
			if tt.admin {
				form.Set("admin", "on")
				// Older clients cannot override the generated credential.
				form.Set("password", "client-chosen-password")
			}
			w := httptest.NewRecorder()
			cfg.handleAdmin(w, formRequest("/admin", form, cookie))
			if w.Code != http.StatusOK {
				t.Fatalf("create user: %d %s", w.Code, w.Body)
			}
			passphrase := displayedPassphrase(t, w)
			if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Location") != "" || len(w.Result().Cookies()) != 0 {
				t.Fatal("generated credential must be in an uncached response, without redirects or cookies")
			}
			alice, err := cfg.store.authenticate(ctx, "Alice Smith", passphrase)
			if err != nil || alice.Username != "Alice Smith" || alice.Slug != "alice-smith" || alice.Admin != tt.admin || alice.QuotaBytes != tt.quotaBytes {
				t.Fatalf("created account does not match the request or displayed credential: %#v, %v", alice, err)
			}
			if _, err := cfg.store.authenticate(ctx, "Alice Smith", "client-chosen-password"); !errors.Is(err, errInvalidCredentials) {
				t.Fatalf("client supplied password was accepted: %v", err)
			}
			var hash []byte
			if err := cfg.store.db.QueryRow(`SELECT password_hash FROM users WHERE id = ?`, alice.ID).Scan(&hash); err != nil {
				t.Fatal(err)
			}
			if !bytes.HasPrefix(hash, []byte("$2")) || bytes.Contains(hash, []byte(passphrase)) {
				t.Fatal("generated credential was not stored as a bcrypt hash")
			}

			// A regular GET cannot recover the displayed passphrase.
			r := httptest.NewRequest(http.MethodGet, "/admin", nil)
			r.AddCookie(cookie)
			w = httptest.NewRecorder()
			cfg.handleAdmin(w, r)
			if w.Code != http.StatusOK || strings.Contains(w.Body.String(), passphrase) || strings.Contains(w.Body.String(), `id="issuedPassphrase"`) || strings.Contains(w.Body.String(), `id="new-password"`) {
				t.Fatal("management page still prompts for a creation password or discloses an earlier passphrase")
			}

			// Repeating a POST must neither reset the existing password nor
			// disclose the unused credential generated for the failed request.
			w = httptest.NewRecorder()
			cfg.handleAdmin(w, formRequest("/admin", form, cookie))
			if w.Code != http.StatusBadRequest || strings.Contains(w.Body.String(), `id="issuedPassphrase"`) {
				t.Fatal("duplicate creation succeeded or displayed an unused credential")
			}
			if _, err := cfg.store.authenticate(ctx, "Alice Smith", passphrase); err != nil {
				t.Fatalf("duplicate creation changed the existing password: %v", err)
			}
		})
	}
}

func TestAdminInvalidCreationDoesNotDisplayCredentials(t *testing.T) {
	t.Parallel()
	cfg := newTestConfig(t, 100)
	if _, err := cfg.store.createUser(context.Background(), "Admin", testPassword, true); err != nil {
		t.Fatal(err)
	}
	cookie, csrf := sessionFor(t, cfg, "Admin")
	for _, form := range []url.Values{
		{"action": {"create"}, "username": {"Alice"}, "quota": {"invalid"}, "csrf_token": {csrf}},
		{"action": {"create"}, "username": {""}, "csrf_token": {csrf}},
	} {
		w := httptest.NewRecorder()
		cfg.handleAdmin(w, formRequest("/admin", form, cookie))
		if w.Code != http.StatusBadRequest || strings.Contains(w.Body.String(), `id="issuedPassphrase"`) {
			t.Fatalf("invalid creation displayed credentials: status %d", w.Code)
		}
	}
	users, err := cfg.store.listUsers(context.Background())
	if err != nil || len(users) != 2 {
		t.Fatalf("invalid creation added an account: %#v, %v", users, err)
	}
}
