package local

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/royvandam/pigeon/pkg/storage"
)

// tree is a namespace, or a folder in one, opened through an os.Root. base is
// the folder's path in its namespace, or "" for the namespace itself.
type tree struct {
	root *os.Root
	base string
}

// namespacePath returns the path that name has in the namespace. It joins
// without cleaning, so validation still sees every element of name.
func (t *tree) namespacePath(name string) string {
	switch {
	case t.base == "":
		return name
	case name == "":
		return t.base
	default:
		return t.base + "/" + name
	}
}

// validate checks name as the path it has in the namespace. Some rules of
// storage.ValidatePath depend on where a path starts, such as the one against
// Windows drive letters, so checking name on its own would reject items in a
// folder that the namespace accepts. The top folder, "", is never valid.
func (t *tree) validate(name string) error {
	if name == "" {
		return storage.ValidatePath(name)
	}
	return storage.ValidatePath(t.namespacePath(name))
}

func native(name string) string {
	if name == "" {
		return "."
	}
	return filepath.FromSlash(name)
}

// stat describes name, or the top folder for "". The root confines every
// operation to the tree; also reject symbolic links in every component,
// including links pointing elsewhere inside the tree.
func (t *tree) stat(name string) (fs.FileInfo, error) {
	if name == "" {
		return t.root.Stat(".")
	}
	if err := t.validate(name); err != nil {
		return nil, err
	}
	var info fs.FileInfo
	prefix := ""
	parts := strings.Split(name, "/")
	for i, component := range parts {
		prefix = path.Join(prefix, component)
		var err error
		info, err = t.root.Lstat(filepath.FromSlash(prefix))
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return nil, fmt.Errorf("%w: symbolic links and special files are not supported", storage.ErrInvalidPath)
		}
		if i < len(parts)-1 && !info.IsDir() {
			return nil, fmt.Errorf("%w: %q is a file", storage.ErrConflict, prefix)
		}
	}
	return info, nil
}

// requireAbsent reports ErrConflict if an item exists at name.
func (t *tree) requireAbsent(name string) error {
	if _, err := t.stat(name); err == nil {
		return fmt.Errorf("%w: %q", storage.ErrConflict, name)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func (t *tree) Stat(ctx context.Context, name string) (fs.FileInfo, error) {
	return t.stat(name)
}

func (t *tree) List(ctx context.Context, dir string) ([]fs.FileInfo, error) {
	if err := storage.RequireDirectory(ctx, t, dir); err != nil {
		return nil, err
	}
	entries, err := fs.ReadDir(t.root.FS(), path.Clean("./"+dir))
	if err != nil {
		return nil, err
	}
	var items []fs.FileInfo
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), storage.ReservedPrefix) {
			continue
		}
		info, err := t.stat(path.Join(dir, entry.Name()))
		if errors.Is(err, fs.ErrNotExist) { // Another request may have moved it.
			continue
		}
		if err != nil {
			return nil, err
		}
		items = append(items, info)
	}
	return items, nil
}

func (t *tree) Walk(ctx context.Context, dir string, fn func(string, fs.FileInfo) error) error {
	if _, err := t.stat(dir); err != nil {
		return err
	}
	return fs.WalkDir(t.root.FS(), path.Clean("./"+dir), func(name string, entry fs.DirEntry, err error) error {
		// An item removed during the walk, such as by an operator, is skipped.
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if strings.HasPrefix(entry.Name(), storage.ReservedPrefix) {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%q is a symbolic link, which is not supported", name)
		}
		info, err := entry.Info()
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("%q is a special file, which is not supported", name)
		}
		if name == "." {
			name = ""
		}
		return fn(name, info)
	})
}

func (t *tree) Open(ctx context.Context, name string) (storage.File, error) {
	info, err := t.stat(name)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%w: %q is a folder", storage.ErrConflict, name)
	}
	file, err := t.root.Open(filepath.FromSlash(name))
	if err != nil {
		return nil, err
	}
	// The path may have been replaced since it was checked.
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		if err == nil {
			err = fmt.Errorf("%w: special files are not supported", storage.ErrInvalidPath)
		}
		return nil, err
	}
	return file, nil
}

func (t *tree) Sub(ctx context.Context, dir string) (storage.Tree, error) {
	if err := storage.RequireDirectory(ctx, t, dir); err != nil {
		return nil, err
	}
	root, err := t.root.OpenRoot(native(dir))
	if err != nil {
		return nil, err
	}
	return &tree{root: root, base: t.namespacePath(dir)}, nil
}

func (t *tree) Mkdir(ctx context.Context, name string) error {
	if err := t.validate(name); err != nil {
		return err
	}
	if err := t.requireAbsent(name); err != nil {
		return err
	}
	return t.root.Mkdir(filepath.FromSlash(name), 0o750)
}

func (t *tree) Rename(ctx context.Context, oldName, newName string) error {
	for _, name := range []string{oldName, newName} {
		if err := t.validate(name); err != nil {
			return err
		}
	}
	if _, err := t.stat(oldName); err != nil {
		return err
	}
	if err := t.requireAbsent(newName); err != nil {
		return err
	}
	return t.root.Rename(filepath.FromSlash(oldName), filepath.FromSlash(newName))
}

func (t *tree) RemoveAll(ctx context.Context, name string) error {
	if err := t.validate(name); err != nil {
		return err // Protects the top folder from recursive deletion.
	}
	if _, err := t.stat(name); err != nil {
		return err
	}
	return t.root.RemoveAll(filepath.FromSlash(name))
}

func (t *tree) BeginUpload(ctx context.Context) (storage.Upload, error) {
	name := storage.ReservedPrefix + randomHex(12)
	if err := t.root.Mkdir(name, 0o750); err != nil {
		return nil, err
	}
	stage, err := t.root.OpenRoot(name)
	if err != nil {
		t.root.RemoveAll(name)
		return nil, err
	}
	return &upload{tree: t, stage: stage, stageName: name}, nil
}

func (t *tree) Close() error {
	return t.root.Close()
}
