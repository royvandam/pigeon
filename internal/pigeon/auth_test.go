package pigeon

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestAdminUsesSharedAccountDialogs(t *testing.T) {
	t.Parallel()
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
	resetButtons := regexp.MustCompile(`<button\b[^>]*data-dialog-action="reset-password"[^>]*>`).FindAllString(body, -1)
	if len(resetButtons) != 2 || strings.Contains(body, `name="action" value="reset"`) {
		t.Fatal("password resets must open a confirmation dialog instead of posting directly")
	}
	for _, button := range resetButtons {
		self := strings.Contains(button, `data-user-id="`+strconv.FormatInt(admin.ID, 10)+`"`)
		name := "Test User"
		if self {
			name = admin.Username
		}
		if !strings.Contains(button, `type="button"`) || !strings.Contains(button, `data-name="`+name+`"`) || !strings.Contains(button, `data-self="`+strconv.FormatBool(self)+`"`) {
			t.Fatalf("reset button cannot confirm the selected account or self-reset: %s", button)
		}
	}
	if strings.Count(body, `<dialog id="actionDialog"`) != 1 || !strings.Contains(body, `<form id="actionDialogForm" method="post"`) {
		t.Fatal("admin page did not render exactly one shared dialog")
	}
}

func TestBrowserLoginLogout(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	passphrase := displayedPassphrase(t, w)
	alice, err := cfg.store.authenticate(ctx, "Alice Smith", passphrase)
	if err != nil || alice.Admin || alice.Slug != "alice-smith" {
		t.Fatalf("created user: %#v, %v", alice, err)
	}
	oldToken, err := cfg.store.createSession(ctx, alice)
	if err != nil {
		t.Fatal(err)
	}
	reset := url.Values{"action": {"reset"}, "user_id": {strconv.FormatInt(alice.ID, 10)}, "csrf_token": {csrf}}
	w = httptest.NewRecorder()
	cfg.handleAdmin(w, formRequest("/admin", reset, adminCookie))
	if w.Code != http.StatusOK {
		t.Fatalf("reset user: %d %s", w.Code, w.Body)
	}
	resetPassphrase := displayedPassphrase(t, w)
	if _, err := cfg.store.session(ctx, oldToken); !errors.Is(err, errInvalidCredentials) {
		t.Fatalf("password reset did not revoke session: %v", err)
	}
	if _, err := cfg.store.createSession(ctx, alice); !errors.Is(err, errInvalidCredentials) {
		t.Fatalf("password reset accepted a login already in progress: %v", err)
	}
	if _, err := cfg.store.authenticate(ctx, "Alice Smith", passphrase); !errors.Is(err, errInvalidCredentials) {
		t.Fatal("old password still works")
	}
	alice, err = cfg.store.authenticate(ctx, "Alice Smith", resetPassphrase)
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
	if _, err := cfg.store.authenticate(ctx, "Alice Smith", resetPassphrase); !errors.Is(err, errInvalidCredentials) {
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
	t.Parallel()
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

func TestOnlyAPIClientsReceiveABasicChallenge(t *testing.T) {
	t.Parallel()
	cfg := newTestConfig(t, 100)
	handler := newServer(cfg).Handler
	expired, _ := sessionFor(t, cfg, "Test User")
	if _, err := cfg.store.db.Exec(`UPDATE sessions SET expires_at = 1`); err != nil {
		t.Fatal(err)
	}
	clients := []struct {
		name     string
		headers  map[string]string
		navigate bool
	}{
		{name: "API client"},
		{name: "script over HTTPS", headers: map[string]string{"Sec-Fetch-Mode": "cors", "Sec-Fetch-Site": "same-origin"}},
		{name: "explorer script over HTTP", headers: map[string]string{"X-Requested-With": "XMLHttpRequest"}},
		{name: "navigation over HTTPS", headers: map[string]string{"Sec-Fetch-Mode": "navigate"}, navigate: true},
		{name: "navigation over HTTP", headers: map[string]string{"Upgrade-Insecure-Requests": "1"}, navigate: true},
	}
	endpoints := []struct{ method, target string }{
		{http.MethodGet, "/files?dir="},
		{http.MethodGet, "/download?path="},
		{http.MethodGet, "/upload-progress?id=0123456789abcdef0123456789abcdef"},
		{http.MethodGet, "/shares?path=file.txt"},
		{http.MethodPost, "/upload"},
		{http.MethodPost, "/shares"},
	}
	for _, client := range clients {
		for _, endpoint := range endpoints {
			for _, session := range []bool{false, true} {
				r := httptest.NewRequest(endpoint.method, endpoint.target, nil)
				for name, value := range client.headers {
					r.Header.Set(name, value)
				}
				if session {
					r.AddCookie(expired)
				}
				w := serve(handler, r)
				challenge := w.Header().Get("WWW-Authenticate")
				where := client.name + " " + endpoint.method + " " + endpoint.target
				switch {
				case client.navigate && endpoint.method == http.MethodGet:
					if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/login" || challenge != "" {
						t.Errorf("%s (session %v) = %d to %q, challenge %q; want redirect to /login", where, session, w.Code, w.Header().Get("Location"), challenge)
					}
				case client.headers == nil:
					if w.Code != http.StatusUnauthorized || !strings.HasPrefix(challenge, "Basic ") {
						t.Errorf("%s (session %v) = %d, challenge %q; want 401 with a Basic challenge", where, session, w.Code, challenge)
					}
				default:
					if w.Code != http.StatusUnauthorized || challenge != "" {
						t.Errorf("%s (session %v) = %d, challenge %q; want 401 without a challenge", where, session, w.Code, challenge)
					}
				}
			}
		}
	}

	// API clients that send credentials, as curl --user does, still authenticate.
	r := httptest.NewRequest(http.MethodGet, "/files?dir=", nil)
	r.SetBasicAuth("Test User", testPassword)
	if w := serve(handler, r); w.Code != http.StatusOK {
		t.Fatalf("Basic-authenticated file list = %d: %s", w.Code, w.Body)
	}
}

func TestNewDatabasesListenOnLoopback(t *testing.T) {
	t.Parallel()
	s, err := openStore(context.Background(), filepath.Join(t.TempDir(), "new.db"), testPasswordCost)
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	got, err := s.loadSettings(context.Background())
	if err != nil || got.listen != "127.0.0.1:8080" || got.publicURL != "" {
		t.Fatalf("new database settings = %#v, %v", got, err)
	}
}

func cookieNamed(w *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	return nil
}

// A TLS-terminating proxy forwards plain HTTP and rewrites Host, as some
// proxies do; the browser still talks HTTPS to the public origin.
func TestHTTPSPublicURLSecuresCookiesAndEnablesHSTS(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		publicURL string
		secure    bool
	}{
		{"https://files.example.com", true},
		{"http://files.example.com", false},
		{"", false},
	} {
		t.Run("public URL "+tt.publicURL, func(t *testing.T) {
			cfg := newTestConfig(t, 100)
			publicURL, err := parsePublicURL(tt.publicURL)
			if err != nil {
				t.Fatal(err)
			}
			cfg.publicURL = publicURL
			if _, err := cfg.store.createUser(context.Background(), "Admin", testPassword, true); err != nil {
				t.Fatal(err)
			}
			handler := newServer(cfg).Handler
			check := func(step string, w *httptest.ResponseRecorder, cookie string, status int) *http.Cookie {
				t.Helper()
				c := cookieNamed(w, cookie)
				hsts := w.Header().Get("Strict-Transport-Security")
				if w.Code != status || c == nil || c.Secure != tt.secure || (hsts == strictTransportSecurity) != tt.secure {
					t.Fatalf("%s: status %d, cookie %+v, HSTS %q", step, w.Code, c, hsts)
				}
				return c
			}
			forwarded := func(r *http.Request) *http.Request {
				r.Host = "127.0.0.1:8080"
				if publicURL != nil {
					r.Header.Set("Origin", publicURL.String())
				}
				return r
			}

			login := check("login page", serve(handler, httptest.NewRequest(http.MethodGet, "/login", nil)), loginCookieName, http.StatusOK)
			r := forwarded(formRequest("/login", url.Values{"username": {"Admin"}, "password": {testPassword}, "csrf_token": {login.Value}}, login))
			w := serve(handler, r)
			session := check("sign-in", w, sessionCookieName, http.StatusSeeOther)
			check("sign-in clears the login cookie", w, loginCookieName, http.StatusSeeOther)

			a, err := cfg.store.session(context.Background(), session.Value)
			if err != nil {
				t.Fatal(err)
			}
			reset := forwarded(formRequest("/admin", url.Values{"action": {"reset"}, "user_id": {strconv.FormatInt(a.User.ID, 10)}, "csrf_token": {a.CSRFToken}}, session))
			if c := check("own password reset", serve(handler, reset), sessionCookieName, http.StatusOK); c.MaxAge >= 0 {
				t.Fatalf("own password reset kept the session cookie: %+v", c)
			}

			session, csrf := sessionFor(t, cfg, "Test User")
			logout := forwarded(formRequest("/logout", url.Values{"csrf_token": {csrf}}, session))
			if c := check("sign-out", serve(handler, logout), sessionCookieName, http.StatusSeeOther); c.MaxAge >= 0 {
				t.Fatalf("sign-out kept the session cookie: %+v", c)
			}
		})
	}
}

