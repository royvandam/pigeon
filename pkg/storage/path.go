package storage

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"unicode"
)

var (
	// ErrInvalidPath reports a malformed or reserved path, or one that
	// crosses an item the provider does not support, such as a symbolic link.
	ErrInvalidPath = errors.New("invalid storage path")
	// ErrConflict reports a path that is taken by an existing item, or by an
	// item of the wrong kind.
	ErrConflict = errors.New("a file or folder already exists at this path")
)

// ReservedPrefix starts the names that providers use for their own items, such
// as uploads in progress. ValidatePath rejects paths with such a name in them.
const ReservedPrefix = ".upload-"

// ValidatePath checks the path of an item in a tree: a slash-separated,
// relative path without empty, dot, or reserved elements, backslashes, control
// characters, or Windows drive letters. Names keep their spaces and Unicode.
// The top folder, "", is not an item and fails the check. Errors wrap
// ErrInvalidPath.
func ValidatePath(name string) error {
	if err := validateSyntax(name); err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidPath, err)
	}
	for _, component := range strings.Split(name, "/") {
		if strings.HasPrefix(component, ReservedPrefix) {
			return fmt.Errorf("%w: this name is reserved", ErrInvalidPath)
		}
	}
	return nil
}

// Uploads send slash-separated relative paths from browsers. Validate the
// original path before converting it to a native path.
func validateSyntax(name string) error {
	if name == "." || !fs.ValidPath(name) || len(name) > 4095 ||
		strings.ContainsRune(name, '\\') || strings.IndexFunc(name, unicode.IsControl) >= 0 ||
		!filepath.IsLocal(filepath.FromSlash(name)) ||
		(len(name) >= 2 && name[1] == ':' && ((name[0] >= 'A' && name[0] <= 'Z') || (name[0] >= 'a' && name[0] <= 'z'))) {
		return fmt.Errorf("invalid relative file path")
	}
	for _, component := range strings.Split(name, "/") {
		if len(component) > 255 {
			return fmt.Errorf("invalid relative file path")
		}
	}
	return nil
}

// RequireDirectory reports an error unless name is a folder in tree; "" is the
// top folder. The error wraps ErrConflict when name is a file.
func RequireDirectory(ctx context.Context, tree Tree, name string) error {
	info, err := tree.Stat(ctx, name)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: destination must be a folder", ErrConflict)
	}
	return nil
}
