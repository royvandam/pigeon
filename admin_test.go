package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

func createdPassphrase(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	match := regexp.MustCompile(`<code id="createdPassphrase"[^>]*>([^<]+)</code>`).FindStringSubmatch(w.Body.String())
	if len(match) != 2 || len(strings.Split(match[1], "-")) != 4 {
		t.Fatal("creation response did not display a four-word passphrase")
	}
	return match[1]
}

func TestAdminGeneratesUserCredentials(t *testing.T) {
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
			passphrase := createdPassphrase(t, w)
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
			if w.Code != http.StatusOK || strings.Contains(w.Body.String(), passphrase) || strings.Contains(w.Body.String(), `id="createdPassphrase"`) || strings.Contains(w.Body.String(), `id="new-password"`) {
				t.Fatal("management page still prompts for a creation password or discloses an earlier passphrase")
			}

			// Repeating a POST must neither reset the existing password nor
			// disclose the unused credential generated for the failed request.
			w = httptest.NewRecorder()
			cfg.handleAdmin(w, formRequest("/admin", form, cookie))
			if w.Code != http.StatusBadRequest || strings.Contains(w.Body.String(), `id="createdPassphrase"`) {
				t.Fatal("duplicate creation succeeded or displayed an unused credential")
			}
			if _, err := cfg.store.authenticate(ctx, "Alice Smith", passphrase); err != nil {
				t.Fatalf("duplicate creation changed the existing password: %v", err)
			}
		})
	}
}

func TestAdminInvalidCreationDoesNotDisplayCredentials(t *testing.T) {
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
		if w.Code != http.StatusBadRequest || strings.Contains(w.Body.String(), `id="createdPassphrase"`) {
			t.Fatalf("invalid creation displayed credentials: status %d", w.Code)
		}
	}
	users, err := cfg.store.listUsers(context.Background())
	if err != nil || len(users) != 2 {
		t.Fatalf("invalid creation added an account: %#v, %v", users, err)
	}
}
