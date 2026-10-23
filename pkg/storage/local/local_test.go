package local

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/royvandam/pigeon/pkg/storage"
)

func newTestProvider(t *testing.T) *Provider {
	t.Helper()
	p, err := NewProvider(filepath.Join(t.TempDir(), "uploads"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func writeFile(t *testing.T, dir, name, contents string) {
	t.Helper()
	filename := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(filename), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(contents), 0o640); err != nil {
		t.Fatal(err)
	}
}

func createTree(t *testing.T, p *Provider, namespace string) storage.Tree {
	t.Helper()
	tree, err := p.Create(context.Background(), namespace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tree.Close() })
	return tree
}

func walked(t *testing.T, tree storage.Tree, dir string) []string {
	t.Helper()
	var names []string
	err := tree.Walk(context.Background(), dir, func(name string, info fs.FileInfo) error {
		names = append(names, name)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return names
}

func TestNamespacesAreConfinedToTheDirectory(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p := newTestProvider(t)
	if info, err := os.Stat(p.Directory()); err != nil || !info.IsDir() || !filepath.IsAbs(p.Directory()) {
		t.Fatalf("provider directory %q was not created: %v", p.Directory(), err)
	}
	for _, namespace := range []string{"", ".", "..", "../outside", "a/b", `a\b`, ".upload-0123"} {
		if _, err := p.Open(ctx, namespace); !errors.Is(err, storage.ErrInvalidPath) {
			t.Errorf("Open(%q) = %v", namespace, err)
		}
		if _, err := p.Create(ctx, namespace); !errors.Is(err, storage.ErrInvalidPath) {
			t.Errorf("Create(%q) = %v", namespace, err)
		}
		if err := p.Remove(ctx, namespace); !errors.Is(err, storage.ErrInvalidPath) {
			t.Errorf("Remove(%q) = %v", namespace, err)
		}
	}
	if _, err := p.Open(ctx, "alice"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("opening a namespace that was never created: %v", err)
	}
	createTree(t, p, "alice").Close()
	tree, err := p.Open(ctx, "alice")
	if err != nil {
		t.Fatalf("opening a created namespace: %v", err)
	}
	tree.Close()
	if err := p.Remove(ctx, "nobody"); err != nil {
		t.Fatalf("removing a missing namespace: %v", err)
	}

	// A namespace replaced by a link is not followed, and removing it leaves
	// the link's target alone.
	writeFile(t, p.Directory(), "bob/kept.txt", "bob's file")
	if err := os.Symlink(filepath.Join(p.Directory(), "bob"), filepath.Join(p.Directory(), "linked")); err != nil {
		t.Fatal(err)
	}
	for _, open := range []func(context.Context, string) (storage.Tree, error){p.Open, p.Create} {
		if _, err := open(ctx, "linked"); !errors.Is(err, storage.ErrInvalidPath) {
			t.Fatalf("opened a linked namespace: %v", err)
		}
	}
	if err := p.Remove(ctx, "linked"); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(p.Directory(), "bob", "kept.txt")); err != nil || string(data) != "bob's file" {
		t.Fatalf("removing a link removed its target: %q, %v", data, err)
	}

	// A directory that cannot be opened fails removal instead of reporting a
	// namespace that is already gone.
	broken := &Provider{directory: filepath.Join(t.TempDir(), "not-a-directory")}
	if err := os.WriteFile(broken.directory, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := broken.Remove(ctx, "alice"); err == nil {
		t.Fatal("removal ignored an unusable storage directory")
	}
}

func TestTreesRejectSymbolicLinksAnywhereInAPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p := newTestProvider(t)
	tree := createTree(t, p, "alice")
	writeFile(t, p.Directory(), "alice/folder/inside.txt", "alice")
	writeFile(t, p.Directory(), "bob/secret.txt", "bob")
	links := map[string]string{
		"outside": filepath.Join(p.Directory(), "bob"),
		"inside":  filepath.Join(p.Directory(), "alice", "folder"),
		"file":    filepath.Join(p.Directory(), "alice", "folder", "inside.txt"),
	}
	for link, target := range links {
		if err := os.Symlink(target, filepath.Join(p.Directory(), "alice", link)); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"outside", "outside/secret.txt", "inside", "inside/inside.txt", "file"} {
		if _, err := tree.Stat(ctx, name); !errors.Is(err, storage.ErrInvalidPath) {
			t.Errorf("Stat(%q) = %v", name, err)
		}
		if _, err := tree.Open(ctx, name); !errors.Is(err, storage.ErrInvalidPath) {
			t.Errorf("Open(%q) = %v", name, err)
		}
		if _, err := tree.List(ctx, name); !errors.Is(err, storage.ErrInvalidPath) {
			t.Errorf("List(%q) = %v", name, err)
		}
		if _, err := tree.Sub(ctx, name); !errors.Is(err, storage.ErrInvalidPath) {
			t.Errorf("Sub(%q) = %v", name, err)
		}
		if err := tree.RemoveAll(ctx, name); !errors.Is(err, storage.ErrInvalidPath) {
			t.Errorf("RemoveAll(%q) = %v", name, err)
		}
		if err := tree.Rename(ctx, name, "renamed"); !errors.Is(err, storage.ErrInvalidPath) {
			t.Errorf("Rename(%q) = %v", name, err)
		}
		if err := tree.Mkdir(ctx, name+"/new"); !errors.Is(err, storage.ErrInvalidPath) {
			t.Errorf("Mkdir(%q/new) = %v", name, err)
		}
	}
	// A folder containing a link can be neither listed nor walked.
	if _, err := tree.List(ctx, ""); !errors.Is(err, storage.ErrInvalidPath) {
		t.Errorf("listing a folder with links: %v", err)
	}
	if err := tree.Walk(ctx, "", func(string, fs.FileInfo) error { return nil }); err == nil {
		t.Error("walked a folder containing symbolic links")
	}
	if data, err := os.ReadFile(filepath.Join(p.Directory(), "bob", "secret.txt")); err != nil || string(data) != "bob" {
		t.Fatalf("another namespace changed: %q, %v", data, err)
	}
}

func TestPathsAreValidatedAndTheTopFolderIsProtected(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p := newTestProvider(t)
	tree := createTree(t, p, "alice")
	writeFile(t, p.Directory(), "alice/file.txt", "data")
	for _, name := range []string{"../escape", "/absolute", "a//b", "./a", "a\\b", "C:drive", "x/.upload-1/y"} {
		if _, err := tree.Stat(ctx, name); !errors.Is(err, storage.ErrInvalidPath) {
			t.Errorf("Stat(%q) = %v", name, err)
		}
	}
	if err := tree.RemoveAll(ctx, ""); !errors.Is(err, storage.ErrInvalidPath) {
		t.Fatalf("removed the top folder: %v", err)
	}
	if err := tree.Rename(ctx, "", "moved"); !errors.Is(err, storage.ErrInvalidPath) {
		t.Fatalf("renamed the top folder: %v", err)
	}
	if info, err := tree.Stat(ctx, ""); err != nil || !info.IsDir() {
		t.Fatalf("top folder: %v, %v", info, err)
	}
	if _, err := tree.Stat(ctx, "file.txt/inner"); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("a file used as a folder: %v", err)
	}
	if _, err := tree.List(ctx, "file.txt"); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("listed a file: %v", err)
	}
	if _, err := tree.Open(ctx, ""); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("opened a folder as a file: %v", err)
	}
}

