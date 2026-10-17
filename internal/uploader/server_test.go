package uploader

import (
	"context"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

func TestRoutesAndEmbeddedPage(t *testing.T) {
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
	err := runServer(context.Background(), &http.Server{Addr: "invalid-address"})
	if err == nil {
		t.Fatal("expected a listen error")
	}
}