func TestOriginCheckAcceptsThePublicURLBehindARewritingProxy(t *testing.T) {
	t.Parallel()
	publicURL, err := parsePublicURL("https://files.example.com")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, host, origin string
		publicURL          bool
		want               bool
	}{
		{"origin matches Host", "files.example.com", "https://files.example.com", false, true},
		{"rewritten Host without a public URL", "127.0.0.1:8080", "https://files.example.com", false, false},
		{"rewritten Host with the public URL", "127.0.0.1:8080", "https://files.example.com", true, true},
		{"public host over the wrong scheme", "127.0.0.1:8080", "http://files.example.com", true, false},
		{"another site", "127.0.0.1:8080", "https://attacker.example", true, false},
		{"no Origin header", "127.0.0.1:8080", "", true, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config{}
			if tt.publicURL {
				cfg.publicURL = publicURL
			}
			r := httptest.NewRequest(http.MethodPost, "/files", nil)
			r.Host = tt.host
			if tt.origin != "" {
				r.Header.Set("Origin", tt.origin)
			}
			if got := cfg.sameOrigin(r); got != tt.want {
				t.Fatalf("sameOrigin = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestPublicURLIsValidatedNormalizedAndPersisted(t *testing.T) {
	t.Parallel()
	for value, want := range map[string]string{
		"https://Files.Example.com/": "https://files.example.com",
		"http://192.168.1.10:8080":   "http://192.168.1.10:8080",
	} {
		u, err := parsePublicURL(value)
		if err != nil || u.String() != want {
			t.Errorf("parsePublicURL(%q) = %v, %v; want %s", value, u, err, want)
		}
	}
	for _, value := range []string{"files.example.com", "ftp://files.example.com", "https://user@files.example.com",
		"https://files.example.com/pigeon", "https://files.example.com/?q=1", "https://files.example.com/#top", "https:///"} {
		if _, err := parsePublicURL(value); !errors.Is(err, errInvalidPublicURL) {
			t.Errorf("%q accepted: %v", value, err)
		}
	}

	cfg := newTestConfig(t, 100)
	ctx := context.Background()
	if err := cfg.store.saveSettings(ctx, settings{dir: cfg.dir, listen: defaultListen, publicURL: "files.example.com"}); err == nil {
		t.Fatal("an invalid public URL was saved")
	}
	want := settings{dir: cfg.dir, listen: defaultListen, publicURL: "https://files.example.com"}
	if err := cfg.store.saveSettings(ctx, want); err != nil {
		t.Fatal(err)
	}
	if got, err := cfg.store.loadSettings(ctx); err != nil || got != want {
		t.Fatalf("settings = %#v, %v", got, err)
	}
	// Databases created before the setting existed gain an empty column.
	if _, err := cfg.store.db.Exec(`ALTER TABLE settings DROP COLUMN public_url`); err != nil {
		t.Fatal(err)
	}
	if err := cfg.store.db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := openStore(ctx, filepath.Join(filepath.Dir(cfg.dir), "app.db"), testPasswordCost)
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	if got, err := s.loadSettings(ctx); err != nil || got != (settings{dir: cfg.dir, listen: defaultListen}) {
		t.Fatalf("migrated settings = %#v, %v", got, err)
	}
}

func TestPlainHTTPOnTheNetworkIsWarnedAbout(t *testing.T) {
	t.Parallel()
	https, _ := parsePublicURL("https://files.example.com")
	plain, _ := parsePublicURL("http://files.example.com")
	for _, tt := range []struct {
		listen    string
		publicURL *url.URL
		warn      bool
	}{
		{"127.0.0.1:8080", nil, false},
		{"[::1]:8080", nil, false},
		{"localhost:8080", nil, false},
		{"0.0.0.0:8080", nil, true},
		{":8080", nil, true},
		{"192.168.1.5:8080", plain, true},
		{"0.0.0.0:8080", https, false},
	} {
		if got := plainHTTPWarning(tt.listen, tt.publicURL) != ""; got != tt.warn {
			t.Errorf("listen %s, public URL %v: warning = %t, want %t", tt.listen, tt.publicURL, got, tt.warn)
		}
	}
}
