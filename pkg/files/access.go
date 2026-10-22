package files

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/royvandam/pigeon/pkg/account"
	"github.com/royvandam/pigeon/pkg/quota"
	"github.com/royvandam/pigeon/pkg/storage"
)

// Access holds one account's lock and quota while a request reads or changes
// its files. Paths are relative to the account's top folder, which is "".
type Access struct {
	service *Service
	user    account.User
	quota   int64
	tree    storage.Tree
	release func()
}

// open opens the account's files on first use, creating its namespace if it
// has none yet.
func (a *Access) open(ctx context.Context) (storage.Tree, error) {
	if a.tree == nil {
		tree, err := a.service.storage.Create(ctx, a.user.Slug)
		if err != nil {
			return nil, err
		}
		a.tree = tree
	}
	return a.tree, nil
}

// Close releases the account. Calling it again does nothing, so error
// handling and deferred cleanup can both close an access.
func (a *Access) Close() {
	if a.tree != nil {
		a.tree.Close()
		a.tree = nil
	}
	if a.release != nil {
		a.release()
		a.release = nil
	}
}

// Usage returns the account's quota and usage, measuring its files only when
// no value is cached. Its error wraps ErrUsageUnavailable.
func (a *Access) Usage(ctx context.Context) (Usage, error) {
	used, err := a.service.usage.Usage(ctx, a.user.ID, a.service.measure(a.user.Slug))
	if err != nil {
		return Usage{}, fmt.Errorf("%w: %w", ErrUsageUnavailable, err)
	}
	return Usage{Quota: a.quota, Used: used}, nil
}

// changed records delta bytes of usage added, or removed when negative.
func (a *Access) changed(delta int64) {
	a.service.usage.Add(a.user.ID, delta)
}

// forgetUsage drops the cached usage after a change that may have partly
// failed, so the next request measures the account again.
func (a *Access) forgetUsage() {
	a.service.usage.Forget(a.user.ID)
}

// requireQuota reports quota.ErrExceeded unless n more bytes fit the quota.
func (a *Access) requireQuota(ctx context.Context, n int64) error {
	usage, err := a.Usage(ctx)
	if err != nil {
		return err
	}
	if usage.Remaining() < n {
		return quota.ErrExceeded
	}
	return nil
}

// Stat describes the item at name, or the top folder for "".
func (a *Access) Stat(ctx context.Context, name string) (fs.FileInfo, error) {
	tree, err := a.open(ctx)
	if err != nil {
		return nil, err
	}
	return tree.Stat(ctx, name)
}

// RequireDirectory reports an error unless dir is a folder.
func (a *Access) RequireDirectory(ctx context.Context, dir string) error {
	tree, err := a.open(ctx)
	if err != nil {
		return err
	}
	return storage.RequireDirectory(ctx, tree, dir)
}

// List describes the items in folder dir.
func (a *Access) List(ctx context.Context, dir string) ([]fs.FileInfo, error) {
	tree, err := a.open(ctx)
	if err != nil {
		return nil, err
	}
	return tree.List(ctx, dir)
}

// Mkdir creates folder name in folder dir. A folder uses disk space and an
// inode, so it is charged like a file.
func (a *Access) Mkdir(ctx context.Context, dir, name string) error {
	tree, err := a.open(ctx)
	if err != nil {
		return err
	}
	if err := storage.RequireDirectory(ctx, tree, dir); err != nil {
		return err
	}
	unit := a.service.unit
	if err := a.requireQuota(ctx, unit); err != nil {
		return err
	}
	if err := validateEntryName(name); err != nil {
		return err
	}
	if err := tree.Mkdir(ctx, path.Join(dir, name)); err != nil {
		return err
	}
	a.changed(unit)
	return nil
}

// Rename gives the item at source a new name in the same folder.
func (a *Access) Rename(ctx context.Context, source, name string) error {
	return a.relocate(ctx, source, func(storage.Tree) (string, error) {
		if err := validateEntryName(name); err != nil {
			return "", err
		}
		return path.Join(path.Dir(source), name), nil
	})
}

// Move moves the item at source into folder destination.
func (a *Access) Move(ctx context.Context, source, destination string) error {
	return a.relocate(ctx, source, func(tree storage.Tree) (string, error) {
		if err := storage.RequireDirectory(ctx, tree, destination); err != nil {
			return "", err
		}
		return path.Join(destination, path.Base(source)), nil
	})
}

// relocate renames the item at source to the path that target returns, which
// may also reject the change. Renaming an item to itself changes nothing.
func (a *Access) relocate(ctx context.Context, source string, target func(storage.Tree) (string, error)) error {
	tree, err := a.open(ctx)
	if err != nil {
		return err
	}
	if err := storage.ValidatePath(source); err != nil {
		return err // Protects the top folder.
	}
	info, err := tree.Stat(ctx, source)
	if err != nil {
		return err
	}
	destination, err := target(tree)
	if err != nil {
		return err
	}
	if err := storage.ValidatePath(destination); err != nil {
		return err
	}
	if source == destination {
		return nil
	}
	if info.IsDir() && strings.HasPrefix(destination, source+"/") {
		return fmt.Errorf("%w: a folder cannot be moved inside itself", storage.ErrInvalidPath)
	}
	if _, err := tree.Stat(ctx, destination); err == nil {
		return fmt.Errorf("%w: %q", storage.ErrConflict, destination)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	// Revoke only after validation, but before changing the path. A failed
	// change may disable a link; a reused path must never revive it.
	err = a.service.shares.RevokePath(ctx, a.user.ID, source)
	if err == nil {
		err = tree.Rename(ctx, source, destination)
	}
	if err != nil {
		a.forgetUsage()
	}
	return err
}

// Delete removes the item at source and everything in it, and the share links
// below it.
func (a *Access) Delete(ctx context.Context, source string) error {
	tree, err := a.open(ctx)
	if err != nil {
		return err
	}
	if err := storage.ValidatePath(source); err != nil {
		return err // Protects the top folder from recursive deletion.
	}
	if _, err := tree.Stat(ctx, source); err != nil {
		return err
	}
	// Measure what the deletion frees while the item still exists.
	removed, err := quota.Measure(ctx, tree, source, a.service.unit)
	if err == nil {
		err = a.service.shares.RevokePath(ctx, a.user.ID, source)
	}
	if err == nil {
		err = tree.RemoveAll(ctx, source)
	}
	if err != nil {
		a.forgetUsage() // A failed deletion may have removed part of a folder.
		return err
	}
	a.changed(-removed)
	return nil
}
