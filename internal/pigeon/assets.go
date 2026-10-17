package pigeon

import (
	"bytes"
	"net/http"
	"strings"
	"time"
)

func (cfg config) handleAsset(w http.ResponseWriter, r *http.Request) {
	if !allowMethods(w, r, http.MethodGet, http.MethodHead) {
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/assets/")
	content, err := applicationAssets.ReadFile("static/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// The URL stays stable across builds, so a restarted application must serve
	// its current embedded code rather than use an older browser cache entry.
	w.Header().Set("Cache-Control", "no-cache")
	if strings.HasSuffix(name, ".js") {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	} else if strings.HasSuffix(name, ".css") {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	}
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(content))
}
