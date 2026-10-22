package pigeon

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/royvandam/pigeon/pkg/storage"
)

func validateEntryName(name string) error {
	if strings.ContainsRune(name, '/') {
		return fmt.Errorf("%w: enter a name without folder separators", storage.ErrInvalidPath)
	}
	if err := storage.ValidatePath(name); err != nil {
		return err
	}
	return validateNewName(name)
}

func manageFile(ctx context.Context, tree storage.Tree, action, directory, source, name, destination string, beforeMutation func() error) error {
	if action == "mkdir" {
		if err := validateEntryName(name); err != nil {
			return err
		}
		return tree.Mkdir(ctx, path.Join(directory, name))
	}
	if action != "delete" && action != "rename" && action != "move" {
		return fmt.Errorf("%w: unknown file action", storage.ErrInvalidPath)
	}
	if err := storage.ValidatePath(source); err != nil {
		return err // Protects the account's root, including recursive deletion.
	}
	info, err := tree.Stat(ctx, source)
	if err != nil {
		return err
	}
	if action == "delete" {
		if err := beforeMutation(); err != nil {
			return err
		}
		return tree.RemoveAll(ctx, source)
	}
	var target string
	if action == "rename" {
		if err := validateEntryName(name); err != nil {
			return err
		}
		target = path.Join(path.Dir(source), name)
	} else {
		if err := storage.RequireDirectory(ctx, tree, destination); err != nil {
			return err
		}
		target = path.Join(destination, path.Base(source))
	}
	if err := storage.ValidatePath(target); err != nil {
		return err
	}
	if source == target {
		return nil
	}
	if info.IsDir() && strings.HasPrefix(target, source+"/") {
		return fmt.Errorf("%w: a folder cannot be moved inside itself", storage.ErrInvalidPath)
	}
	if _, err := tree.Stat(ctx, target); err == nil {
		return fmt.Errorf("%w: %q", storage.ErrConflict, target)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	// Revoke only after validation, but before changing the path. A failed
	// filesystem operation may disable a link; a reused path must never revive it.
	if err := beforeMutation(); err != nil {
		return err
	}
	return tree.Rename(ctx, source, target)
}
