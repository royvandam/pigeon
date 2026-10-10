package main

import (
	"archive/zip"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

func (cfg config) handleDownload(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	a, ok := cfg.requireAuth(w, r, false, false)
	if !ok {
		return
	}
	root, err := cfg.openUserRoot(a.User)
	if err != nil {
		message, status := storageError(err)
		http.Error(w, message, status)
		return
	}
	defer root.Close()
	name := r.URL.Query().Get("path")
	info, err := storageInfo(root, name)
	if err != nil {
		message, status := storageError(err)
		http.Error(w, message, status)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if info.IsDir() {
		folder, err := root.OpenRoot(nativeStoragePath(name))
		if err != nil {
			message, status := storageError(err)
			http.Error(w, message, status)
			return
		}
		defer folder.Close()
		filename := path.Base(name)
		if name == "" {
			filename = a.User.Slug
		}
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename + ".zip"}))
		if r.Method == http.MethodHead {
			return
		}
		if err := writeFolderZip(w, r, folder, filename); err != nil {
			log.Printf("folder download failed: %v", err)
		}
		return
	}
	file, err := root.Open(filepath.FromSlash(name))
	if err != nil {
		message, status := storageError(err)
		http.Error(w, message, status)
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": info.Name()}))
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
}

func writeFolderZip(w io.Writer, r *http.Request, root *os.Root, prefix string) error {
	archive := zip.NewWriter(w)
	err := fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := r.Context().Err(); err != nil {
			return err
		}
		if strings.HasPrefix(entry.Name(), stagingPrefix) {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symbolic links cannot be downloaded")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("special files cannot be downloaded")
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = path.Join(prefix, name)
		if info.IsDir() {
			header.Name += "/"
		} else {
			header.Method = zip.Deflate
		}
		out, err := archive.CreateHeader(header)
		if err != nil || info.IsDir() {
			return err
		}
		file, err := root.Open(filepath.FromSlash(name))
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	closeErr := archive.Close()
	if err != nil {
		return err
	}
	return closeErr
}
