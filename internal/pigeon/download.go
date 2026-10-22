package pigeon

import (
	"archive/zip"
	"context"
	"io"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"path"

	"github.com/royvandam/pigeon/pkg/account"
	"github.com/royvandam/pigeon/pkg/storage"
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

func (cfg config) openPrivateDownload(ctx context.Context, u account.User, name string) (*storedDownload, error) {
	access, err := cfg.accessAccount(ctx, u)
	if err != nil {
		return nil, err
	}
	defer access.close()
	tree, err := access.Files.open(ctx)
	if err != nil {
		return nil, err
	}
	defer tree.Close()
	// The download retains its own handle; release account access before streaming.
	return openStoredDownload(ctx, tree, name, u.Slug)
}

type storedDownload struct {
	file     storage.File
	folder   storage.Tree
	info     fs.FileInfo
	filename string
}

func openStoredDownload(ctx context.Context, tree storage.Tree, name, rootName string) (*storedDownload, error) {
	info, err := tree.Stat(ctx, name)
	if err != nil {
		return nil, err
	}
	download := &storedDownload{info: info, filename: info.Name()}
	if info.IsDir() {
		folder, err := tree.Sub(ctx, name)
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
	file, err := tree.Open(ctx, name)
	if err != nil {
		return nil, err
	}
	info, err = file.Stat()
	if err != nil {
		file.Close()
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

func writeFolderZip(ctx context.Context, w io.Writer, folder storage.Tree, prefix string) error {
	archive := zip.NewWriter(w)
	err := folder.Walk(ctx, "", func(name string, info fs.FileInfo) error {
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
		file, err := folder.Open(ctx, name)
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
