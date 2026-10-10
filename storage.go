package main

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

var (
	errStoragePath  = errors.New("invalid storage path")
	errFileConflict = errors.New("a file or folder already exists at this path")
)

const stagingPrefix = ".upload-"

func validateStoragePath(name string, allowRoot bool) error {
	if name == "" && allowRoot {
		return nil
	}
	if err := validateUploadPath(name); err != nil {
		return fmt.Errorf("%w: %s", errStoragePath, err)
	}
	for _, component := range strings.Split(name, "/") {
		if strings.HasPrefix(component, stagingPrefix) {
			return fmt.Errorf("%w: this name is reserved", errStoragePath)
		}
	}
	return nil
}

func nativeStoragePath(name string) string {
	if name == "" {
		return "."
	}
	return filepath.FromSlash(name)
}

func (cfg config) openUserRoot(u user) (*os.Root, error) {
	dir := filepath.Join(cfg.dir, u.Slug)
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: user storage must be a directory", errStoragePath)
	}
	return os.OpenRoot(dir)
}

// Root confines operations to one account. Reject symbolic links in every
// component as well, including links pointing elsewhere inside that account.
func storageInfo(root *os.Root, name string) (fs.FileInfo, error) {
	if err := validateStoragePath(name, true); err != nil {
		return nil, err
	}
	if name == "" {
		return root.Stat(".")
	}
	var info fs.FileInfo
	prefix := ""
	parts := strings.Split(name, "/")
	for i, component := range parts {
		prefix = path.Join(prefix, component)
		var err error
		info, err = root.Lstat(filepath.FromSlash(prefix))
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return nil, fmt.Errorf("%w: symbolic links and special files are not supported", errStoragePath)
		}
		if i < len(parts)-1 && !info.IsDir() {
			return nil, fmt.Errorf("%w: %q is a file", errFileConflict, prefix)
		}
	}
	return info, nil
}

func requireDirectory(root *os.Root, name string) error {
	info, err := storageInfo(root, name)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: destination must be a folder", errFileConflict)
	}
	return nil
}

func storageError(err error) (string, int) {
	switch {
	case errors.Is(err, errStoragePath):
		return err.Error(), http.StatusBadRequest
	case errors.Is(err, errFileConflict), errors.Is(err, os.ErrExist):
		return err.Error(), http.StatusConflict
	case errors.Is(err, os.ErrNotExist):
		return "file or folder not found", http.StatusNotFound
	case errors.Is(err, errInvalidCredentials):
		return "unauthorized", http.StatusUnauthorized
	default:
		return "unable to access files", http.StatusInternalServerError
	}
}
