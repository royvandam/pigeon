package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/royvandam/pigeon/pkg/share"
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
	name, action := r.URL.Query().Get("path"), ""
	if r.Method == http.MethodPost {
		name, action = r.PostForm.Get("path"), r.PostForm.Get("action")
		if action != "create" && action != "revoke" {
			http.Error(w, "unknown share action", http.StatusBadRequest)
			return
		}
	}
	access, err := s.files.Open(r.Context(), a.User)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	defer access.Close()
	link, active := "", false
	switch action {
	case "":
		active, err = access.Shared(r.Context(), name)
	case "create":
		var secret string
		secret, err = access.Share(r.Context(), name)
		link, active = "/s/"+secret, err == nil
	case "revoke":
		err = access.Unshare(r.Context(), name)
	}
	if err != nil {
		writeStorageError(w, err)
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
