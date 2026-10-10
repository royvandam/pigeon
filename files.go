package main

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

type fileEntry struct {
	Name        string
	Path        string
	Directory   bool
	Size        string
	Modified    string
	BrowseURL   string
	DownloadURL string
}

type breadcrumb struct {
	Name string
	URL  string
}

type explorerData struct {
	Directory   string
	Entries     []fileEntry
	Breadcrumbs []breadcrumb
	CSRFToken   string
}

func explorerURL(directory string) string {
	if directory == "" {
		return "/"
	}
	return "/?" + url.Values{"dir": {directory}}.Encode()
}

func readExplorer(root *os.Root, directory, csrf string) (explorerData, error) {
	data := explorerData{Directory: directory, CSRFToken: csrf}
	if err := requireDirectory(root, directory); err != nil {
		return data, err
	}
	listingPath := directory
	if listingPath == "" {
		listingPath = "."
	}
	entries, err := fs.ReadDir(root.FS(), listingPath)
	if err != nil {
		return data, err
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), stagingPrefix) {
			continue
		}
		name := path.Join(directory, entry.Name())
		info, err := storageInfo(root, name)
		if errors.Is(err, os.ErrNotExist) { // Another request may have moved it.
			continue
		}
		if err != nil {
			return data, err
		}
		size := "—"
		if !info.IsDir() {
			size = humanBytes(info.Size())
		}
		data.Entries = append(data.Entries, fileEntry{
			Name: entry.Name(), Path: name, Directory: info.IsDir(), Size: size,
			Modified:    info.ModTime().Local().Format("2006-01-02 15:04"),
			BrowseURL:   explorerURL(name),
			DownloadURL: "/download?" + url.Values{"path": {name}}.Encode(),
		})
	}
	sort.Slice(data.Entries, func(i, j int) bool {
		a, b := data.Entries[i], data.Entries[j]
		if a.Directory != b.Directory {
			return a.Directory
		}
		if strings.ToLower(a.Name) == strings.ToLower(b.Name) {
			return a.Name < b.Name
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	data.Breadcrumbs = append(data.Breadcrumbs, breadcrumb{Name: "My files", URL: "/"})
	if directory != "" {
		prefix := ""
		for _, component := range strings.Split(directory, "/") {
			prefix = path.Join(prefix, component)
			data.Breadcrumbs = append(data.Breadcrumbs, breadcrumb{Name: component, URL: explorerURL(prefix)})
		}
	}
	return data, nil
}

func (cfg config) handleFiles(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	a, ok := cfg.requireAuth(w, r, false, r.Method == http.MethodPost)
	if !ok {
		return
	}
	if r.Method == http.MethodGet {
		root, err := cfg.openUserRoot(a.User)
		if err != nil {
			message, status := storageError(err)
			http.Error(w, message, status)
			return
		}
		defer root.Close()
		data, err := readExplorer(root, r.URL.Query().Get("dir"), a.CSRFToken)
		if err != nil {
			message, status := storageError(err)
			http.Error(w, message, status)
			return
		}
		renderTemplate(w, filesTmpl, data, http.StatusOK)
		return
	}
	// Serialize file mutations with uploads and quota edits, then recheck the
	// account in case access changed while the request was waiting for the lock.
	unlock, err := cfg.store.lockUploads(r.Context(), a.User.ID)
	if err != nil {
		http.Error(w, "operation canceled", http.StatusRequestTimeout)
		return
	}
	defer unlock()
	if _, err := cfg.store.userQuota(r.Context(), a.User); err != nil {
		message, status := storageError(err)
		http.Error(w, message, status)
		return
	}
	root, err := cfg.openUserRoot(a.User)
	if err != nil {
		message, status := storageError(err)
		http.Error(w, message, status)
		return
	}
	defer root.Close()
	directory := r.PostForm.Get("dir")
	if err := requireDirectory(root, directory); err != nil {
		message, status := storageError(err)
		http.Error(w, message, status)
		return
	}
	action := r.PostForm.Get("action")
	err = manageFile(root, action, directory, r.PostForm.Get("path"), r.PostForm.Get("name"), r.PostForm.Get("destination"))
	if err != nil {
		message, status := storageError(err)
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

func validateEntryName(name string) error {
	if strings.ContainsRune(name, '/') {
		return fmt.Errorf("%w: enter a name without folder separators", errStoragePath)
	}
	return validateStoragePath(name, false)
}

func manageFile(root *os.Root, action, directory, source, name, destination string) error {
	if action == "mkdir" {
		if err := validateEntryName(name); err != nil {
			return err
		}
		target := path.Join(directory, name)
		if err := validateStoragePath(target, false); err != nil {
			return err
		}
		if _, err := storageInfo(root, target); err == nil {
			return fmt.Errorf("%w: %q", errFileConflict, target)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return root.Mkdir(filepath.FromSlash(target), 0o750)
	}
	if action != "delete" && action != "rename" && action != "move" {
		return fmt.Errorf("%w: unknown file action", errStoragePath)
	}
	if err := validateStoragePath(source, false); err != nil {
		return err // Protects the account's root, including recursive deletion.
	}
	info, err := storageInfo(root, source)
	if err != nil {
		return err
	}
	if action == "delete" {
		return root.RemoveAll(filepath.FromSlash(source))
	}
	var target string
	if action == "rename" {
		if err := validateEntryName(name); err != nil {
			return err
		}
		target = path.Join(path.Dir(source), name)
	} else {
		if err := requireDirectory(root, destination); err != nil {
			return err
		}
		target = path.Join(destination, path.Base(source))
	}
	if err := validateStoragePath(target, false); err != nil {
		return err
	}
	if source == target {
		return nil
	}
	if info.IsDir() && strings.HasPrefix(target, source+"/") {
		return fmt.Errorf("%w: a folder cannot be moved inside itself", errStoragePath)
	}
	if _, err := storageInfo(root, target); err == nil {
		return fmt.Errorf("%w: %q", errFileConflict, target)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return root.Rename(filepath.FromSlash(source), filepath.FromSlash(target))
}
