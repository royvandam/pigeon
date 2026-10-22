package pigeon

import (
	"context"
	"log"
	"mime"
	"net/http"

	"github.com/royvandam/pigeon/pkg/account"
	"github.com/royvandam/pigeon/pkg/files"
)

func (cfg config) handleDownload(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !allowMethods(w, r, http.MethodGet, http.MethodHead) {
		return
	}
	a, ok := cfg.requireAuth(w, r, authOptions{})
	if !ok {
		return
	}
	download, err := cfg.openPrivateDownload(r.Context(), a.User, r.URL.Query().Get("path"))
	if err != nil {
		writeStorageError(w, err)
		return
	}
	defer download.Close()
	serveDownload(w, r, download)
}

func (cfg config) openPrivateDownload(ctx context.Context, u account.User, name string) (*files.Download, error) {
	access, err := cfg.files.Open(ctx, u)
	if err != nil {
		return nil, err
	}
	defer access.Close()
	// The download retains its own handle; release account access before streaming.
	return access.OpenDownload(ctx, name)
}

func serveDownload(w http.ResponseWriter, r *http.Request, d *files.Download) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if d.Folder() {
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": d.Name() + ".zip"}))
		if r.Method != http.MethodHead {
			if err := d.WriteZip(r.Context(), w); err != nil {
				log.Printf("folder download failed: %v", err)
			}
		}
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": d.Name()}))
	http.ServeContent(w, r, d.Name(), d.ModTime(), d.File())
}
