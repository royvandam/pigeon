package files

import (
	"archive/zip"
	"context"
	"io"
	"io/fs"
	"path"
	"time"

	"github.com/royvandam/pigeon/pkg/share"
	"github.com/royvandam/pigeon/pkg/storage"
)

// Download is an open file or folder. It holds its own handle, which stays
// bound to the item even if it moves later, so it can be streamed after the
// account's lock is released.
type Download struct {
	file   storage.File
	folder storage.Tree
	info   fs.FileInfo
	name   string
}

// OpenDownload opens the file or folder at name for downloading. The download
// stays usable after the access is closed.
func (a *Access) OpenDownload(ctx context.Context, name string) (*Download, error) {
	tree, err := a.open(ctx)
	if err != nil {
		return nil, err
	}
	return openDownload(ctx, tree, name, a.user.Slug)
}

// openDownload opens name in tree. topName names a download of the top
// folder.
func openDownload(ctx context.Context, tree storage.Tree, name, topName string) (*Download, error) {
	info, err := tree.Stat(ctx, name)
	if err != nil {
		return nil, err
	}
	d := &Download{info: info, name: info.Name()}
	if info.IsDir() {
		folder, err := tree.Sub(ctx, name)
		if err != nil {
			return nil, err
		}
		d.folder = folder
		d.name = path.Base(name)
		if name == "" {
			d.name = topName
		}
		return d, nil
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
	d.file, d.info = file, info
	return d, nil
}

// OpenShared opens the item that the share link with token secret points to.
// Its error wraps share.ErrNotFound when the link or its item is gone, or the
// item changed from a file to a folder or back.
func (s *Service) OpenShared(ctx context.Context, secret string) (*Download, error) {
	link, err := s.shares.Find(ctx, secret)
	if err != nil {
		return nil, err
	}
	unlock, err := s.locks.Lock(ctx, link.User.ID)
	if err != nil {
		return nil, err
	}
	defer unlock()
	// A revoke or file change may have happened while waiting for an upload.
	link, err = s.shares.Find(ctx, secret)
	if err != nil {
		return nil, err
	}
	if err := storage.ValidatePath(link.Path); err != nil {
		return nil, share.ErrNotFound
	}
	tree, err := s.storage.Create(ctx, link.User.Slug)
	if err != nil {
		return nil, share.ErrNotFound
	}
	defer tree.Close()
	d, err := openDownload(ctx, tree, link.Path, link.User.Slug)
	if err != nil {
		return nil, share.ErrNotFound
	}
	if d.Folder() != link.Directory {
		d.Close()
		return nil, share.ErrNotFound
	}
	// Release the lock before streaming a potentially large file.
	return d, nil
}

// Name is the filename the download suggests, without hidden formatting
// characters, so existing files with such names cannot disguise their type. A
// folder's archive adds .zip.
func (d *Download) Name() string {
	return downloadName(d.name)
}

// Folder reports whether the download is a folder, sent as a ZIP archive.
func (d *Download) Folder() bool {
	return d.folder != nil
}

// ModTime is when the file was last changed.
func (d *Download) ModTime() time.Time {
	return d.info.ModTime()
}

// File returns the contents of a file download.
func (d *Download) File() io.ReadSeeker {
	return d.file
}

// WriteZip writes a folder download as a ZIP archive with the folder's name
// as its top folder.
func (d *Download) WriteZip(ctx context.Context, w io.Writer) error {
	prefix := d.Name()
	archive := zip.NewWriter(w)
	err := d.folder.Walk(ctx, "", func(name string, info fs.FileInfo) error {
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
		file, err := d.folder.Open(ctx, name)
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

// Close releases the download's handle.
func (d *Download) Close() {
	if d.file != nil {
		d.file.Close()
	}
	if d.folder != nil {
		d.folder.Close()
	}
}
