package server

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/royvandam/pigeon/pkg/auth"
)

func basicRequest(remote, password string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/files", nil)
	r.RemoteAddr = remote
	r.SetBasicAuth("Test User", password)
	return r
}

// limitedConfig stops attempts from recovering while a test runs, since bcrypt
// can take longer than an interval under the race detector.
func limitedServer(t *testing.T) *testServer {
	t.Helper()
	srv := newTestServer(t, 100)
	now := time.Now()
	srv.authenticator = auth.NewAuthenticator(srv.accounts, func() time.Time { return now })
	return srv
}

func TestFailedPasswordChecksAreLimitedPerClient(t *testing.T) {
	t.Parallel()
	srv := limitedServer(t)
	handler := srv.Handler()
	attacker := "198.51.100.7:4000"
	for i := range auth.AttemptBurst {
		if w := serve(handler, basicRequest(attacker, "wrong-password")); w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i+1, w.Code)
		}
	}
	w := serve(handler, basicRequest(attacker, "wrong-password"))
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "6" {
		t.Fatalf("attempt beyond the limit: %d, Retry-After %q", w.Code, w.Header().Get("Retry-After"))
	}
	// The limit applies before the password is checked, so bcrypt never runs.
	if w := serve(handler, basicRequest(attacker, testPassword)); w.Code != http.StatusTooManyRequests {
		t.Fatalf("correct password while limited: %d", w.Code)
	}
	if w := serve(handler, basicRequest("203.0.113.9:4000", testPassword)); w.Code != http.StatusOK {
		t.Fatalf("other clients are unaffected: %d", w.Code)
	}
}

func TestSuccessfulPasswordChecksAreNotLimited(t *testing.T) {
	t.Parallel()
	srv := limitedServer(t)
	handler := srv.Handler()
	for i := range auth.AttemptBurst + 2 {
		if w := serve(handler, basicRequest("198.51.100.7:4000", testPassword)); w.Code != http.StatusOK {
			t.Fatalf("API request %d with valid credentials: %d", i+1, w.Code)
		}
	}
	// The client still has all of its attempts for wrong passwords.
	for i := range auth.AttemptBurst {
		if w := serve(handler, basicRequest("198.51.100.7:4000", "wrong-password")); w.Code != http.StatusUnauthorized {
			t.Fatalf("failed attempt %d after successful ones: %d", i+1, w.Code)
		}
	}
	if w := serve(handler, basicRequest("198.51.100.7:4000", "wrong-password")); w.Code != http.StatusTooManyRequests {
		t.Fatalf("attempt beyond the limit: %d", w.Code)
	}
}

func TestLoginFormIsLimited(t *testing.T) {
	t.Parallel()
	srv := limitedServer(t)
	handler := srv.Handler()
	page := serve(handler, httptest.NewRequest(http.MethodGet, "/login", nil))
	var csrf *http.Cookie
	for _, cookie := range page.Result().Cookies() {
		if cookie.Name == loginCookieName {
			csrf = cookie
		}
	}
	if csrf == nil {
		t.Fatal("login page did not set its CSRF cookie")
	}
	login := func(password string) *httptest.ResponseRecorder {
		r := formRequest("/login", url.Values{"username": {"Test User"}, "password": {password}, "csrf_token": {csrf.Value}}, csrf)
		r.RemoteAddr = "198.51.100.7:4000"
		return serve(handler, r)
	}
	for i := range auth.AttemptBurst {
		if w := login("wrong-password"); w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i+1, w.Code)
		}
	}
	w := login(testPassword)
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" ||
		!strings.Contains(w.Body.String(), "Too many sign-in attempts. Try again in 6 seconds.") {
		t.Fatalf("limited sign-in: %d %v %s", w.Code, w.Header(), w.Body)
	}
}

func TestClientAddressUsesForwardedForOnlyFromTrustedProxies(t *testing.T) {
	t.Parallel()
	trusted, err := parseTrustedProxies([]string{"10.0.0.0/8", "::1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, remote, forwarded, want string
	}{
		{"direct client ignores the header", "198.51.100.7:4000", "203.0.113.1", "198.51.100.7"},
		{"trusted proxy names the client", "10.1.2.3:4000", "203.0.113.1", "203.0.113.1"},
		{"spoofed entries before the client are ignored", "10.1.2.3:4000", "192.0.2.66, 203.0.113.1", "203.0.113.1"},
		{"chained trusted proxies are skipped", "10.1.2.3:4000", "203.0.113.1, 10.9.9.9", "203.0.113.1"},
		{"missing header falls back to the proxy", "10.1.2.3:4000", "", "10.1.2.3"},
		{"malformed entry stops at the last trusted hop", "10.1.2.3:4000", "203.0.113.1, garbage", "10.1.2.3"},
		{"IPv6 loopback proxy", "[::1]:4000", "2001:db8::5", "2001:db8::5"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = tt.remote
			if tt.forwarded != "" {
				r.Header.Set("X-Forwarded-For", tt.forwarded)
			}
			if got := clientAddress(r, trusted).String(); got != tt.want {
				t.Fatalf("client = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestForwardedClientsBehindTrustedProxyAreLimitedSeparately(t *testing.T) {
	t.Parallel()
	srv := limitedServer(t)
	srv.trustedProxies, _ = parseTrustedProxies([]string{"10.0.0.1"})
	handler := srv.Handler()
	request := func(client, password string) int {
		r := basicRequest("10.0.0.1:4000", password)
		r.Header.Set("X-Forwarded-For", client)
		return serve(handler, r).Code
	}
	for range auth.AttemptBurst {
		request("198.51.100.7", "wrong-password")
	}
	if code := request("198.51.100.7", "wrong-password"); code != http.StatusTooManyRequests {
		t.Fatalf("forwarded attacker: %d", code)
	}
	if code := request("203.0.113.9", testPassword); code != http.StatusOK {
		t.Fatalf("another client behind the same proxy was blocked: %d", code)
	}
}

func TestIPv6ClientsShareTheirSlash64Limit(t *testing.T) {
	t.Parallel()
	srv := limitedServer(t)
	handler := srv.Handler()
	for i := range auth.AttemptBurst {
		remote := fmt.Sprintf("[2001:db8:1:2::%x]:4000", i+1)
		serve(handler, basicRequest(remote, "wrong-password"))
	}
	if w := serve(handler, basicRequest("[2001:db8:1:2:ffff::1]:4000", "wrong-password")); w.Code != http.StatusTooManyRequests {
		t.Fatalf("rotating addresses within a /64 escaped the limit: %d", w.Code)
	}
	if w := serve(handler, basicRequest("[2001:db8:1:3::1]:4000", testPassword)); w.Code != http.StatusOK {
		t.Fatalf("a different /64 was blocked: %d", w.Code)
	}
}

func TestTrustedProxiesAreValidated(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"proxy.example", "10.0.0.0/33", "fe80::1%eth0"} {
		if _, err := parseTrustedProxies([]string{"10.0.0.1", value}); !errors.Is(err, errInvalidTrustedProxy) {
			t.Errorf("%q accepted: %v", value, err)
		}
	}
	prefixes, err := parseTrustedProxies([]string{" 10.0.0.1 ", "", "::ffff:192.0.2.1", "10.1.2.3/8"})
	if err != nil || len(prefixes) != 3 || prefixes[0].String() != "10.0.0.1/32" || prefixes[1].String() != "192.0.2.1/32" || prefixes[2].String() != "10.0.0.0/8" {
		t.Fatalf("trusted proxies = %v, %v", prefixes, err)
	}
}
