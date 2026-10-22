package pigeon

import (
	"bytes"
	"context"
	"html"
	"io/fs"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/royvandam/pigeon/pkg/quota"
)

func TestRoutesAndEmbeddedPage(t *testing.T) {
	t.Parallel()
	handler := newServer(newTestConfig(t, 1024)).Handler
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
	cfg := newTestConfig(t, 1024)
	cookie, csrf := sessionFor(t, cfg, "Test User")
	directory := `Quotes "' <tag> & spaces`
	writeStoredFile(t, cfg, "test-user", directory+"/kept.txt", "kept")
	r := httptest.NewRequest(http.MethodGet, "/?"+url.Values{"dir": {directory}}.Encode(), nil)
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	newServer(cfg).Handler.ServeHTTP(w, r)
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
	cfg := newTestConfig(t, 1024)
	if _, err := cfg.store.accounts.Create(context.Background(), "Admin", testPassword, true, quota.DefaultQuota); err != nil {
		t.Fatal(err)
	}
	handler := newServer(cfg).Handler
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

// This test replaces the global log output, so it must not call t.Parallel:
// Go runs it before the parallel tests start, so their requests are not
// logged into its buffer.
func TestRequestLogEscapesPathsAndRedactsShareTokens(t *testing.T) {
	var output bytes.Buffer
	old := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(old)
	handler := newServer(newTestConfig(t, 100)).Handler
	token := strings.Repeat("A", 43)
	for _, test := range []struct{ path, logged string }{
		{"/files", `"/files"`},
		{"/x\n2026/10/17 12:00:00 POST /admin 1ms", `"/x\n2026/10/17 12:00:00 POST /admin 1ms"`},
		{"/x\r\x1b[2K", `"/x\r\x1b[2K"`},
		{"/invoice\u202efdp.exe", `"/invoice\u202efdp.exe"`},
		{"/s/" + token, `"/s/[redacted]"`},
		{"/s/" + token + "/", `"/s/[redacted]"`},
		{"//s/" + token, `"//s/[redacted]"`},
		{"/s//" + token, `"/s/[redacted]"`},
		{"/x/../s/" + token, `"/x/../s/[redacted]"`},
		{"/s/" + token + "/../../x", `"/s/[redacted]"`},
		{"/s/" + token + "\nGET /forged", `"/s/[redacted]"`},
	} {
		output.Reset()
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.URL.Path = test.path // As decoded from the request line, such as %0a or //.
		handler.ServeHTTP(httptest.NewRecorder(), r)
		line := output.String()
		if strings.Count(line, "\n") != 1 || !strings.Contains(line, " GET "+test.logged+" ") || strings.Contains(line, token) {
			t.Errorf("request for %q logged %q; want one line with %s", test.path, line, test.logged)
		}
	}
}
