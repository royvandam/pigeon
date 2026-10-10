package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
)

// Measure file sizes on disk so existing files, removals, and restarts are
// reflected without a separate database usage counter.
func diskUsage(ctx context.Context, dir string) (int64, error) {
	var used int64
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) && path == dir {
			return nil
		}
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("upload folders cannot contain symbolic links")
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("upload folders can only contain regular files and directories")
		}
		if info.Size() > math.MaxInt64-used {
			return fmt.Errorf("stored file sizes exceed the maximum supported usage")
		}
		used += info.Size()
		return nil
	})
	return used, err
}
