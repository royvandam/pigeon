package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/royvandam/pigeon/pkg/account"
	"github.com/royvandam/pigeon/pkg/share"
	"github.com/royvandam/pigeon/pkg/storage"
)

func (s *Server) handleShares(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if !allowMethods(w, r, http.MethodGet, http.MethodPost) {
		return
	}
	a, ok := s.requireAuth(w, r, authOptions{})
	if !ok {
		return
	}
	name := r.URL.Query().Get("path")
	if r.Method == http.MethodPost {
		name = r.PostForm.Get("path")
	}
	if err := storage.ValidatePath(name); err != nil {
		writeStorageError(w, err)
		return
	}
	access, err := s.files.Open(r.Context(), a.User)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	defer access.Close()
	link, active := "", false
	if r.Method == http.MethodPost && r.PostForm.Get("action") == "revoke" {
		// Revoking does not require the item to still exist on disk.
		err = s.shares.Revoke(r.Context(), a.User.ID, name)
	} else {
		info, statErr := access.Stat(r.Context(), name)
		if statErr != nil {
			writeStorageError(w, statErr)
			return
		}
		if r.Method == http.MethodGet {
			active, err = s.shares.Active(r.Context(), a.User, name)
		} else if r.PostForm.Get("action") == "create" {
			var secret string
			secret, err = s.shares.Create(r.Context(), a.User, name, info.IsDir())
			link = "/s/" + secret
			active = err == nil
		} else {
			http.Error(w, "unknown share action", http.StatusBadRequest)
			return
		}
	}
	if err != nil {
		if errors.Is(err, account.ErrInvalidCredentials) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		} else {
			serverError(w, r, "failed to update share link", err)
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	// The URL is only known right after creation.
	json.NewEncoder(w).Encode(struct {
		URL    string `json:"url"`
		Active bool   `json:"active"`
	}{URL: link, Active: active})
}

func (s *Server) handleSharedDownload(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
	if !allowMethods(w, r, http.MethodGet, http.MethodHead) {
		return
	}
	download, err := s.files.OpenShared(r.Context(), r.PathValue("token"))
	if errors.Is(err, share.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			status = http.StatusRequestTimeout
			slog.Debug("shared download canceled", "err", err)
		} else {
			slog.Error("shared download unavailable", "err", err)
		}
		http.Error(w, "download unavailable", status)
		return
	}
	defer download.Close()
	serveDownload(w, r, download)
}