func TestMkdirAndRenameNeverReplaceItems(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p := newTestProvider(t)
	tree := createTree(t, p, "alice")
	writeFile(t, p.Directory(), "alice/a.txt", "a")
	writeFile(t, p.Directory(), "alice/b.txt", "b")
	if err := os.Mkdir(filepath.Join(p.Directory(), "alice", "empty"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(p.Directory(), "alice", "folder"), 0o750); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.txt", "empty"} {
		if err := tree.Mkdir(ctx, name); !errors.Is(err, storage.ErrConflict) {
			t.Errorf("Mkdir(%q) = %v", name, err)
		}
	}
	// On Linux, renaming a folder over an empty folder would replace it.
	for _, rename := range [][2]string{{"a.txt", "b.txt"}, {"folder", "empty"}, {"a.txt", "empty"}} {
		if err := tree.Rename(ctx, rename[0], rename[1]); !errors.Is(err, storage.ErrConflict) {
			t.Errorf("Rename(%q, %q) = %v", rename[0], rename[1], err)
		}
	}
	if data, err := os.ReadFile(filepath.Join(p.Directory(), "alice", "b.txt")); err != nil || string(data) != "b" {
		t.Fatalf("a rename replaced a file: %q, %v", data, err)
	}
	if err := tree.Mkdir(ctx, "missing/child"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("created a folder without its parent: %v", err)
	}
	if err := tree.Mkdir(ctx, "folder/child"); err != nil {
		t.Fatal(err)
	}
	if err := tree.Rename(ctx, "a.txt", "folder/child/a.txt"); err != nil {
		t.Fatal(err)
	}
	if err := tree.RemoveAll(ctx, "folder"); err != nil {
		t.Fatal(err)
	}
	if got := walked(t, tree, ""); !slices.Equal(got, []string{"", "b.txt", "empty"}) {
		t.Fatalf("tree after changes: %q", got)
	}
}

