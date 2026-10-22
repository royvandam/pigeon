package pigeon

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/royvandam/pigeon/pkg/storage"
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
	ctx := r.Context()
	access, err := cfg.files.Open(ctx, a.User)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	defer access.Close()
	if r.Method == http.MethodGet {
		data, err := readExplorer(ctx, access, r.URL.Query().Get("dir"), a.CSRFToken)
		if err != nil {
			writeStorageError(w, err)
			return
		}
		renderTemplate(w, filesTmpl, data, http.StatusOK)
		return
	}
	directory := r.PostForm.Get("dir")
	if err := access.RequireDirectory(ctx, directory); err != nil {
		writeStorageError(w, err)
		return
	}
	action := r.PostForm.Get("action")
	source := r.PostForm.Get("path")
	switch action {
	case "mkdir":
		err = access.Mkdir(ctx, directory, r.PostForm.Get("name"))
	case "rename":
		err = access.Rename(ctx, source, r.PostForm.Get("name"))
	case "move":
		err = access.Move(ctx, source, r.PostForm.Get("destination"))
	case "delete":
		err = access.Delete(ctx, source)
	default:
		err = fmt.Errorf("%w: unknown file action", storage.ErrInvalidPath)
	}
	if err != nil {
		message, status := storageError(err)
		access.Close() // Rendering the error page acquires its own read access.
		cfg.renderIndex(w, r, a, directory, "", message, status)
		return
	}
	if err := access.RequireDirectory(ctx, directory); err != nil {
		directory = "" // The API may have moved or removed the viewed folder.
	}
	redirect := explorerURL(directory)
	separator := "?"
	if strings.Contains(redirect, "?") {
		separator = "&"
	}
	http.Redirect(w, r, redirect+separator+"result="+url.QueryEscape(action), http.StatusSeeOther)
}
