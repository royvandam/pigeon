package pigeon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"

	"github.com/royvandam/pigeon/pkg/quota"
	"github.com/royvandam/pigeon/pkg/storage"
)

// Stage files out of sight and publish them only after every multipart part has
// been validated.
type uploadBatch struct {
	tree      storage.Tree
	stage     storage.Upload
	paths     map[string]bool // true for files, false for their parent directories
	firstPath string
	files     int
	bytes     int64 // file contents received
	charged   int64 // quota charge for those files and the folders they add
	folders   int64 // folders included in charged
	remaining int64 // quota available before this batch
	unit      int64 // minimum charge per file and folder
	progress  *uploadProgress
	target    string
	names     []string
}

// newUploadBatch stages files for folder target of tree, which stays open until
// the batch is closed.
func newUploadBatch(ctx context.Context, tree storage.Tree, target string, remaining, unit int64) (*uploadBatch, error) {
	if err := storage.RequireDirectory(ctx, tree, target); err != nil {
		return nil, err
	}
	stage, err := tree.BeginUpload(ctx)
	if err != nil {
		return nil, err
	}
	return &uploadBatch{tree: tree, stage: stage, paths: make(map[string]bool), target: target,
		remaining: remaining, unit: unit}, nil
}

func (b *uploadBatch) close() {
	b.stage.Close()
}

func (b *uploadBatch) checkPath(ctx context.Context, name string) error {
	if err := storage.ValidatePath(name); err != nil {
		return err
	}
	// Only the uploaded path is new; the destination folder may be older.
	if err := validateNewName(name); err != nil {
		return err
	}
	if _, exists := b.paths[name]; exists {
		return fmt.Errorf("%w: duplicate or conflicting file path", storage.ErrInvalidPath)
	}
	for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
		if b.paths[parent] {
			return fmt.Errorf("%w: file path conflicts with a parent file", storage.ErrInvalidPath)
		}
	}
	destination := path.Join(b.target, name)
	if info, err := b.tree.Stat(ctx, destination); err == nil && info != nil {
		return fmt.Errorf("%w: %q; rename or delete the existing item first", storage.ErrConflict, destination)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// newDirectories counts the folders that publishing name would create: parents
// that neither exist on disk nor belong to an earlier file in this batch.
func (b *uploadBatch) newDirectories(ctx context.Context, name string) (int64, error) {
	var count int64
	for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
		if _, planned := b.paths[parent]; planned {
			break
		}
		if _, err := b.tree.Stat(ctx, path.Join(b.target, parent)); err == nil {
			break
		} else if !errors.Is(err, fs.ErrNotExist) {
			return 0, err
		}
		count++
	}
	return count, nil
}

func (b *uploadBatch) add(ctx context.Context, name string, contents io.Reader) error {
	folders, err := b.newDirectories(ctx, name)
	if err != nil {
		return err
	}
	// Even an empty file needs one unit. File contents may use whole units only.
	available := b.remaining - b.charged - folders*b.unit
	if available < b.unit {
		return quota.ErrExceeded
	}
	out, err := b.stage.Create(name)
	if err != nil {
		return err
	}
	// Never write the byte used to detect an overflow, even when earlier files
	// in this batch have consumed all of the remaining quota.
	limited := &io.LimitedReader{R: contents, N: available / b.unit * b.unit}
	n, copyErr := io.Copy(uploadProgressWriter{Writer: out, progress: b.progress}, limited)
	if copyErr == nil && limited.N == 0 {
		var extra [1]byte
		_, probeErr := io.ReadFull(contents, extra[:])
		if probeErr == nil {
			copyErr = quota.ErrExceeded
		} else if probeErr != io.EOF {
			copyErr = probeErr
		}
	}
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if b.files == 0 {
		b.firstPath = name
	}
	b.paths[name] = true
	for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
		b.paths[parent] = false
	}
	b.files++
	b.bytes += n
	b.charged += folders*b.unit + quota.Charge(n, b.unit)
	b.folders += folders
	b.names = append(b.names, name)
	return nil
}

// currentCharge recomputes the batch's charge against the destination as it is
// now. Uploads stream without the account lock, so folders may have been
// created or removed since the files were added.
func (b *uploadBatch) currentCharge(ctx context.Context) (int64, error) {
	planned := map[string]bool{}
	var folders int64
	for _, name := range b.names {
		for parent := path.Dir(name); parent != "." && !planned[parent]; parent = path.Dir(parent) {
			planned[parent] = true
			if _, err := b.tree.Stat(ctx, path.Join(b.target, parent)); err == nil {
				break
			} else if !errors.Is(err, fs.ErrNotExist) {
				return 0, err
			}
			folders++
		}
	}
	return b.charged + (folders-b.folders)*b.unit, nil
}

// publish moves the staged files into place. File management uses the same
// per-user lock as uploads, so nothing changes the destination meanwhile.
func (b *uploadBatch) publish(ctx context.Context) (string, bool, error) {
	if err := b.stage.Publish(ctx, b.target, b.names); err != nil {
		return "", false, err
	}
	group := b.files > 1 || path.Dir(b.firstPath) != "."
	saved := path.Join(b.target, b.firstPath)
	if group {
		saved = b.target
		if saved == "" {
			saved = "."
		}
	}
	return saved, group, nil
}
