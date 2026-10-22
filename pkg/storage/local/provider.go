// Package local stores account files in folders on the local filesystem.
//
// Each namespace is a folder directly below the provider's directory. Every
// operation goes through an os.Root, which confines it to the namespace, and
// symbolic links are rejected anywhere in a path, including links that point
// elsewhere inside the same namespace. Uploads are staged in a reserved folder
// inside the namespace and published by renaming, so a batch appears at once.
package local

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/royvandam/pigeon/pkg/storage"
)

// Provider stores each namespace in a folder of its directory.
type Provider struct {
	directory string
}

var _ storage.Provider = (*Provider)(nil)

// NewProvider stores namespaces in directory, creating it if needed. A
// relative directory is resolved from the current working directory once, so
// later changes of working directory do not move the files.
func NewProvider(directory string) (*Provider, error) {
	if directory == "" {
		return nil, errors.New("storage directory cannot be empty")
	}
	directory, err := filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return nil, err
	}
	return &Provider{directory: directory}, nil
}

// Directory returns the absolute path of the folder holding the namespaces.
func (p *Provider) Directory() string {
	return p.directory
}

func (p *Provider) path(namespace string) (string, error) {
	if err := storage.ValidatePath(namespace); err != nil || strings.Contains(namespace, "/") {
		return "", fmt.Errorf("%w: invalid account folder", storage.ErrInvalidPath)
	}
	return filepath.Join(p.directory, namespace), nil
}

func (p *Provider) Open(ctx context.Context, namespace string) (storage.Tree, error) {
	dir, err := p.path(namespace)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: user storage must be a directory", storage.ErrInvalidPath)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	return &tree{root: root}, nil
}

func (p *Provider) Create(ctx context.Context, namespace string) (storage.Tree, error) {
	t, err := p.Open(ctx, namespace)
	if !errors.Is(err, fs.ErrNotExist) {
		return t, err
	}
	dir, err := p.path(namespace)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	return &tree{root: root}, nil
}

func (p *Provider) Remove(ctx context.Context, namespace string) error {
	if _, err := p.path(namespace); err != nil {
		return err
	}
	root, err := os.OpenRoot(p.directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	// Unlink symbolic links without following their targets, including a link
	// replacing the namespace's folder. Confine removal to the directory.
	return root.RemoveAll(namespace)
}

// RemoveStaleUploads deletes the staging folders of uploads that were cut off
// when the server last stopped. Left in place, they would use disk space that
// their owners cannot see or delete.
func (p *Provider) RemoveStaleUploads(ctx context.Context) (int, error) {
	root, err := os.OpenRoot(p.directory)
	if err != nil {
		return 0, err
	}
	defer root.Close()
	namespaces, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return 0, err
	}
	removed := 0
	var errs []error
	for _, namespace := range namespaces {
		if !namespace.IsDir() { // Also skips symbolic links.
			continue
		}
		entries, err := fs.ReadDir(root.FS(), namespace.Name())
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, entry := range entries {
			if !strings.HasPrefix(entry.Name(), storage.ReservedPrefix) {
				continue
			}
			if err := root.RemoveAll(filepath.Join(namespace.Name(), entry.Name())); err != nil {
				errs = append(errs, err)
				continue
			}
			removed++
		}
	}
	return removed, errors.Join(errs...)
}

func randomHex(nbytes int) string {
	buf := make([]byte, nbytes)
	rand.Read(buf) // Never fails, see crypto/rand.Read.
	return hex.EncodeToString(buf)
}
