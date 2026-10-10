package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"time"
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
	bytes     int64
	progress  *uploadProgress
}

func newUploadBatch(dir string) (*uploadBatch, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	name := ".upload-" + shortRandHex(12)
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
	return &uploadBatch{root: root, stage: stage, stageName: name, paths: make(map[string]bool)}, nil
}

func (b *uploadBatch) close() {
	b.stage.Close()
	b.root.RemoveAll(b.stageName)
	b.root.Close()
}

func (b *uploadBatch) checkPath(name string) error {
	if err := validateUploadPath(name); err != nil {
		return err
	}
	if _, exists := b.paths[name]; exists {
		return fmt.Errorf("duplicate or conflicting file path")
	}
	for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
		if b.paths[parent] {
			return fmt.Errorf("file path conflicts with a parent file")
		}
	}
	return nil
}

func (b *uploadBatch) add(name string, contents io.Reader, remaining int64) error {
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
	limited := &io.LimitedReader{R: contents, N: remaining}
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
	return nil
}

func (b *uploadBatch) publish() (string, bool, error) {
	name := time.Now().UTC().Format("20060102T150405Z") + "_" + shortRandHex(6)
	source := b.stageName
	group := b.files > 1 || path.Dir(b.firstPath) != "."
	if !group {
		safe := sanitizeFilename(b.firstPath)
		if safe == "" {
			safe = "upload.bin"
		}
		name += "_" + safe
		source = filepath.Join(b.stageName, filepath.FromSlash(b.firstPath))
	}
	// The per-user upload lock protects publication from other server uploads.
	if _, err := b.root.Lstat(name); !errors.Is(err, os.ErrNotExist) {
		return "", false, fmt.Errorf("upload destination already exists or is unavailable")
	}
	if err := b.root.Rename(source, name); err != nil {
		return "", false, err
	}
	return name, group, nil
}