func TestOpenFilesAndSubtreesStayBoundToTheirItems(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p := newTestProvider(t)
	tree := createTree(t, p, "alice")
	writeFile(t, p.Directory(), "alice/kept.txt", "original")
	writeFile(t, p.Directory(), "alice/project/a.txt", "a")
	file, err := tree.Open(ctx, "kept.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	folder, err := tree.Sub(ctx, "project")
	if err != nil {
		t.Fatal(err)
	}
	defer folder.Close()
	tree.Close() // Opened items outlive the tree they came from.

	dir := filepath.Join(p.Directory(), "alice")
	if err := os.Rename(filepath.Join(dir, "kept.txt"), filepath.Join(dir, "renamed.txt")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, p.Directory(), "alice/kept.txt", "replacement")
	if err := os.Rename(filepath.Join(dir, "project"), filepath.Join(dir, "moved")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, p.Directory(), "alice/project/other.txt", "replacement")

	if data, err := io.ReadAll(file); err != nil || string(data) != "original" {
		t.Fatalf("file followed its old path: %q, %v", data, err)
	}
	if got := walked(t, folder, ""); !slices.Equal(got, []string{"", "a.txt"}) {
		t.Fatalf("folder followed its old path: %q", got)
	}
}

// The provider stages uploads in reserved folders of each namespace. They never
// show, and staging folders left behind by a crash are removed at startup.
func TestStagedUploadsStayHiddenAndStaleOnesAreRemoved(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p := newTestProvider(t)
	for name, contents := range map[string]string{
		"alice/keep.txt":                   "kept",
		"alice/.upload-0123456789ab/a.txt": "unfinished",
		"bob/.upload-ba9876543210/x/y.txt": "unfinished",
	} {
		writeFile(t, p.Directory(), name, contents)
	}
	if err := os.WriteFile(filepath.Join(p.Directory(), "not-a-namespace"), nil, 0o640); err != nil {
		t.Fatal(err)
	}
	tree := createTree(t, p, "alice")
	items, err := tree.List(ctx, "")
	if err != nil || len(items) != 1 || items[0].Name() != "keep.txt" {
		t.Fatalf("listing with a staging folder = %v, %v", items, err)
	}
	if got := walked(t, tree, ""); !slices.Equal(got, []string{"", "keep.txt"}) {
		t.Fatalf("walk with a staging folder = %q", got)
	}
	removed, err := p.RemoveStaleUploads(ctx)
	if err != nil || removed != 2 {
		t.Fatalf("removed %d, %v; want 2", removed, err)
	}
	for _, name := range []string{"alice/.upload-0123456789ab", "bob/.upload-ba9876543210"} {
		if _, err := os.Stat(filepath.Join(p.Directory(), filepath.FromSlash(name))); !os.IsNotExist(err) {
			t.Fatalf("%s remains: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(p.Directory(), "alice", "keep.txt")); err != nil {
		t.Fatalf("a namespace's file was removed: %v", err)
	}
}

func stage(t *testing.T, upload storage.Upload, files map[string]string) []string {
	t.Helper()
	var names []string
	for name, contents := range files {
		out, err := upload.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(out, contents); err != nil {
			t.Fatal(err)
		}
		if err := out.Close(); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func TestUploadsPublishAllFilesOrNone(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p := newTestProvider(t)
	tree := createTree(t, p, "alice")
	writeFile(t, p.Directory(), "alice/destination/project/existing.txt", "keep")
	dir := filepath.Join(p.Directory(), "alice")

	up, err := tree.BeginUpload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := walked(t, tree, ""); slices.ContainsFunc(got, func(name string) bool { return strings.Contains(name, storage.ReservedPrefix) }) {
		t.Fatalf("staging folder is visible: %q", got)
	}
	names := stage(t, up, map[string]string{"project/new/a.txt": "data", "other/b.txt": "data"})
	for _, name := range []string{"project/new/a.txt", "../escape.txt", ".upload-x/a.txt"} {
		if _, err := up.Create(name); err == nil {
			t.Errorf("staged %q twice or outside the upload", name)
		}
	}
	// A conflict found before publishing changes nothing.
	if err := up.Publish(ctx, "destination", append(names, "project/existing.txt")); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("published over an existing file: %v", err)
	}
	if err := up.Publish(ctx, "missing", names); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("published into a missing folder: %v", err)
	}
	// Fail after the first file has been published and a second parent created.
	if err := up.(*upload).stage.Remove(filepath.FromSlash("other/b.txt")); err != nil {
		t.Fatal(err)
	}
	if err := up.Publish(ctx, "destination", []string{"project/new/a.txt", "other/b.txt"}); err == nil {
		t.Fatal("publication succeeded with a missing staged file")
	}
	for _, name := range []string{"destination/project/new", "destination/other"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(name))); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("rollback left %s behind: %v", name, err)
		}
	}
	if got := walked(t, tree, "destination"); !slices.Equal(got, []string{"destination", "destination/project", "destination/project/existing.txt"}) {
		t.Fatalf("rollback changed existing data: %q", got)
	}
	if err := up.Close(); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 1 {
		t.Fatalf("closing the upload left its staging folder: %v, %v", entries, err)
	}

	up, err = tree.BeginUpload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()
	names = stage(t, up, map[string]string{"project/new/a.txt": "a", "top.txt": "top"})
	if err := up.Publish(ctx, "destination", names); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "destination", "project", "new", "a.txt")); err != nil || string(data) != "a" {
		t.Fatalf("published file: %q, %v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "destination", "top.txt")); err != nil || string(data) != "top" {
		t.Fatalf("published file: %q, %v", data, err)
	}
}

