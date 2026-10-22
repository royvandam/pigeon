package files

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"

	"github.com/royvandam/pigeon/pkg/account"
	"github.com/royvandam/pigeon/pkg/quota"
	"github.com/royvandam/pigeon/pkg/storage"
)

// QuotaError reports files that do not fit the account's quota. It wraps
// quota.ErrExceeded.
type QuotaError struct {
	// Available is how much of the quota the upload could use.
	Available int64
}

func (e *QuotaError) Error() string {
	return quota.ErrExceeded.Error()
}

func (e *QuotaError) Unwrap() error {
	return quota.ErrExceeded
}

// Receipt describes a published upload.
type Receipt struct {
	// SavedAs is the path of a single file, or the destination folder of a
	// group, "." for the top folder.
	SavedAs string
	// Group reports several files, or files in folders.
	Group bool
	Files int
	Bytes int64
	// Usage is the account's usage once Publish returns, when known.
	Usage Usage
}

// Progress follows an upload. Its methods are called from the goroutine that
// drives the upload.
type Progress interface {
	// Preparing is called once the upload has the account to itself and starts
	// checking its storage, which can take a while the first time.
	Preparing()
	// Written is called with the number of file bytes after each write to
	// storage.
	Written(n int64)
}

// Upload receives files for one account and publishes them together, so a
// batch appears all at once or not at all; existing items are never
// replaced.
//
// An upload holds the account's upload slot from BeginUpload until Close, so
// uploads to one account stream one at a time and each starts from the quota
// the previous one left. It holds the account lock only in Prepare and
// Publish, so the account's pages, share links, and file actions do not wait
// while files stream.
type Upload struct {
	service     *Service
	user        account.User
	progress    Progress
	releaseSlot func()
	tree        storage.Tree
	stage       storage.Upload
	target      string
	remaining   int64           // quota available before this batch
	paths       map[string]bool // true for files, false for their parent folders
	names       []string
	firstPath   string
	files       int
	bytes       int64 // file contents received
	charged     int64 // quota charge for those files and the folders they add
	folders     int64 // folders included in charged
}

