package server

import (
	"bytes"
	"context"
	"errors"
	"html"
	"io/fs"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/royvandam/pigeon/pkg/configuration"
	"github.com/royvandam/pigeon/pkg/quota"
)

func TestRoutesAndEmbeddedPage(t *testing.T) {
	t.Parallel()
	handler := newTestServer(t, 1024).Handler()
	for _, tt := range []struct {
		method string
		path   string
		status int
		allow  string
	}{
		{http.MethodGet, "/", http.StatusOK, ""},
		{http.MethodGet, "/missing", http.StatusNotFound, ""},
		{http.MethodPost, "/", http.StatusMethodNotAllowed, http.MethodGet},
		{http.MethodGet, "/upload", http.StatusMethodNotAllowed, http.MethodPost},
	} {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(tt.method, tt.path, nil)
			r.SetBasicAuth("Test User", testPassword)
			handler.ServeHTTP(w, r)
			if w.Code != tt.status || w.Header().Get("Allow") != tt.allow {
				t.Fatalf("status = %d, Allow = %q; want %d, %q", w.Code, w.Header().Get("Allow"), tt.status, tt.allow)
			}
			if tt.status == http.StatusOK {
				body := w.Body.String()
				if w.Header().Get("Content-Type") != "text/html; charset=utf-8" ||
					!strings.Contains(body, "Storage:") ||
					!strings.Contains(body, `data-remaining-bytes="1024"`) ||
					!strings.Contains(body, `src="/assets/upload.js" defer`) {
					t.Fatalf("embedded page was not rendered correctly: %s", body)
				}
			}
		})
	}
}

func TestUploadSettingsAreEscapedAsHTMLData(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, 1024)
	cookie, csrf := sessionFor(t, srv, "Test User")
	directory := `Quotes "' <tag> & spaces`
	writeStoredFile(t, srv, "test-user", directory+"/kept.txt", "kept")
	r := httptest.NewRequest(http.MethodGet, "/?"+url.Values{"dir": {directory}}.Encode(), nil)
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)
	match := regexp.MustCompile(`<div id="uploadPanel"[^>]*data-directory="([^"]*)"`).FindStringSubmatch(w.Body.String())
	if w.Code != http.StatusOK || len(match) != 2 || html.UnescapeString(match[1]) != directory {
		t.Fatalf("directory was not transported safely to browser code: status=%d, matches=%v", w.Code, match)
	}
	if !strings.Contains(w.Body.String(), `data-csrf-token="`+csrf+`"`) || strings.Contains(w.Body.String(), "(function ()") {
		t.Fatal("browser settings must be escaped HTML data rather than interpolated JavaScript")
	}
}

func TestRunServerReportsListenErrors(t *testing.T) {
	t.Parallel()
	err := runServer(context.Background(), &http.Server{Addr: "invalid-address"})
	if err == nil {
		t.Fatal("expected a listen error")
	}
}

func TestResponsesCarrySecurityHeaders(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, 1024)
	if _, err := srv.accounts.Create(context.Background(), "Admin", testPassword, true, quota.DefaultQuota); err != nil {
		t.Fatal(err)
	}
	handler := srv.Handler()
	for _, tt := range []struct {
		name, path     string
		basic          string
		status         int
		referrerPolicy string
	}{
		{"login page", "/login", "", http.StatusOK, "same-origin"},
		{"explorer", "/", "Test User", http.StatusOK, "same-origin"},
		{"admin page", "/admin", "Admin", http.StatusOK, "same-origin"},
		{"sign-in redirect", "/", "", http.StatusSeeOther, "same-origin"},
		{"not found", "/missing", "Test User", http.StatusNotFound, "same-origin"},
		{"asset", "/assets/tailwind.css", "", http.StatusOK, "same-origin"},
		{"authentication challenge", "/files", "", http.StatusUnauthorized, "same-origin"},
		{"share link", "/s/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "", http.StatusNotFound, "no-referrer"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, tt.path, nil)
			if tt.basic != "" {
				r.SetBasicAuth(tt.basic, testPassword)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			h := w.Header()
			if w.Code != tt.status || h.Get("Content-Security-Policy") != contentSecurityPolicy ||
				h.Get("Referrer-Policy") != tt.referrerPolicy || h.Get("X-Content-Type-Options") != "nosniff" ||
				h.Get("X-Frame-Options") != "DENY" {
				t.Fatalf("%d %v", w.Code, h)
			}
		})
	}
}

// The Content-Security-Policy blocks inline code and styles; keep templates free of them.
func TestTemplatesContainNoInlineScriptsOrStyles(t *testing.T) {
	t.Parallel()
	scripts := regexp.MustCompile(`(?is)<script([^>]*)>(.*?)</script>`)
	inline := regexp.MustCompile(`(?i)<style|\sstyle=|\son[a-z]+=|javascript:`)
	templates, err := fs.Glob(applicationAssets, "templates/*.html")
	if err != nil || len(templates) == 0 {
		t.Fatalf("templates: %v %v", templates, err)
	}
	for _, name := range templates {
		content, err := applicationAssets.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, script := range scripts.FindAllSubmatch(content, -1) {
			if !bytes.Contains(script[1], []byte("src=")) || len(bytes.TrimSpace(script[2])) != 0 {
				t.Errorf("%s contains an inline script blocked by the CSP: %s", name, script[0])
			}
		}
		if match := inline.Find(content); match != nil {
			t.Errorf("%s contains inline code or styles blocked by the CSP: %s", name, match)
		}
	}
}

