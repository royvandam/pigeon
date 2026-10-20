package pigeon

import (
	"net/http"
	"net/url"
	"strings"
)

func (cfg config) handleFiles(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !allowMethods(w, r, http.MethodGet, http.MethodPost) {
		return
	}
	a, ok := cfg.requireAuth(w, r, authOptions{Browser: r.Method == http.MethodPost})
	if !ok {
		return
	}
	access, err := cfg.accessAccount(r.Context(), a.User)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	defer access.close()
	if r.Method == http.MethodGet {
		root, err := access.Files.open()
		if err != nil {
			writeStorageError(w, err)
			return
		}
		defer root.Close()
		data, err := readExplorer(root, r.URL.Query().Get("dir"), a.CSRFToken)
		if err != nil {
			writeStorageError(w, err)
			return
		}
		renderTemplate(w, filesTmpl, data, http.StatusOK)
		return
	}
	root, err := access.Files.open()
	if err != nil {
		writeStorageError(w, err)
		return
	}
	defer root.Close()
	directory := r.PostForm.Get("dir")
	if err := requireDirectory(root, directory); err != nil {
		writeStorageError(w, err)
		return
	}
	action := r.PostForm.Get("action")
	source := r.PostForm.Get("path")
	var change int64 // to the account's usage
	mutating := false
	if action == "mkdir" {
		// A folder uses disk space and an inode, so it is charged like a file.
		err = access.requireQuota(r.Context(), access.Files.unit)
		change = access.Files.unit
	}
	if err == nil {
		err = manageFile(root, action, directory, source, r.PostForm.Get("name"), r.PostForm.Get("destination"), func() error {
			mutating = true
			if action == "delete" {
				// Measure what the deletion frees while the item still exists.
				removed, err := measureUsage(r.Context(), root.FS(), source, access.Files.unit)
				if err != nil {
					return err
				}
				change = -removed
			}
			return cfg.store.revokePathShares(r.Context(), a.User.ID, source)
		})
	}
	if err != nil && mutating {
		access.forgetUsage() // A failed deletion may have removed part of a folder.
	}
	if err == nil {
		access.changed(change)
	}
	if err != nil {
		message, status := storageError(err)
		access.close() // Rendering the error page acquires its own read access.
		cfg.renderIndex(w, r, a, directory, "", message, status)
		return
	}
	if err := requireDirectory(root, directory); err != nil {
		directory = "" // The API may have moved or removed the viewed folder.
	}
	redirect := explorerURL(directory)
	separator := "?"
	if strings.Contains(redirect, "?") {
		separator = "&"
	}
	http.Redirect(w, r, redirect+separator+"result="+url.QueryEscape(action), http.StatusSeeOther)
}
