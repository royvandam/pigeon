package pigeon

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
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

// Measure stored entries on disk so existing files, removals, and restarts are
// reflected without a separate database usage counter.
func diskUsage(ctx context.Context, dir string, unit int64) (int64, error) {
	return diskUsageExcept(ctx, dir, unit, "")
}

// diskUsageExcept leaves out the top-level entry skip, such as an upload's own
// staging folder; an empty skip counts everything.
func diskUsageExcept(ctx context.Context, dir string, unit int64, skip string) (int64, error) {
	var used int64
	skipped := filepath.Join(dir, skip)
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		// Uploads stream into their staging folders without the account lock, so
		// a failed upload may remove entries during the walk; they no longer count.
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if skip != "" && path == skipped {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("upload folders cannot contain symbolic links")
		}
		charge := unit
		if entry.IsDir() {
			if path == dir {
				return nil // The account's own folder is not charged.
			}
		} else {
			info, err := entry.Info()
			if errors.Is(err, os.ErrNotExist) {
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