// BeginUpload waits until no other upload to u's account is streaming.
// progress, if not nil, follows the upload. Close the upload when done with it.
func (s *Service) BeginUpload(ctx context.Context, u account.User, progress Progress) (*Upload, error) {
	release, err := s.locks.LockUploadSlot(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	return &Upload{service: s, user: u, progress: progress, releaseSlot: release, paths: make(map[string]bool)}, nil
}

// Prepare checks the account and that folder dir has room for at least one
// more item, and starts staging files for it. It returns the account's usage
// when known, also when the quota is exhausted, which a *QuotaError reports.
func (up *Upload) Prepare(ctx context.Context, dir string) (Usage, error) {
	access, err := up.service.Open(ctx, up.user)
	if err != nil {
		return Usage{}, err
	}
	defer access.Close()
	if up.progress != nil {
		up.progress.Preparing()
	}
	// The upload keeps its own handle, which it needs after the lock is
	// released.
	tree, err := up.service.storage.Create(ctx, up.user.Slug)
	if err != nil {
		return Usage{}, err
	}
	usage, err := access.Usage(ctx)
	if err == nil && usage.Remaining() < up.service.unit {
		err = &QuotaError{Available: usage.Remaining()}
	}
	if err == nil {
		err = storage.RequireDirectory(ctx, tree, dir)
	}
	var stage storage.Upload
	if err == nil {
		stage, err = tree.BeginUpload(ctx)
	}
	if err != nil {
		tree.Close()
		return usage, err
	}
	up.tree, up.stage, up.target, up.remaining = tree, stage, dir, usage.Remaining()
	return usage, nil
}

// Files returns how many files were added.
func (up *Upload) Files() int {
	return up.files
}

func (up *Upload) checkPath(ctx context.Context, name string) error {
	if err := storage.ValidatePath(name); err != nil {
		return err
	}
	// Only the uploaded path is new; the destination folder may be older.
	if err := validateNewName(name); err != nil {
		return err
	}
	if _, exists := up.paths[name]; exists {
		return fmt.Errorf("%w: duplicate or conflicting file path", storage.ErrInvalidPath)
	}
	for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
		if up.paths[parent] {
			return fmt.Errorf("%w: file path conflicts with a parent file", storage.ErrInvalidPath)
		}
	}
	destination := path.Join(up.target, name)
	if info, err := up.tree.Stat(ctx, destination); err == nil && info != nil {
		return fmt.Errorf("%w: %q; rename or delete the existing item first", storage.ErrConflict, destination)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// newDirectories counts the folders that publishing name would create: parents
// that neither exist in storage nor belong to an earlier file in this batch.
func (up *Upload) newDirectories(ctx context.Context, name string) (int64, error) {
	var count int64
	for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
		if _, planned := up.paths[parent]; planned {
			break
		}
		if _, err := up.tree.Stat(ctx, path.Join(up.target, parent)); err == nil {
			break
		} else if !errors.Is(err, fs.ErrNotExist) {
			return 0, err
		}
		count++
	}
	return count, nil
}

// Add stages a file at name, relative to the destination folder, reading its
// contents. It rejects invalid, duplicate, and taken paths before reading,
// with errors that wrap storage.ErrInvalidPath or storage.ErrConflict, and
// reports a *QuotaError as soon as the batch outgrows the quota.
func (up *Upload) Add(ctx context.Context, name string, contents io.Reader) error {
	if err := up.checkPath(ctx, name); err != nil {
		return err
	}
	folders, err := up.newDirectories(ctx, name)
	if err != nil {
		return err
	}
	unit := up.service.unit
	// Even an empty file needs one unit. File contents may use whole units only.
	available := up.remaining - up.charged - folders*unit
	if available < unit {
		return &QuotaError{Available: up.remaining}
	}
	out, err := up.stage.Create(name)
	if err != nil {
		return err
	}
	// Never write the byte used to detect an overflow, even when earlier files
	// in this batch have consumed all of the remaining quota.
	limited := &io.LimitedReader{R: contents, N: available / unit * unit}
	n, copyErr := io.Copy(progressWriter{Writer: out, progress: up.progress}, limited)
	if copyErr == nil && limited.N == 0 {
		var extra [1]byte
		_, probeErr := io.ReadFull(contents, extra[:])
		if probeErr == nil {
			copyErr = &QuotaError{Available: up.remaining}
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
	if up.files == 0 {
		up.firstPath = name
	}
	up.paths[name] = true
	for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
		up.paths[parent] = false
	}
	up.files++
	up.bytes += n
	up.charged += folders*unit + quota.Charge(n, unit)
	up.folders += folders
	up.names = append(up.names, name)
	return nil
}

// currentCharge recomputes the batch's charge against the destination as it is
// now. Files stream without the account lock, so folders may have been created
// or removed since they were added.
func (up *Upload) currentCharge(ctx context.Context) (int64, error) {
	planned := map[string]bool{}
	var folders int64
	for _, name := range up.names {
		for parent := path.Dir(name); parent != "." && !planned[parent]; parent = path.Dir(parent) {
			planned[parent] = true
			if _, err := up.tree.Stat(ctx, path.Join(up.target, parent)); err == nil {
				break
			} else if !errors.Is(err, fs.ErrNotExist) {
				return 0, err
			}
			folders++
		}
	}
	return up.charged + (folders-up.folders)*up.service.unit, nil
}

// Publish checks the account and its quota again and publishes the files.
// The account may have been deleted, its quota lowered, or its storage filled
// by file actions while the files streamed.
func (up *Upload) Publish(ctx context.Context) (Receipt, error) {
	access, err := up.service.Open(ctx, up.user)
	if err != nil {
		return Receipt{}, err
	}
	defer access.Close()
	usage, err := access.Usage(ctx) // Staged files are never counted.
	if err != nil {
		return Receipt{}, err
	}
	charged, err := up.currentCharge(ctx)
	if err != nil {
		return Receipt{}, err
	}
	receipt := Receipt{Usage: usage}
	if usage.Remaining() < charged {
		return receipt, &QuotaError{Available: usage.Remaining()}
	}
	// File actions take the same account lock, so nothing changes the
	// destination meanwhile.
	if err := up.stage.Publish(ctx, up.target, up.names); err != nil {
		access.forgetUsage() // Publication rolls back, but only as far as it can.
		return receipt, err
	}
	access.changed(charged)
	receipt.Usage.Used += charged
	receipt.Files, receipt.Bytes = up.files, up.bytes
	receipt.Group = up.files > 1 || path.Dir(up.firstPath) != "."
	receipt.SavedAs = path.Join(up.target, up.firstPath)
	if receipt.Group {
		receipt.SavedAs = up.target
		if receipt.SavedAs == "" {
			receipt.SavedAs = "."
		}
	}
	return receipt, nil
}

// Close discards every file that was not published and releases the upload
// slot.
func (up *Upload) Close() {
	if up.stage != nil {
		up.stage.Close()
		up.stage = nil
	}
	if up.tree != nil {
		up.tree.Close()
		up.tree = nil
	}
	if up.releaseSlot != nil {
		up.releaseSlot()
		up.releaseSlot = nil
	}
}

// progressWriter reports file bytes only after the output accepts them.
// Multipart framing and bytes still buffered in the browser or a proxy do not
// count.
type progressWriter struct {
	io.Writer
	progress Progress
}

func (w progressWriter) Write(data []byte) (int, error) {
	n, err := w.Writer.Write(data)
	if w.progress != nil {
		w.progress.Written(int64(n))
	}
	return n, err
}
