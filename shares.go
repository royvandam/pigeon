package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"
)

var errShareNotFound = errors.New("share link not found")

type shareLink struct {
	User      user
	Path      string
	Directory bool
}

func (s *store) shareURL(ctx context.Context, u user, name string) (string, error) {
	var token string
	err := s.db.QueryRowContext(ctx, `
SELECT s.token FROM shares s JOIN users u ON u.id = s.user_id
WHERE s.user_id = ? AND s.path = ? AND u.auth_version = ? AND u.deleted_at IS NULL`,
		u.ID, name, u.Version).Scan(&token)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return "/s/" + token, nil
}

func (s *store) createShare(ctx context.Context, u user, name string, directory bool) (string, error) {
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO shares (token, user_id, path, is_directory, created_at)
SELECT ?, id, ?, ?, ? FROM users WHERE id = ? AND auth_version = ? AND deleted_at IS NULL
ON CONFLICT(user_id, path) DO NOTHING`, token, name, directory, time.Now().Unix(), u.ID, u.Version)
	if err != nil {
		return "", err
	}
	link, err := s.shareURL(ctx, u, name)
	if err == nil && link == "" {
		err = errInvalidCredentials
	}
	return link, err
}

func (s *store) revokeShare(ctx context.Context, id int64, name string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM shares WHERE user_id = ? AND path = ?`, id, name)
	return err
}

func (s *store) revokePathShares(ctx context.Context, id int64, name string) error {
	// Compare an exact path prefix; filenames may contain SQL LIKE wildcards.
	_, err := s.db.ExecContext(ctx, `
DELETE FROM shares WHERE user_id = ? AND (path = ? OR substr(path, 1, length(?) + 1) = ? || '/')`,
		id, name, name, name)
	return err
}

func (s *store) findShare(ctx context.Context, token string) (shareLink, error) {
	if len(token) != 43 {
		return shareLink{}, errShareNotFound
	}
	bytes, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(bytes) != 32 {
		return shareLink{}, errShareNotFound
	}
	var link shareLink
	err = s.db.QueryRowContext(ctx, `
SELECT u.id, u.slug, s.path, s.is_directory FROM shares s JOIN users u ON u.id = s.user_id
WHERE s.token = ? AND u.deleted_at IS NULL`, token).
		Scan(&link.User.ID, &link.User.Slug, &link.Path, &link.Directory)
	if errors.Is(err, sql.ErrNoRows) {
		return shareLink{}, errShareNotFound
	}
	return link, err
}

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
	if err := validateStoragePath(name, false); err != nil {
		writeStorageError(w, err)
		return
	}
	access, err := cfg.accessAccount(r.Context(), a.User)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	defer access.close()
	link := ""
	if r.Method == http.MethodPost && r.PostForm.Get("action") == "revoke" {
		// Revoking does not require the item to still exist on disk.
		err = cfg.store.revokeShare(r.Context(), a.User.ID, name)
	} else {
		root, openErr := access.Files.open()
		if openErr != nil {
			writeStorageError(w, openErr)
			return
		}
		defer root.Close()
		info, statErr := storageInfo(root, name)
		if statErr != nil {
			writeStorageError(w, statErr)
			return
		}
		if r.Method == http.MethodGet {
			link, err = cfg.store.shareURL(r.Context(), a.User, name)
		} else if r.PostForm.Get("action") == "create" {
			link, err = cfg.store.createShare(r.Context(), a.User, name, info.IsDir())
		} else {
			http.Error(w, "unknown share action", http.StatusBadRequest)
			return
		}
	}
	if err != nil {
		if errors.Is(err, errInvalidCredentials) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		} else {
			http.Error(w, "failed to update share link", http.StatusInternalServerError)
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(struct {
		URL string `json:"url"`
	}{URL: link})
}

func (cfg config) openSharedDownload(ctx context.Context, token string) (*storedDownload, error) {
	link, err := cfg.store.findShare(ctx, token)
	if err != nil {
		return nil, err
	}
	unlock, err := cfg.store.lockUploads(ctx, link.User.ID)
	if err != nil {
		return nil, err
	}
	defer unlock()
	// A revoke or file mutation may have happened while waiting for an upload.
	link, err = cfg.store.findShare(ctx, token)
	if err != nil {
		return nil, err
	}
	if err := validateStoragePath(link.Path, false); err != nil {
		return nil, errShareNotFound
	}
	root, err := cfg.filesFor(link.User).open()
	if err != nil {
		return nil, errShareNotFound
	}
	defer root.Close()
	download, err := openStoredDownload(root, link.Path, link.User.Slug)
	if err != nil {
		return nil, errShareNotFound
	}
	if (download.folder != nil) != link.Directory {
		download.Close()
		return nil, errShareNotFound
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
	if errors.Is(err, errShareNotFound) {
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
