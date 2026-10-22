package local

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"

	"github.com/royvandam/pigeon/pkg/storage"
)

// upload writes into a private staging folder of the tree. Its root confines
// the staged files to that folder.
type upload struct {
	tree      *tree
	stage     *os.Root
	stageName string
}

func (u *upload) Create(name string) (io.WriteCloser, error) {
	if err := storage.ValidatePath(name); err != nil {
		return nil, err
	}
	native := filepath.FromSlash(name)
	if err := u.stage.MkdirAll(filepath.Dir(native), 0o750); err != nil {
		return nil, err
	}
	return u.stage.OpenFile(native, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o640)
}

func (u *upload) Publish(ctx context.Context, dir string, names []string) error {
	// Preflight every destination before changing anything, and roll back on
	// publication errors.
	if err := storage.RequireDirectory(ctx, u.tree, dir); err != nil {
		return err
	}
	for _, name := range names {
		destination := path.Join(dir, name)
		if _, err := u.tree.stat(destination); !errors.Is(err, fs.ErrNotExist) {
			if err == nil {
				err = fmt.Errorf("%w: %q", storage.ErrConflict, destination)
			}
			return err
		}
	}
	root := u.tree.root
	var createdDirs, published []string
	rollback := func() {
		for i := len(published) - 1; i >= 0; i-- {
			root.Remove(filepath.FromSlash(published[i]))
		}
		for i := len(createdDirs) - 1; i >= 0; i-- {
			root.Remove(filepath.FromSlash(createdDirs[i]))
		}
	}
	for _, name := range names {
		destination := path.Join(dir, name)
		var missing []string
		for parent := path.Dir(destination); parent != "."; parent = path.Dir(parent) {
			if _, err := root.Lstat(filepath.FromSlash(parent)); err == nil {
				break
			} else if !errors.Is(err, fs.ErrNotExist) {
				rollback()
				return err
			}
			missing = append(missing, parent)
		}
		for i := len(missing) - 1; i >= 0; i-- {
			if err := root.Mkdir(filepath.FromSlash(missing[i]), 0o750); err != nil {
				rollback()
				return err
			}
			createdDirs = append(createdDirs, missing[i])
		}
		source := filepath.Join(u.stageName, filepath.FromSlash(name))
		if err := root.Rename(source, filepath.FromSlash(destination)); err != nil {
			rollback()
			return err
		}
		published = append(published, destination)
	}
	return nil
}

func (u *upload) Close() error {
	return errors.Join(u.stage.Close(), u.tree.root.RemoveAll(u.stageName))
}
