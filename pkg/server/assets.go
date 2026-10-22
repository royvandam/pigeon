package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"path"
	"strings"
	"time"
)

// Only these embedded file types are served; anything else is not found.
var assetContentTypes = map[string]string{
	".css":   "text/css; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".txt":   "text/plain; charset=utf-8",
	".webp":  "image/webp",
	".woff2": "font/woff2",
}

func (s *Server) handleAsset(w http.ResponseWriter, r *http.Request) {
	if !allowMethods(w, r, http.MethodGet, http.MethodHead) {
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/assets/")
	contentType, ok := assetContentTypes[path.Ext(name)]
	if !ok {
		http.NotFound(w, r)
		return
	}
	content, err := applicationAssets.ReadFile("static/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", contentType)
	// The URL stays stable across builds, so a restarted application must serve
	// its current embedded code rather than use an older browser cache entry.
	// The content hash lets browsers revalidate an unchanged copy cheaply.
	w.Header().Set("Cache-Control", "no-cache")
	sum := sha256.Sum256(content)
	w.Header().Set("ETag", `"`+hex.EncodeToString(sum[:16])+`"`)
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(content))
}
