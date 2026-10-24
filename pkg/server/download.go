package server

import (
	"context"
	"log/slog"
	"mime"
	"net/http"

	"github.com/royvandam/pigeon/pkg/account"
	"github.com/royvandam/pigeon/pkg/files"
)

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !allowMethods(w, r, http.MethodGet, http.MethodHead) {
		return
	}
	a, ok := s.requireAuth(w, r, authOptions{})
	if !ok {
		return
	}
	download, err := s.openPrivateDownload(r.Context(), a.User, r.URL.Query().Get("path"))
	if err != nil {
		writeStorageError(w, err)
		return
	}
	defer download.Close()
	serveDownload(w, r, download)
}

func (s *Server) openPrivateDownload(ctx context.Context, u account.User, name string) (*files.Download, error) {
	access, err := s.files.Open(ctx, u)
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
			// A client that disconnects cancels the request context, also when
			// the failed write notices it first; that is not a server fault.
			if err := d.WriteZip(r.Context(), w); err != nil && r.Context().Err() != nil {
				slog.Debug("folder download canceled", "err", err)
			} else if err != nil {
				slog.Error("folder download failed", "err", err)
			}
		}
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": d.Name()}))
	http.ServeContent(w, r, d.Name(), d.ModTime(), d.File())
}