// Only the first element of a namespace path may not look like a Windows
// drive, so a folder must check names as paths in the namespace.
func TestSubtreesAcceptTheNamesOfTheirNamespace(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p := newTestProvider(t)
	tree := createTree(t, p, "alice")
	writeFile(t, p.Directory(), "alice/project/C:notes.txt", "payload")
	writeFile(t, p.Directory(), "alice/project/D:deeper/z.txt", "later file")
	if _, err := tree.Stat(ctx, "project/C:notes.txt"); err != nil {
		t.Fatalf("namespace path: %v", err)
	}
	project, err := tree.Sub(ctx, "project")
	if err != nil {
		t.Fatal(err)
	}
	defer project.Close()
	if got := walked(t, project, ""); !slices.Equal(got, []string{"", "C:notes.txt", "D:deeper", "D:deeper/z.txt"}) {
		t.Fatalf("walk = %q", got)
	}
	file, err := project.Open(ctx, "C:notes.txt")
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(file)
	file.Close()
	if err != nil || string(data) != "payload" {
		t.Fatalf("contents = %q, %v", data, err)
	}
	deeper, err := project.Sub(ctx, "D:deeper")
	if err != nil {
		t.Fatal(err)
	}
	defer deeper.Close()
	if _, err := deeper.Stat(ctx, "z.txt"); err != nil {
		t.Fatalf("nested folder: %v", err)
	}
	if err := project.Mkdir(ctx, "E:new"); err != nil {
		t.Fatalf("new folder in a subtree: %v", err)
	}
	if err := project.Rename(ctx, "E:new", "F:renamed"); err != nil {
		t.Fatalf("rename in a subtree: %v", err)
	}
	if err := project.RemoveAll(ctx, "F:renamed"); err != nil {
		t.Fatalf("removal in a subtree: %v", err)
	}
	// The namespace rules still apply in full: at the top of the namespace,
	// and to every element in a subtree.
	if err := tree.Mkdir(ctx, "C:top"); !errors.Is(err, storage.ErrInvalidPath) {
		t.Fatalf("a drive-like name at the top: %v", err)
	}
	for _, name := range []string{"../escape", "./a", "a//b", ".upload-1", "x/.upload-1"} {
		if _, err := project.Stat(ctx, name); !errors.Is(err, storage.ErrInvalidPath) {
			t.Errorf("Stat(%q) in a subtree = %v", name, err)
		}
	}
	for _, remove := range []func() error{
		func() error { return project.RemoveAll(ctx, "") },
		func() error { return project.Rename(ctx, "", "moved") },
		func() error { return project.Mkdir(ctx, "") },
	} {
		if err := remove(); !errors.Is(err, storage.ErrInvalidPath) {
			t.Errorf("changed the top of a subtree: %v", err)
		}
	}
}
