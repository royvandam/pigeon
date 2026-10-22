package server

import (
	"bytes"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path"
	"regexp"
	"strings"
	"testing"
)

func TestAssetsServeEmbeddedCodeWithoutAuthentication(t *testing.T) {
	t.Parallel()
	for name, kind := range map[string]string{
		"dialog.js": "text/javascript; charset=utf-8", "shares.js": "text/javascript; charset=utf-8",
		"validation.js": "text/javascript; charset=utf-8", "upload.js": "text/javascript; charset=utf-8",
		"styles.css": "text/css; charset=utf-8", "tailwind.css": "text/css; charset=utf-8",
		"logo.webp":                               "image/webp",
		"fontawesome/css/fontawesome.min.css":     "text/css; charset=utf-8",
		"fontawesome/css/solid.min.css":           "text/css; charset=utf-8",
		"fontawesome/webfonts/fa-solid-900.woff2": "font/woff2",
		"fontawesome/LICENSE.txt":                 "text/plain; charset=utf-8",
	} {
		expected, err := applicationAssets.ReadFile("static/" + name)
		if err != nil {
			t.Fatal(err)
		}
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			w := httptest.NewRecorder()
			(&Server{}).Handler().ServeHTTP(w, httptest.NewRequest(method, "/assets/"+name, nil))
			if w.Code != http.StatusOK || w.Header().Get("Content-Type") != kind || w.Header().Get("Cache-Control") != "no-cache" ||
				w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("ETag") == "" {
				t.Fatalf("%s asset %s: %d %v", method, name, w.Code, w.Header())
			}
			if method == http.MethodGet && !bytes.Equal(w.Body.Bytes(), expected) || method == http.MethodHead && w.Body.Len() != 0 {
				t.Fatalf("%s %s did not serve the embedded asset", method, name)
			}
		}
	}
}

func TestAssetsRevalidateUnchangedContent(t *testing.T) {
	t.Parallel()
	handler := (&Server{}).Handler()
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/assets/tailwind.css", nil))
	etag := w.Header().Get("ETag")
	if w.Code != http.StatusOK || !strings.HasPrefix(etag, `"`) {
		t.Fatalf("first request: %d ETag %q", w.Code, etag)
	}

	r := httptest.NewRequest(http.MethodGet, "/assets/tailwind.css", nil)
	r.Header.Set("If-None-Match", etag)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusNotModified || w.Body.Len() != 0 {
		t.Fatalf("matching ETag: %d with %d body bytes", w.Code, w.Body.Len())
	}

	other := httptest.NewRecorder()
	handler.ServeHTTP(other, httptest.NewRequest(http.MethodGet, "/assets/styles.css", nil))
	if other.Header().Get("ETag") == etag {
		t.Fatal("different assets share an ETag")
	}
}

func TestAssetPathsCannotExposeTemplatesOrLocalFiles(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"", "missing.js", "../templates/admin.html", "../../go.mod", "uploads/private.txt", "pigeon.db",
		"fontawesome", "fontawesome/css", "../tailwind.input.css", "../embed.go"} {
		w := httptest.NewRecorder()
		// Call directly so path validation is covered without ServeMux redirects.
		(&Server{}).handleAsset(w, httptest.NewRequest(http.MethodGet, "/assets/"+name, nil))
		if w.Code != http.StatusNotFound {
			t.Fatalf("unsafe asset path %q: %d %s", name, w.Code, w.Body)
		}
	}
}

// Pages must not load code, styles, or fonts from other origins (audit A1).
func TestTemplatesReferenceOnlyLocalAssets(t *testing.T) {
	t.Parallel()
	external := regexp.MustCompile(`(?i)(https?:)?//[a-z0-9.-]+\.[a-z]{2,}`)
	templates, err := fs.Glob(applicationAssets, "templates/*.html")
	if err != nil || len(templates) == 0 {
		t.Fatalf("templates: %v %v", templates, err)
	}
	for _, name := range templates {
		content, err := applicationAssets.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if match := external.Find(content); match != nil {
			t.Errorf("%s references an external origin: %s", name, match)
		}
	}
}

func TestPageStylesheetsAndFontsResolve(t *testing.T) {
	t.Parallel()
	handler := (&Server{}).Handler()
	page, err := applicationAssets.ReadFile("templates/assets.html")
	if err != nil {
		t.Fatal(err)
	}
	links := regexp.MustCompile(`href="(/assets/[^"]+\.css)"`).FindAllSubmatch(page, -1)
	if len(links) != 4 {
		t.Fatalf("expected four stylesheets, found %d", len(links))
	}
	fontURL := regexp.MustCompile(`url\(([^)]+)\)`)
	for _, link := range links {
		stylesheet := string(link[1])
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, stylesheet, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d", stylesheet, w.Code)
		}
		// Relative font URLs resolve against the stylesheet's own location.
		for _, match := range fontURL.FindAllStringSubmatch(w.Body.String(), -1) {
			ref := strings.Trim(match[1], `"'`)
			if strings.HasPrefix(ref, "data:") {
				continue
			}
			font := path.Join(path.Dir(stylesheet), ref)
			fw := httptest.NewRecorder()
			handler.ServeHTTP(fw, httptest.NewRequest(http.MethodGet, font, nil))
			if fw.Code != http.StatusOK || fw.Header().Get("Content-Type") != "font/woff2" {
				t.Fatalf("%s references %s: %d %s", stylesheet, font, fw.Code, fw.Header().Get("Content-Type"))
			}
		}
	}
}
