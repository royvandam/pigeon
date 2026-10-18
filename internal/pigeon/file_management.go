package pigeon

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

func validateEntryName(name string) error {
	if strings.ContainsRune(name, '/') {
		return fmt.Errorf("%w: enter a name without folder separators", errStoragePath)
	}
	if err := validateStoragePath(name, false); err != nil {
		return err
	}
	return validateNewName(name)
}

func manageFile(root *os.Root, action, directory, source, name, destination string, beforeMutation func() error) error {
	if action == "mkdir" {
		if err := validateEntryName(name); err != nil {
			return err
		}
		target := path.Join(directory, name)
		if err := validateStoragePath(target, false); err != nil {
			return err
		}
		if _, err := storageInfo(root, target); err == nil {
			return fmt.Errorf("%w: %q", errFileConflict, target)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return root.Mkdir(filepath.FromSlash(target), 0o750)
	}
	if action != "delete" && action != "rename" && action != "move" {
		return fmt.Errorf("%w: unknown file action", errStoragePath)
	}
	if err := validateStoragePath(source, false); err != nil {
		return err // Protects the account's root, including recursive deletion.
	}
	info, err := storageInfo(root, source)
	if err != nil {
		return err
	}
	if action == "delete" {
		if err := beforeMutation(); err != nil {
			return err
		}
		return root.RemoveAll(filepath.FromSlash(source))
	}
	var target string
	if action == "rename" {
		if err := validateEntryName(name); err != nil {
			return err
		}
		target = path.Join(path.Dir(source), name)
	} else {
		if err := requireDirectory(root, destination); err != nil {
			return err
		}
		target = path.Join(destination, path.Base(source))
	}
	if err := validateStoragePath(target, false); err != nil {
		return err
	}
	if source == target {
		return nil
	}
	if info.IsDir() && strings.HasPrefix(target, source+"/") {
		return fmt.Errorf("%w: a folder cannot be moved inside itself", errStoragePath)
	}
	if _, err := storageInfo(root, target); err == nil {
		return fmt.Errorf("%w: %q", errFileConflict, target)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// Revoke only after validation, but before changing the path. A failed
	// filesystem operation may disable a link; a reused path must never revive it.
	if err := beforeMutation(); err != nil {
		return err
	}
	return root.Rename(filepath.FromSlash(source), filepath.FromSlash(target))
}
