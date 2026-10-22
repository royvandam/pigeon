package pigeon

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/royvandam/pigeon/pkg/account"
	"github.com/royvandam/pigeon/pkg/share"
	"github.com/royvandam/pigeon/pkg/storage"
)

func (cfg config) handleShares(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if !allowMethods(w, r, http.MethodGet, http.MethodPost) {
		return
	}
	a, ok := cfg.requireAuth(w, r, authOptions{})
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
	access, err := cfg.accessAccount(r.Context(), a.User)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	defer access.close()
	link, active := "", false
	if r.Method == http.MethodPost && r.PostForm.Get("action") == "revoke" {
		// Revoking does not require the item to still exist on disk.
		err = cfg.store.shares.Revoke(r.Context(), a.User.ID, name)
	} else {
		tree, openErr := access.Files.open(r.Context())
		if openErr != nil {
			writeStorageError(w, openErr)
			return
		}
		defer tree.Close()
		info, statErr := tree.Stat(r.Context(), name)
		if statErr != nil {
			writeStorageError(w, statErr)
			return
		}
		if r.Method == http.MethodGet {
			active, err = cfg.store.shares.Active(r.Context(), a.User, name)
		} else if r.PostForm.Get("action") == "create" {
			var secret string
			secret, err = cfg.store.shares.Create(r.Context(), a.User, name, info.IsDir())
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
			http.Error(w, "failed to update share link", http.StatusInternalServerError)
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

func (cfg config) openSharedDownload(ctx context.Context, token string) (*storedDownload, error) {
	link, err := cfg.store.shares.Find(ctx, token)
	if err != nil {
		return nil, err
	}
	unlock, err := cfg.locks.Lock(ctx, link.User.ID)
	if err != nil {
		return nil, err
	}
	defer unlock()
	// A revoke or file mutation may have happened while waiting for an upload.
	link, err = cfg.store.shares.Find(ctx, token)
	if err != nil {
		return nil, err
	}
	if err := storage.ValidatePath(link.Path); err != nil {
		return nil, share.ErrNotFound
	}
	tree, err := cfg.filesFor(link.User).open(ctx)
	if err != nil {
		return nil, share.ErrNotFound
	}
	defer tree.Close()
	download, err := openStoredDownload(ctx, tree, link.Path, link.User.Slug)
	if err != nil {
		return nil, share.ErrNotFound
	}
	if (download.folder != nil) != link.Directory {
		download.Close()
		return nil, share.ErrNotFound
	}
	// The file handle/subdirectory root remains bound to this item even if it
	// moves later. Release the lock before streaming a potentially large file.
	return download, nil
}

func (cfg config) handleSharedDownload(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
	if !allowMethods(w, r, http.MethodGet, http.MethodHead) {
		return
	}
	download, err := cfg.openSharedDownload(r.Context(), r.PathValue("token"))
	if errors.Is(err, share.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		log.Printf("shared download unavailable: %v", err)
		status := http.StatusInternalServerError
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			status = http.StatusRequestTimeout
		}
		http.Error(w, "download unavailable", status)
		return
	}
	defer download.Close()
	download.ServeHTTP(w, r)
}