// This test replaces the global log output and level, so it must not call
// t.Parallel: Go runs it before the parallel tests start, so their requests
// are not logged into its buffer.
func TestRequestLogEscapesPathsAndRedactsShareTokens(t *testing.T) {
	var output bytes.Buffer
	old := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(old)
	defer slog.SetLogLoggerLevel(slog.SetLogLoggerLevel(slog.LevelDebug))
	handler := newTestServer(t, 100).Handler()
	token := strings.Repeat("A", 43)
	for _, test := range []struct{ path, logged string }{
		{"/files", `/files`},
		{"/x path=/admin", `"/x path=/admin"`},
		{"/x\n2026/10/17 12:00:00 DEBUG request method=POST path=/admin", `"/x\n2026/10/17 12:00:00 DEBUG request method=POST path=/admin"`},
		{"/x\r\x1b[2K", `"/x\r\x1b[2K"`},
		{"/invoice\u202efdp.exe", `"/invoice\u202efdp.exe"`},
		{"/s/" + token, `/s/[redacted]`},
		{"/s/" + token + "/", `/s/[redacted]`},
		{"//s/" + token, `//s/[redacted]`},
		{"/s//" + token, `/s/[redacted]`},
		{"/x/../s/" + token, `/x/../s/[redacted]`},
		{"/s/" + token + "/../../x", `/s/[redacted]`},
		{"/s/" + token + "\nGET /forged", `/s/[redacted]`},
	} {
		output.Reset()
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.URL.Path = test.path // As decoded from the request line, such as %0a or //.
		handler.ServeHTTP(httptest.NewRecorder(), r)
		line := output.String()
		if strings.Count(line, "\n") != 1 || !strings.Contains(line, " method=GET path="+test.logged+" ") || strings.Contains(line, token) {
			t.Errorf("request for %q logged %q; want one line with %s", test.path, line, test.logged)
		}
	}
}

func TestNewAppliesDefaultsAndRejectsInvalidSettings(t *testing.T) {
	t.Parallel()
	s, err := New(Configuration{}, Dependencies{})
	if err != nil || s.listen != "127.0.0.1:8080" || s.formTimeout != 30*time.Second || s.uploadTimeout != 60*time.Second ||
		s.publicURL != nil || len(s.trustedProxies) != 0 {
		t.Fatalf("defaults: %+v, %v", s, err)
	}
	s, err = New(Configuration{ListenAddress: ":9000", PublicURL: "https://Files.Example.com", TrustedProxies: []string{"::1"},
		FormReadTimeout: configuration.Duration(time.Second), UploadIdleTimeout: configuration.Duration(time.Minute)}, Dependencies{})
	if err != nil || s.listen != ":9000" || s.publicURL.String() != "https://files.example.com" || len(s.trustedProxies) != 1 ||
		s.formTimeout != time.Second || s.uploadTimeout != time.Minute {
		t.Fatalf("settings: %+v, %v", s, err)
	}
	for name, cfg := range map[string]Configuration{
		"listen address without a port": {ListenAddress: "127.0.0.1"},
		"listen port out of range":      {ListenAddress: "127.0.0.1:65536"},
		"public URL with a path":        {PublicURL: "https://files.example.com/pigeon"},
		"trusted proxy by name":         {TrustedProxies: []string{"proxy.example"}},
		"negative timeout":              {UploadIdleTimeout: configuration.Duration(-time.Second)},
	} {
		if _, err := New(cfg, Dependencies{}); err == nil {
			t.Errorf("%s was accepted", name)
		}
		if err := cfg.Validate(); err == nil {
			t.Errorf("%s passed validation", name)
		}
	}
}

func TestNormalizeShowsTheSettingsAsApplied(t *testing.T) {
	t.Parallel()
	got, err := Configuration{PublicURL: "https://Files.Example.com/", TrustedProxies: []string{" 127.0.0.1", "", "10.1.2.3/8"}}.Normalize()
	want := Configuration{ListenAddress: DefaultListenAddress, PublicURL: "https://files.example.com", TrustedProxies: []string{"127.0.0.1/32", "10.0.0.0/8"},
		FormReadTimeout: configuration.Duration(DefaultFormReadTimeout), UploadIdleTimeout: configuration.Duration(DefaultUploadIdleTimeout)}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Normalize = %#v, %v; want %#v", got, err, want)
	}
	if _, err := (Configuration{ListenAddress: "nowhere"}).Normalize(); err == nil {
		t.Fatal("normalized an invalid configuration")
	}
}

// This test replaces the global log output, so it must not call t.Parallel.
func TestServerErrorsAreLoggedUnlessTheClientLeft(t *testing.T) {
	var output bytes.Buffer
	old := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(old)
	failure := errors.New("disk on fire")
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, test := range []struct {
		name   string
		answer func(http.ResponseWriter, *http.Request)
		ctx    context.Context
		logged string
	}{
		{"server error", func(w http.ResponseWriter, r *http.Request) { serverError(w, r, "logout failed", failure) },
			context.Background(), `ERROR logout failed err="disk on fire"`},
		{"abandoned server error", func(w http.ResponseWriter, r *http.Request) { serverError(w, r, "logout failed", failure) },
			canceled, ""},
		{"storage failure", func(w http.ResponseWriter, r *http.Request) { writeStorageError(w, failure) },
			context.Background(), `ERROR unable to access files err="disk on fire"`},
		{"missing file", func(w http.ResponseWriter, r *http.Request) { writeStorageError(w, os.ErrNotExist) },
			context.Background(), ""},
	} {
		output.Reset()
		w := httptest.NewRecorder()
		test.answer(w, httptest.NewRequestWithContext(test.ctx, http.MethodPost, "/", nil))
		if line := output.String(); test.logged == "" && line != "" || !strings.Contains(line, test.logged) {
			t.Errorf("%s logged %q; want %q", test.name, line, test.logged)
		}
	}
}
