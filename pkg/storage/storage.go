// Package storage defines how Pigeon stores the files of its accounts.
//
// A Provider keeps a separate namespace for each account. An open namespace is
// a Tree of folders and regular files. Paths in a tree are slash-separated and
// relative to its top folder, which is the empty path. Implementations confine
// every operation to the tree it was opened for, and enforce the path rules of
// ValidatePath.
//
// Callers serialize changes to an account's tree; providers need not guard
// against concurrent changes to the same tree.
package storage

import (
	"context"
	"io"
	"io/fs"
)

// Provider stores the trees of every account, each in a namespace named after
// the account.
type Provider interface {
	// Open opens an existing namespace. Its error wraps fs.ErrNotExist when
	// the namespace has never been created.
	Open(ctx context.Context, namespace string) (Tree, error)
	// Create opens a namespace, creating it first if it does not exist.
	Create(ctx context.Context, namespace string) (Tree, error)
	// Remove deletes a namespace and everything in it. Removing a namespace
	// that does not exist succeeds.
	Remove(ctx context.Context, namespace string) error
	// RemoveStaleUploads deletes the staged files of uploads that were cut off
	// when the server last stopped, and returns how many uploads it removed.
	// Call it before serving requests, while no upload can be running.
	RemoveStaleUploads(ctx context.Context) (int, error)
}

// Tree is an open namespace. Methods that take the path of an existing item
// reject symbolic links and other special files anywhere along that path.
// Items whose names start with ReservedPrefix belong to the provider: listings
// and walks leave them out.
type Tree interface {
	// Stat describes the item at name, or the top folder for "".
	Stat(ctx context.Context, name string) (fs.FileInfo, error)
	// List describes the items in folder dir, sorted by name. Items removed
	// while the folder is being listed are left out.
	List(ctx context.Context, dir string) ([]fs.FileInfo, error)
	// Walk calls fn for folder dir and then for every item below it, each
	// folder before its contents. fn receives each item's path in the tree;
	// dir itself is "" when it is the top folder. Items removed during the
	// walk are skipped, and an error from fn stops the walk.
	Walk(ctx context.Context, dir string, fn func(name string, info fs.FileInfo) error) error
	// Open opens the regular file at name for reading.
	Open(ctx context.Context, name string) (File, error)
	// Sub opens folder dir as a tree of its own. The new tree stays bound to
	// the folder until it is closed, even if the folder is renamed, moved, or
	// deleted.
	Sub(ctx context.Context, dir string) (Tree, error)
	// Mkdir creates folder name in an existing parent folder.
	Mkdir(ctx context.Context, name string) error
	// Rename moves the item at oldName to newName, in an existing parent
	// folder. It never replaces an item that exists at newName.
	Rename(ctx context.Context, oldName, newName string) error
	// RemoveAll deletes the item at name and everything in it. The top folder
	// cannot be removed.
	RemoveAll(ctx context.Context, name string) error
	// BeginUpload starts staging files out of sight, to be published into the
	// tree all at once. The tree must stay open until the upload is closed.
	BeginUpload(ctx context.Context) (Upload, error)
	// Close releases the tree. Files and trees opened from it stay usable
	// until they are closed themselves.
	Close() error
}

// File is an open regular file. It stays bound to the file until it is closed,
// even if the file is renamed, moved, or deleted.
type File interface {
	io.ReadSeekCloser
	Stat() (fs.FileInfo, error)
}

// Upload holds files staged for publication into a tree.
type Upload interface {
	// Create creates a staged file. name is the file's path relative to the
	// folder it will be published into; its parent folders are created as
	// needed. Each name can be created only once.
	Create(name string) (io.WriteCloser, error)
	// Publish moves the staged files names into folder dir, creating missing
	// folders on the way. It fails without changing the tree if an item
	// already exists at any destination, and undoes what it can if moving a
	// file fails.
	Publish(ctx context.Context, dir string, names []string) error
	// Close discards every staged file that was not published.
	Close() error
}
