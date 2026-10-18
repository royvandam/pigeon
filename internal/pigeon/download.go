package pigeon

import (
	"archive/zip"
	"context"
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
	if !allowMethods(w, r, http.MethodGet, http.MethodHead) {
		return
	}
	a, ok := cfg.requireAuth(w, r, authOptions{})
	if !ok {
		return
	}
	download, err := cfg.openPrivateDownload(r.Context(), a.User, r.URL.Query().Get("path"))
	if err != nil {
		writeStorageError(w, err)
		return
	}
	defer download.Close()
	download.ServeHTTP(w, r)
}

func (cfg config) openPrivateDownload(ctx context.Context, u user, name string) (*storedDownload, error) {
	access, err := cfg.accessAccount(ctx, u)
	if err != nil {
		return nil, err
	}
	defer access.close()
	root, err := access.Files.open()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	// The download retains its own handle; release account access before streaming.
	return openStoredDownload(root, name, u.Slug)
}

type storedDownload struct {
	file     *os.File
	folder   *os.Root
	info     fs.FileInfo
	filename string
}

func openStoredDownload(root *os.Root, name, rootName string) (*storedDownload, error) {
	info, err := storageInfo(root, name)
	if err != nil {
		return nil, err
	}
	download := &storedDownload{info: info, filename: info.Name()}
	if info.IsDir() {
		folder, err := root.OpenRoot(nativeStoragePath(name))
		if err != nil {
			return nil, err
		}
		download.folder = folder
		download.filename = path.Base(name)
		if name == "" {
			download.filename = rootName
		}
		return download, nil
	}
	file, err := root.Open(filepath.FromSlash(name))
	if err != nil {
		return nil, err
	}
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		if err == nil {
			err = fmt.Errorf("%w: special files cannot be downloaded", errStoragePath)
		}
		return nil, err
	}
	download.file, download.info = file, info
	return download, nil
}

func (d *storedDownload) Close() {
	if d.file != nil {
		d.file.Close()
	}
	if d.folder != nil {
		d.folder.Close()
	}
}

func (d *storedDownload) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if d.folder != nil {
		w.Header().Set("Content-Type", "application/zip")
		name := downloadName(d.filename)
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name + ".zip"}))
		if r.Method != http.MethodHead {
			if err := writeFolderZip(r.Context(), w, d.folder, name); err != nil {
				log.Printf("folder download failed: %v", err)
			}
		}
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": downloadName(d.filename)}))
	http.ServeContent(w, r, d.filename, d.info.ModTime(), d.file)
}

func writeFolderZip(ctx context.Context, w io.Writer, root *os.Root, prefix string) error {
	archive := zip.NewWriter(w)
	err := fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
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
