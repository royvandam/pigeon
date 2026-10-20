package pigeon

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"strings"
	"sync"
	"time"
)

// Each file and folder is charged at least one allocation unit, rounding file
// sizes up, so a quota bounds disk allocation and the number of entries rather
// than only file contents. 4 KiB matches common filesystem block sizes.
const defaultAllocationUnit = int64(4 << 10)

// chargedSize returns the quota charge for a file of size bytes.
func chargedSize(size, unit int64) int64 {
	if size <= 0 {
		return unit
	}
	units := (size-1)/unit + 1
	if units > math.MaxInt64/unit {
		return math.MaxInt64
	}
	return units * unit
}

// measureUsage returns the quota charge of name and everything below it in
// fsys. The top folder "." is not charged, because it is the account itself.
// Staging folders are skipped: an upload is charged when it is published.
func measureUsage(ctx context.Context, fsys fs.FS, name string, unit int64) (int64, error) {
	var used int64
	err := fs.WalkDir(fsys, name, func(path string, entry fs.DirEntry, err error) error {
		// An entry removed during the walk, such as by an operator, no longer
		// counts.
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
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
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("upload folders cannot contain symbolic links")
		}
		charge := unit
		if entry.IsDir() {
			if path == "." {
				return nil
			}
		} else {
			info, err := entry.Info()
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("upload folders can only contain regular files and directories")
			}
			charge = chargedSize(info.Size(), unit)
		}
		if charge > math.MaxInt64-used {
			return fmt.Errorf("stored file sizes exceed the maximum supported usage")
		}
		used += charge
		return nil
	})
	return used, err
}

// diskUsage measures the folder dir, which is not charged itself.
func diskUsage(ctx context.Context, dir string, unit int64) (int64, error) {
	root, err := os.OpenRoot(dir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer root.Close()
	return measureUsage(ctx, root.FS(), ".", unit)
}

// usageRemeasureAfter bounds how long files changed directly on disk, outside
// Pigeon, take to show in an account's usage.
const usageRemeasureAfter = 10 * time.Minute

// usageCache remembers each account's storage usage, so requests do not walk
// the account's folder every time. Callers hold the account lock while they
// read, fill, or change an account's entry; every change to an account's files
// holds that lock too, so an entry matches the files as changed through Pigeon.
// Entries are measured again after usageRemeasureAfter.
type usageCache struct {
	mu      sync.Mutex
	entries map[int64]usageEntry
	now     func() time.Time
}

type usageEntry struct {
	used     int64
	measured time.Time
}

func newUsageCache() *usageCache {
	return &usageCache{entries: make(map[int64]usageEntry), now: time.Now}
}

// usage returns the cached usage of account id, or measures files and caches
// the result.
func (c *usageCache) usage(ctx context.Context, id int64, files userStorage) (int64, error) {
	c.mu.Lock()
	entry, ok := c.entries[id]
	now := c.now()
	c.mu.Unlock()
	if ok && now.Sub(entry.measured) < usageRemeasureAfter {
		return entry.used, nil
	}
	used, err := files.usage(ctx)
	if err != nil {
		return 0, err
	}
	c.mu.Lock()
	c.entries[id] = usageEntry{used: used, measured: now}
	c.mu.Unlock()
	return used, nil
}

// add adjusts a cached value by delta; an account that was never measured stays
// unmeasured.
func (c *usageCache) add(id, delta int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry, ok := c.entries[id]; ok {
		entry.used = max(0, entry.used+delta)
		c.entries[id] = entry
	}
}

// forget drops a cached value, so the next request measures the folder again.
func (c *usageCache) forget(id int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, id)
}
