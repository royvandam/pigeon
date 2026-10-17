package pigeon

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
)

var errDiskQuotaExceeded = errors.New("disk quota exceeded")

// Write into a private staging directory and publish only after every multipart
// part has been validated. Root confines filesystem operations to that directory.
type uploadBatch struct {
	root      *os.Root
	stage     *os.Root
	stageName string
	paths     map[string]bool // true for files, false for their parent directories
	firstPath string
	files     int
	bytes     int64 // file contents received
	charged   int64 // quota charge for those files and the folders they add
	remaining int64 // quota available before this batch
	unit      int64 // minimum charge per file and folder
	progress  *uploadProgress
	target    string
	names     []string
}

func newUploadBatch(dir, target string, remaining, unit int64) (*uploadBatch, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	if err := requireDirectory(root, target); err != nil {
		root.Close()
		return nil, err
	}
	name := stagingPrefix + shortRandHex(12)
	if err := root.Mkdir(name, 0o750); err != nil {
		root.Close()
		return nil, err
	}
	stage, err := root.OpenRoot(name)
	if err != nil {
		root.RemoveAll(name)
		root.Close()
		return nil, err
	}
	return &uploadBatch{root: root, stage: stage, stageName: name, paths: make(map[string]bool), target: target,
		remaining: remaining, unit: unit}, nil
}

func (b *uploadBatch) close() {
	b.stage.Close()
	b.root.RemoveAll(b.stageName)
	b.root.Close()
}

func (b *uploadBatch) checkPath(name string) error {
	if err := validateStoragePath(name, false); err != nil {
		return err
	}
	if _, exists := b.paths[name]; exists {
		return fmt.Errorf("%w: duplicate or conflicting file path", errStoragePath)
	}
	for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
		if b.paths[parent] {
			return fmt.Errorf("%w: file path conflicts with a parent file", errStoragePath)
		}
	}
	destination := path.Join(b.target, name)
	if info, err := storageInfo(b.root, destination); err == nil && info != nil {
		return fmt.Errorf("%w: %q; rename or delete the existing item first", errFileConflict, destination)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// newDirectories counts the folders that publishing name would create: parents
// that neither exist on disk nor belong to an earlier file in this batch.
func (b *uploadBatch) newDirectories(name string) (int64, error) {
	var count int64
	for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
		if _, planned := b.paths[parent]; planned {
			break
		}
		if _, err := storageInfo(b.root, path.Join(b.target, parent)); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return 0, err
		}
		count++
	}
	return count, nil
}

func (b *uploadBatch) add(name string, contents io.Reader) error {
	folders, err := b.newDirectories(name)
	if err != nil {
		return err
	}
	// Even an empty file needs one unit. File contents may use whole units only.
	available := b.remaining - b.charged - folders*b.unit
	if available < b.unit {
		return errDiskQuotaExceeded
	}
	native := filepath.FromSlash(name)
	if err := b.stage.MkdirAll(filepath.Dir(native), 0o750); err != nil {
		return err
	}
	out, err := b.stage.OpenFile(native, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o640)
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
			copyErr = errDiskQuotaExceeded
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
	b.charged += folders*b.unit + chargedSize(n, b.unit)
	b.names = append(b.names, name)
	return nil
}

func (b *uploadBatch) publish() (string, bool, error) {
	// File management uses the same per-user lock as uploads. Preflight every
	// destination before changing anything and roll back on publication errors.
	if err := requireDirectory(b.root, b.target); err != nil {
		return "", false, err
	}
	for _, name := range b.names {
		if _, err := storageInfo(b.root, path.Join(b.target, name)); !errors.Is(err, os.ErrNotExist) {
			if err == nil {
				err = fmt.Errorf("%w: %q", errFileConflict, path.Join(b.target, name))
			}
			return "", false, err
		}
	}
	var createdDirs, published []string
	rollback := func() {
		for i := len(published) - 1; i >= 0; i-- {
			b.root.Remove(filepath.FromSlash(published[i]))
		}
		for i := len(createdDirs) - 1; i >= 0; i-- {
			b.root.Remove(filepath.FromSlash(createdDirs[i]))
		}
	}
	for _, name := range b.names {
		destination := path.Join(b.target, name)
		parent := path.Dir(destination)
		var missing []string
		for parent != "." {
			if _, err := b.root.Lstat(filepath.FromSlash(parent)); err == nil {
				break
			} else if !errors.Is(err, os.ErrNotExist) {
				rollback()
				return "", false, err
			}
			missing = append(missing, parent)
			parent = path.Dir(parent)
		}
		for i := len(missing) - 1; i >= 0; i-- {
			if err := b.root.Mkdir(filepath.FromSlash(missing[i]), 0o750); err != nil {
				rollback()
				return "", false, err
			}
			createdDirs = append(createdDirs, missing[i])
		}
		source := filepath.Join(b.stageName, filepath.FromSlash(name))
		if err := b.root.Rename(source, filepath.FromSlash(destination)); err != nil {
			rollback()
			return "", false, err
		}
		published = append(published, destination)
	}
	group := b.files > 1 || path.Dir(b.firstPath) != "."
	saved := path.Join(b.target, b.firstPath)
	if group {
		saved = nativeStoragePath(b.target)
		saved = filepath.ToSlash(saved)
	}
	return saved, group, nil
}
