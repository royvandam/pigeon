package pigeon

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
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

// userStorage owns the filesystem operations for one account. Database and HTTP
// code pass an identity; they never construct or remove account paths themselves.
type userStorage struct {
	directory string
	slug      string
	unit      int64
}

func (cfg config) filesFor(u user) userStorage {
	unit := cfg.allocationUnit
	if unit <= 0 {
		unit = defaultAllocationUnit
	}
	return userStorage{directory: cfg.dir, slug: u.Slug, unit: unit}
}

func (files userStorage) path() (string, error) {
	if err := validateStoragePath(files.slug, false); err != nil || strings.Contains(files.slug, "/") {
		return "", fmt.Errorf("%w: invalid account folder", errStoragePath)
	}
	return filepath.Join(files.directory, files.slug), nil
}

// open opens the account's folder, creating it on first use.
func (files userStorage) open() (*os.Root, error) {
	root, err := files.openExisting()
	if root != nil || err != nil {
		return root, err
	}
	dir, err := files.path()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	return os.OpenRoot(dir)
}

// openExisting opens the account's folder, or returns nil if it does not exist.
func (files userStorage) openExisting() (*os.Root, error) {
	dir, err := files.path()
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: user storage must be a directory", errStoragePath)
	}
	return os.OpenRoot(dir)
}

// usage measures the account's folder. Callers normally use usageCache instead.
func (files userStorage) usage(ctx context.Context) (int64, error) {
	root, err := files.openExisting()
	if err != nil || root == nil {
		return 0, err
	}
	defer root.Close()
	return measureUsage(ctx, root.FS(), ".", files.unit)
}

func (files userStorage) purge() error {
	if _, err := files.path(); err != nil {
		return err
	}
	root, err := os.OpenRoot(files.directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	// Unlink symbolic links without following their targets, including a link
	// replacing the account's root. Confine removal to the configured uploads root.
	return root.RemoveAll(files.slug)
}

// removeStaleStaging deletes the staging folders of uploads that were cut off
// when the server last stopped, and returns how many it removed. No upload runs
// during startup, so every staging folder is stale. Left in place, they would
// use disk space that their owners cannot see or delete.
func removeStaleStaging(dir string) (int, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return 0, err
	}
	defer root.Close()
	accounts, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return 0, err
	}
	removed := 0
	var errs []error
	for _, account := range accounts {
		if !account.IsDir() { // Also skips symbolic links.
			continue
		}
		entries, err := fs.ReadDir(root.FS(), account.Name())
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, entry := range entries {
			if !strings.HasPrefix(entry.Name(), stagingPrefix) {
				continue
			}
			if err := root.RemoveAll(filepath.Join(account.Name(), entry.Name())); err != nil {
				errs = append(errs, err)
				continue
			}
			removed++
		}
	}
	return removed, errors.Join(errs...)
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
