package uploader

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAssetsServeEmbeddedCodeWithoutAuthentication(t *testing.T) {
	for _, name := range []string{"dialog.js", "shares.js", "validation.js", "upload.js", "styles.css"} {
		expected, err := applicationAssets.ReadFile("static/" + name)
		if err != nil {
			t.Fatal(err)
		}
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			w := httptest.NewRecorder()
			newServer(config{}).Handler.ServeHTTP(w, httptest.NewRequest(method, "/assets/"+name, nil))
			kind := "text/javascript; charset=utf-8"
			if strings.HasSuffix(name, ".css") {
				kind = "text/css; charset=utf-8"
			}
			if w.Code != http.StatusOK || w.Header().Get("Content-Type") != kind || w.Header().Get("Cache-Control") != "no-cache" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatalf("%s asset %s: %d %v", method, name, w.Code, w.Header())
			}
			if method == http.MethodGet && !bytes.Equal(w.Body.Bytes(), expected) || method == http.MethodHead && w.Body.Len() != 0 {
				t.Fatalf("%s %s did not serve the embedded asset", method, name)
			}
		}
	}
}

func TestAssetPathsCannotExposeTemplatesOrLocalFiles(t *testing.T) {
	for _, name := range []string{"", "missing.js", "../templates/admin.html", "../../go.mod", "uploads/private.txt", "uploader.db"} {
		w := httptest.NewRecorder()
		// Call directly so path validation is covered without ServeMux redirects.
		(config{}).handleAsset(w, httptest.NewRequest(http.MethodGet, "/assets/"+name, nil))
		if w.Code != http.StatusNotFound {
			t.Fatalf("unsafe asset path %q: %d %s", name, w.Code, w.Body)
		}
	}
}
