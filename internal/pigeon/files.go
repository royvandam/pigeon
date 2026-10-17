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
	err = manageFile(root, action, directory, source, r.PostForm.Get("name"), r.PostForm.Get("destination"), func() error {
		return cfg.store.revokePathShares(r.Context(), a.User.ID, source)
	})
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
