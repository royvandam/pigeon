package pigeon

import (
	"context"
	"errors"
	"io/fs"

	"github.com/royvandam/pigeon/pkg/storage"
)

// userStorage is one account's namespace in the storage provider. Database and
// HTTP code pass an identity; they never construct or remove account paths
// themselves.
type userStorage struct {
	provider storage.Provider
	slug     string
	unit     int64
}

func (cfg config) filesFor(u user) userStorage {
	unit := cfg.allocationUnit
	if unit <= 0 {
		unit = defaultAllocationUnit
	}
	return userStorage{provider: cfg.storage, slug: u.Slug, unit: unit}
}

// open opens the account's files, creating its namespace on first use.
func (files userStorage) open(ctx context.Context) (storage.Tree, error) {
	return files.provider.Create(ctx, files.slug)
}

// usage measures the account's files. Callers normally use usageCache instead.
func (files userStorage) usage(ctx context.Context) (int64, error) {
	tree, err := files.provider.Open(ctx, files.slug)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer tree.Close()
	return measureUsage(ctx, tree, "", files.unit)
}

func (files userStorage) purge(ctx context.Context) error {
	return files.provider.Remove(ctx, files.slug)
}
